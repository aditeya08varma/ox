package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/agentcli"
	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- runDistill partial-failure containment ---
//
// Before this fix, a single failing stage (e.g. a Claude rate-limit hit
// summarizing one week) aborted runDistill entirely via a bare `return
// fmt.Errorf(...)`, and saveDistillStateV2 — reached only at the very end —
// never ran, discarding any weekly/monthly bookkeeping from earlier in the
// same run. Failure prevented: a transient AI-coworker failure silently
// costing everything a distill run had already produced, not just the one
// failed item.

// sequencedBackend fails on its Nth call (1-indexed) and succeeds on every
// other call, so a test can force a specific stage to fail while later
// stages in the same run still get a chance to run (and prove they're not
// reached, if containment is broken).
type sequencedBackend struct {
	failOnCall int
	calls      int
}

func (b *sequencedBackend) Name() string    { return "sequenced-test-backend" }
func (b *sequencedBackend) Available() bool { return true }
func (b *sequencedBackend) Run(_ context.Context, _ string) (string, error) {
	b.calls++
	if b.calls == b.failOnCall {
		return "", fmt.Errorf("simulated: claude exited 1 (session limit)")
	}
	return "## Summary\n\nsynthesized content\n", nil
}

// isoWeekOf returns the ISO year/week for a "2006-01-02"-formatted date,
// matching how distillWeekly derives weekID from an isoWeek.
func isoWeekOf(t *testing.T, dateStr string) (year, week int) {
	t.Helper()
	parsed, err := time.Parse("2006-01-02", dateStr)
	require.NoError(t, err)
	year, week = parsed.ISOWeek()
	return year, week
}

// setupDistillWeeklyFixture builds a real project + git-backed team context
// with three daily memory files seeded in three distinct, already-due ISO
// weeks (all fall inside determineLayers' ~91-day lookback from a fresh
// distillStateV2), spaced far enough apart that each lands in its own week.
// Returns the team context path and the three seeded dates in chronological
// order — enumerateWeeks processes weeks oldest-to-newest, so week[0] is
// always attempted before week[1], which is always attempted before week[2].
// Three (not two) weeks matters: it's what makes it possible to distinguish
// "stopped after the failure" from "kept going past it" — with only two
// weeks, a failure on the last one looks identical either way.
func setupDistillWeeklyFixture(t *testing.T) (teamContextDir string, weeks [3]string) {
	t.Helper()

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	projectRoot := createInitializedProjectWithConfig(t, &config.ProjectConfig{
		ProjectID: "test-project",
		TeamID:    "test-team",
		Endpoint:  "https://fake.test.invalid",
	})

	teamContextDir = t.TempDir()
	require.NoError(t, exec.Command("git", "-C", teamContextDir, "init", "--initial-branch=main").Run())
	require.NoError(t, exec.Command("git", "-C", teamContextDir, "config", "user.email", "test@test.com").Run())
	require.NoError(t, exec.Command("git", "-C", teamContextDir, "config", "user.name", "Test").Run())

	require.NoError(t, config.SaveLocalConfig(projectRoot, &config.LocalConfig{
		TeamContexts: []config.TeamContext{
			{TeamID: "test-team", TeamName: "test-team", Path: teamContextDir},
		},
	}))

	dailyDir := filepath.Join(teamContextDir, "memory", "daily")
	require.NoError(t, os.MkdirAll(dailyDir, 0o755))

	// Anchor on the most recently fully-completed ISO week's LAST day
	// (Sunday), not "now" directly. runDistill's dailyBacklogComplete guard
	// requires the daily high-water mark (the latest dated file anywhere
	// under memory/daily/) to reach a week's END before rolling it up, so
	// the most-recent seeded file below must land exactly on ITS week's
	// Sunday — anchoring off "now" directly would only coincidentally
	// satisfy that on days the suite happens to run on a Sunday itself.
	// now.AddDate(0,0,-7) always falls in the immediately preceding ISO
	// week (weeks are fixed 7-day blocks), which has always fully elapsed
	// relative to now, making its Sunday a safe, deterministic anchor.
	now := time.Now().UTC()
	_, anchorSunday := isoWeekRange(now.AddDate(0, 0, -7).ISOWeek())
	// ~11, ~6, ~1 weeks before the anchor — comfortably inside the 91-day
	// lookback, spaced 5+ weeks apart so each unambiguously lands in its
	// own ISO week (same 70/35-day spacing as before).
	weeksBack := [3]int{-10, -5, 0}
	for i, wk := range weeksBack {
		d := anchorSunday.AddDate(0, 0, wk*7).Format("2006-01-02")
		weeks[i] = d
		require.NoError(t, os.WriteFile(filepath.Join(dailyDir, d+".md"),
			[]byte("## "+d+"\n\nDaily summary for "+d+".\n"), 0o644))
	}

	origCwd, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(origCwd) })
	require.NoError(t, os.Chdir(projectRoot))

	return teamContextDir, weeks
}

