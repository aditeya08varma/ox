package read

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mustWriteDaily writes a daily .md file with a deterministic mtime and
// returns the absolute path.
func mustWriteDaily(t *testing.T, teamRoot, name, body string, mtime time.Time) string {
	t.Helper()
	dir := filepath.Join(teamRoot, "memory", "daily")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir daily: %v", err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", name, err)
	}
	return p
}

const sampleDailyBody = `---
sources:
  - memory/.github-facts/2026-04-12-019c8a0e-pr-487.jsonl
  - memory/.session-facts/2026-04-12/OxAAAA.jsonl
---
# Daily Memory — 2026-04-12

A body paragraph. [1]

## Sources

1. [PR #487 — fix(doctor)][1]

[1]: https://github.com/sageox/ox/pull/487
`

func TestResolveLayer(t *testing.T) {
	t.Parallel()
	// Anchor week: 2026-04-06 is a Monday.
	mon := time.Date(2026, 4, 6, 0, 0, 0, 0, time.UTC)
	tue := mon.AddDate(0, 0, 1)
	nextMon := mon.AddDate(0, 0, 7)
	apr1 := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	may1 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	mid := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name  string
		since time.Time
		until time.Time
		want  Layer
	}{
		{
			name:  "single day is daily",
			since: tue.Add(6 * time.Hour),
			until: tue.Add(10 * time.Hour),
			want:  LayerDaily,
		},
		{
			name:  "six days is daily (no full week contained)",
			since: mon.AddDate(0, 0, 1),
			until: mon.AddDate(0, 0, 6),
			want:  LayerDaily,
		},
		{
			name:  "monday to next monday is weekly",
			since: mon,
			until: nextMon.Add(-time.Second),
			want:  LayerWeekly,
		},
		{
			name:  "monday through next monday exclusive covers a full week",
			since: mon,
			until: nextMon,
			want:  LayerWeekly,
		},
		{
			name:  "window straddling a week boundary but not covering it is daily",
			since: mon.AddDate(0, 0, -2),
			until: mon.AddDate(0, 0, 3),
			want:  LayerDaily,
		},
		{
			name:  "full april is monthly",
			since: apr1,
			until: may1,
			want:  LayerMonthly,
		},
		{
			name:  "month crossing but not containing a full month is weekly",
			since: mid,
			until: mid.AddDate(0, 0, 20),
			want:  LayerWeekly,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q := ReadQuery{Since: tc.since, Until: tc.until}
			if got := resolveLayer(q); got != tc.want {
				t.Fatalf("resolveLayer(%s..%s) = %q, want %q",
					tc.since.Format(time.RFC3339), tc.until.Format(time.RFC3339), got, tc.want)
			}
		})
	}
}

