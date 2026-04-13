//go:build slow

// journal_read_e2e_list_test.go — JR-* skeletons for `ox journal list`.
// All 14 cases that exercise the list command. Harness, fixtures, and
// recipes live in journal_read_e2e_test.go; this file only adds the
// per-case test funcs.
//
// Each skeleton stages its fixture and invokes the real ox subprocess
// BEFORE calling t.Skip. The Skip guard is narrow by design: it covers
// only the assertion block. That way a regression in the harness
// (binary build, XDG reroute, recipe staging) still fails this file
// even while the assertion block is skip-gated. Once the implementer
// lands Unit 3 (ox journal list + journal_time.go), replace the Skip
// with real assertions on the JSON envelope.
//
// See docs/ai/specs/journal-read-test-plan.md §2 rows JR-01..JR-03,
// JR-11..JR-15, JR-19..JR-24.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// skipPendingUnit3 gates the assertion block of every list skeleton
// until Unit 3 of journal-read-plan.md lands.
const skipPendingUnit3 = "impl not landed yet: depends on Unit 3 (ox journal list + journal_time.go)"

// TestJournalRead_JR01_MinimalDaily_List — one in-window daily.
// Failure prevented: reader cannot locate memory/daily/ under the
// staged team context at all.
func TestJournalRead_JR01_MinimalDaily_List(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	e := setupJournalE2E(t, now)
	fx := recipeMinimalDaily(t, e.primaryTeam, now)

	out, exit := e.Run(t, "journal", "list", "--since=24h", "--format=json")
	t.Logf("JR-01 exit=%d out_bytes=%d dailies=%d", exit, len(out), len(fx.Dailies))

	t.Skip(skipPendingUnit3)
	// TODO Unit 3: assert exit 0; parse envelope; entries has exactly one row
	// with id=fx.Dailies[0].ID, layer=daily, date=fx.Dailies[0].Date, path set,
	// fact_count, citation_count, source_files; window.since/until are RFC3339 Z;
	// window.layer_resolved=daily.
}

// TestJournalRead_JR02_EmptyWindow_YesterdayOnly — 1h window over a
// team that only has yesterday's daily. Failure prevented: day-rounding
// rule wrong, or reader errors on empty windows.
func TestJournalRead_JR02_EmptyWindow_YesterdayOnly(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	if now.Hour() == 23 && now.Minute() >= 59 {
		t.Skip("within seconds of UTC midnight — window rounding unstable, retry")
	}
	e := setupJournalE2E(t, now)
	fx := recipeYesterdayOnly(t, e.primaryTeam, now)

	out, exit := e.Run(t, "journal", "list", "--since=1h", "--format=json")
	t.Logf("JR-02 exit=%d out_bytes=%d dailies=%d", exit, len(out), len(fx.Dailies))

	t.Skip(skipPendingUnit3)
	// TODO Unit 3: exit 0, success=true, data.entries=[], data.truncated=false,
	// window.since=today 00:00:00Z, window.until=tomorrow 00:00:00Z, no warnings.
}

// TestJournalRead_JR03_MultiSnapshotSameDay_ListOrdering — two
// snapshots same day, older mtime first. Failure prevented: reader
// dedupes by date and hides one, or ordering is wrong (spec §5.c).
func TestJournalRead_JR03_MultiSnapshotSameDay_ListOrdering(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	e := setupJournalE2E(t, now)
	fx := recipeMultiSnapshotSameDay(t, e.primaryTeam, now)

	out, exit := e.Run(t, "journal", "list", "--since=24h", "--format=json")
	t.Logf("JR-03 exit=%d out_bytes=%d dailies=%d", exit, len(out), len(fx.Dailies))

	t.Skip(skipPendingUnit3)
	// TODO Unit 3: two entries, ordered by date asc then created_at asc →
	// fx.Dailies[0] (older mtime) before fx.Dailies[1] (newer). Both share
	// date=today.
}

