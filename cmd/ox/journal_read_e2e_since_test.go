//go:build slow

// journal_read_e2e_since_test.go — JR-* skeletons for `ox journal since`.
// The 3 cases that exercise the since composite (list + load + format).
// Harness, fixtures, and recipes live in journal_read_e2e_test.go;
// this file only adds the per-case test funcs.
//
// Fixture staging and subprocess invocation execute BEFORE t.Skip, so
// a regression in the harness still trips this file even while the
// assertion block is skip-gated. Once Unit 5 (Since + ox journal since)
// lands, replace each Skip with real assertions.
//
// See docs/ai/specs/journal-read-test-plan.md §2 rows JR-09, JR-10,
// JR-16.

package main

import (
	"testing"
	"time"
)

// skipPendingUnit5 gates the assertion block of every since skeleton
// until Unit 5 of journal-read-plan.md lands.
const skipPendingUnit5 = "impl not landed yet: depends on Unit 5 (Since + ox journal since)"

// TestJournalRead_JR09_SinceContentFormat_ChronoOrder — since-24h
// content format emits in-window bodies in chronological order with
// per-entry markers. Failure prevented: ordering reversed, window
// off-by-one day, or entry marker wrong shape (spec §3.6).
func TestJournalRead_JR09_SinceContentFormat_ChronoOrder(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	if now.Hour() == 23 && now.Minute() >= 59 {
		t.Skip("within seconds of UTC midnight — window rounding unstable, retry")
	}
	e := setupJournalE2E(t, now)
	fx := recipeThreeDays(t, e.primaryTeam, now)

	out, exit := e.Run(t, "journal", "since", "24h", "--format=content")
	t.Logf("JR-09 exit=%d out_bytes=%d dailies=%d", exit, len(out), len(fx.Dailies))

	t.Skip(skipPendingUnit5)
	// TODO Unit 5: exit 0; stdout contains the middle (now-24h) and newest
	// (now-6h) bodies in chronological order, separated by "\n---\n", each
	// preceded by a "<!-- entry: <id> | layer: daily | date: <date> -->"
	// marker; the 48h file is EXCLUDED (out of window).
}

// TestJournalRead_JR10_SinceJsonFormat_ParallelBodies — since-24h JSON
// format returns entries + parallel bodies array, day-rounded window.
// Failure prevented: bodies missing from JSON, raw now-24h reported
// instead of day-floored, or window reported as 24h wide instead of
// rounded N×24h.
func TestJournalRead_JR10_SinceJsonFormat_ParallelBodies(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	if now.Hour() == 23 && now.Minute() >= 59 {
		t.Skip("within seconds of UTC midnight — window rounding unstable, retry")
	}
	e := setupJournalE2E(t, now)
	fx := recipeThreeDays(t, e.primaryTeam, now)

	out, exit := e.Run(t, "journal", "since", "24h", "--format=json")
	t.Logf("JR-10 exit=%d out_bytes=%d dailies=%d", exit, len(out), len(fx.Dailies))

	t.Skip(skipPendingUnit5)
	// TODO Unit 5: exit 0; data.entries and data.bodies are parallel arrays
	// of length 2; window.since = day-floor(now - 24h); window.until =
	// day-ceil(now), both RFC3339 Z-suffixed. Tolerate ±few-seconds drift
	// between captured now and subprocess time.Now() (plan §4.3 item 2).
}

// TestJournalRead_JR16_SinceAllTeamsContent_ConcatenatedMarkers —
// since + --all-teams + content merges bodies across teams in chrono-
// then-team order with per-entry markers. Failure prevented: merge
// orders by team instead of date, or per-entry marker is missing.
func TestJournalRead_JR16_SinceAllTeamsContent_ConcatenatedMarkers(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	e := setupJournalE2E(t, now)
	secondary := addSecondaryTeam(t, e, "team_read_e2e_t2", "Journal Read E2E T2", "journal-read-e2e-t2")
	fx := recipeMultiTeamList(t, e.primaryTeam, secondary, now)

	out, exit := e.Run(t, "journal", "since", "24h", "--all-teams", "--format=content")
	t.Logf("JR-16 exit=%d out_bytes=%d dailies=%d", exit, len(out), len(fx.Dailies))

	t.Skip(skipPendingUnit5)
	// TODO Unit 5: exit 0; stdout concatenates both team bodies; each body is
	// preceded by "<!-- entry: <id> | layer: daily | date: ... -->"; ordering
	// is chronological first, then team slug as tiebreaker (spec §5.g).
	// Whether a per-team marker is ALSO included is an open question (plan §6);
	// this test asserts only the per-entry marker the spec commits to.
}