func TestParseEntryFile(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		body           string
		wantSources    int
		wantCitations  int
		wantBodyMD     bool
		bodyContains   string
		wantCiteLabel0 string
	}{
		{
			name:           "with frontmatter and sources",
			body:           sampleDailyBody,
			wantSources:    2,
			wantCitations:  1,
			wantBodyMD:     true,
			bodyContains:   "# Daily Memory — 2026-04-12",
			wantCiteLabel0: "PR #487 — fix(doctor)",
		},
		{
			name:          "without frontmatter",
			body:          "# Just a header\n\nbody\n",
			wantSources:   0,
			wantCitations: 0,
			wantBodyMD:    true,
			bodyContains:  "# Just a header",
		},
		{
			name:          "frontmatter with zero sources",
			body:          "---\ntitle: test\n---\n# Heading\n",
			wantSources:   0,
			wantCitations: 0,
			wantBodyMD:    true,
			bodyContains:  "# Heading",
		},
		{
			name:          "malformed frontmatter (no close delimiter) passes through",
			body:          "---\nsources:\n  - one\n# no close\n",
			wantSources:   0,
			wantCitations: 0,
			wantBodyMD:    true,
			bodyContains:  "---",
		},
		{
			name:          "no trailing newline",
			body:          "---\nsources:\n  - memory/foo.jsonl\n---\n# done",
			wantSources:   1,
			wantCitations: 0,
			wantBodyMD:    true,
			bodyContains:  "# done",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			p := filepath.Join(dir, "2026-04-12-019c8a3f.md")
			if err := os.WriteFile(p, []byte(tc.body), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			entry, err := parseEntryFile(p, LayerDaily, "sageox", "2026-04-12", true)
			if err != nil {
				t.Fatalf("parseEntryFile: %v", err)
			}
			if got := len(entry.SourceFiles); got != tc.wantSources {
				t.Fatalf("sources = %d, want %d (%v)", got, tc.wantSources, entry.SourceFiles)
			}
			if tc.wantBodyMD {
				if entry.BodyMD == "" {
					t.Fatalf("empty BodyMD")
				}
				if !strings.Contains(entry.BodyMD, tc.bodyContains) {
					t.Fatalf("BodyMD missing %q, got %q", tc.bodyContains, entry.BodyMD)
				}
			}
			if entry.ID != "2026-04-12-019c8a3f" {
				t.Fatalf("ID = %q", entry.ID)
			}
			if entry.Status != "ok" {
				t.Fatalf("Status = %q, want ok", entry.Status)
			}
			if entry.CreatedAt.IsZero() {
				t.Fatalf("CreatedAt is zero")
			}
			// FactCount stays at zero — the reader does not compute facts;
			// that lives in the distill pipeline, not the read surface.
			if entry.FactCount != 0 {
				t.Fatalf("FactCount = %d, want 0 (reader does not compute facts)", entry.FactCount)
			}
			// CitationCount is populated from memoryio.ParseSourcesSection
			// regardless of wantBody so callers can surface the count on
			// list without materializing the body.
			if entry.CitationCount != tc.wantCitations {
				t.Fatalf("CitationCount = %d, want %d", entry.CitationCount, tc.wantCitations)
			}
			// Citations slice is only populated when wantBody is true —
			// pins the memory discipline for list vs. show.
			if len(entry.Citations) != tc.wantCitations {
				t.Fatalf("len(Citations) = %d, want %d", len(entry.Citations), tc.wantCitations)
			}
			if tc.wantCitations > 0 && tc.wantCiteLabel0 != "" {
				if entry.Citations[0].Label != tc.wantCiteLabel0 {
					t.Fatalf("Citations[0].Label = %q, want %q",
						entry.Citations[0].Label, tc.wantCiteLabel0)
				}
			}
		})
	}
}

