package read

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSinceEntries_EmptyWindow — an empty window returns non-nil
// empty parallel slices + nil error. The spec §3.6 exit-codes section
// requires success:true with `entries: []` and empty bodies for the
// empty-result case, which means the envelope builder must never
// encode `null` here — hence the non-nil empty slices.
//
// Failure prevented: a refactor returns `nil, nil, meta, nil` and
// the envelope emits `entries: null` / `bodies: null`, breaking
// callers that iterate without a nil check.
func TestSinceEntries_EmptyWindow(t *testing.T) {
	t.Parallel()
	team := stageSinceFixture(t)

	// Window that ends BEFORE the fixture's earliest entry. All three
	// fixture files are dated 2026-04-12+; the window here closes in
	// 2026-04-01.
	q := ReadQuery{
		Since:    time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		Until:    time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		Layer:    LayerDaily,
		Teams:    []TeamRef{team},
		WantBody: true,
	}
	entries, bodies, _, err := sinceEntries(context.Background(), q)
	if err != nil {
		t.Fatalf("sinceEntries: %v", err)
	}
	if entries == nil {
		t.Fatalf("entries is nil — must be an empty slice")
	}
	if bodies == nil {
		t.Fatalf("bodies is nil — must be an empty slice")
	}
	if len(entries) != 0 || len(bodies) != 0 {
		t.Fatalf("entries=%d bodies=%d, want both 0", len(entries), len(bodies))
	}
}

// TestSinceEntries_ParallelSlices — happy path: fixture has three
// same-day entries, all in window. Assert:
//
//   - len(entries) == len(bodies)
//   - entries are ordered Date asc / CreatedAt asc (Unit 3 contract)
//   - bodies[i] is the non-empty, frontmatter-stripped markdown for
//     entries[i].ID (parallel-slice correspondence)
//   - bodies are frontmatter-stripped (no leading "---")
//
// Failure prevented: a refactor that breaks parallel-slice alignment
// (reordering loaded rows, skipping read_error rows silently,
// dropping the frontmatter strip) — any of these would desync bodies
// from entries and feed agents the wrong content with the wrong
// marker.
func TestSinceEntries_ParallelSlices(t *testing.T) {
	t.Parallel()
	team := stageSinceFixture(t)

	q := ReadQuery{
		Since:    time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
		Until:    time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC),
		Layer:    LayerDaily,
		Teams:    []TeamRef{team},
		WantBody: true,
	}
	entries, bodies, meta, err := sinceEntries(context.Background(), q)
	if err != nil {
		t.Fatalf("sinceEntries: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("len(entries) = %d, want 3: %+v", len(entries), entries)
	}
	if len(bodies) != len(entries) {
		t.Fatalf("len(bodies)=%d != len(entries)=%d", len(bodies), len(entries))
	}

	// Ordering: Date asc then CreatedAt asc. The fixture is staged
	// so 11 < 12a < 12b, mtimes are monotonic, so the expected order
	// is ["2026-04-11-019c7777-0001", "2026-04-12-019c8a3f-0001",
	// "2026-04-12-019c8a3f-0002"].
	wantIDs := []string{
		"2026-04-11-019c7777-0001",
		"2026-04-12-019c8a3f-0001",
		"2026-04-12-019c8a3f-0002",
	}
	for i, want := range wantIDs {
		if entries[i].ID != want {
			t.Fatalf("entries[%d].ID = %q, want %q (full: %+v)",
				i, entries[i].ID, want, entries)
		}
		if entries[i].Status != "ok" {
			t.Fatalf("entries[%d].Status = %q, want ok", i, entries[i].Status)
		}
		if bodies[i] == "" {
			t.Fatalf("bodies[%d] empty for id=%s", i, entries[i].ID)
		}
		if strings.HasPrefix(bodies[i], "---") {
			t.Fatalf("bodies[%d] still has frontmatter fence: %q", i, bodies[i][:20])
		}
	}

	// Meta window comes from the bodyless list scan — day-rounded
	// per spec.
	wantSince := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	wantUntil := time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)
	if !meta.EffectiveSince.Equal(wantSince) {
		t.Fatalf("EffectiveSince = %v, want %v", meta.EffectiveSince, wantSince)
	}
	if !meta.EffectiveUntil.Equal(wantUntil) {
		t.Fatalf("EffectiveUntil = %v, want %v", meta.EffectiveUntil, wantUntil)
	}
}