// withDistillFlags saves and restores the package-level distill flag vars
// runDistill reads directly (they're cobra.Command flag targets, not read
// via cmd.Flags() — see distillCmd's flag registration) so this test's
// mutations never leak into other tests in this package.
func withDistillFlags(t *testing.T, configure func()) {
	t.Helper()
	origLayer, origDryRun, origNoPush, origAll, origConcurrency := distillLayer, distillDryRun, distillNoPush, distillAll, distillConcurrency
	origDetect := detectAgentBackend
	t.Cleanup(func() {
		distillLayer, distillDryRun, distillNoPush, distillAll, distillConcurrency = origLayer, origDryRun, origNoPush, origAll, origConcurrency
		detectAgentBackend = origDetect
	})
	distillLayer, distillDryRun, distillNoPush, distillAll, distillConcurrency = "weekly", false, true, false, 1
	configure()
}

func TestRunDistill_WeeklyFailure_StopsRemainingWeeksButSavesCompletedState(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	teamContextDir, weeks := setupDistillWeeklyFixture(t)

	// week[0] (call 1) succeeds; week[1] (call 2) fails; week[2] must never
	// be attempted — that's the actual break-vs-continue distinction this
	// test exists to catch. With only two seeded weeks, a failure on the
	// last one is indistinguishable from "stopped" vs "kept going" (there's
	// nothing left to over-run into); three weeks makes it observable.
	backend := &sequencedBackend{failOnCall: 2}
	var projectRoot string
	withDistillFlags(t, func() {
		detectAgentBackend = func() (agentcli.Backend, error) { return backend, nil }
	})
	projectRoot, err := os.Getwd()
	require.NoError(t, err)

	runErr := runDistill(distillCmd, nil)
	require.Error(t, runErr, "a failing week must still surface as a non-zero result")
	assert.Contains(t, runErr.Error(), "weekly distill")
	assert.Equal(t, 2, backend.calls, "must stop after week[1]'s failure — week[2] must never be attempted")

	// the successful (week[0]) summary must exist, and ONLY that one...
	weeklyDir := filepath.Join(teamContextDir, "memory", "weekly")
	entries, err := os.ReadDir(weeklyDir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "only the succeeded week should have produced a summary file — not the failed week, and not the never-attempted third week")

	wantYear, wantWeek := isoWeekOf(t, weeks[0])
	wantWeekID := fmt.Sprintf("%d-W%02d", wantYear, wantWeek)
	assert.Contains(t, entries[0].Name(), wantWeekID, "the written summary must be week[0] (succeeded), not week[1] (failed) or week[2] (never attempted)")

	// ...and — the actual regression this test guards — that progress must
	// be PERSISTED to distill-state-v2.json itself despite the run
	// ultimately returning an error. Before the fix, saveDistillStateV2 was
	// only reached at the very end of runDistill, so a mid-run failure like
	// this one meant the state file was never written at all.
	//
	// Deliberately read the raw file instead of going through
	// loadDistillStateV2: that helper falls back to inferring LastWeekly by
	// scanning memory/weekly/*.md when no state file exists, which would
	// make this assertion pass even with the bug present (the weekly file
	// itself is written independently of state persistence) — a vacuous
	// test that catches nothing. Reading the file directly proves
	// saveDistillStateV2 specifically ran.
	statePath := filepath.Join(projectRoot, ".sageox", "cache", "distill-state-v2.json")
	data, statErr := os.ReadFile(statePath)
	require.NoError(t, statErr, "distill-state-v2.json must exist — saveDistillStateV2 must run even on a mid-run failure")
	// last_weekly is `json:"last_weekly,omitempty"` — its presence at all
	// (not just the key existing with an empty value) proves it was set.
	assert.Contains(t, string(data), `"last_weekly"`, "the persisted state must record the succeeded week's progress")
}