func TestParseEntryFile_BodyOff(t *testing.T) {
	t.Parallel()
	// When wantBody=false the entry must expose the cheap metadata
	// (SourceFiles, CitationCount) but NOT the body or the Citations
	// slice — those are pulled only for show/since. This pins the
	// list-vs-show memory discipline.
	dir := t.TempDir()
	p := filepath.Join(dir, "2026-04-12-019c8a3f.md")
	if err := os.WriteFile(p, []byte(sampleDailyBody), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	entry, err := parseEntryFile(p, LayerDaily, "sageox", "2026-04-12", false)
	if err != nil {
		t.Fatalf("parseEntryFile: %v", err)
	}
	if entry.BodyMD != "" {
		t.Fatalf("WantBody=false should leave BodyMD empty, got %q", entry.BodyMD)
	}
	if len(entry.SourceFiles) != 2 {
		t.Fatalf("sources count = %d, want 2", len(entry.SourceFiles))
	}
	if entry.CitationCount != 1 {
		t.Fatalf("CitationCount = %d, want 1 (populated on list)", entry.CitationCount)
	}
	if entry.Citations != nil {
		t.Fatalf("Citations = %#v, want nil when WantBody=false", entry.Citations)
	}
}

func TestListEntries_DailyOrdering(t *testing.T) {
	t.Parallel()
	teamRoot := t.TempDir()

	// Two entries for 2026-04-12 (mtime older first), one for 2026-04-11.
	apr11mtime := time.Date(2026, 4, 11, 18, 0, 0, 0, time.UTC)
	apr12oldMtime := time.Date(2026, 4, 12, 10, 0, 0, 0, time.UTC)
	apr12newMtime := time.Date(2026, 4, 12, 14, 0, 0, 0, time.UTC)

	mustWriteDaily(t, teamRoot, "2026-04-11-019c7c10.md", sampleDailyBody, apr11mtime)
	mustWriteDaily(t, teamRoot, "2026-04-12-019c8a3f.md", sampleDailyBody, apr12oldMtime)
	mustWriteDaily(t, teamRoot, "2026-04-12-019c87aa.md", sampleDailyBody, apr12newMtime)

	q := ReadQuery{
		Since: time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC),
		Layer: LayerDaily,
		Teams: []TeamRef{{Slug: "sageox", Path: teamRoot}},
	}
	entries, meta, err := ListEntries(context.Background(), q)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries count = %d, want 3", len(entries))
	}

	// Expect: 2026-04-11, then 2026-04-12 (old mtime), then 2026-04-12 (new mtime).
	wantOrder := []string{
		"2026-04-11-019c7c10",
		"2026-04-12-019c8a3f",
		"2026-04-12-019c87aa",
	}
	for i, want := range wantOrder {
		if entries[i].ID != want {
			t.Fatalf("entries[%d].ID = %q, want %q", i, entries[i].ID, want)
		}
	}

	if entries[0].Team != "sageox" {
		t.Fatalf("team = %q, want sageox", entries[0].Team)
	}
	if entries[0].RelPath != "memory/daily/2026-04-11-019c7c10.md" {
		t.Fatalf("RelPath = %q", entries[0].RelPath)
	}
	if meta.LayerResolved != LayerDaily {
		t.Fatalf("LayerResolved = %q", meta.LayerResolved)
	}
	if meta.Truncated {
		t.Fatalf("unexpected Truncated=true")
	}
	if !meta.EffectiveSince.Equal(time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("EffectiveSince = %s", meta.EffectiveSince)
	}
	// q.Until was 2026-04-13T00:00Z, so day-ceiling (exclusive upper
	// bound) is 2026-04-14T00:00Z. The half-open window thus covers
	// days 10/11/12/13.
	if !meta.EffectiveUntil.Equal(time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("EffectiveUntil = %s", meta.EffectiveUntil)
	}
}

func TestListEntries_Truncated(t *testing.T) {
	t.Parallel()
	teamRoot := t.TempDir()
	apr12 := time.Date(2026, 4, 12, 10, 0, 0, 0, time.UTC)
	mustWriteDaily(t, teamRoot, "2026-04-12-019c8a3f.md", sampleDailyBody, apr12)
	mustWriteDaily(t, teamRoot, "2026-04-12-019c87aa.md", sampleDailyBody, apr12.Add(time.Hour))

	q := ReadQuery{
		Since: time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 4, 12, 23, 0, 0, 0, time.UTC),
		Layer: LayerDaily,
		Teams: []TeamRef{{Slug: "sageox", Path: teamRoot}},
		Limit: 1,
	}
	entries, meta, err := ListEntries(context.Background(), q)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1 (limit)", len(entries))
	}
	if !meta.Truncated {
		t.Fatalf("expected Truncated=true when Limit hit")
	}
}

func TestListEntries_EmptyDir(t *testing.T) {
	t.Parallel()
	teamRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(teamRoot, "memory", "daily"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	q := ReadQuery{
		Since: time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC),
		Layer: LayerDaily,
		Teams: []TeamRef{{Slug: "sageox", Path: teamRoot}},
	}
	entries, meta, err := ListEntries(context.Background(), q)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %d, want 0", len(entries))
	}
	if len(meta.Warnings) != 0 {
		t.Fatalf("warnings = %d, want 0: %+v", len(meta.Warnings), meta.Warnings)
	}
}

func TestListEntries_MissingDir(t *testing.T) {
	t.Parallel()
	teamRoot := t.TempDir() // memory/daily/ never created

	q := ReadQuery{
		Since: time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC),
		Layer: LayerDaily,
		Teams: []TeamRef{{Slug: "sageox", Path: teamRoot}},
	}
	entries, _, err := ListEntries(context.Background(), q)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %d, want 0 for missing dir", len(entries))
	}
}

