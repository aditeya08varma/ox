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

// runJournalSinceInProc executes journalSinceCmd in-process with a fresh
// flag state and positional args. Mirrors runJournalListInProc and
// runJournalShowInProc so all three command tests share the "reset
// flags, rebuild cobra.Command, capture stdout/stderr" pattern. Callers
// expecting typed exit errors inspect the returned error.
//
// We rebuild the cobra.Command each call because cobra keeps flag state
// on the command object, which would bleed across subtests.
func runJournalSinceInProc(t *testing.T, args ...string) (string, string, error) {
	t.Helper()

	journalSinceFlagSet = journalSinceFlags{}

	cmd := &cobra.Command{
		Use:           "since",
		RunE:          runJournalSince,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	f := cmd.Flags()
	f.StringVar(&journalSinceFlagSet.Layer, "layer", "auto", "")
	f.StringVar(&journalSinceFlagSet.Team, "team", "", "")
	f.BoolVar(&journalSinceFlagSet.AllTeams, "all-teams", false, "")
	f.StringVar(&journalSinceFlagSet.Format, "format", "content", "")
	f.IntVar(&journalSinceFlagSet.Limit, "limit", 100, "")

	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errb.String(), err
}

// sinceTestBody returns a minimal daily entry body with frontmatter +
// heading + note. The frontmatter fence exercises strip-frontmatter on
// the load path so content-mode assertions confirm it never leaks.
func sinceTestBody(date, note string) string {
	return "---\nsources:\n  - memory/.github-facts/" + date + "-source.jsonl\n---\n# Daily Memory — " + date + "\n\n" + note + "\n"
}

// stageSinceTodayFixtures writes two daily entries dated today under
// the given team root, with monotonic mtimes (now-4h and now-2h) so
// CreatedAt-asc sort is deterministic. Returns the pair of stem IDs in
// expected order.
//
// The files are dated today so any reasonable --since window catches
// them; the test's job is to exercise the composite path, not the
// date-window math (that belongs to list tests).
func stageSinceTodayFixtures(t *testing.T, teamRoot string, now time.Time) (string, string) {
	t.Helper()
	today := now.UTC().Format("2006-01-02")
	id1 := today + "-019c8a3f-0001"
	id2 := today + "-019c8a3f-0002"
	writeFile(t,
		filepath.Join(teamRoot, "memory", "daily", id1+".md"),
		sinceTestBody(today, "Morning rollup."),
		now.Add(-4*time.Hour),
	)
	writeFile(t,
		filepath.Join(teamRoot, "memory", "daily", id2+".md"),
		sinceTestBody(today, "Afternoon rollup."),
		now.Add(-2*time.Hour),
	)
	return id1, id2
}

// TestJournalSince_InProc_HappyPathJSON — the composite returns two
// entries in chronological order with bodies parallel by index. The
// envelope type is journal_since, window is day-rounded, and the
// success/elapsed_ms scaffolding matches list/show.
//
// Failure prevented: the composite drops bodies, reverses order, or
// publishes the raw --since window instead of the day-rounded one.
func TestJournalSince_InProc_HappyPathJSON(t *testing.T) {
	projectRoot, teamRoot := stageJournalListProject(t)
	t.Chdir(projectRoot)

	now := time.Now().UTC()
	skipIfNearUTCMidnight(t, now)
	id1, id2 := stageSinceTodayFixtures(t, teamRoot, now)

	out, stderrStr, err := runJournalSinceInProc(t, "6h", "--format=json", "--layer=daily")
	if err != nil {
		t.Fatalf("since err=%v\nstdout=%s\nstderr=%s", err, out, stderrStr)
	}
	var env journalEnvelope
	if err := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &env); err != nil {
		t.Fatalf("unmarshal: %v\nstdout=%s", err, out)
	}
	if !env.Success {
		t.Fatalf("success=false: error=%+v", env.Error)
	}
	if env.Type != "journal_since" {
		t.Fatalf("type=%q, want journal_since", env.Type)
	}
	if env.Data == nil {
		t.Fatalf("data is nil")
	}
	if len(env.Data.Entries) != 2 {
		t.Fatalf("entries = %d, want 2: %+v", len(env.Data.Entries), env.Data.Entries)
	}
	if env.Data.Bodies == nil {
		t.Fatalf("bodies pointer is nil — since must always set it")
	}
	bodies := *env.Data.Bodies
	if len(bodies) != 2 {
		t.Fatalf("bodies = %d, want 2", len(bodies))
	}
	// Parallel-slice alignment: entries[i].ID ↔ bodies[i] body text.
	if env.Data.Entries[0].ID != id1 || env.Data.Entries[1].ID != id2 {
		t.Fatalf("order = [%s, %s], want [%s, %s]",
			env.Data.Entries[0].ID, env.Data.Entries[1].ID, id1, id2)
	}
	if !strings.Contains(bodies[0], "Morning rollup") {
		t.Fatalf("bodies[0] wrong: %q", bodies[0])
	}
	if !strings.Contains(bodies[1], "Afternoon rollup") {
		t.Fatalf("bodies[1] wrong: %q", bodies[1])
	}
	// Frontmatter must be stripped in body_md too — the load path owns
	// this, so the test here is a sanity check on the composite.
	for i, b := range bodies {
		if strings.HasPrefix(b, "---") {
			t.Fatalf("bodies[%d] still has frontmatter fence: %q", i, b[:20])
		}
	}
	if env.Data.Window == nil {
		t.Fatalf("window is nil")
	}
	since, perr := time.Parse(time.RFC3339, env.Data.Window.Since)
	if perr != nil {
		t.Fatalf("parse window.since: %v", perr)
	}
	until, perr := time.Parse(time.RFC3339, env.Data.Window.Until)
	if perr != nil {
		t.Fatalf("parse window.until: %v", perr)
	}
	if since.Hour() != 0 || since.Minute() != 0 {
		t.Fatalf("window.since not midnight-aligned: %v", since)
	}
	if until.Hour() != 0 || until.Minute() != 0 {
		t.Fatalf("window.until not midnight-aligned: %v", until)
	}
	if env.Data.Window.LayerResolved != "daily" {
		t.Fatalf("layer_resolved = %q, want daily", env.Data.Window.LayerResolved)
	}
}

