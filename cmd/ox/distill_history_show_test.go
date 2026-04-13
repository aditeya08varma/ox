package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// runDistillHistoryShowInProc executes distillHistoryShowCmd in-process with a fresh
// flag state and positional IDs. Mirrors runDistillHistoryListInProc so both
// command tests share the "reset flags, rebuild cobra.Command, capture
// stdout/stderr" pattern. Callers expecting failures inspect the
// returned error for *distillHistoryExitError.
//
// We rebuild the cobra.Command each call because cobra keeps flag
// state on the command, which would bleed between subtests.
func runDistillHistoryShowInProc(t *testing.T, args ...string) (string, string, error) {
	t.Helper()

	distillHistoryShowFlagSet = distillHistoryShowFlags{}

	cmd := &cobra.Command{
		Use:           "show",
		RunE:          runDistillHistoryShow,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	// Reuse the production flag binder so the test surface cannot drift
	// out of sync with `distillHistoryShowCmd`.
	registerDistillHistoryShowFlags(cmd, &distillHistoryShowFlagSet)

	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errb.String(), err
}

// showTestSampleDaily is the fixture body for a daily entry. Includes
// frontmatter (sources:) + heading + citation so WantBody
// materialization, StripFrontmatter, and the Sources-section parser
// all exercise a real path.
const showTestSampleDaily = `---
sources:
  - memory/.github-facts/2026-04-12-019c8a0e-pr-487.jsonl
---
# Daily Memory — 2026-04-12

Morning rollup. [1]

## Sources

1. [PR #487 — fix(doctor)][1]

[1]: https://github.com/sageox/ox/pull/487
`

const showTestSampleDailyB = `---
sources:
  - memory/.github-facts/2026-04-12-019c8a0e-pr-488.jsonl
---
# Daily Memory — 2026-04-12

Afternoon rollup with a different body.
`

// TestDistillHistoryShow_InProc_ExactStemJSON — happy path: full stem match
// returns a single ok row with body_md stripped of frontmatter, the
// citation array populated from the ## Sources section, and the
// envelope type set. Failure prevented: a regression that leaves
// frontmatter in body_md, or that drops citations, or that forgets to
// set type=distill_history_show.
func TestDistillHistoryShow_InProc_ExactStemJSON(t *testing.T) {
	projectRoot, teamRoot := stageJournalListProject(t)
	t.Chdir(projectRoot)

	apr12 := time.Date(2026, 4, 12, 12, 0, 0, 0, time.UTC)
	writeFile(t, filepath.Join(teamRoot, "memory", "daily", "2026-04-12-019c8a3f.md"),
		showTestSampleDaily, apr12)

	out, stderrStr, err := runDistillHistoryShowInProc(t, "2026-04-12-019c8a3f", "--format=json")
	if err != nil {
		t.Fatalf("show err=%v stdout=%s stderr=%s", err, out, stderrStr)
	}

	var env distillHistoryEnvelope
	if err := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &env); err != nil {
		t.Fatalf("unmarshal: %v\nstdout=%s", err, out)
	}
	if !env.Success {
		t.Fatalf("success=false: error=%+v", env.Error)
	}
	if env.Type != "distill_history_show" {
		t.Fatalf("type=%q, want distill_history_show", env.Type)
	}
	if env.Data == nil || len(env.Data.Entries) != 1 {
		t.Fatalf("entries = %+v", env.Data)
	}
	got := env.Data.Entries[0]
	if got.ID != "2026-04-12-019c8a3f" {
		t.Fatalf("id = %q", got.ID)
	}
	if got.Status != "ok" {
		t.Fatalf("status = %q, want ok", got.Status)
	}
	if got.BodyMD == "" {
		t.Fatalf("body_md empty — not materialized")
	}
	if strings.HasPrefix(got.BodyMD, "---") {
		t.Fatalf("body_md still starts with frontmatter fence: %q", got.BodyMD[:20])
	}
	if !strings.Contains(got.BodyMD, "Morning rollup") {
		t.Fatalf("body_md missing body text: %q", got.BodyMD)
	}
	if got.CitationCount != 1 {
		t.Fatalf("citation_count = %d, want 1", got.CitationCount)
	}
	if len(got.Citations) != 1 {
		t.Fatalf("citations = %+v", got.Citations)
	}
}