func TestListEntries_MalformedFilenameWarns(t *testing.T) {
	t.Parallel()
	teamRoot := t.TempDir()
	apr12 := time.Date(2026, 4, 12, 10, 0, 0, 0, time.UTC)
	mustWriteDaily(t, teamRoot, "2026-04-12-019c8a3f.md", sampleDailyBody, apr12)
	mustWriteDaily(t, teamRoot, "not-a-date.md", sampleDailyBody, apr12)

	q := ReadQuery{
		Since: time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC),
		Layer: LayerDaily,
		Teams: []TeamRef{{Slug: "sageox", Path: teamRoot}},
	}
	entries, meta, err := ListEntries(context.Background(), q)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1 (bad file skipped)", len(entries))
	}
	if len(meta.Warnings) == 0 {
		t.Fatalf("expected warning for malformed filename")
	}
	found := false
	for _, w := range meta.Warnings {
		if w.Code == "malformed_filename" {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings = %+v, want one with code=malformed_filename", meta.Warnings)
	}
}

func TestListEntries_WindowFiltersByFilenamePrefix(t *testing.T) {
	t.Parallel()
	// Regression for spec §2.1 "Event day, not write time": a file whose
	// filename prefix is 90 days ago but whose mtime is 1 h ago must be
	// EXCLUDED from a 24 h window. The reader filters by filename prefix.
	teamRoot := t.TempDir()
	now := time.Date(2026, 4, 12, 12, 0, 0, 0, time.UTC)
	staleDate := now.AddDate(0, 0, -90)
	staleName := staleDate.Format("2006-01-02") + "-019c8a3f.md"
	mustWriteDaily(t, teamRoot, staleName, sampleDailyBody, now.Add(-time.Hour))

	q := ReadQuery{
		Since: now.Add(-24 * time.Hour),
		Until: now,
		Layer: LayerDaily,
		Teams: []TeamRef{{Slug: "sageox", Path: teamRoot}},
	}
	entries, _, err := ListEntries(context.Background(), q)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %d, want 0 (stale prefix must be out of window)", len(entries))
	}
}

func TestListEntries_LegacyPrefixIsTrusted(t *testing.T) {
	t.Parallel()
	// Spec §5.b: the reader trusts the filename prefix. A legacy file
	// whose UUID7 encodes a different UTC day must still surface with
	// date == filename prefix, and the reader must emit NO warning.
	teamRoot := t.TempDir()
	mustWriteDaily(t, teamRoot,
		"2026-04-12-019c8a3f-legacy.md", sampleDailyBody,
		time.Date(2026, 4, 12, 23, 0, 0, 0, time.UTC))

	q := ReadQuery{
		Since: time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC),
		Layer: LayerDaily,
		Teams: []TeamRef{{Slug: "sageox", Path: teamRoot}},
	}
	entries, meta, err := ListEntries(context.Background(), q)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].Date != "2026-04-12" {
		t.Fatalf("Date = %q, want trust-prefix %q", entries[0].Date, "2026-04-12")
	}
	for _, w := range meta.Warnings {
		if w.Code == "legacy_prefix" || w.Code == "uuid7_mismatch" {
			t.Fatalf("silent legacy policy violated: %+v", w)
		}
	}
}

// mustWriteLayerFile writes a .md file under memory/<layer>/ with a
// deterministic mtime. Mirrors mustWriteDaily but parameterized by
// subdirectory so weekly/monthly tests can share one helper.
func mustWriteLayerFile(t *testing.T, teamRoot, layer, name, body string, mtime time.Time) string {
	t.Helper()
	dir := filepath.Join(teamRoot, "memory", layer)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", layer, err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", name, err)
	}
	return p
}

const sampleWeeklyBody = `---
sources:
  - memory/daily/2026-04-06-019c8a0e.md
  - memory/daily/2026-04-07-019c8a0f.md
---
# Weekly Memory — 2026-W15 (2026-04-06 → 2026-04-12)

A weekly synthesis. [1]

## Sources

1. [PR #500 — weekly][1]

[1]: https://github.com/sageox/ox/pull/500
`

