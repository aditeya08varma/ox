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
	"strconv"
	"strings"
	"time"

	"github.com/sageox/ox/internal/distill/history/memoryio"
)

// The filename regexes are compiled from string constants defined in
// internal/distill/history/memoryio/patterns.go. memoryio is the shared source of
// truth between this standalone reader and cmd/ox/distill.go, whose own
// regex vars are pinned to the same memoryio constants by a bridge test
// in cmd/ox/distill_history_citations_bridge_test.go. That keeps the three places
// that touch memory filenames in lockstep without cmd/ox needing to
// import the reader (it cannot — cmd/ox is package main).
var (
	dailyDateRe = regexp.MustCompile(memoryio.DailyDatePattern)
	weeklyRe    = regexp.MustCompile(memoryio.WeeklyPattern)
	monthlyRe   = regexp.MustCompile(memoryio.MonthlyPattern)
)

// listEntries is the real implementation behind ListEntries. See the
// exported wrapper for contract; this function assumes q has already
// been checked once by the exported call site.
//
// When q.NoTimeFilter is true, Since/Until validation is skipped and
// the effective window widens to [time.Time{}, 9999-12-31) — a range
// the per-layer walkers' half-open filters trivially pass every file
// through. In that mode the caller is responsible for passing an
// explicit Layer (LayerAuto requires a real window to resolve).
//
// WARNING — sentinel meta window under NoTimeFilter: when NoTimeFilter
// is set, meta.EffectiveSince is the zero time and meta.EffectiveUntil
// is 9999-12-31. These are NOT real instants; they exist only to make
// the per-layer filename half-open filters a no-op. Callers must NOT
// surface these values in user-facing envelopes (e.g. the CLI command
// layer's `window.since` / `window.until` fields). Today ox distill
// history show is the only NoTimeFilter consumer and it never emits the window
// field — the value lives purely inside the reader. Any new consumer
// that chooses NoTimeFilter inherits the same contract.
func listEntries(ctx context.Context, q ReadQuery) ([]Entry, ListMeta, error) {
	if !q.NoTimeFilter {
		if q.Since.IsZero() || q.Until.IsZero() {
			return nil, ListMeta{}, errors.New("distill history read: Since and Until must be set")
		}
		if q.Until.Before(q.Since) {
			return nil, ListMeta{}, errors.New("distill history read: Until is before Since")
		}
	}

	var effSince, effUntil time.Time
	if q.NoTimeFilter {
		// Sentinels, not timestamps. See the function-level WARNING.
		effSince = time.Time{}
		effUntil = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)
	} else {
		effSince = dayFloor(q.Since)
		effUntil = dayCeil(q.Until)
	}

	meta := ListMeta{
		LayerResolved:  q.Layer,
		EffectiveSince: effSince,
		EffectiveUntil: effUntil,
	}

	layer := q.Layer
	if layer == "" || layer == LayerAuto {
		if q.NoTimeFilter {
			return nil, meta, errors.New("distill history read: NoTimeFilter requires an explicit Layer (not auto)")
		}
		layer = resolveLayer(q)
		meta.LayerResolved = layer
	}

	if len(q.Teams) == 0 {
		return nil, meta, errors.New("distill history read: no team provided")
	}

	var out []Entry
	for _, team := range q.Teams {
		if err := ctx.Err(); err != nil {
			return nil, meta, err
		}
		var err error
		switch layer {
		case LayerDaily:
			err = listDailyForTeam(ctx, team, effSince, effUntil, q.WantBody, &out, &meta)
		case LayerWeekly:
			err = listWeeklyForTeam(ctx, team, effSince, effUntil, q.WantBody, &out, &meta)
		case LayerMonthly:
			err = listMonthlyForTeam(ctx, team, effSince, effUntil, q.WantBody, &out, &meta)
		default:
			return nil, meta, fmt.Errorf("distill history read: unknown layer %q", string(layer))
		}
		if err != nil {
			return nil, meta, fmt.Errorf("list %s team=%s: %w", layer, team.Slug, err)
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

// listWeeklyForTeam walks memory/weekly under one team root and returns
// entries whose ISO week overlaps [effSince, effUntil). The week span is
// [Monday 00:00Z, next Monday 00:00Z). Entry.Date is the Monday date in
// YYYY-MM-DD form so weekly rows sort naturally alongside daily rows
// under the spec §3.4 "Date ascending" rule.
func listWeeklyForTeam(ctx context.Context, team TeamRef, effSince, effUntil time.Time, wantBody bool, out *[]Entry, meta *ListMeta) error {
	if team.Path == "" {
		return errors.New("team path is empty")
	}
	dir := weeklyDir(team.Path)
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
		rel := filepath.ToSlash(filepath.Join("memory", "weekly", name))

		m := weeklyRe.FindStringSubmatch(name)
		if m == nil {
			meta.Warnings = append(meta.Warnings, Warning{
				Path:    rel,
				Code:    "malformed_filename",
				Message: "weekly file missing YYYY-WXX prefix",
			})
			continue
		}
		year, err1 := strconv.Atoi(m[1])
		week, err2 := strconv.Atoi(m[2])
		if err1 != nil || err2 != nil || week < 1 || week > 53 {
			meta.Warnings = append(meta.Warnings, Warning{
				Path:    rel,
				Code:    "malformed_date",
				Message: fmt.Sprintf("parse ISO year/week from %q", name),
			})
			continue
		}
		weekStart := isoWeekMonday(year, week)
		// Reject nonexistent ISO week numbers for the given year (e.g.
		// W53 in a year that only has 52 ISO weeks). We detect this by
		// round-tripping: if the Monday we computed doesn't report the
		// same (year, week) under ISOWeek, the input week is invalid.
		if gotYear, gotWeek := weekStart.ISOWeek(); gotYear != year || gotWeek != week {
			meta.Warnings = append(meta.Warnings, Warning{
				Path:    rel,
				Code:    "malformed_date",
				Message: fmt.Sprintf("ISO year/week %d-W%02d does not exist in %q", year, week, name),
			})
			continue
		}
		weekEnd := weekStart.AddDate(0, 0, 7)
		// Half-open intersection with the half-open effective window.
		if !weekStart.Before(effUntil) || !weekEnd.After(effSince) {
			continue
		}

		abs := filepath.Join(dir, name)
		dateStr := weekStart.Format("2006-01-02")
		entry, err := parseEntryFile(abs, LayerWeekly, team.Slug, dateStr, wantBody)
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

// listMonthlyForTeam walks memory/monthly under one team root and returns
// entries whose calendar month overlaps [effSince, effUntil). Entry.Date
// is the first-of-month in YYYY-MM-DD form.
func listMonthlyForTeam(ctx context.Context, team TeamRef, effSince, effUntil time.Time, wantBody bool, out *[]Entry, meta *ListMeta) error {
	if team.Path == "" {
		return errors.New("team path is empty")
	}
	dir := monthlyDir(team.Path)
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
		rel := filepath.ToSlash(filepath.Join("memory", "monthly", name))

		m := monthlyRe.FindStringSubmatch(name)
		if m == nil {
			meta.Warnings = append(meta.Warnings, Warning{
				Path:    rel,
				Code:    "malformed_filename",
				Message: "monthly file missing YYYY-MM prefix",
			})
			continue
		}
		monthStart, err := time.ParseInLocation("2006-01", m[1], time.UTC)
		if err != nil {
			meta.Warnings = append(meta.Warnings, Warning{
				Path:    rel,
				Code:    "malformed_date",
				Message: fmt.Sprintf("parse month from %q: %v", name, err),
			})
			continue
		}
		monthEnd := monthStart.AddDate(0, 1, 0)
		if !monthStart.Before(effUntil) || !monthEnd.After(effSince) {
			continue
		}

		abs := filepath.Join(dir, name)
		dateStr := monthStart.Format("2006-01-02")
		entry, err := parseEntryFile(abs, LayerMonthly, team.Slug, dateStr, wantBody)
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

// isoWeekMonday returns the UTC midnight of the Monday that starts ISO
// week (year, week). Computed by picking January 4 of `year` — which
// ISO 8601 guarantees is in week 1 — and advancing to the Monday of
// that week, then stepping (week-1) * 7 days forward. See
// https://en.wikipedia.org/wiki/ISO_week_date.
func isoWeekMonday(year, week int) time.Time {
	jan4 := time.Date(year, 1, 4, 0, 0, 0, 0, time.UTC)
	// time.Weekday: Sunday=0..Saturday=6. ISO Monday=1..Sunday=7.
	wd := int(jan4.Weekday())
	if wd == 0 {
		wd = 7
	}
	week1Monday := jan4.AddDate(0, 0, -(wd - 1))
	return week1Monday.AddDate(0, 0, (week-1)*7)
}

// parseEntryFile opens one .md file, pulls source-list metadata from the
// YAML frontmatter, parses the rendered "## Sources" section into
// Citations, stamps the Entry with filesystem mtime, and optionally
// includes the body with the frontmatter stripped.
//
// Frontmatter parsing, body stripping, and Sources-section parsing all
// delegate to internal/distill/history/memoryio so the reader stays standalone
// (no imports from cmd/ox). The CitationCount field is computed from the
// parsed Citations slice regardless of wantBody, so list calls can
// surface the count without materializing the body.
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

	sources := memoryio.ParseFrontmatterSources(content)
	citations := memoryio.ParseSourcesSection(content)
	id := strings.TrimSuffix(filepath.Base(absPath), ".md")

	e := &Entry{
		ID:            id,
		Layer:         layer,
		Date:          dateStr,
		Team:          teamSlug,
		SourceFiles:   sources,
		CitationCount: len(citations),
		CreatedAt:     stat.ModTime().UTC(),
		Status:        "ok",
	}
	if wantBody {
		e.BodyMD = memoryio.StripFrontmatter(content)
		e.Citations = citations
	}
	return e, nil
}

// sortEntries orders by Date ascending, then CreatedAt ascending, then
// Team, then ID within the same (Date, CreatedAt) pair. Matches the
// spec §3.4 ordering contract for list; the Team and ID tie-breakers
// give `--all-teams` merges a deterministic order even when two
// entries share an identical Date+CreatedAt (e.g. cross-team files
// staged at the same instant).
func sortEntries(out []Entry) {
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date < out[j].Date
		}
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		if out[i].Team != out[j].Team {
			return out[i].Team < out[j].Team
		}
		return out[i].ID < out[j].ID
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