// TestDistillHistoryShow_InProc_BareDateMultiRow — bare date expands to the
// full day union, in created_at ascending order. Failure prevented:
// the reader silently picks one file as canonical, or reverses the
// ordering so callers cannot rely on chronology.
func TestDistillHistoryShow_InProc_BareDateMultiRow(t *testing.T) {
	projectRoot, teamRoot := stageJournalListProject(t)
	t.Chdir(projectRoot)

	apr12a := time.Date(2026, 4, 12, 9, 0, 0, 0, time.UTC)
	apr12b := time.Date(2026, 4, 12, 18, 0, 0, 0, time.UTC)
	writeFile(t, filepath.Join(teamRoot, "memory", "daily", "2026-04-12-019c8a3f-0001.md"),
		showTestSampleDaily, apr12a)
	writeFile(t, filepath.Join(teamRoot, "memory", "daily", "2026-04-12-019c8a3f-0002.md"),
		showTestSampleDailyB, apr12b)

	out, stderrStr, err := runDistillHistoryShowInProc(t, "2026-04-12", "--format=json")
	if err != nil {
		t.Fatalf("show err=%v stdout=%s stderr=%s", err, out, stderrStr)
	}

	var env distillHistoryEnvelope
	if err := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !env.Success {
		t.Fatalf("success=false: %+v", env.Error)
	}
	if len(env.Data.Entries) != 2 {
		t.Fatalf("entries = %d, want 2: %+v", len(env.Data.Entries), env.Data.Entries)
	}
	// Both rows ok and Date=2026-04-12.
	for i, row := range env.Data.Entries {
		if row.Status != "ok" {
			t.Fatalf("row %d status=%q, want ok", i, row.Status)
		}
		if row.Date != "2026-04-12" {
			t.Fatalf("row %d date=%q", i, row.Date)
		}
	}
	// Chronological order: 0001 (9am) before 0002 (6pm).
	if !strings.Contains(env.Data.Entries[0].ID, "0001") ||
		!strings.Contains(env.Data.Entries[1].ID, "0002") {
		t.Fatalf("ordering wrong: %s then %s",
			env.Data.Entries[0].ID, env.Data.Entries[1].ID)
	}
}

// TestDistillHistoryShow_InProc_PartialSuccess — mix of ok + not_found IDs
// produces success:true exit 0 with per-row status fields. Failure
// prevented: one missing ID aborting the whole call, or success
// flipping to false on ANY per-row failure.
func TestDistillHistoryShow_InProc_PartialSuccess(t *testing.T) {
	projectRoot, teamRoot := stageJournalListProject(t)
	t.Chdir(projectRoot)

	apr12 := time.Date(2026, 4, 12, 12, 0, 0, 0, time.UTC)
	writeFile(t, filepath.Join(teamRoot, "memory", "daily", "2026-04-12-019c8a3f.md"),
		showTestSampleDaily, apr12)

	out, _, err := runDistillHistoryShowInProc(t,
		"2026-04-12-019c8a3f", "deadbeef", "--format=json")
	if err != nil {
		t.Fatalf("show err=%v stdout=%s", err, out)
	}

	var env distillHistoryEnvelope
	if err := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !env.Success {
		t.Fatalf("success=false on partial: %+v", env.Error)
	}
	if len(env.Data.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(env.Data.Entries))
	}
	counts := map[string]int{}
	for _, row := range env.Data.Entries {
		counts[row.Status]++
	}
	if counts["ok"] != 1 || counts["not_found"] != 1 {
		t.Fatalf("statuses = %+v, want one ok + one not_found", counts)
	}
}

