package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/spf13/cobra"
)

// stageJournalListProject wires up a fake initialized ox project whose
// team context points at a bare directory we can drop memory/daily,
// memory/weekly, and memory/monthly files into. Returns (projectRoot,
// teamRoot) — projectRoot becomes the test's cwd so findProjectRoot can
// discover it, and teamRoot is where fixture files go.
//
// The indirection exists because the reader's team resolver goes
// through config.FindRepoTeamContext, which loads ProjectConfig and
// LocalConfig. If we staged files directly under a random tempdir the
// reader would fail to find the team context and we'd be testing the
// wrong path.
func stageJournalListProject(t *testing.T) (string, string) {
	t.Helper()
	projectRoot := t.TempDir()
	teamRoot := t.TempDir()

	if err := os.MkdirAll(filepath.Join(projectRoot, ".sageox"), 0o755); err != nil {
		t.Fatalf("mkdir .sageox: %v", err)
	}
	if err := config.SaveProjectConfig(projectRoot, &config.ProjectConfig{
		ProjectID:   "proj_distill_history_list",
		WorkspaceID: "ws_distill_history_list",
		TeamID:      "team_distill_history_list",
		TeamName:    "Journal List Test",
	}); err != nil {
		t.Fatalf("SaveProjectConfig: %v", err)
	}
	if err := config.SaveLocalConfig(projectRoot, &config.LocalConfig{
		TeamContexts: []config.TeamContext{{
			TeamID:   "team_distill_history_list",
			TeamName: "Journal List Test",
			Slug:     "distill-history-list-test",
			Path:     teamRoot,
		}},
	}); err != nil {
		t.Fatalf("SaveLocalConfig: %v", err)
	}
	return projectRoot, teamRoot
}