const sampleMonthlyBody = `---
sources:
  - memory/weekly/2026-W14-019c7c10.md
  - memory/weekly/2026-W15-019c8a0e.md
---
# Monthly Memory — 2026-04

A monthly synthesis.
`

// TestListEntries_WeeklyInWindow — a weekly file whose ISO week overlaps
// the requested window must be returned with date=<Monday of that week>.
// Failure prevented: weekly listing regresses to the pre-Unit 3 stub
// that surfaced a layer_not_implemented warning.
func TestListEntries_WeeklyInWindow(t *testing.T) {
	t.Parallel()
	teamRoot := t.TempDir()
	// ISO week 2026-W15 = Mon 2026-04-06 through Sun 2026-04-12.
	w15Mtime := time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC)
	mustWriteLayerFile(t, teamRoot, "weekly",
		"2026-W15-019c8a0e.md", sampleWeeklyBody, w15Mtime)
	// An out-of-window weekly that the reader must NOT return.
	w10Mtime := time.Date(2026, 3, 9, 10, 0, 0, 0, time.UTC)
	mustWriteLayerFile(t, teamRoot, "weekly",
		"2026-W10-019c7000.md", sampleWeeklyBody, w10Mtime)

	q := ReadQuery{
		Since: time.Date(2026, 4, 6, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC),
		Layer: LayerWeekly,
		Teams: []TeamRef{{Slug: "sageox", Path: teamRoot}},
	}
	entries, meta, err := ListEntries(context.Background(), q)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1 (only W15 overlaps): %+v", len(entries), entries)
	}
	if entries[0].Layer != LayerWeekly {
		t.Fatalf("Layer = %q, want weekly", entries[0].Layer)
	}
	if entries[0].Date != "2026-04-06" {
		t.Fatalf("Date = %q, want 2026-04-06 (Monday of W15)", entries[0].Date)
	}
	if entries[0].RelPath != "memory/weekly/2026-W15-019c8a0e.md" {
		t.Fatalf("RelPath = %q", entries[0].RelPath)
	}
	if meta.LayerResolved != LayerWeekly {
		t.Fatalf("LayerResolved = %q", meta.LayerResolved)
	}
	for _, w := range meta.Warnings {
		if w.Code == "layer_not_implemented" {
			t.Fatalf("reader still emits stub warning: %+v", w)
		}
	}
}

// TestListEntries_MonthlyInWindow — a monthly file whose calendar month
// overlaps the requested window must be returned with date=<first of
// month>. Failure prevented: monthly listing regresses to the pre-Unit 3
// stub.
func TestListEntries_MonthlyInWindow(t *testing.T) {
	t.Parallel()
	teamRoot := t.TempDir()
	aprMtime := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	mustWriteLayerFile(t, teamRoot, "monthly",
		"2026-04.md", sampleMonthlyBody, aprMtime)
	// Out-of-window monthly.
	janMtime := time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC)
	mustWriteLayerFile(t, teamRoot, "monthly",
		"2026-01.md", sampleMonthlyBody, janMtime)

	q := ReadQuery{
		Since: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		Layer: LayerMonthly,
		Teams: []TeamRef{{Slug: "sageox", Path: teamRoot}},
	}
	entries, meta, err := ListEntries(context.Background(), q)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1 (only 2026-04 overlaps)", len(entries))
	}
	if entries[0].Layer != LayerMonthly {
		t.Fatalf("Layer = %q, want monthly", entries[0].Layer)
	}
	if entries[0].Date != "2026-04-01" {
		t.Fatalf("Date = %q, want 2026-04-01", entries[0].Date)
	}
	if entries[0].RelPath != "memory/monthly/2026-04.md" {
		t.Fatalf("RelPath = %q", entries[0].RelPath)
	}
	if meta.LayerResolved != LayerMonthly {
		t.Fatalf("LayerResolved = %q", meta.LayerResolved)
	}
}

