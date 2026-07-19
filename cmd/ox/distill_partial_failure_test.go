package main

import (
	"context"
	"fmt"
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

	now := time.Now().UTC()
	// ~11, ~6, ~1 weeks ago — comfortably inside the 91-day lookback,
	// spaced 5+ weeks apart so each unambiguously lands in its own ISO week.
	offsets := [3]int{-77, -42, -7}
	for i, days := range offsets {
		d := now.AddDate(0, 0, days).Format("2006-01-02")
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