// writeFile writes body to path and chtimes it to mtime. Bundled because
// every fixture writer needs both calls and the inlined pair obscures
// the test intent.
func writeFile(t *testing.T, path, body string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

// runDistillHistoryListInProc executes distillHistoryListCmd in-process with a fresh
// flag state (important: distillHistoryListFlagSet is package-scoped, so a
// previous test's flags can leak into the next one if we don't reset)
// and returns the captured stdout, stderr, and the error returned from
// RunE. Callers that expect usage errors also check the returned error
// for *distillHistoryExitError.
//
// We rebuild the command each call instead of reusing the package-level
// var because cobra retains flag state on the command object.
func runDistillHistoryListInProc(t *testing.T, args ...string) (string, string, error) {
	t.Helper()

	// Reset the package-level flag set so a prior call's values do not
	// bleed into this call. runDistillHistoryList reads from distillHistoryListFlagSet
	// directly (that is the flag-binding pattern the init() function set
	// up), so zeroing it here is both necessary and sufficient.
	distillHistoryListFlagSet = distillHistoryListFlags{}

	cmd := &cobra.Command{
		Use:           "list",
		RunE:          runDistillHistoryList,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	// Reuse the production flag binder so the test surface cannot drift
	// out of sync with `distillHistoryListCmd`.
	registerDistillHistoryListFlags(cmd, &distillHistoryListFlagSet)

	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errb.String(), err
}

const listTestSampleDaily = `---
sources:
  - memory/.github-facts/2026-04-12-019c8a0e-pr-487.jsonl
---
# Daily Memory — 2026-04-12

A body paragraph. [1]

## Sources

1. [PR #487 — fix(doctor)][1]

[1]: https://github.com/sageox/ox/pull/487
`

const listTestSampleWeekly = `---
sources:
  - memory/daily/2026-04-06-019c8a0e.md
  - memory/daily/2026-04-07-019c8a0f.md
---
# Weekly Memory — 2026-W15 (2026-04-06 → 2026-04-12)

Weekly synthesis body.
`

const listTestSampleMonthly = `---
sources:
  - memory/weekly/2026-W14-019c7c10.md
  - memory/weekly/2026-W15-019c8a0e.md
---
# Monthly Memory — 2026-04

Monthly synthesis body.
`

// TestDistillHistoryList_InProc_DailyJSON — happy-path daily list through the
// full command layer. Failure prevented: regress the Cobra flag binding,
// the envelope shape, or the reader wiring such that a list call no
// longer populates data.entries / data.window / type.
func TestDistillHistoryList_InProc_DailyJSON(t *testing.T) {
	projectRoot, teamRoot := stageJournalListProject(t)
	t.Chdir(projectRoot)

	apr12 := time.Date(2026, 4, 12, 12, 0, 0, 0, time.UTC)
	writeFile(t, filepath.Join(teamRoot, "memory", "daily", "2026-04-12-019c8a3f.md"),
		listTestSampleDaily, apr12)

	out, stderrStr, err := runDistillHistoryListInProc(t,
		"--since=2026-04-12", "--until=2026-04-12", "--format=json")
	if err != nil {
		t.Fatalf("list err=%v stdout=%s stderr=%s", err, out, stderrStr)
	}

	var env distillHistoryEnvelope
	if err := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v\nstdout=%s", err, out)
	}
	if !env.Success {
		t.Fatalf("success=false, error=%+v", env.Error)
	}
	if env.Type != "distill_history_list" {
		t.Fatalf("type = %q, want distill_history_list", env.Type)
	}
	if env.Data == nil {
		t.Fatalf("data is nil")
	}
	if len(env.Data.Entries) != 1 {
		t.Fatalf("entries = %d, want 1: %+v", len(env.Data.Entries), env.Data.Entries)
	}
	got := env.Data.Entries[0]
	if got.ID != "2026-04-12-019c8a3f" {
		t.Fatalf("ID = %q", got.ID)
	}
	if got.Layer != "daily" {
		t.Fatalf("Layer = %q", got.Layer)
	}
	if got.Date != "2026-04-12" {
		t.Fatalf("Date = %q", got.Date)
	}
	if got.Team != "distill-history-list-test" {
		t.Fatalf("Team = %q", got.Team)
	}
	if got.Path != "memory/daily/2026-04-12-019c8a3f.md" {
		t.Fatalf("Path = %q", got.Path)
	}
	if got.CitationCount != 1 {
		t.Fatalf("CitationCount = %d", got.CitationCount)
	}
	if len(got.SourceFiles) != 1 {
		t.Fatalf("SourceFiles = %+v", got.SourceFiles)
	}
	if !strings.HasSuffix(got.CreatedAt, "Z") {
		t.Fatalf("CreatedAt = %q, want UTC Z-suffix", got.CreatedAt)
	}
	if env.Data.Window == nil {
		t.Fatalf("window is nil")
	}
	if env.Data.Window.LayerResolved != "daily" {
		t.Fatalf("LayerResolved = %q", env.Data.Window.LayerResolved)
	}
	if !strings.HasSuffix(env.Data.Window.Since, "Z") || !strings.HasSuffix(env.Data.Window.Until, "Z") {
		t.Fatalf("window timestamps missing Z suffix: %+v", env.Data.Window)
	}
}

// TestDistillHistoryList_InProc_AutoResolvesWeeklyAndMonthly — verifies
// --layer=auto routes a full-week window to the weekly reader and a
// full-month window to the monthly reader, end-to-end through the
// command layer. Failure prevented: auto resolution regresses silently
// and every `ox distill history list` call hits the daily branch regardless of
// window width.
func TestDistillHistoryList_InProc_AutoResolvesWeeklyAndMonthly(t *testing.T) {
	projectRoot, teamRoot := stageJournalListProject(t)
	t.Chdir(projectRoot)

	writeFile(t, filepath.Join(teamRoot, "memory", "weekly", "2026-W15-019c8a0e.md"),
		listTestSampleWeekly, time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC))
	writeFile(t, filepath.Join(teamRoot, "memory", "monthly", "2026-04.md"),
		listTestSampleMonthly, time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC))

	t.Run("weekly window", func(t *testing.T) {
		out, _, err := runDistillHistoryListInProc(t,
			"--since=2026-04-06", "--until=2026-04-13", "--format=json")
		if err != nil {
			t.Fatalf("list: %v\n%s", err, out)
		}
		var env distillHistoryEnvelope
		if err := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &env); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if env.Data.Window.LayerResolved != "weekly" {
			t.Fatalf("LayerResolved = %q, want weekly", env.Data.Window.LayerResolved)
		}
		if len(env.Data.Entries) != 1 || env.Data.Entries[0].Layer != "weekly" {
			t.Fatalf("entries = %+v", env.Data.Entries)
		}
	})

	t.Run("monthly window", func(t *testing.T) {
		out, _, err := runDistillHistoryListInProc(t,
			"--since=2026-04-01", "--until=2026-05-01", "--format=json")
		if err != nil {
			t.Fatalf("list: %v\n%s", err, out)
		}
		var env distillHistoryEnvelope
		if err := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &env); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if env.Data.Window.LayerResolved != "monthly" {
			t.Fatalf("LayerResolved = %q, want monthly", env.Data.Window.LayerResolved)
		}
		if len(env.Data.Entries) != 1 || env.Data.Entries[0].Layer != "monthly" {
			t.Fatalf("entries = %+v", env.Data.Entries)
		}
	})
}