// TestJournalRead_JR11_MalformedFilename_ListWarnsNotCrash — a stray
// non-date file alongside one valid daily. Failure prevented: reader
// crashes/fails the whole call on a single malformed filename.
func TestJournalRead_JR11_MalformedFilename_ListWarnsNotCrash(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	e := setupJournalE2E(t, now)
	fx := recipeMalformedFilename(t, e.primaryTeam, now)

	out, exit := e.Run(t, "journal", "list", "--since=24h", "--format=json")
	t.Logf("JR-11 exit=%d out_bytes=%d dailies=%d extras=%d",
		exit, len(out), len(fx.Dailies), len(fx.ExtraFiles))

	t.Skip(skipPendingUnit3)
	// TODO Unit 3: exit 0, entries has exactly 1 row (the valid file), stderr
	// carries a warning naming not-a-date.md, no error envelope, no panic.
}

// TestJournalRead_JR12_EmptyMarkerOnly_ListIgnoresFactDirs — no
// dailies; one fact file. Failure prevented: daily reader walks into
// memory/.github-facts/ and surfaces fact files as daily entries.
func TestJournalRead_JR12_EmptyMarkerOnly_ListIgnoresFactDirs(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	e := setupJournalE2E(t, now)
	fx := recipeEmptyMarkerOnly(t, e.primaryTeam, now)

	out, exit := e.Run(t, "journal", "list", "--since=24h", "--format=json")
	t.Logf("JR-12 exit=%d out_bytes=%d dailies=%d facts=%d",
		exit, len(out), len(fx.Dailies), len(fx.Facts))

	t.Skip(skipPendingUnit3)
	// TODO Unit 3: exit 0, data.entries=[]; fact file not surfaced; no error.
}

// TestJournalRead_JR13_MixedTzAndUtcLegacy_TrustsPrefix — legacy +
// modern daily files. Failure prevented: reader re-derives date from
// UUID7 instead of trusting the filename prefix (spec §5.b option a);
// stderr carries a spurious legacy warning despite the silent policy.
func TestJournalRead_JR13_MixedTzAndUtcLegacy_TrustsPrefix(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	e := setupJournalE2E(t, now)
	fx := recipeMixedTzAndUtcLegacy(t, e.primaryTeam, now)

	out, exit := e.Run(t, "journal", "list", "--since=48h", "--format=json")
	t.Logf("JR-13 exit=%d out_bytes=%d dailies=%d", exit, len(out), len(fx.Dailies))

	t.Skip(skipPendingUnit3)
	// TODO Unit 3: both entries returned; legacy has date=yesterday,
	// modern has date=today; ordered by date asc → legacy first;
	// stderr is SILENT (no legacy warning). This is the spec §5.b trust-prefix
	// lock-in.
}

// TestJournalRead_JR14_MultiTeamDefault_SingleTeam — default list only
// returns the active team. Failure prevented: reader defaults to
// all-teams and leaks the secondary team's entry; or team field missing.
func TestJournalRead_JR14_MultiTeamDefault_SingleTeam(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	e := setupJournalE2E(t, now)
	secondary := addSecondaryTeam(t, e, "team_read_e2e_t2", "Journal Read E2E T2", "journal-read-e2e-t2")
	fx := recipeMultiTeamList(t, e.primaryTeam, secondary, now)

	out, exit := e.Run(t, "journal", "list", "--since=24h", "--format=json")
	t.Logf("JR-14 exit=%d out_bytes=%d dailies=%d", exit, len(out), len(fx.Dailies))

	t.Skip(skipPendingUnit3)
	// TODO Unit 3: exit 0, entries has exactly 1 row (primary only); that row's
	// team field equals e.primaryTeam.id; secondary's daily is NOT present.
}