// TestJournalSince_InProc_ContentFormatRichMarkers — the default
// --format=content stream emits per-entry rich markers of the shape
//
//	<!-- entry: <id> | layer: <layer> | date: <date> -->
//
// separated by "\n---\n", with frontmatter stripped and no JSON
// envelope leak. factory/distill/summary.ts consumes exactly this
// contract.
//
// Failure prevented: content mode regresses to show's minimal
// `<!-- entry: <id> -->` marker (dropping layer/date routing info), or
// writes the envelope to stdout, or forgets the separator.
func TestJournalSince_InProc_ContentFormatRichMarkers(t *testing.T) {
	projectRoot, teamRoot := stageJournalListProject(t)
	t.Chdir(projectRoot)

	now := time.Now().UTC()
	skipIfNearUTCMidnight(t, now)
	id1, id2 := stageSinceTodayFixtures(t, teamRoot, now)
	today := now.Format("2006-01-02")

	// --format=content is the default — omit the flag to pin that.
	out, _, err := runJournalSinceInProc(t, "6h", "--layer=daily")
	if err != nil {
		t.Fatalf("since err=%v stdout=%s", err, out)
	}
	if strings.HasPrefix(strings.TrimLeft(out, "\n"), "{") {
		t.Fatalf("content mode leaked JSON envelope: %q", out[:min(80, len(out))])
	}
	if strings.Contains(out, "---\nsources:") {
		t.Fatalf("frontmatter fence leaked into content output: %q", out)
	}
	wantMarker1 := "<!-- entry: " + id1 + " | layer: daily | date: " + today + " -->"
	wantMarker2 := "<!-- entry: " + id2 + " | layer: daily | date: " + today + " -->"
	if !strings.Contains(out, wantMarker1) {
		t.Fatalf("marker 1 missing:\nwant %q\ngot %s", wantMarker1, out)
	}
	if !strings.Contains(out, wantMarker2) {
		t.Fatalf("marker 2 missing:\nwant %q\ngot %s", wantMarker2, out)
	}
	// Separator between the two entries.
	if !strings.Contains(out, "\n---\n") {
		t.Fatalf("separator missing:\n%s", out)
	}
	// Chronological order: marker1 appears before marker2 in the stream.
	idx1 := strings.Index(out, wantMarker1)
	idx2 := strings.Index(out, wantMarker2)
	if idx1 < 0 || idx2 < 0 || idx1 >= idx2 {
		t.Fatalf("markers out of order: idx1=%d idx2=%d", idx1, idx2)
	}
	if !strings.Contains(out, "Morning rollup") || !strings.Contains(out, "Afternoon rollup") {
		t.Fatalf("body text missing: %s", out)
	}
}