// TestDistillHistoryList_InProc_UsageErrorExitCode — verifies a malformed
// --since value funnels through newDistillHistoryUsageExit and surfaces on
// stdout as a JSON error envelope with code=usage_error, while also
// returning a *distillHistoryExitError with ExitCode=2. The exit-code plumbing
// in main.go unwraps that type and surfaces exit 2 to the shell.
//
// Failure prevented: usage errors collapse into exit 1 and agents
// cannot distinguish "bad flag" from "real failure".
func TestDistillHistoryList_InProc_UsageErrorExitCode(t *testing.T) {
	projectRoot, _ := stageJournalListProject(t)
	t.Chdir(projectRoot)

	out, _, err := runDistillHistoryListInProc(t, "--since=not-a-duration", "--format=json")
	if err == nil {
		t.Fatalf("want usage error, got nil (stdout=%s)", out)
	}
	var jexit *distillHistoryExitError
	if !asJournalExitError(err, &jexit) {
		t.Fatalf("want *distillHistoryExitError, got %T: %v", err, err)
	}
	if jexit.ExitCode != 2 {
		t.Fatalf("ExitCode = %d, want 2", jexit.ExitCode)
	}

	var env distillHistoryEnvelope
	if err := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &env); err != nil {
		t.Fatalf("unmarshal: %v\nstdout=%s", err, out)
	}
	if env.Success {
		t.Fatalf("success=true on usage error envelope")
	}
	if env.Error == nil || env.Error.Code != "usage_error" {
		t.Fatalf("error = %+v, want code=usage_error", env.Error)
	}
	if env.Error.Retryable {
		t.Fatalf("retryable=true on usage error")
	}
	if !strings.Contains(env.Error.Message, "--since") {
		t.Fatalf("message = %q, want to mention --since", env.Error.Message)
	}
}

// TestDistillHistoryList_InProc_TeamAllTeamsConflict — passing --team and
// --all-teams at the same time must surface as usage_error (exit 2),
// not silently drop one of them. The previous behavior silently
// preferred --all-teams and ignored --team, which is an agent-ux
// footgun: a caller retrying with a narrower filter thought it
// worked.
//
// Failure prevented: flag precedence regression that hides user
// intent and gives wrong results.
func TestDistillHistoryList_InProc_TeamAllTeamsConflict(t *testing.T) {
	projectRoot, _ := stageJournalListProject(t)
	t.Chdir(projectRoot)

	out, _, err := runDistillHistoryListInProc(t,
		"--since=2026-04-12", "--until=2026-04-12",
		"--team=distill-history-list-test", "--all-teams", "--format=json")
	if err == nil {
		t.Fatalf("want usage error, got nil (stdout=%s)", out)
	}
	var jexit *distillHistoryExitError
	if !asJournalExitError(err, &jexit) {
		t.Fatalf("want *distillHistoryExitError, got %T: %v", err, err)
	}
	if jexit.ExitCode != 2 {
		t.Fatalf("ExitCode = %d, want 2", jexit.ExitCode)
	}
	var env distillHistoryEnvelope
	if err := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &env); err != nil {
		t.Fatalf("unmarshal: %v\nstdout=%s", err, out)
	}
	if env.Success {
		t.Fatalf("success=true on conflict envelope")
	}
	if env.Error == nil || env.Error.Code != "usage_error" {
		t.Fatalf("error = %+v, want code=usage_error", env.Error)
	}
	if !strings.Contains(env.Error.Message, "--team") || !strings.Contains(env.Error.Message, "--all-teams") {
		t.Fatalf("message = %q, want mention of --team and --all-teams",
			env.Error.Message)
	}
}