// TestListEntries_MonthlyWithUUIDSuffix — the monthly regex allows an
// optional UUID7 suffix ("2026-04-<uuid>.md"). Failure prevented: the
// reader only accepts the bare "2026-04.md" form and silently drops
// modern UUID-suffixed monthlies.
func TestListEntries_MonthlyWithUUIDSuffix(t *testing.T) {
	t.Parallel()
	teamRoot := t.TempDir()
	mtime := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	mustWriteLayerFile(t, teamRoot, "monthly",
		"2026-04-019c8a0e.md", sampleMonthlyBody, mtime)

	q := ReadQuery{
		Since: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		Layer: LayerMonthly,
		Teams: []TeamRef{{Slug: "sageox", Path: teamRoot}},
	}
	entries, _, err := ListEntries(context.Background(), q)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
}

// TestListEntries_AutoResolvesLayer — Layer=auto must pick daily for a
// narrow window, weekly for a full ISO week, monthly for a full calendar
// month, and reflect the resolved layer in ListMeta.LayerResolved.
// Failure prevented: --layer=auto regression that leaves LayerResolved
// empty or mis-resolves.
func TestListEntries_AutoResolvesLayer(t *testing.T) {
	t.Parallel()
	teamRoot := t.TempDir()
	mustWriteDaily(t, teamRoot,
		"2026-04-12-019c8a3f.md", sampleDailyBody,
		time.Date(2026, 4, 12, 12, 0, 0, 0, time.UTC))
	mustWriteLayerFile(t, teamRoot, "weekly",
		"2026-W15-019c8a0e.md", sampleWeeklyBody,
		time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC))
	mustWriteLayerFile(t, teamRoot, "monthly",
		"2026-04.md", sampleMonthlyBody,
		time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC))

	cases := []struct {
		name      string
		since     time.Time
		until     time.Time
		wantLayer Layer
	}{
		{
			name:      "24h window resolves daily",
			since:     time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC),
			until:     time.Date(2026, 4, 12, 23, 59, 0, 0, time.UTC),
			wantLayer: LayerDaily,
		},
		{
			name:      "full ISO week resolves weekly",
			since:     time.Date(2026, 4, 6, 0, 0, 0, 0, time.UTC),
			until:     time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC),
			wantLayer: LayerWeekly,
		},
		{
			name:      "full calendar month resolves monthly",
			since:     time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
			until:     time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
			wantLayer: LayerMonthly,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q := ReadQuery{
				Since: tc.since,
				Until: tc.until,
				Layer: LayerAuto,
				Teams: []TeamRef{{Slug: "sageox", Path: teamRoot}},
			}
			_, meta, err := ListEntries(context.Background(), q)
			if err != nil {
				t.Fatalf("ListEntries: %v", err)
			}
			if meta.LayerResolved != tc.wantLayer {
				t.Fatalf("LayerResolved = %q, want %q", meta.LayerResolved, tc.wantLayer)
			}
		})
	}
}