// TestJournalSince_InProc_ContentFormatGolden — pins the exact
// content-mode byte stream for a single-entry fixture. Golden form is
// the factory/distill/summary.ts contract — any whitespace or marker
// shape drift has to be a deliberate edit here, not a silent
// regression.
//
// Failure prevented: the renderer adds/removes a trailing newline or
// changes separator whitespace in a way that the non-golden tests miss
// (they use Contains, which is permissive).
func TestJournalSince_InProc_ContentFormatGolden(t *testing.T) {
	projectRoot, teamRoot := stageJournalListProject(t)
	t.Chdir(projectRoot)

	now := time.Now().UTC()
	skipIfNearUTCMidnight(t, now)
	today := now.Format("2006-01-02")
	id := today + "-019c8a3f-0001"
	writeFile(t,
		filepath.Join(teamRoot, "memory", "daily", id+".md"),
		sinceTestBody(today, "Solo rollup."),
		now.Add(-1*time.Hour),
	)

	out, _, err := runJournalSinceInProc(t, "6h", "--layer=daily")
	if err != nil {
		t.Fatalf("since err=%v", err)
	}
	want := "<!-- entry: " + id + " | layer: daily | date: " + today + " -->\n" +
		"# Daily Memory — " + today + "\n\nSolo rollup.\n"
	if out != want {
		t.Fatalf("content golden mismatch:\n got=%q\nwant=%q", out, want)
	}
}

// TestJournalSince_InProc_EmptyWindowJSON — a window that catches
// nothing is success:true + exit 0 with `entries: []` and `bodies: []`
// (non-null empty arrays per spec §3.6). Failure prevented: a refactor
// returns nil slices and the encoder emits `null`, breaking callers
// that iterate without a nil check.
func TestJournalSince_InProc_EmptyWindowJSON(t *testing.T) {
	projectRoot, _ := stageJournalListProject(t)
	t.Chdir(projectRoot)

	// No fixtures — any duration yields zero entries.
	out, _, err := runJournalSinceInProc(t, "1h", "--format=json", "--layer=daily")
	if err != nil {
		t.Fatalf("since err=%v stdout=%s", err, out)
	}
	// Reparse the stdout byte-for-byte so we can check that the JSON
	// contains literal empty arrays (not `null`).
	if !bytes.Contains([]byte(out), []byte(`"entries":[]`)) {
		t.Fatalf("entries not emitted as empty array: %s", out)
	}
	var env journalEnvelope
	if jerr := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &env); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if !env.Success {
		t.Fatalf("success=false on empty window: %+v", env.Error)
	}
	if env.Data == nil {
		t.Fatalf("data is nil")
	}
	if len(env.Data.Entries) != 0 {
		t.Fatalf("entries = %d, want 0", len(env.Data.Entries))
	}
	// Bodies is *[]string with omitempty: nil pointer would be dropped,
	// so for since the command always passes a non-nil pointer even
	// when the inner slice is empty. The raw-bytes check above is the
	// binding contract; this check documents the Go-side invariant.
	if env.Data.Bodies == nil {
		t.Fatalf("bodies pointer is nil — since must always set it")
	}
	if len(*env.Data.Bodies) != 0 {
		t.Fatalf("bodies = %d, want 0", len(*env.Data.Bodies))
	}
}