// TestDistillHistoryList_InProc_MalformedFormatFlag — a rejected --format
// value must still produce a JSON envelope on stdout (not a silent
// exit), carry code=usage_error, and surface ExitCode==2. The earlier
// implementation returned the typed exit error without first writing
// the envelope, which left stdout blank on bad --format and violated
// the agent-ux contract that says machine-readable output is always
// present for usage errors.
//
// Failure prevented: bad --format exits 2 with empty stdout and agents
// parsing the command's output hit an unexpected EOF.
func TestDistillHistoryList_InProc_MalformedFormatFlag(t *testing.T) {
	projectRoot, _ := stageJournalListProject(t)
	t.Chdir(projectRoot)

	out, _, err := runDistillHistoryListInProc(t,
		"--since=2026-04-12", "--format=yaml")
	if err == nil {
		t.Fatalf("want usage error, got nil (stdout=%s)", out)
	}
	var jexit *distillHistoryExitError
	if !asJournalExitError(err, &jexit) {
		t.Fatalf("want *distillHistoryExitError, got %T: %v", err, err)
	}
	if jexit.ExitCode != 2 {
		t.Fatalf("ExitCode = %d, want 2", jexit.ExitCode)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatalf("stdout empty on usage error — envelope must still be emitted")
	}
	var env distillHistoryEnvelope
	if err := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &env); err != nil {
		t.Fatalf("unmarshal: %v\nstdout=%s", err, out)
	}
	if env.Success {
		t.Fatalf("success=true on malformed --format envelope")
	}
	if env.Error == nil || env.Error.Code != "usage_error" {
		t.Fatalf("error = %+v, want code=usage_error", env.Error)
	}
	if !strings.Contains(env.Error.Message, "--format") {
		t.Fatalf("message = %q, want mention of --format", env.Error.Message)
	}
}

// TestDistillHistoryList_InProc_LegacyPrefixTrusted — a daily file whose UUID7
// encodes a different UTC day than its filename prefix must surface
// with date == filename prefix, and stderr must be silent (no legacy
// warning). This is the one in-process lock-in for spec §5.b.
func TestDistillHistoryList_InProc_LegacyPrefixTrusted(t *testing.T) {
	projectRoot, teamRoot := stageJournalListProject(t)
	t.Chdir(projectRoot)

	// Prefix says 2026-04-12; UUID7 encodes ~2026-04-13 but the reader
	// must not care — the filename prefix is load-bearing.
	writeFile(t,
		filepath.Join(teamRoot, "memory", "daily", "2026-04-12-019c8a3f-legacy.md"),
		listTestSampleDaily,
		time.Date(2026, 4, 12, 23, 0, 0, 0, time.UTC))

	out, stderrStr, err := runDistillHistoryListInProc(t,
		"--since=2026-04-12", "--until=2026-04-12", "--format=json")
	if err != nil {
		t.Fatalf("list: %v\nstdout=%s\nstderr=%s", err, out, stderrStr)
	}

	var env distillHistoryEnvelope
	if err := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(env.Data.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(env.Data.Entries))
	}
	if env.Data.Entries[0].Date != "2026-04-12" {
		t.Fatalf("Date = %q, want 2026-04-12 (trust prefix)",
			env.Data.Entries[0].Date)
	}
	// Silent legacy policy: no stderr warnings about prefix/UUID drift.
	if strings.Contains(strings.ToLower(stderrStr), "legacy") ||
		strings.Contains(strings.ToLower(stderrStr), "uuid7") {
		t.Fatalf("stderr carries a spurious legacy warning: %q", stderrStr)
	}
}

// TestDistillHistoryList_InProc_TextFormatGolden — the --format=text renderer
// produces one line per entry plus a trailing window summary. Pinned to
// a small golden output so any future renderer tweak (column layout,
// separator, window line format) fails loudly.
//
// Failure prevented: renderDistillHistoryListText regresses in a way that
// breaks human-scanned output or downstream shell pipelines that grep
// the line format.
func TestDistillHistoryList_InProc_TextFormatGolden(t *testing.T) {
	projectRoot, teamRoot := stageJournalListProject(t)
	t.Chdir(projectRoot)

	apr12 := time.Date(2026, 4, 12, 12, 0, 0, 0, time.UTC)
	writeFile(t, filepath.Join(teamRoot, "memory", "daily", "2026-04-12-019c8a3f.md"),
		listTestSampleDaily, apr12)

	out, _, err := runDistillHistoryListInProc(t,
		"--since=2026-04-12", "--until=2026-04-12", "--format=text")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	want := "2026-04-12  daily    2026-04-12-019c8a3f  team=distill-history-list-test  facts=0  cites=1\n" +
		"\nwindow: 2026-04-12T00:00:00Z .. 2026-04-13T00:00:00Z  layer=daily\n"
	if out != want {
		t.Fatalf("text output mismatch:\n got=%q\nwant=%q", out, want)
	}
}