// TestListEntries_WeeklyMonthlyMalformedFilenamesWarn — the weekly and
// monthly walkers must surface malformed filenames as ListMeta.Warnings
// and keep going, not halt the listing. Covers both regex-miss paths
// (no YYYY-Www or YYYY-MM prefix) and parse-failure paths (week out of
// range for weekly, month > 12 for monthly).
//
// Failure prevented: a single stray file in memory/weekly or
// memory/monthly crashes the whole list, or hides valid neighbors.
func TestListEntries_WeeklyMonthlyMalformedFilenamesWarn(t *testing.T) {
	t.Parallel()
	teamRoot := t.TempDir()

	// Regex-miss: neither matches ^(\d{4})-W(\d{2}) / ^(\d{4}-\d{2}).
	mustWriteLayerFile(t, teamRoot, "weekly",
		"not-a-week.md", sampleWeeklyBody,
		time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC))
	mustWriteLayerFile(t, teamRoot, "monthly",
		"not-a-month.md", sampleMonthlyBody,
		time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC))
	// Parse-failure: regex matches but the extracted values are out of
	// range. Week 00 fails the `week < 1` check; month 13 fails
	// time.ParseInLocation for "2006-01".
	mustWriteLayerFile(t, teamRoot, "weekly",
		"2026-W00-019c0000.md", sampleWeeklyBody,
		time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC))
	mustWriteLayerFile(t, teamRoot, "monthly",
		"2026-13.md", sampleMonthlyBody,
		time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC))
	// A neighboring valid file to prove the walker does not give up.
	mustWriteLayerFile(t, teamRoot, "weekly",
		"2026-W15-019c8a0e.md", sampleWeeklyBody,
		time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC))

	q := ReadQuery{
		Since: time.Date(2026, 4, 6, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC),
		Layer: LayerWeekly,
		Teams: []TeamRef{{Slug: "sageox", Path: teamRoot}},
	}
	entries, meta, err := ListEntries(context.Background(), q)
	if err != nil {
		t.Fatalf("ListEntries weekly: %v", err)
	}
	if len(entries) != 1 || entries[0].Date != "2026-04-06" {
		t.Fatalf("weekly entries = %+v, want 1 row for W15", entries)
	}
	sawMalformedFilename := false
	sawMalformedDate := false
	for _, w := range meta.Warnings {
		if w.Path == "memory/weekly/not-a-week.md" && w.Code == "malformed_filename" {
			sawMalformedFilename = true
		}
		if w.Path == "memory/weekly/2026-W00-019c0000.md" && w.Code == "malformed_date" {
			sawMalformedDate = true
		}
	}
	if !sawMalformedFilename {
		t.Errorf("weekly warnings missing malformed_filename for not-a-week.md: %+v", meta.Warnings)
	}
	if !sawMalformedDate {
		t.Errorf("weekly warnings missing malformed_date for W00 file: %+v", meta.Warnings)
	}

	q.Layer = LayerMonthly
	q.Since = time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	q.Until = time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	_, meta, err = ListEntries(context.Background(), q)
	if err != nil {
		t.Fatalf("ListEntries monthly: %v", err)
	}
	sawMalformedFilename = false
	sawMalformedDate = false
	for _, w := range meta.Warnings {
		if w.Path == "memory/monthly/not-a-month.md" && w.Code == "malformed_filename" {
			sawMalformedFilename = true
		}
		if w.Path == "memory/monthly/2026-13.md" && w.Code == "malformed_date" {
			sawMalformedDate = true
		}
	}
	if !sawMalformedFilename {
		t.Errorf("monthly warnings missing malformed_filename for not-a-month.md: %+v", meta.Warnings)
	}
	if !sawMalformedDate {
		t.Errorf("monthly warnings missing malformed_date for 2026-13.md: %+v", meta.Warnings)
	}
}

func TestListEntries_EmptyTeamPathErrors(t *testing.T) {
	t.Parallel()
	// A TeamRef with an empty Path is a caller bug — listDailyForTeam
	// must surface it as an error rather than happily walking "".
	q := ReadQuery{
		Since: time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC),
		Layer: LayerDaily,
		Teams: []TeamRef{{Slug: "sageox", Path: ""}},
	}
	if _, _, err := ListEntries(context.Background(), q); err == nil {
		t.Fatalf("want error for empty TeamRef.Path")
	}
}