// TestSinceEntries_LimitAppliedBeforeLoad — list applies Limit, then
// load materializes only the winners. Failure prevented: a refactor
// loads every file in the window and then truncates, wasting I/O and
// masking the real-world pressure that Limit exists to relieve.
func TestSinceEntries_LimitAppliedBeforeLoad(t *testing.T) {
	t.Parallel()
	team := stageSinceFixture(t)

	q := ReadQuery{
		Since:    time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
		Until:    time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC),
		Layer:    LayerDaily,
		Teams:    []TeamRef{team},
		Limit:    2,
		WantBody: true,
	}
	entries, bodies, meta, err := sinceEntries(context.Background(), q)
	if err != nil {
		t.Fatalf("sinceEntries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	if len(bodies) != 2 {
		t.Fatalf("len(bodies) = %d, want 2", len(bodies))
	}
	if !meta.Truncated {
		t.Fatalf("meta.Truncated = false, want true — list should have flagged the limit hit")
	}
}

// TestListEntries_NoTimeFilter — explicit regression for the Unit 5
// I-2 migration. When NoTimeFilter is true, listEntries must return
// every entry scoped to Layer + Teams regardless of the query's
// Since/Until (which may be zero values). This is the hook
// runDistillHistoryShow uses to mean "whole history".
//
// Failure prevented: the Since/Until zero check still fires under
// NoTimeFilter, or the per-layer walker's effSince/effUntil sentinels
// accidentally exclude files at the year-boundary.
func TestListEntries_NoTimeFilter(t *testing.T) {
	t.Parallel()
	team := stageSinceFixture(t)

	q := ReadQuery{
		NoTimeFilter: true,
		Layer:        LayerDaily,
		Teams:        []TeamRef{team},
	}
	entries, _, err := listEntries(context.Background(), q)
	if err != nil {
		t.Fatalf("listEntries NoTimeFilter: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("len(entries) = %d, want 3 (all fixture files): %+v", len(entries), entries)
	}
}

// TestListEntries_NoTimeFilter_RejectsAutoLayer — NoTimeFilter with
// LayerAuto is a whole-call error because layer auto-resolution
// reads the window width to pick the layer, which is undefined when
// there is no window. Failure prevented: a silent pick of LayerDaily
// from a wide sentinel window, which would be a semi-random default.
func TestListEntries_NoTimeFilter_RejectsAutoLayer(t *testing.T) {
	t.Parallel()
	team := stageSinceFixture(t)

	q := ReadQuery{
		NoTimeFilter: true,
		Layer:        LayerAuto,
		Teams:        []TeamRef{team},
	}
	if _, _, err := listEntries(context.Background(), q); err == nil {
		t.Fatalf("want error for NoTimeFilter + LayerAuto, got nil")
	}
}

// stageSinceFixture writes three daily entries (one 2026-04-11, two
// same-day 2026-04-12) under a fresh temp team root and returns a
// TeamRef. Mtimes are monotonic so sortEntries can be exercised.
//
// The 2026-04-12 pair has the same UUID7 prefix but different
// snapshot suffixes, matching the real stem shape the distiller
// produces (<date>-<uuid7>-<NNNN>.md).
func stageSinceFixture(t *testing.T) TeamRef {
	t.Helper()
	root := t.TempDir()
	dailyDir := filepath.Join(root, "memory", "daily")
	if err := os.MkdirAll(dailyDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	fixtures := []struct {
		name string
		body string
		when time.Time
	}{
		{
			name: "2026-04-11-019c7777-0001.md",
			body: sinceFixtureBody("2026-04-11", "Monday rollup."),
			when: time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC),
		},
		{
			name: "2026-04-12-019c8a3f-0001.md",
			body: sinceFixtureBody("2026-04-12", "Tuesday morning rollup."),
			when: time.Date(2026, 4, 12, 9, 0, 0, 0, time.UTC),
		},
		{
			name: "2026-04-12-019c8a3f-0002.md",
			body: sinceFixtureBody("2026-04-12", "Tuesday afternoon rollup."),
			when: time.Date(2026, 4, 12, 18, 0, 0, 0, time.UTC),
		},
	}
	for _, f := range fixtures {
		p := filepath.Join(dailyDir, f.name)
		if err := os.WriteFile(p, []byte(f.body), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
		if err := os.Chtimes(p, f.when, f.when); err != nil {
			t.Fatalf("chtimes %s: %v", p, err)
		}
	}
	return TeamRef{Slug: "since-fixture", Path: root}
}

func sinceFixtureBody(date, note string) string {
	return "---\nsources:\n  - memory/.github-facts/" + date + "-source.jsonl\n---\n# Daily Memory — " + date + "\n\n" + note + "\n"
}