// TestJournalSince_InProc_EmptyWindowContent — an empty window in the
// default content format writes zero bytes on stdout and exits 0. No
// envelope fallback, no noise — the downstream summarizer sees an
// empty stream and handles it as "nothing to distill".
//
// Failure prevented: the content renderer emits a stray separator or
// marker on the empty path.
func TestJournalSince_InProc_EmptyWindowContent(t *testing.T) {
	projectRoot, _ := stageJournalListProject(t)
	t.Chdir(projectRoot)

	out, _, err := runJournalSinceInProc(t, "1h", "--layer=daily")
	if err != nil {
		t.Fatalf("since err=%v stdout=%s", err, out)
	}
	if out != "" {
		t.Fatalf("content mode on empty window wrote %q, want empty", out)
	}
}

// TestJournalSince_InProc_AllTeamsMerged — --all-teams merges entries
// across every registered team context and emits rich markers for
// both in content mode. Failure prevented: --all-teams regresses to
// single-team behavior, or the content renderer drops entries from
// the second team.
func TestJournalSince_InProc_AllTeamsMerged(t *testing.T) {
	projectRoot := t.TempDir()
	teamA := t.TempDir()
	teamB := t.TempDir()

	if err := os.MkdirAll(filepath.Join(projectRoot, ".sageox"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := config.SaveProjectConfig(projectRoot, &config.ProjectConfig{
		ProjectID:   "proj_since_at",
		WorkspaceID: "ws_since_at",
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

	now := time.Now().UTC()
	skipIfNearUTCMidnight(t, now)
	today := now.Format("2006-01-02")
	idA := today + "-019c8a3f-0001"
	idB := today + "-019c8aaa-0001"
	writeFile(t,
		filepath.Join(teamA, "memory", "daily", idA+".md"),
		sinceTestBody(today, "Team A rollup."),
		now.Add(-3*time.Hour),
	)
	writeFile(t,
		filepath.Join(teamB, "memory", "daily", idB+".md"),
		sinceTestBody(today, "Team B rollup."),
		now.Add(-1*time.Hour),
	)

	out, _, err := runJournalSinceInProc(t, "6h", "--all-teams", "--layer=daily")
	if err != nil {
		t.Fatalf("since err=%v stdout=%s", err, out)
	}
	wantA := "<!-- entry: " + idA + " | layer: daily | date: " + today + " -->"
	wantB := "<!-- entry: " + idB + " | layer: daily | date: " + today + " -->"
	if !strings.Contains(out, wantA) {
		t.Fatalf("team A marker missing:\n%s", out)
	}
	if !strings.Contains(out, wantB) {
		t.Fatalf("team B marker missing:\n%s", out)
	}
	if !strings.Contains(out, "Team A rollup") || !strings.Contains(out, "Team B rollup") {
		t.Fatalf("both team bodies missing: %s", out)
	}
	if !strings.Contains(out, "\n---\n") {
		t.Fatalf("separator missing between team entries: %s", out)
	}
}

// TestJournalSince_InProc_TeamAllTeamsConflict — combining --team and
// --all-teams is a usage error before any reader work happens. Mirrors
// the list test of the same shape.
func TestJournalSince_InProc_TeamAllTeamsConflict(t *testing.T) {
	projectRoot, _ := stageJournalListProject(t)
	t.Chdir(projectRoot)

	out, _, err := runJournalSinceInProc(t, "6h", "--team=foo", "--all-teams", "--format=json")
	if err == nil {
		t.Fatalf("want usage error, got nil stdout=%s", out)
	}
	var exit *journalExitError
	if !errors.As(err, &exit) {
		t.Fatalf("want *journalExitError, got %T", err)
	}
	if exit.ExitCode != 2 {
		t.Fatalf("exit = %d, want 2", exit.ExitCode)
	}
}

// TestJournalSince_InProc_LimitRespected — --limit=1 caps the returned
// entries at 1 and flags Truncated. The cap is applied in the list
// scan (before load), so the bodies slice is also length 1.
//
// Failure prevented: a refactor that materializes every body in the
// window first and then truncates, hiding the real I/O cost Limit
// exists to relieve.
func TestJournalSince_InProc_LimitRespected(t *testing.T) {
	projectRoot, teamRoot := stageJournalListProject(t)
	t.Chdir(projectRoot)

	now := time.Now().UTC()
	skipIfNearUTCMidnight(t, now)
	stageSinceTodayFixtures(t, teamRoot, now)

	out, _, err := runJournalSinceInProc(t, "6h", "--layer=daily", "--limit=1", "--format=json")
	if err != nil {
		t.Fatalf("since err=%v stdout=%s", err, out)
	}
	var env journalEnvelope
	if jerr := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &env); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if len(env.Data.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(env.Data.Entries))
	}
	if env.Data.Bodies == nil || len(*env.Data.Bodies) != 1 {
		t.Fatalf("bodies = %v, want 1", env.Data.Bodies)
	}
	if !env.Data.Truncated {
		t.Fatalf("truncated = false, want true")
	}
}

// TestJournalSince_InProc_UsageErrorMissingDur — no positional
// argument is a usage error (exit 2) and stdout still carries a JSON
// envelope with code=usage_error. Enforces the
// cobra-json-envelope-anti-pattern lesson: every typed exit path must
// write its envelope.
func TestJournalSince_InProc_UsageErrorMissingDur(t *testing.T) {
	projectRoot, _ := stageJournalListProject(t)
	t.Chdir(projectRoot)

	out, _, err := runJournalSinceInProc(t, "--format=json")
	if err == nil {
		t.Fatalf("want usage error, got nil stdout=%s", out)
	}
	var exit *journalExitError
	if !errors.As(err, &exit) {
		t.Fatalf("want *journalExitError, got %T", err)
	}
	if exit.ExitCode != 2 {
		t.Fatalf("exit = %d, want 2", exit.ExitCode)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatalf("usage error stdout was empty — envelope contract violated")
	}
	var env journalEnvelope
	if jerr := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &env); jerr != nil {
		t.Fatalf("unmarshal: %v stdout=%s", jerr, out)
	}
	if env.Success {
		t.Fatalf("success=true on usage error")
	}
	if env.Error == nil || env.Error.Code != "usage_error" {
		t.Fatalf("error=%+v, want code=usage_error", env.Error)
	}
}

// TestJournalSince_InProc_UsageErrorInvalidDur — an un-parseable
// duration is a usage error (exit 2) with a JSON envelope on stdout
// whose message quotes the offending value.
func TestJournalSince_InProc_UsageErrorInvalidDur(t *testing.T) {
	projectRoot, _ := stageJournalListProject(t)
	t.Chdir(projectRoot)

	out, _, err := runJournalSinceInProc(t, "banana", "--format=json")
	if err == nil {
		t.Fatalf("want usage error, got nil stdout=%s", out)
	}
	var exit *journalExitError
	if !errors.As(err, &exit) {
		t.Fatalf("want *journalExitError, got %T", err)
	}
	if exit.ExitCode != 2 {
		t.Fatalf("exit = %d, want 2", exit.ExitCode)
	}
	var env journalEnvelope
	if jerr := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &env); jerr != nil {
		t.Fatalf("unmarshal: %v stdout=%s", jerr, out)
	}
	if env.Error == nil || env.Error.Code != "usage_error" {
		t.Fatalf("error=%+v, want usage_error", env.Error)
	}
	if !strings.Contains(env.Error.Message, "banana") {
		t.Fatalf("message does not quote offending arg: %q", env.Error.Message)
	}
}