func TestRunDistill_WeeklySuccess_LaterWeekStillRunsAfterEarlierNoOpWeek(t *testing.T) {
	// sanity/contrast case: prove weeks WITHOUT a failure all run (this is
	// what confirms the "stops remaining weeks" assertion above is actually
	// about the failure, not some unrelated reason only one week ever runs).
	if testing.Short() {
		t.Skip("short: git operations")
	}
	teamContextDir, _ := setupDistillWeeklyFixture(t)

	backend := &sequencedBackend{failOnCall: -1} // never fails
	withDistillFlags(t, func() {
		detectAgentBackend = func() (agentcli.Backend, error) { return backend, nil }
	})

	err := runDistill(distillCmd, nil)
	require.NoError(t, err)

	assert.Equal(t, 3, backend.calls, "all three seeded weeks must run to completion when nothing fails")

	weeklyDir := filepath.Join(teamContextDir, "memory", "weekly")
	entries, err := os.ReadDir(weeklyDir)
	require.NoError(t, err)
	assert.Len(t, entries, 3, "all three weeks' summaries must be written")
}

// --- runDistill weekly/monthly rollup: incomplete daily backlog behind an
// explicit --layer=weekly/monthly run ---
//
// dailyOK (see runDistill) only catches "daily ran THIS invocation and
// failed." An explicit `ox distill --layer=weekly` never sets plan.Daily, so
// dailyOK stays true by construction even when an earlier, separate,
// interrupted run (e.g. a partial `ox distill --all` backfill) left the
// daily backlog behind an already-due week incomplete. Before the
// dailyBacklogComplete guard, distillWeekly only skipped a week if
// readDailyFilesForDateRange returned literally ZERO files for its date
// range — it had no way to distinguish "3 of 7 days present" from "7 of 7
// days present," so a partial week was silently synthesized and marked done
// forever via LastWeekly, permanently losing the days daily never got to.