// TestJournalRead_JR15_MultiTeamAllTeams_Merged — --all-teams merges
// both teams with stable ordering. Failure prevented: cross-team merge
// fails or ordering collapses to insertion order (spec §5.g).
func TestJournalRead_JR15_MultiTeamAllTeams_Merged(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	e := setupJournalE2E(t, now)
	secondary := addSecondaryTeam(t, e, "team_read_e2e_t2", "Journal Read E2E T2", "journal-read-e2e-t2")
	fx := recipeMultiTeamList(t, e.primaryTeam, secondary, now)

	out, exit := e.Run(t, "journal", "list", "--since=24h", "--all-teams", "--format=json")
	t.Logf("JR-15 exit=%d out_bytes=%d dailies=%d", exit, len(out), len(fx.Dailies))

	t.Skip(skipPendingUnit3)
	// TODO Unit 3: two entries; ordering = date asc, created_at asc, then team
	// slug asc as tiebreaker; each row carries its own team field.
}

// TestJournalRead_JR19_TeamNotFound_ErrorEnvelope — no team context
// registered. Failure prevented: reader panics on missing team, or
// returns empty-success (which hides misconfiguration from agents).
func TestJournalRead_JR19_TeamNotFound_ErrorEnvelope(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	e := setupJournalE2E(t, now)

	// Wipe the team context: empty config.local.toml and remove the
	// primary team root so nothing the reader walks finds a registered team.
	emptyCfg := filepath.Join(e.workspace, ".sageox", "config.local.toml")
	if err := os.WriteFile(emptyCfg, []byte(""), 0o600); err != nil {
		t.Fatalf("wipe config.local.toml: %v", err)
	}
	if err := os.RemoveAll(e.primaryTeam.path); err != nil {
		t.Fatalf("remove primary team path: %v", err)
	}

	out, exit := e.Run(t, "journal", "list", "--since=24h", "--format=json")
	t.Logf("JR-19 exit=%d out_bytes=%d", exit, len(out))

	t.Skip(skipPendingUnit3)
	// TODO Unit 3: exit 1, success=false, error.code="team_not_found",
	// retryable=false.
}

// TestJournalRead_JR20_UsageError_InvalidSince — bad duration flag.
// Failure prevented: usage errors land on exit 1 instead of exit 2,
// collapsing "bad flag" and "real failure" for agents.
func TestJournalRead_JR20_UsageError_InvalidSince(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	e := setupJournalE2E(t, now)
	fx := recipeMinimalDaily(t, e.primaryTeam, now)

	out, exit := e.Run(t, "journal", "list", "--since=not-a-duration", "--format=json")
	t.Logf("JR-20 exit=%d out_bytes=%d dailies=%d", exit, len(out), len(fx.Dailies))

	t.Skip(skipPendingUnit3)
	// TODO Unit 3: exit 2; stdout is JSON error envelope with
	// error.code="usage_error" (or equivalent).
}

// TestJournalRead_JR21_AbsoluteTzRoundTrip — LA-local day window that
// spans UTC midnight. Failure prevented: --tz ignored, offset applied
// wrong direction, or rounding computed before conversion (spec §3.4).
func TestJournalRead_JR21_AbsoluteTzRoundTrip(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	if now.Hour() == 23 && now.Minute() >= 59 {
		t.Skip("within seconds of UTC midnight — window rounding unstable, retry")
	}
	e := setupJournalE2E(t, now)
	fx := recipeTwoConsecutiveUtcDays(t, e.primaryTeam, now)

	// Absolute window derived from the captured `now`: "yesterday in LA,
	// the whole day". Once converted to UTC this straddles UTC midnight
	// and, after outward day-rounding, covers both yesterday(now) and
	// today(now) UTC days — matching the two files the recipe staged.
	yesterday := dayString(daysBack(now, 1))
	since := fmt.Sprintf("%sT00:00", yesterday)
	until := fmt.Sprintf("%sT23:59", yesterday)

	out, exit := e.Run(t, "journal", "list",
		"--since="+since,
		"--until="+until,
		"--tz=America/Los_Angeles",
		"--format=json")
	t.Logf("JR-21 exit=%d out_bytes=%d dailies=%d since=%s until=%s",
		exit, len(out), len(fx.Dailies), since, until)

	t.Skip(skipPendingUnit3)
	// TODO Unit 3: exit 0; both files returned; window.since = yesterday 00:00Z;
	// window.until = <yesterday+2d> 00:00Z (the 48h rounded window).
}