// TestJournalSince_InProc_UsageErrorNegativeDur — a negative duration
// is explicitly rejected (time.ParseDuration accepts "-5m") because
// since's semantics — `[now-dur, now]` — are undefined for negative
// dur. Failure prevented: a refactor that lets "-5m" through and
// flips since/until, silently returning today's entries under an
// upside-down window.
//
// Cobra would otherwise parse "-5m" as a short-flag token, so the `--`
// sentinel is required to force it into the positional slot.
func TestJournalSince_InProc_UsageErrorNegativeDur(t *testing.T) {
	projectRoot, _ := stageJournalListProject(t)
	t.Chdir(projectRoot)

	out, _, err := runJournalSinceInProc(t, "--format=json", "--", "-5m")
	if err == nil {
		t.Fatalf("want usage error, got nil stdout=%s", out)
	}
	var exit *journalExitError
	if !errors.As(err, &exit) {
		t.Fatalf("want *journalExitError, got %T", err)
	}
	if exit.ExitCode != 2 {
		t.Fatalf("exit = %d, want 2", exit.ExitCode)
	}
	var env journalEnvelope
	if jerr := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &env); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if env.Error == nil || env.Error.Code != "usage_error" {
		t.Fatalf("error=%+v, want usage_error", env.Error)
	}
}