func TestListEntries_UnreadableDirErrors(t *testing.T) {
	t.Parallel()
	// A non-ErrNotExist failure from os.ReadDir (e.g. permission denied)
	// must surface as an error rather than silently emptying out, so an
	// ops failure does not look like a clean "no entries".
	teamRoot := t.TempDir()
	dailyDirPath := filepath.Join(teamRoot, "memory", "daily")
	if err := os.MkdirAll(dailyDirPath, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Drop a valid file so the dir is not empty, then chmod 0 to force
	// a read error. Restored via t.Cleanup so the tempdir can be removed.
	apr12 := time.Date(2026, 4, 12, 10, 0, 0, 0, time.UTC)
	mustWriteDaily(t, teamRoot, "2026-04-12-019c8a3f.md", sampleDailyBody, apr12)
	if err := os.Chmod(dailyDirPath, 0o000); err != nil {
		t.Skipf("chmod 0 unsupported: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dailyDirPath, 0o755) })

	q := ReadQuery{
		Since: time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC),
		Layer: LayerDaily,
		Teams: []TeamRef{{Slug: "sageox", Path: teamRoot}},
	}
	if _, _, err := ListEntries(context.Background(), q); err == nil {
		t.Fatalf("want error when daily dir is unreadable")
	}
}

func TestListEntries_ContextCancelledMidWalk(t *testing.T) {
	t.Parallel()
	// Canceling the context must stop the walk and surface ctx.Err()
	// rather than continuing to parse files. The guarantee is a
	// cancelable long-running call (e.g. a huge weekly window) that
	// honors the caller's deadline.
	teamRoot := t.TempDir()
	apr12 := time.Date(2026, 4, 12, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		mustWriteDaily(t,
			teamRoot,
			"2026-04-12-019c8a"+string(rune('a'+i))+"f.md",
			sampleDailyBody,
			apr12.Add(time.Duration(i)*time.Minute))
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	q := ReadQuery{
		Since: time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC),
		Layer: LayerDaily,
		Teams: []TeamRef{{Slug: "sageox", Path: teamRoot}},
	}
	_, _, err := ListEntries(ctx, q)
	if err == nil {
		t.Fatalf("want error from canceled context")
	}
}

func TestListEntries_NoTeamErrors(t *testing.T) {
	t.Parallel()
	q := ReadQuery{
		Since: time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC),
		Layer: LayerDaily,
	}
	_, _, err := ListEntries(context.Background(), q)
	if err == nil {
		t.Fatalf("want error when Teams is empty")
	}
}

func TestStubsReturnNotImplemented(t *testing.T) {
	t.Parallel()
	// Unit 1 ships LoadEntries and Since as stubs; Units 4/5 wire them.
	// These tests exist so the signatures and error contract are pinned
	// from day one — a silent stub change would break upstream.
	q := ReadQuery{
		Since: time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC),
		Layer: LayerDaily,
	}
	if _, err := LoadEntries(context.Background(), q, nil); err == nil {
		t.Fatalf("LoadEntries: want sentinel error")
	}
	if _, _, _, err := Since(context.Background(), q); err == nil {
		t.Fatalf("Since: want sentinel error")
	}
}

func TestPathHelpers(t *testing.T) {
	t.Parallel()
	root := "/tmp/team"
	wantDaily := filepath.Join(root, "memory", "daily")
	wantWeekly := filepath.Join(root, "memory", "weekly")
	wantMonthly := filepath.Join(root, "memory", "monthly")
	if got := dailyDir(root); got != wantDaily {
		t.Fatalf("dailyDir = %q", got)
	}
	if got := weeklyDir(root); got != wantWeekly {
		t.Fatalf("weeklyDir = %q", got)
	}
	if got := monthlyDir(root); got != wantMonthly {
		t.Fatalf("monthlyDir = %q", got)
	}
}

func TestListEntries_BadQuery(t *testing.T) {
	t.Parallel()
	// Per plan §2.1 the CLI layer resolves Since/Until to non-zero UTC
	// instants before calling the reader. The guard at list.go:34-35
	// enforces that contract — a future refactor that reintroduces an
	// "unbounded" meaning for the zero time would silently fall through
	// the walk without it. These rows pin both halves of the guard plus
	// the classic Until<Since case.
	apr10 := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	apr12 := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	teamRoot := t.TempDir()
	cases := []struct {
		name  string
		since time.Time
		until time.Time
	}{
		{"until before since", apr12, apr10},
		{"zero since", time.Time{}, apr12},
		{"zero until", apr10, time.Time{}},
		{"both zero", time.Time{}, time.Time{}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q := ReadQuery{
				Since: tc.since,
				Until: tc.until,
				Layer: LayerDaily,
				Teams: []TeamRef{{Slug: "sageox", Path: teamRoot}},
			}
			if _, _, err := ListEntries(context.Background(), q); err == nil {
				t.Fatalf("want error for %s", tc.name)
			}
		})
	}
}