// TestDistillHistoryShow_InProc_AllFailed — every requested ID is a miss →
// success:false + exit 1 + first error propagated to the envelope's
// top-level error block. Failure prevented: all-miss silently
// returning exit 0 with success:true and empty entries.
func TestDistillHistoryShow_InProc_AllFailed(t *testing.T) {
	projectRoot, _ := stageJournalListProject(t)
	t.Chdir(projectRoot)

	// No fixture files — every ID must miss.
	out, _, err := runDistillHistoryShowInProc(t, "2026-04-12-deadbeef", "--format=json")
	if err == nil {
		t.Fatalf("want exit error, got nil stdout=%s", out)
	}
	var exit *distillHistoryExitError
	if !errors.As(err, &exit) {
		t.Fatalf("want *distillHistoryExitError, got %T", err)
	}
	if exit.ExitCode != 1 {
		t.Fatalf("exit code = %d, want 1", exit.ExitCode)
	}

	var env distillHistoryEnvelope
	if jerr := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &env); jerr != nil {
		t.Fatalf("unmarshal: %v\nstdout=%s", jerr, out)
	}
	if env.Success {
		t.Fatalf("success=true on all-failed")
	}
	if env.Error == nil || env.Error.Code != "id_not_found" {
		t.Fatalf("error = %+v, want code=id_not_found", env.Error)
	}
}

// TestDistillHistoryShow_InProc_AmbiguousExit1 — single ambiguous ID is an
// "all failed" case (one row, not ok) so success:false + exit 1.
// Failure prevented: ambiguous prefix silently resolving to an
// arbitrary match, which would feed agents the wrong content with
// exit 0.
func TestDistillHistoryShow_InProc_AmbiguousExit1(t *testing.T) {
	projectRoot, teamRoot := stageJournalListProject(t)
	t.Chdir(projectRoot)

	apr12a := time.Date(2026, 4, 12, 9, 0, 0, 0, time.UTC)
	apr12b := time.Date(2026, 4, 12, 18, 0, 0, 0, time.UTC)
	writeFile(t, filepath.Join(teamRoot, "memory", "daily", "2026-04-12-019c8a3f-0001.md"),
		showTestSampleDaily, apr12a)
	writeFile(t, filepath.Join(teamRoot, "memory", "daily", "2026-04-12-019c8a3f-0002.md"),
		showTestSampleDailyB, apr12b)

	out, _, err := runDistillHistoryShowInProc(t, "019c8a3f", "--format=json")
	if err == nil {
		t.Fatalf("want exit error for ambiguous, got nil\nstdout=%s", out)
	}
	var exit *distillHistoryExitError
	if !errors.As(err, &exit) {
		t.Fatalf("want *distillHistoryExitError, got %T", err)
	}
	if exit.ExitCode != 1 {
		t.Fatalf("exit = %d, want 1", exit.ExitCode)
	}

	var env distillHistoryEnvelope
	if jerr := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &env); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if env.Success {
		t.Fatalf("success=true on ambiguous")
	}
	if env.Error == nil || env.Error.Code != "id_ambiguous" {
		t.Fatalf("error=%+v, want code=id_ambiguous", env.Error)
	}
}

// TestDistillHistoryShow_InProc_ContentFormatStripsEnvelope — --format=content
// emits only markdown bodies on stdout (no JSON envelope), with the
// spec §3.5 marker and separator. factory/distill/summary.ts consumes
// this exact contract.
//
// Failure prevented: frontmatter leaking into content mode, or the
// JSON envelope being written to stdout, or the separator missing
// between entries.
func TestDistillHistoryShow_InProc_ContentFormatStripsEnvelope(t *testing.T) {
	projectRoot, teamRoot := stageJournalListProject(t)
	t.Chdir(projectRoot)

	apr12a := time.Date(2026, 4, 12, 9, 0, 0, 0, time.UTC)
	apr12b := time.Date(2026, 4, 12, 18, 0, 0, 0, time.UTC)
	writeFile(t, filepath.Join(teamRoot, "memory", "daily", "2026-04-12-019c8a3f-0001.md"),
		showTestSampleDaily, apr12a)
	writeFile(t, filepath.Join(teamRoot, "memory", "daily", "2026-04-12-019c8a3f-0002.md"),
		showTestSampleDailyB, apr12b)

	out, _, err := runDistillHistoryShowInProc(t, "2026-04-12", "--format=content")
	if err != nil {
		t.Fatalf("show err=%v stdout=%s", err, out)
	}
	if strings.HasPrefix(strings.TrimLeft(out, "\n"), "{") {
		t.Fatalf("content mode leaked JSON envelope: %q", out[:80])
	}
	if strings.Contains(out, "---\nsources:") {
		t.Fatalf("frontmatter fence leaked into content output: %q", out)
	}
	// Both marker lines and the separator between them must appear.
	if !strings.Contains(out, "<!-- entry: 2026-04-12-019c8a3f-0001 -->") {
		t.Fatalf("marker for 0001 missing:\n%s", out)
	}
	if !strings.Contains(out, "<!-- entry: 2026-04-12-019c8a3f-0002 -->") {
		t.Fatalf("marker for 0002 missing:\n%s", out)
	}
	if !strings.Contains(out, "\n---\n") {
		t.Fatalf("separator missing:\n%s", out)
	}
	// Both bodies survive.
	if !strings.Contains(out, "Morning rollup") {
		t.Fatalf("body 0001 missing: %s", out)
	}
	if !strings.Contains(out, "Afternoon rollup") {
		t.Fatalf("body 0002 missing: %s", out)
	}
}