// setupDistillWeeklyPartialFixture builds a real project + git-backed team
// context with exactly ONE already-due ISO week whose daily backing is
// incomplete: only the week's first 3 (of 7) days have a memory/daily/*.md
// file — the on-disk shape an interrupted backfill leaves behind mid-week.
// distill-state-v2.json is pre-seeded so plan.Weeks resolves to exactly this
// one week, keeping the test's signal isolated from the unrelated (and
// harmless) "no daily summaries, skipping" path that would otherwise fire
// for every other empty week in determineLayers' 91-day fallback lookback.
//
// Contrast with setupDistillWeeklyFixture above, which seeds three COMPLETE
// (Sunday-dated, single-file) weeks.
func setupDistillWeeklyPartialFixture(t *testing.T) (teamContextDir, projectRoot string, weekYear, weekNum int) {
	t.Helper()

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	projectRoot = createInitializedProjectWithConfig(t, &config.ProjectConfig{
		ProjectID: "test-project",
		TeamID:    "test-team",
		Endpoint:  "https://fake.test.invalid",
	})

	teamContextDir = t.TempDir()
	require.NoError(t, exec.Command("git", "-C", teamContextDir, "init", "--initial-branch=main").Run())
	require.NoError(t, exec.Command("git", "-C", teamContextDir, "config", "user.email", "test@test.com").Run())
	require.NoError(t, exec.Command("git", "-C", teamContextDir, "config", "user.name", "Test").Run())

	require.NoError(t, config.SaveLocalConfig(projectRoot, &config.LocalConfig{
		TeamContexts: []config.TeamContext{
			{TeamID: "test-team", TeamName: "test-team", Path: teamContextDir},
		},
	}))

	dailyDir := filepath.Join(teamContextDir, "memory", "daily")
	require.NoError(t, os.MkdirAll(dailyDir, 0o755))

	// Most recently fully-completed ISO week — deterministic regardless of
	// what weekday the suite runs on (see setupDistillWeeklyFixture above
	// for why a raw "now" offset isn't safe to seed a week's boundary from).
	now := time.Now().UTC()
	weekYear, weekNum = now.AddDate(0, 0, -7).ISOWeek()
	weekStart, _ := isoWeekRange(weekYear, weekNum)

	// seed only the first 3 (of 7) days — Mon, Tue, Wed. Thu-Sun are
	// deliberately absent, so the week's own daily high-water mark
	// (Wed) falls short of the week's end (Sun).
	for i := 0; i < 3; i++ {
		d := weekStart.AddDate(0, 0, i).Format("2006-01-02")
		require.NoError(t, os.WriteFile(filepath.Join(dailyDir, d+".md"),
			[]byte("## "+d+"\n\nDaily summary for "+d+".\n"), 0o644))
	}

	// pre-seed state so plan.Weeks resolves to exactly this one week —
	// enumerateWeeks starts from the week AFTER LastWeekly.
	require.NoError(t, saveDistillStateV2(projectRoot, &distillStateV2{
		SchemaVersion: "2",
		LastWeekly:    weekStart.Add(-time.Second).Format(time.RFC3339),
	}))

	origCwd, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(origCwd) })
	require.NoError(t, os.Chdir(projectRoot))

	return teamContextDir, projectRoot, weekYear, weekNum
}

func TestRunDistill_ExplicitWeeklyLayer_SkipsWeekWithIncompleteDailyBacklog(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	teamContextDir, projectRoot, weekYear, weekNum := setupDistillWeeklyPartialFixture(t)
	weekStart, _ := isoWeekRange(weekYear, weekNum)
	wantLastWeekly := weekStart.Add(-time.Second).Format(time.RFC3339) // unchanged from the fixture's seed

	// must never be called — the week must be skipped before any AI
	// coworker call is attempted.
	backend := &sequencedBackend{failOnCall: -1}
	withDistillFlags(t, func() {
		detectAgentBackend = func() (agentcli.Backend, error) { return backend, nil }
	})

	prevLogger := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prevLogger) })
	var logBuf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	err := runDistill(distillCmd, nil)
	require.NoError(t, err, "an incomplete daily backlog behind a due week is a benign skip, not a run failure")

	assert.Equal(t, 0, backend.calls, "the AI coworker must never be invoked for a week whose daily backlog is incomplete")

	weeklyDir := filepath.Join(teamContextDir, "memory", "weekly")
	entries, err := os.ReadDir(weeklyDir)
	require.NoError(t, err)
	assert.Empty(t, entries, "no weekly summary file must be written for a week with a partial (3 of 7 day) daily backlog")

	statePath := filepath.Join(projectRoot, ".sageox", "cache", "distill-state-v2.json")
	data, err := os.ReadFile(statePath)
	require.NoError(t, err)
	var state distillStateV2
	require.NoError(t, json.Unmarshal(data, &state))
	assert.Equal(t, wantLastWeekly, state.LastWeekly, "LastWeekly must not advance past a week whose daily backlog is incomplete — that would permanently lose the days daily never got to")

	logged := logBuf.String()
	assert.Contains(t, logged, "backlog", "must log a clear warning explaining why the week was skipped")
	assert.Contains(t, logged, fmt.Sprintf("%d", weekYear), "warning should identify the affected week")
}