// TestJournalRead_JR22_TzConflictAndInvalid — two usage subtests on
// malformed --tz combinations. Failure prevented: conflict silently
// honored, invalid zone defaults to UTC or panics.
func TestJournalRead_JR22_TzConflictAndInvalid(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	e := setupJournalE2E(t, now)

	// Both subtests share the same harness/binary — neither mutates disk
	// state and neither cares about fixtures. They only differ in flags.

	t.Run("A_conflicting_tz", func(t *testing.T) {
		out, exit := e.Run(t, "journal", "list",
			"--since=2026-04-12T09:00-07:00",
			"--tz=America/Los_Angeles",
			"--format=json")
		t.Logf("JR-22A exit=%d out_bytes=%d", exit, len(out))

		t.Skip(skipPendingUnit3)
		// TODO Unit 3: exit 2; JSON error envelope; error.code="usage_error";
		// message mentions "conflicting timezone" (or equivalent).
	})

	t.Run("B_invalid_zone", func(t *testing.T) {
		out, exit := e.Run(t, "journal", "list",
			"--since=2026-04-12T09:00",
			"--tz=Not/A/Real/Zone",
			"--format=json")
		t.Logf("JR-22B exit=%d out_bytes=%d", exit, len(out))

		t.Skip(skipPendingUnit3)
		// TODO Unit 3: exit 2; JSON error envelope; error.code="usage_error";
		// message mentions "invalid timezone" (or equivalent).
	})
}

// TestJournalRead_JR23_EffectiveWindowRounding — envelope reports
// day-rounded boundaries, not raw --since/--until. Failure prevented:
// CLI leaks raw input instants, silently defeating caller's ability to
// detect rounding.
func TestJournalRead_JR23_EffectiveWindowRounding(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	if now.Hour() == 23 && now.Minute() >= 59 {
		t.Skip("within seconds of UTC midnight — window rounding unstable, retry")
	}
	e := setupJournalE2E(t, now)
	fx := recipeMinimalDaily(t, e.primaryTeam, now)

	out, exit := e.Run(t, "journal", "list", "--since=6h", "--format=json")
	t.Logf("JR-23 exit=%d out_bytes=%d dailies=%d", exit, len(out), len(fx.Dailies))

	t.Skip(skipPendingUnit3)
	// TODO Unit 3: exit 0; entries has the one minimal daily;
	// window.since = day-floor(now - 6h).Format(RFC3339) (not raw now-6h),
	// window.until = day-ceil(now).Format(RFC3339) (not raw now).
}

// TestJournalRead_JR24_StaleFilenameRecentMtime_EventDayWins — 90-day-
// old filename with recent mtime. Failure prevented: reader uses mtime
// or UUID7 for window filtering instead of filename prefix, breaking
// the event-day vs write-time contract. Single most important semantic
// lock-in for spec §2.1.
func TestJournalRead_JR24_StaleFilenameRecentMtime_EventDayWins(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	if now.Hour() == 23 && now.Minute() >= 59 {
		t.Skip("within seconds of UTC midnight — window rounding unstable, retry")
	}
	e := setupJournalE2E(t, now)
	fx := recipeStaleFilenameRecentMtime(t, e.primaryTeam, now)

	out, exit := e.Run(t, "journal", "list", "--since=24h", "--format=json")
	t.Logf("JR-24 exit=%d out_bytes=%d dailies=%d", exit, len(out), len(fx.Dailies))

	t.Skip(skipPendingUnit3)
	// TODO Unit 3: exit 0; data.entries=[]; window.since=day-floor(now-24h);
	// window.until=day-ceil(now); the stale-filename file is EXCLUDED because
	// the reader filters by filename prefix (90 days ago), not by mtime.
}
