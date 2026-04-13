package read

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// loadEntries is the implementation behind the exported LoadEntries
// wrapper (defined in read.go). It resolves each requested ID against a
// bodyless index scoped to q.Layer + q.Teams and materializes the
// winning file's body for every match. Per-ID failures (not_found,
// ambiguous, read_error) are attached to Entry.Status / Entry.Error and
// surfaced alongside successful rows so the caller can render a
// partial-success envelope. Whole-call failures (empty teams, index
// scan I/O error, invalid window) are returned as a Go error.
//
// The ID grammar is spec §4.4:
//
//   - Full stem: exact `YYYY-MM-DD-<uuid7...>` match wins outright.
//   - Bare `YYYY-MM-DD`: returns every entry with that Date. This is
//     NOT an ambiguous case — per plan §3 Unit 4, the day's content is
//     the union of all D-*.md files. Zero matches → not_found.
//   - Date-bearing stem prefix (`YYYY-MM-DD-<partial>`): HasPrefix
//     match against each stem. More than one → ambiguous.
//   - UUID7 short form (6+ hex-ish chars that is neither a bare date
//     nor a stem prefix): match against the portion of each stem after
//     `YYYY-MM-DD-`. More than one → ambiguous.
//
// show is daily-only in Unit 4. The command layer must not pass
// LayerWeekly / LayerMonthly here; LayerAuto and "" are normalized to
// LayerDaily so a caller that forgets to set Layer still gets the
// expected behavior.
func loadEntries(ctx context.Context, q ReadQuery, ids []string) ([]Entry, error) {
	if len(ids) == 0 {
		return nil, errors.New("distill history read: LoadEntries requires at least one id")
	}
	if len(q.Teams) == 0 {
		return nil, errors.New("distill history read: no team provided")
	}
	if !q.NoTimeFilter {
		if q.Since.IsZero() || q.Until.IsZero() {
			return nil, errors.New("distill history read: Since and Until must be set")
		}
	}

	indexQ := q
	indexQ.WantBody = false
	indexQ.Limit = 0
	if indexQ.Layer == "" || indexQ.Layer == LayerAuto {
		indexQ.Layer = LayerDaily
	}
	index, _, err := listEntries(ctx, indexQ)
	if err != nil {
		return nil, err
	}

	teamByslug := make(map[string]TeamRef, len(q.Teams))
	for _, t := range q.Teams {
		teamByslug[t.Slug] = t
	}

	out := make([]Entry, 0, len(ids))
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		trimmed := strings.TrimSpace(id)
		matches := matchID(trimmed, index)
		switch {
		case len(matches) == 0:
			out = append(out, Entry{
				ID:     id,
				Status: "not_found",
				Error: &EntryError{
					Code:    "id_not_found",
					Message: fmt.Sprintf("no entry matches %q", id),
				},
			})
		case bareDateRe.MatchString(trimmed):
			for _, m := range matches {
				out = append(out, materializeIndexEntry(m, teamByslug))
			}
		case len(matches) == 1:
			out = append(out, materializeIndexEntry(matches[0], teamByslug))
		default:
			candidates := make([]string, 0, len(matches))
			for _, m := range matches {
				candidates = append(candidates, m.ID)
			}
			out = append(out, Entry{
				ID:     id,
				Status: "ambiguous",
				Error: &EntryError{
					Code: "id_ambiguous",
					Message: fmt.Sprintf("id %q matches multiple entries: %s",
						id, strings.Join(candidates, ", ")),
				},
			})
		}
	}
	return out, nil
}

// materializeIndexEntry re-reads one index entry's file with WantBody
// true so the caller gets BodyMD + Citations populated. Returns a
// synthetic Entry with Status="read_error" on failure so the partial-
// success envelope can report the problem per-ID without aborting the
// whole call.
func materializeIndexEntry(idx Entry, teamByslug map[string]TeamRef) Entry {
	team, ok := teamByslug[idx.Team]
	if !ok || team.Path == "" {
		return Entry{
			ID:      idx.ID,
			Layer:   idx.Layer,
			Date:    idx.Date,
			Team:    idx.Team,
			RelPath: idx.RelPath,
			Status:  "read_error",
			Error: &EntryError{
				Code: "read_error",
				Message: fmt.Sprintf(
					"team slug %q not in query.Teams", idx.Team),
			},
		}
	}
	abs := filepath.Join(team.Path, idx.RelPath)
	full, err := parseEntryFile(abs, idx.Layer, idx.Team, idx.Date, true)
	if err != nil {
		return Entry{
			ID:      idx.ID,
			Layer:   idx.Layer,
			Date:    idx.Date,
			Team:    idx.Team,
			RelPath: idx.RelPath,
			Status:  "read_error",
			Error: &EntryError{
				Code:    "read_error",
				Message: err.Error(),
			},
		}
	}
	full.RelPath = idx.RelPath
	return *full
}

// matchID returns every index entry matching id per spec §4.4 (see
// loadEntries for the full grammar). An empty return means "no match";
// a single return is the happy path; multiple returns are either the
// bare-date expansion (a legitimate multi-match, distinguished by the
// caller) or a prefix ambiguity (which the caller promotes to a per-ID
// id_ambiguous error).
func matchID(id string, index []Entry) []Entry {
	if id == "" {
		return nil
	}

	for _, e := range index {
		if e.ID == id {
			return []Entry{e}
		}
	}

	if bareDateRe.MatchString(id) {
		var out []Entry
		for _, e := range index {
			if e.Date == id {
				out = append(out, e)
			}
		}
		return out
	}

	if stemPrefixRe.MatchString(id) {
		var out []Entry
		for _, e := range index {
			if strings.HasPrefix(e.ID, id) {
				out = append(out, e)
			}
		}
		return out
	}

	if len(id) >= 6 && looksHexOrDash(id) {
		var out []Entry
		for _, e := range index {
			const datePrefix = len("2006-01-02") + 1 // "YYYY-MM-DD-"
			if len(e.ID) <= datePrefix {
				continue
			}
			if strings.HasPrefix(e.ID[datePrefix:], id) {
				out = append(out, e)
			}
		}
		return out
	}

	return nil
}

var (
	bareDateRe   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	stemPrefixRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}-.+`)
)

// looksHexOrDash reports whether s is composed entirely of hex digits
// and dashes. It gates the UUID7-short-form branch of matchID: the
// canonical short form is "019c8a3f" (hex prefix), but longer prefixes
// like "019c8a3f-0001" include a dash and must still qualify. Anything
// with letters outside [a-f] or non-hex punctuation is disqualified.
func looksHexOrDash(s string) bool {
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		case c == '-':
		default:
			return false
		}
	}
	return true
}