// TestDistillHistoryShow_InProc_UsageErrorMissingArgs — no positional IDs is
// a usage error (exit 2) with a JSON envelope on stdout. Failure
// prevented: a naive refactor that lets show run against zero IDs
// and silently returns empty entries with exit 0.
func TestDistillHistoryShow_InProc_UsageErrorMissingArgs(t *testing.T) {
	projectRoot, _ := stageJournalListProject(t)
	t.Chdir(projectRoot)

	out, _, err := runDistillHistoryShowInProc(t, "--format=json")
	if err == nil {
		t.Fatalf("want usage error, got nil, stdout=%s", out)
	}
	var exit *distillHistoryExitError
	if !errors.As(err, &exit) {
		t.Fatalf("want *distillHistoryExitError, got %T", err)
	}
	if exit.ExitCode != 2 {
		t.Fatalf("exit = %d, want 2", exit.ExitCode)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatalf("usage error stdout was empty — envelope contract violated")
	}
	var env distillHistoryEnvelope
	if jerr := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &env); jerr != nil {
		t.Fatalf("unmarshal: %v\nstdout=%s", jerr, out)
	}
	if env.Success {
		t.Fatalf("success=true on usage error")
	}
	if env.Error == nil || env.Error.Code != "usage_error" {
		t.Fatalf("error=%+v, want code=usage_error", env.Error)
	}
}

// TestDistillHistoryShow_InProc_LatestFlagRejectedByCobra — --latest must
// NOT be a registered flag. Cobra rejects unknown flags at parse time
// with its own error, before runDistillHistoryShow sees anything. Failure
// prevented: a future commit accidentally adds --latest and matches
// only one same-day entry, contradicting the plan's explicit
// rejection of the "newest snapshot" shortcut.
func TestDistillHistoryShow_InProc_LatestFlagRejectedByCobra(t *testing.T) {
	projectRoot, teamRoot := stageJournalListProject(t)
	t.Chdir(projectRoot)

	apr12 := time.Date(2026, 4, 12, 12, 0, 0, 0, time.UTC)
	writeFile(t, filepath.Join(teamRoot, "memory", "daily", "2026-04-12-019c8a3f.md"),
		showTestSampleDaily, apr12)

	_, _, err := runDistillHistoryShowInProc(t, "2026-04-12", "--latest")
	if err == nil {
		t.Fatalf("--latest must not be accepted")
	}
	// cobra returns its own error for unknown flags (NOT
	// *distillHistoryExitError). The test only needs to confirm the command
	// does not run to completion under --latest.
	if errors.As(err, new(*distillHistoryExitError)) {
		t.Fatalf("--latest reached runDistillHistoryShow instead of cobra's unknown-flag path: %v", err)
	}
}