// TestDistillHistoryList_InProc_AllTeamsMerged — --all-teams merges entries
// across every registered team context and sorts them by Date, then
// CreatedAt, then team slug (spec §5.g). Each row carries its own team
// field.
//
// Failure prevented: --all-teams regresses to single-team behavior, or
// the merged ordering collapses to insertion order.
func TestDistillHistoryList_InProc_AllTeamsMerged(t *testing.T) {
	projectRoot := t.TempDir()
	teamA := t.TempDir()
	teamB := t.TempDir()

	if err := os.MkdirAll(filepath.Join(projectRoot, ".sageox"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := config.SaveProjectConfig(projectRoot, &config.ProjectConfig{
		ProjectID:   "proj_at",
		WorkspaceID: "ws_at",
		TeamID:      "team_a",
		TeamName:    "Team A",
	}); err != nil {
		t.Fatalf("SaveProjectConfig: %v", err)
	}
	if err := config.SaveLocalConfig(projectRoot, &config.LocalConfig{
		TeamContexts: []config.TeamContext{
			{TeamID: "team_a", TeamName: "Team A", Slug: "team-a", Path: teamA},
			{TeamID: "team_b", TeamName: "Team B", Slug: "team-b", Path: teamB},
		},
	}); err != nil {
		t.Fatalf("SaveLocalConfig: %v", err)
	}
	t.Chdir(projectRoot)

	apr12 := time.Date(2026, 4, 12, 12, 0, 0, 0, time.UTC)
	writeFile(t, filepath.Join(teamA, "memory", "daily", "2026-04-12-019c8a3f.md"),
		listTestSampleDaily, apr12)
	writeFile(t, filepath.Join(teamB, "memory", "daily", "2026-04-12-019c8aaa.md"),
		listTestSampleDaily, apr12.Add(time.Hour))

	out, _, err := runDistillHistoryListInProc(t,
		"--since=2026-04-12", "--until=2026-04-12", "--all-teams", "--format=json")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	var env distillHistoryEnvelope
	if err := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(env.Data.Entries) != 2 {
		t.Fatalf("entries = %d, want 2 across both teams", len(env.Data.Entries))
	}
	teams := map[string]bool{}
	for _, e := range env.Data.Entries {
		teams[e.Team] = true
	}
	if !teams["team-a"] || !teams["team-b"] {
		t.Fatalf("both teams missing from merged result: %+v", teams)
	}
	// Older-mtime entry sorts first within the same Date.
	if env.Data.Entries[0].ID != "2026-04-12-019c8a3f" {
		t.Fatalf("entries[0].ID = %q, want 2026-04-12-019c8a3f (older mtime)",
			env.Data.Entries[0].ID)
	}
}

// TestDistillHistoryList_InProc_WindowRoundingReported — a --since=6h query
// funnels through resolveDistillHistoryWindow + day-floor/ceil math. The
// envelope's window.since must be the day-floored instant (not the raw
// now-6h) and window.until must be the day-ceilinged instant. This is
// the end-to-end equivalent of TestDistillHistoryWindowRounding.
//
// Failure prevented: the CLI layer leaks raw --since/--until into the
// envelope, silently defeating caller's ability to detect the rounding.
func TestDistillHistoryList_InProc_WindowRoundingReported(t *testing.T) {
	projectRoot, _ := stageJournalListProject(t)
	t.Chdir(projectRoot)

	out, _, err := runDistillHistoryListInProc(t, "--since=6h", "--format=json")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	var env distillHistoryEnvelope
	if err := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Data.Window == nil {
		t.Fatalf("nil window")
	}
	since, err := time.Parse(time.RFC3339, env.Data.Window.Since)
	if err != nil {
		t.Fatalf("parse since: %v", err)
	}
	until, err := time.Parse(time.RFC3339, env.Data.Window.Until)
	if err != nil {
		t.Fatalf("parse until: %v", err)
	}
	// Both endpoints must be exact UTC midnights — the single invariant
	// that day-floor/ceil guarantees regardless of wall-clock drift.
	if since.Hour() != 0 || since.Minute() != 0 || since.Second() != 0 {
		t.Fatalf("since = %v, want midnight-aligned", since)
	}
	if until.Hour() != 0 || until.Minute() != 0 || until.Second() != 0 {
		t.Fatalf("until = %v, want midnight-aligned", until)
	}
	// For --since=6h + default until=now, the window is always at least
	// 24h wide (today's day is fully included by the day-ceil rule).
	if until.Sub(since) < 24*time.Hour {
		t.Fatalf("window span = %s, want ≥ 24h", until.Sub(since))
	}
}

// asJournalExitError unwraps a *distillHistoryExitError via errors.As so a
// future wrapping of the exit error does not silently skip this branch.
// Exists to mirror asJournalUsageError's shape so the test body reads
// uniformly.
func asJournalExitError(err error, target **distillHistoryExitError) bool {
	return errors.As(err, target)
}
