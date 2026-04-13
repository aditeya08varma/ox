package read

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Local copies of the filename regexes from cmd/ox/distill.go. The reader
// package is standalone by design — it must not import from cmd/ox —
// so the canonical patterns are duplicated here. Unit 3 adds a pin-test
// that fails if either copy drifts from the original. weeklyRe and
// monthlyRe are unused in Unit 1 (daily only); Unit 3 wires them into
// listWeeklyForTeam / listMonthlyForTeam. Package-level vars do not
// trigger Go's unused-variable rule.
var (
	dailyDateRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})`)
	weeklyRe    = regexp.MustCompile(`^(\d{4})-W(\d{2})`)
	monthlyRe   = regexp.MustCompile(`^(\d{4}-\d{2})(?:-[0-9a-f-]+)?\.md$`)
)

// listEntries is the real implementation behind ListEntries. See the
// exported wrapper for contract; this function assumes q has already
// been checked once by the exported call site.
func listEntries(ctx context.Context, q ReadQuery) ([]Entry, ListMeta, error) {
	if q.Since.IsZero() || q.Until.IsZero() {
		return nil, ListMeta{}, errors.New("journal read: Since and Until must be set")
	}
	if q.Until.Before(q.Since) {
		return nil, ListMeta{}, errors.New("journal read: Until is before Since")
	}

	effSince := dayFloor(q.Since)
	effUntil := dayCeil(q.Until)

	meta := ListMeta{
		LayerResolved:  q.Layer,
		EffectiveSince: effSince,
		EffectiveUntil: effUntil,
	}

	layer := q.Layer
	if layer == "" || layer == LayerAuto {
		layer = resolveLayer(q)
		meta.LayerResolved = layer
	}

	if layer != LayerDaily {
		meta.Warnings = append(meta.Warnings, Warning{
			Code:    "layer_not_implemented",
			Message: fmt.Sprintf("layer %q not yet implemented", string(layer)),
		})
		return nil, meta, nil
	}

	if len(q.Teams) == 0 {
		return nil, meta, errors.New("journal read: no team provided")
	}

	var out []Entry
	for _, team := range q.Teams {
		if err := ctx.Err(); err != nil {
			return nil, meta, err
		}
		if err := listDailyForTeam(ctx, team, effSince, effUntil, q.WantBody, &out, &meta); err != nil {
			return nil, meta, fmt.Errorf("list daily team=%s: %w", team.Slug, err)
		}
	}

	sortEntries(out)

	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
		meta.Truncated = true
	}

	return out, meta, nil
}

// listDailyForTeam walks memory/daily under one team root, parses each
// .md file, and appends in-window Entries to out. Per-file problems are
// recorded on meta.Warnings; a missing or unreadable directory returns a
// clean empty result (matching discoverMemoryFiles at
// cmd/ox/agent_prime.go:1677).
func listDailyForTeam(ctx context.Context, team TeamRef, effSince, effUntil time.Time, wantBody bool, out *[]Entry, meta *ListMeta) error {
	if team.Path == "" {
		return errors.New("team path is empty")
	}
	dir := dailyDir(team.Path)

	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read dir=%s: %w", dir, err)
	}

	for _, de := range dirEntries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := de.Name()
		if de.IsDir() || !strings.HasSuffix(name, ".md") {
			continue
		}
		rel := filepath.ToSlash(filepath.Join("memory", "daily", name))

		m := dailyDateRe.FindStringSubmatch(name)
		if m == nil {
			meta.Warnings = append(meta.Warnings, Warning{
				Path:    rel,
				Code:    "malformed_filename",
				Message: "daily file missing YYYY-MM-DD prefix",
			})
			continue
		}
		dateStr := m[1]
		day, err := time.ParseInLocation("2006-01-02", dateStr, time.UTC)
		if err != nil {
			meta.Warnings = append(meta.Warnings, Warning{
				Path:    rel,
				Code:    "malformed_date",
				Message: fmt.Sprintf("parse date prefix: %v", err),
			})
			continue
		}
		// Half-open inclusion: [effSince, effUntil). The day IS the UTC
		// midnight of the filename's date prefix, so this check filters by
		// event day (filename) rather than write time (mtime / UUID7).
		if day.Before(effSince) || !day.Before(effUntil) {
			continue
		}

		abs := filepath.Join(dir, name)
		entry, err := parseEntryFile(abs, LayerDaily, team.Slug, dateStr, wantBody)
		if err != nil {
			meta.Warnings = append(meta.Warnings, Warning{
				Path:    rel,
				Code:    "read_error",
				Message: err.Error(),
			})
			continue
		}
		entry.RelPath = rel
		*out = append(*out, *entry)
	}
	return nil
}

// parseEntryFile opens one .md file, pulls source-list metadata from the
// YAML frontmatter, stamps the Entry with filesystem mtime, and
// optionally includes the body with the frontmatter stripped.
//
// Unit 1 leaves Citations/CitationCount at zero — Unit 2 bridges through
// internal/journal/memoryio to reuse cmd/ox/distill_citations.go without
// duplicating the parser here.
func parseEntryFile(absPath string, layer Layer, teamSlug, dateStr string, wantBody bool) (*Entry, error) {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	stat, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("stat file: %w", err)
	}
	content := string(data)

	sources := parseFrontmatterSources(content)
	id := strings.TrimSuffix(filepath.Base(absPath), ".md")

	e := &Entry{
		ID:          id,
		Layer:       layer,
		Date:        dateStr,
		Team:        teamSlug,
		SourceFiles: sources,
		CreatedAt:   stat.ModTime().UTC(),
		Status:      "ok",
	}
	if wantBody {
		e.BodyMD = stripFrontmatter(content)
	}
	return e, nil
}

// parseFrontmatterSources extracts the `sources:` list from YAML
// frontmatter. Mirrors parseDailySources at cmd/ox/distill.go:1516 so
// the reader does not import cmd/ox. Unit 2 moves the shared copy into
// internal/journal/memoryio and re-points both callers at it.
func parseFrontmatterSources(content string) []string {
	if !strings.HasPrefix(content, "---\n") {
		return nil
	}
	end := strings.Index(content[4:], "\n---")
	if end < 0 {
		return nil
	}
	frontmatter := content[4 : 4+end]

	var sources []string
	inSources := false
	for _, line := range strings.Split(frontmatter, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "sources:" {
			inSources = true
			continue
		}
		if inSources && strings.HasPrefix(line, "  - ") {
			sources = append(sources, strings.TrimPrefix(trimmed, "- "))
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			inSources = false
		}
	}
	return sources
}

// stripFrontmatter returns content with any leading YAML frontmatter
// block (between `---\n` delimiters) removed, including the trailing
// newline after the closing delimiter. A body that does not start with
// a frontmatter block is returned unchanged.
func stripFrontmatter(content string) string {
	if !strings.HasPrefix(content, "---\n") {
		return content
	}
	end := strings.Index(content[4:], "\n---")
	if end < 0 {
		return content
	}
	rest := content[4+end+len("\n---"):]
	rest = strings.TrimPrefix(rest, "\n")
	return rest
}

// sortEntries orders by Date ascending, then CreatedAt ascending within
// the same Date. Matches the spec §3.4 ordering contract for list.
func sortEntries(out []Entry) {
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date < out[j].Date
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
}

// dayFloor returns the UTC midnight at or before t.
func dayFloor(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// dayCeil returns the UTC midnight strictly after t's day. The result is
// the exclusive upper bound of a half-open day window that contains t.
func dayCeil(t time.Time) time.Time {
	return dayFloor(t).Add(24 * time.Hour)
}

// resolveLayer implements the --layer=auto rule from spec §3.4: monthly
// if the effective window covers a full calendar month, else weekly if
// it covers a full ISO week (Monday start), else daily. Inputs are the
// raw ReadQuery bounds; the function applies its own day-rounding so
// callers can invoke it independently of listEntries.
func resolveLayer(q ReadQuery) Layer {
	effSince := dayFloor(q.Since)
	effUntil := dayCeil(q.Until)
	if coversFullMonth(effSince, effUntil) {
		return LayerMonthly
	}
	if coversFullWeek(effSince, effUntil) {
		return LayerWeekly
	}
	return LayerDaily
}

// coversFullMonth reports whether [effSince, effUntil) contains a whole
// calendar month [first-of-month, first-of-next-month). Because later
// month candidates can never fit a narrower tail, checking the earliest
// month start on or after effSince is sufficient.
func coversFullMonth(effSince, effUntil time.Time) bool {
	candidate := firstOfMonthOnOrAfter(effSince)
	next := candidate.AddDate(0, 1, 0)
	return !next.After(effUntil)
}

// coversFullWeek reports whether [effSince, effUntil) contains a whole
// ISO week [Monday 00:00Z, next Monday 00:00Z).
func coversFullWeek(effSince, effUntil time.Time) bool {
	candidate := mondayOnOrAfter(effSince)
	next := candidate.AddDate(0, 0, 7)
	return !next.After(effUntil)
}

func firstOfMonthOnOrAfter(t time.Time) time.Time {
	u := t.UTC()
	first := time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
	if !first.Before(u) {
		return first
	}
	return first.AddDate(0, 1, 0)
}

func mondayOnOrAfter(t time.Time) time.Time {
	d := dayFloor(t)
	// time.Weekday: Sunday=0, Monday=1, ..., Saturday=6.
	offset := (int(time.Monday) - int(d.Weekday()) + 7) % 7
	return d.AddDate(0, 0, offset)
}