// TestDistillHistoryShow_InProc_TextFormat pins the --format=text shape
// against spec §3.5:587-596. The header line must start with "entry
// <id>  <layer>  team=<slug>"; the path and counts lines must be
// 2-space indented; a blank line separates the counts block from the
// body; each body line is 2-space indented.
//
// Failure prevented: a refactor that regresses to the old one-line
// "<date>  <layer>  <id>..." shape (which dropped path, counts, and
// body) — the spec example treats this as a prescriptive rendering and
// downstream tooling that skims text mode relies on the leading
// "entry " token.
func TestDistillHistoryShow_InProc_TextFormat(t *testing.T) {
	projectRoot, teamRoot := stageJournalListProject(t)
	t.Chdir(projectRoot)

	apr12 := time.Date(2026, 4, 12, 12, 0, 0, 0, time.UTC)
	writeFile(t, filepath.Join(teamRoot, "memory", "daily", "2026-04-12-019c8a3f.md"),
		showTestSampleDaily, apr12)

	out, _, err := runDistillHistoryShowInProc(t, "2026-04-12-019c8a3f", "--format=text")
	if err != nil {
		t.Fatalf("show text err=%v stdout=%s", err, out)
	}
	if strings.HasPrefix(strings.TrimLeft(out, "\n"), "{") {
		t.Fatalf("text mode leaked JSON envelope: %q", out[:min(80, len(out))])
	}
	if !strings.Contains(out, "entry 2026-04-12-019c8a3f  daily  team=") {
		t.Fatalf("text header missing spec shape: %q", out)
	}
	if !strings.Contains(out, "\n  path: ") {
		t.Fatalf("text path line missing or not indented: %q", out)
	}
	if !strings.Contains(out, "  facts: ") ||
		!strings.Contains(out, "   citations: ") ||
		!strings.Contains(out, "   source files: ") {
		t.Fatalf("text counts line missing spec fields: %q", out)
	}
	// Blank line between counts block and body.
	if !strings.Contains(out, "\n\n  # Daily Memory") {
		t.Fatalf("body not separated + indented from counts: %q", out)
	}
	// Frontmatter must NOT appear in text mode — stripFrontmatter runs
	// inside LoadEntries so BodyMD is already fence-free before it
	// reaches the renderer.
	if strings.Contains(out, "---\nsources:") {
		t.Fatalf("frontmatter leaked into text mode: %q", out)
	}
}

// TestDistillHistoryShow_InProc_ContentFormatAllFailedFallsBackToJSON —
// when every requested ID misses and the caller asked for
// --format=content, show falls back to a JSON error envelope on
// stdout rather than leaving stdout silent. The fallback is the
// distillHistoryShowErrorFormat("content") → "json" mapping in the command
// layer.
//
// Failure prevented: content-mode callers getting an empty stdout on
// all-fail (exit 1 with no parseable failure detail), which would
// violate the envelope-on-every-exit-path rule from the Unit 3
// distill_history_list anti-pattern lesson (stored in bd memory as
// cobra-json-envelope-anti-pattern-when-a-command).
func TestDistillHistoryShow_InProc_ContentFormatAllFailedFallsBackToJSON(t *testing.T) {
	projectRoot, _ := stageJournalListProject(t)
	t.Chdir(projectRoot)

	// No fixture files — every ID must miss.
	out, _, err := runDistillHistoryShowInProc(t, "2026-04-12-deadbeef", "--format=content")
	if err == nil {
		t.Fatalf("want exit error on all-failed content mode, got nil stdout=%s", out)
	}
	var exit *distillHistoryExitError
	if !errors.As(err, &exit) {
		t.Fatalf("want *distillHistoryExitError, got %T", err)
	}
	if exit.ExitCode != 1 {
		t.Fatalf("exit = %d, want 1", exit.ExitCode)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatalf("content-mode all-fail left stdout empty — envelope fallback not engaged")
	}
	var env distillHistoryEnvelope
	if jerr := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &env); jerr != nil {
		t.Fatalf("content-mode fallback should be JSON: %v\nstdout=%s", jerr, out)
	}
	if env.Success {
		t.Fatalf("success=true on all-failed content fallback")
	}
	if env.Error == nil || env.Error.Code != "id_not_found" {
		t.Fatalf("error = %+v, want code=id_not_found", env.Error)
	}
}