// TestJournalSince_InProc_UsageErrorInvalidFormat — --format=yaml is
// not a registered format, so runJournalSince must reject it up front
// with a usage envelope. Failure prevented: the command reaches the
// reader before validating format and writes a non-envelope error
// line.
func TestJournalSince_InProc_UsageErrorInvalidFormat(t *testing.T) {
	projectRoot, _ := stageJournalListProject(t)
	t.Chdir(projectRoot)

	out, _, err := runJournalSinceInProc(t, "6h", "--format=yaml")
	if err == nil {
		t.Fatalf("want usage error, got nil stdout=%s", out)
	}
	var exit *journalExitError
	if !errors.As(err, &exit) {
		t.Fatalf("want *journalExitError, got %T", err)
	}
	if exit.ExitCode != 2 {
		t.Fatalf("exit = %d, want 2", exit.ExitCode)
	}
	// Fallback rule: content → json for errors, yaml → yaml (passthrough
	// to writeJournalEnvelope, which then writes JSON because text is
	// the only special case). Either way, stdout must not be empty.
	if strings.TrimSpace(out) == "" {
		t.Fatalf("invalid-format stdout empty — envelope fallback not engaged")
	}
	var env journalEnvelope
	if jerr := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &env); jerr != nil {
		t.Fatalf("unmarshal: %v stdout=%s", jerr, out)
	}
	if env.Error == nil || env.Error.Code != "usage_error" {
		t.Fatalf("error=%+v, want usage_error", env.Error)
	}
}

// TestJournalSince_InProc_TzFlagRejectedByCobra — --tz is deliberately
// NOT registered on since (only on list) because `since` is relative-
// duration-only and the window is anchored in UTC. cobra must reject
// the unknown flag at parse time, before runJournalSince sees
// anything.
//
// Failure prevented: a future commit adds --tz to since and silently
// accepts a zone hint that is ignored by the UTC-only reader, creating
// a trap where humans think they got a zoned window but didn't.
func TestJournalSince_InProc_TzFlagRejectedByCobra(t *testing.T) {
	projectRoot, _ := stageJournalListProject(t)
	t.Chdir(projectRoot)

	_, _, err := runJournalSinceInProc(t, "6h", "--tz=America/Los_Angeles")
	if err == nil {
		t.Fatalf("--tz must not be accepted")
	}
	// Cobra returns its own error for unknown flags (NOT
	// *journalExitError). The test only needs to confirm the command
	// does not run to completion under --tz.
	if errors.As(err, new(*journalExitError)) {
		t.Fatalf("--tz reached runJournalSince instead of cobra unknown-flag path: %v", err)
	}
}

// TestJournalSince_InProc_LatestFlagRejectedByCobra — --latest is
// plan-banned on every journal read command. Since must reject it at
// cobra parse time. Same rationale as the list/show tests of the same
// shape: no newest-snapshot shortcut.
func TestJournalSince_InProc_LatestFlagRejectedByCobra(t *testing.T) {
	projectRoot, _ := stageJournalListProject(t)
	t.Chdir(projectRoot)

	_, _, err := runJournalSinceInProc(t, "6h", "--latest")
	if err == nil {
		t.Fatalf("--latest must not be accepted")
	}
	if errors.As(err, new(*journalExitError)) {
		t.Fatalf("--latest reached runJournalSince: %v", err)
	}
}
