package tokenopt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestCompress_PassesThroughUserAndHeader(t *testing.T) {
	userLine, _ := json.Marshal(map[string]any{
		"type":      "user",
		"content":   "hello \x1b[31m red \x1b[0m",
		"timestamp": "2026-01-01T00:00:00Z",
	})
	input := `{"metadata":{"version":"1.0"},"type":"header"}` + "\n" + string(userLine) + "\n"

	var out bytes.Buffer
	stats, err := CompressWith(strings.NewReader(input), &out, Options{Mode: ModeLossless})
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	var userOut entry
	if err := json.Unmarshal([]byte(lines[1]), &userOut); err != nil {
		t.Fatalf("unmarshal user entry: %v", err)
	}
	if !strings.Contains(userOut.Content, "\x1b[31m") {
		t.Errorf("expected ANSI preserved in user turn, got: %q", userOut.Content)
	}
	if stats.ANSIStripped != 0 {
		t.Errorf("expected 0 ANSI stripped on user/header, got %d", stats.ANSIStripped)
	}
	if stats.EntriesIn != 2 || stats.EntriesOut != 2 {
		t.Errorf("entries mismatch: in=%d out=%d", stats.EntriesIn, stats.EntriesOut)
	}
}

func TestCompress_StripsANSIFromAssistant(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"type":    "assistant",
		"content": "hello \x1b[31mred\x1b[0m world",
	})
	input := string(raw) + "\n"

	var out bytes.Buffer
	stats, err := CompressWith(strings.NewReader(input), &out, Options{Mode: ModeLossless})
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}

	var e entry
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &e); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if strings.Contains(e.Content, "\x1b") {
		t.Errorf("expected ANSI stripped, got: %q", e.Content)
	}
	if !strings.Contains(e.Content, "red") {
		t.Errorf("expected text preserved, got: %q", e.Content)
	}
	if stats.ANSIStripped != 1 {
		t.Errorf("expected ANSIStripped=1, got %d", stats.ANSIStripped)
	}
}

func TestCompress_CollapsesProgressBars(t *testing.T) {
	input := `{"type":"tool","tool_name":"Bash","content":"downloading [    ]\rdownloading [====]\rdownloading [========] done"}` + "\n"

	var out bytes.Buffer
	stats, err := CompressWith(strings.NewReader(input), &out, Options{Mode: ModeLossless})
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}

	var e entry
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &e); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if strings.Contains(e.Content, "\r") {
		t.Errorf("expected carriage returns collapsed, got: %q", e.Content)
	}
	if !strings.Contains(e.Content, "done") {
		t.Errorf("expected final frame preserved, got: %q", e.Content)
	}
	if stats.ProgressCollapsed != 1 {
		t.Errorf("expected ProgressCollapsed=1, got %d", stats.ProgressCollapsed)
	}
}

func TestCompress_ElidesBase64DataURIs(t *testing.T) {
	payload := strings.Repeat("A", 200)
	input := `{"type":"tool","tool_name":"Read","content":"![img](data:image/png;base64,` + payload + `)"}` + "\n"

	var out bytes.Buffer
	stats, err := CompressWith(strings.NewReader(input), &out, Options{Mode: ModeLossless})
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}

	if strings.Contains(out.String(), payload) {
		t.Errorf("expected base64 payload elided, output still contains it")
	}
	if !strings.Contains(out.String(), "[image: image/png") {
		t.Errorf("expected image marker, got: %s", out.String())
	}
	if stats.ImagesElided != 1 {
		t.Errorf("expected ImagesElided=1, got %d", stats.ImagesElided)
	}
}

func TestCompress_TruncatesLargeReadBodies(t *testing.T) {
	lines := make([]string, 200)
	for i := range lines {
		lines[i] = "line" + strings.Repeat("x", 20)
	}
	body, _ := json.Marshal(strings.Join(lines, "\n"))
	input := `{"type":"tool","tool_name":"Read","tool_output":` + string(body) + `}` + "\n"

	var out bytes.Buffer
	stats, err := CompressWith(strings.NewReader(input), &out, Options{Mode: ModeLossless})
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}

	var e entry
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &e); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.Contains(e.ToolOutput, "lines elided from Read") {
		t.Errorf("expected elision marker, got: %s", e.ToolOutput)
	}
	if stats.LargeReadsElided != 1 {
		t.Errorf("expected LargeReadsElided=1, got %d", stats.LargeReadsElided)
	}
	if stats.BytesOut >= stats.BytesIn {
		t.Errorf("expected output smaller than input, got in=%d out=%d", stats.BytesIn, stats.BytesOut)
	}
}

func TestCompress_DedupsSystemReminders(t *testing.T) {
	reminder := "<system-reminder>long boilerplate text that appears twice</system-reminder>"
	line1, _ := json.Marshal(map[string]any{"type": "assistant", "content": "ack1 " + reminder})
	line2, _ := json.Marshal(map[string]any{"type": "assistant", "content": "ack2 " + reminder})
	input := string(line1) + "\n" + string(line2) + "\n"

	var out bytes.Buffer
	stats, err := CompressWith(strings.NewReader(input), &out, Options{Mode: ModeLossless})
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}

	if stats.RemindersDeduped != 1 {
		t.Errorf("expected RemindersDeduped=1, got %d", stats.RemindersDeduped)
	}
	// Parse each output line and check the decoded content for the ref marker.
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 output lines, got %d", len(lines))
	}
	var second entry
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("unmarshal line 2: %v", err)
	}
	if !strings.Contains(second.Content, "<system-reminder ref=") {
		t.Errorf("expected dedup ref marker in second line, got: %q", second.Content)
	}
}

func TestCompress_DedupsToolResults(t *testing.T) {
	body := strings.Repeat("foo bar baz\n", 100)
	bodyJSON, _ := json.Marshal(body)
	input := strings.Join([]string{
		`{"type":"tool","tool_name":"Bash","tool_output":` + string(bodyJSON) + `}`,
		`{"type":"tool","tool_name":"Bash","tool_output":` + string(bodyJSON) + `}`,
		`{"type":"tool","tool_name":"Bash","tool_output":` + string(bodyJSON) + `}`,
	}, "\n") + "\n"

	var out bytes.Buffer
	stats, err := CompressWith(strings.NewReader(input), &out, Options{Mode: ModeLossless})
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}

	if stats.ToolResultsRefd != 2 {
		t.Errorf("expected ToolResultsRefd=2, got %d", stats.ToolResultsRefd)
	}
	if !strings.Contains(out.String(), `"type":"tool_ref"`) {
		t.Errorf("expected tool_ref entries in output, got: %s", out.String())
	}
	if stats.BytesOut >= stats.BytesIn {
		t.Errorf("expected reduction, got in=%d out=%d", stats.BytesIn, stats.BytesOut)
	}
}

func TestCompress_SmallToolResultsNotDeduped(t *testing.T) {
	bodyJSON, _ := json.Marshal("tiny output")
	input := strings.Join([]string{
		`{"type":"tool","tool_name":"Bash","tool_output":` + string(bodyJSON) + `}`,
		`{"type":"tool","tool_name":"Bash","tool_output":` + string(bodyJSON) + `}`,
	}, "\n") + "\n"

	var out bytes.Buffer
	stats, err := CompressWith(strings.NewReader(input), &out, Options{Mode: ModeLossless})
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if stats.ToolResultsRefd != 0 {
		t.Errorf("expected small bodies not deduped, got ToolResultsRefd=%d", stats.ToolResultsRefd)
	}
}

func TestCompress_MalformedJSONLinesPassThrough(t *testing.T) {
	input := `not json at all` + "\n" +
		`{"type":"user","content":"ok"}` + "\n"

	var out bytes.Buffer
	stats, err := CompressWith(strings.NewReader(input), &out, Options{Mode: ModeLossless})
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if !strings.Contains(out.String(), "not json at all") {
		t.Errorf("malformed line should pass through verbatim")
	}
	if stats.EntriesOut != 2 {
		t.Errorf("expected both entries emitted, got %d", stats.EntriesOut)
	}
}

// ---- ConversationOnly mode (default) ----

func TestConversationOnly_KeepsUserAndAssistantDropsToolsAndSystem(t *testing.T) {
	userLine, _ := json.Marshal(map[string]any{"type": "user", "content": "fix the lint errors"})
	asstLine, _ := json.Marshal(map[string]any{"type": "assistant", "content": "I'll run make lint first"})
	// Bash carries a `description` field — emits a tool_mark with that
	// description as the only narrative field.
	toolInput, _ := json.Marshal(map[string]any{"command": "make lint", "description": "run lint"})
	toolLine, _ := json.Marshal(map[string]any{"type": "tool", "tool_name": "Bash", "tool_input": string(toolInput)})
	sysLine, _ := json.Marshal(map[string]any{"type": "system", "content": "reminder: commit soon"})
	input := `{"metadata":{"v":"1"},"type":"header"}` + "\n" +
		string(userLine) + "\n" + string(asstLine) + "\n" +
		string(toolLine) + "\n" + string(sysLine) + "\n"

	var out bytes.Buffer
	stats, err := Compress(strings.NewReader(input), &out)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	// user + assistant + tool_mark (header AND system both dropped in
	// ConversationOnly) = 3
	if len(lines) != 3 {
		t.Fatalf("expected 3 output lines, got %d: %v", len(lines), lines)
	}
	if stats.ToolsMarked != 1 {
		t.Errorf("expected ToolsMarked=1, got %d", stats.ToolsMarked)
	}
	// SystemDropped counts the actual system entry plus any
	// description-less tool drops (none here, since Bash had a description).
	if stats.SystemDropped != 1 {
		t.Errorf("expected SystemDropped=1 (the system entry only), got %d", stats.SystemDropped)
	}
	if stats.HeaderDropped != 1 {
		t.Errorf("expected HeaderDropped=1, got %d", stats.HeaderDropped)
	}
	if stats.EntriesOut != 3 {
		t.Errorf("expected EntriesOut=3, got %d", stats.EntriesOut)
	}

	// Assistant passes through verbatim.
	var asst entry
	_ = json.Unmarshal([]byte(lines[1]), &asst)
	if asst.Content != "I'll run make lint first" {
		t.Errorf("assistant content not verbatim: %q", asst.Content)
	}

	// Tool entry becomes a tool_mark carrying ONLY the description.
	// Failure prevented: a regression that re-introduces tool_name, brief,
	// or raw input fields silently re-inflates the optimized stream.
	var mark entry
	_ = json.Unmarshal([]byte(lines[2]), &mark)
	if mark.Type != "tool_mark" {
		t.Errorf("tool_mark type mismatch: %+v", mark)
	}
	if mark.Description != "run lint" {
		t.Errorf("tool_mark description=%q, want %q", mark.Description, "run lint")
	}
	if mark.ToolName != "" {
		t.Errorf("tool_mark must not carry tool_name (cost-driven; see ModeConversationOnly doc): %+v", mark)
	}
	// Wire-format guards against silent re-inflation.
	for _, banned := range []string{"\"brief\"", "\"tool_name\"", "\"tool_input\"", "\"tool_output\"", "\"is_error\""} {
		if strings.Contains(lines[2], banned) {
			t.Errorf("tool_mark JSON must not include %s: %s", banned, lines[2])
		}
	}
}

// TestConversationOnly_ToolWithoutDescriptionDropped verifies that tool
// entries lacking a `description` field in tool_input (Edit, Read, Write,
// Glob, Grep, ...) are dropped from the optimized stream entirely.
//
// Failure prevented: a regression that re-emits content-free tool_marks
// for description-less tools, inflating the optimized stream with markers
// the summarizer can't extract any narrative from anyway.
func TestConversationOnly_ToolWithoutDescriptionDropped(t *testing.T) {
	asst, _ := json.Marshal(map[string]any{"type": "assistant", "content": "ok"})
	editInput, _ := json.Marshal(map[string]any{"file_path": "/foo.go", "old_string": "x", "new_string": "y"})
	editLine, _ := json.Marshal(map[string]any{"type": "tool", "tool_name": "Edit", "tool_input": string(editInput)})
	asst2, _ := json.Marshal(map[string]any{"type": "assistant", "content": "done"})
	input := string(asst) + "\n" + string(editLine) + "\n" + string(asst2) + "\n"

	var out bytes.Buffer
	stats, err := Compress(strings.NewReader(input), &out)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}

	// Output should be assistant + assistant — the Edit dropped entirely.
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 output lines (asst + asst, no tool_mark), got %d: %v", len(lines), lines)
	}
	if strings.Contains(out.String(), "tool_mark") {
		t.Errorf("description-less tool must produce no tool_mark; output: %s", out.String())
	}
	// ToolsMarked still increments (we observed a tool entry); the
	// drop is recorded under SystemDropped (the "dropped without
	// replacement" counter).
	if stats.ToolsMarked != 1 {
		t.Errorf("ToolsMarked=%d, want 1", stats.ToolsMarked)
	}
}

// TestConversationOnly_DescriptionTrimmedAndCapped verifies that the
// description field is trimmed of surrounding whitespace and capped at
// 200 chars to bound pathological agent output.
func TestConversationOnly_DescriptionTrimmedAndCapped(t *testing.T) {
	long := strings.Repeat("very long description ", 30) // ~660 chars
	toolInput, _ := json.Marshal(map[string]any{"description": "  " + long + "  "})
	toolLine, _ := json.Marshal(map[string]any{"type": "tool", "tool_name": "Agent", "tool_input": string(toolInput)})

	var out bytes.Buffer
	if _, err := Compress(strings.NewReader(string(toolLine)+"\n"), &out); err != nil {
		t.Fatalf("Compress: %v", err)
	}
	var mark entry
	_ = json.Unmarshal(bytes.TrimSpace(out.Bytes()), &mark)
	if len(mark.Description) > 200 {
		t.Errorf("description not capped at 200 chars: len=%d", len(mark.Description))
	}
	if strings.HasPrefix(mark.Description, " ") || strings.HasSuffix(mark.Description, " ") {
		t.Errorf("description not whitespace-trimmed: %q", mark.Description)
	}
}

// TestConversationOnly_HeaderDropped verifies header entries are dropped
// from the optimized stream in ConversationOnly mode. Failure prevented:
// silent regression that puts the ~50-token header back into every
// summarization prompt — wasted tokens with zero downstream consumer.
// Failure prevented (more important): header NOT dropped from
// ModeLossless, where replay/debug consumers expect every entry.
func TestConversationOnly_HeaderDropped(t *testing.T) {
	hdr := `{"type":"header","metadata":{"agent_type":"claude-code","model":"claude-sonnet-4","ox_version":"0.7.1"}}`
	user, _ := json.Marshal(map[string]any{"type": "user", "content": "hi"})
	asst, _ := json.Marshal(map[string]any{"type": "assistant", "content": "hello"})
	input := hdr + "\n" + string(user) + "\n" + string(asst) + "\n"

	var out bytes.Buffer
	stats, err := Compress(strings.NewReader(input), &out)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if stats.HeaderDropped != 1 {
		t.Errorf("ConversationOnly: HeaderDropped=%d, want 1", stats.HeaderDropped)
	}
	if strings.Contains(out.String(), `"type":"header"`) {
		t.Errorf("ConversationOnly: header survived in output: %s", out.String())
	}

	// Lossless mode preserves header.
	var outL bytes.Buffer
	statsL, err := CompressWith(strings.NewReader(input), &outL, Options{Mode: ModeLossless})
	if err != nil {
		t.Fatalf("CompressWith Lossless: %v", err)
	}
	if statsL.HeaderDropped != 0 {
		t.Errorf("Lossless: HeaderDropped=%d, want 0", statsL.HeaderDropped)
	}
	if !strings.Contains(outL.String(), `"type":"header"`) {
		t.Errorf("Lossless: header missing from output: %s", outL.String())
	}
}

// TestConversationOnly_ToolMarkBatching verifies the description-batching
// pipeline:
//
//   - Edits without descriptions drop entirely (no tool_mark)
//   - Adjacent Bash calls with identical descriptions collapse into one
//     tool_mark with count=N
//   - Bash calls with different descriptions stay as separate marks
//
// Failure prevented: a regression that batches across distinct
// descriptions (collapsing "run tests" + "run lint" into a single
// indistinguishable mark) loses real intent signal in the summary;
// or one that fails to drop description-less tools, re-inflating the
// stream with content-free markers.
func TestConversationOnly_ToolMarkBatching(t *testing.T) {
	mk := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return string(b)
	}
	// description-less Edit: should drop entirely
	editFoo := mk(map[string]any{
		"type": "tool", "tool_name": "Edit",
		"tool_input": `{"file_path":"/foo.go"}`,
	})
	// Bash with description "run tests": batchable run
	bashTests := mk(map[string]any{
		"type": "tool", "tool_name": "Bash",
		"tool_input": `{"command":"make test","description":"run tests"}`,
	})
	// Bash with description "run lint": different run, must NOT batch with above
	bashLint := mk(map[string]any{
		"type": "tool", "tool_name": "Bash",
		"tool_input": `{"command":"make lint","description":"run lint"}`,
	})

	input := strings.Join([]string{
		mk(map[string]any{"type": "user", "content": "go"}),
		mk(map[string]any{"type": "assistant", "content": "ok"}),
		editFoo, editFoo, editFoo, // 3 description-less Edits → all dropped
		bashTests, bashTests, // 2 same-description → collapse to count=2
		bashLint, // different description → separate mark
		mk(map[string]any{"type": "assistant", "content": "done"}),
		"",
	}, "\n")

	var out bytes.Buffer
	stats, err := Compress(strings.NewReader(input), &out)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}

	// 9 input entries → user + asst + tool_mark("run tests" ×2) +
	// tool_mark("run lint") + asst = 5 output entries. The 3 Edits drop.
	if stats.EntriesOut != 5 {
		t.Errorf("EntriesOut=%d, want 5", stats.EntriesOut)
	}
	if stats.ToolsMarked != 6 {
		t.Errorf("ToolsMarked=%d, want 6 (counts every input tool entry, including dropped ones)", stats.ToolsMarked)
	}
	// ToolsBatched: only the second bashTests is absorbed into pending
	// (1 absorption). The Edits drop without going through pending.
	if stats.ToolsBatched != 1 {
		t.Errorf("ToolsBatched=%d, want 1 (only 1 same-description duplicate)", stats.ToolsBatched)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	descriptions := map[string]int{} // description → count
	for _, line := range lines {
		if !strings.Contains(line, `"type":"tool_mark"`) {
			continue
		}
		// Must NOT contain any banned per-call detail.
		for _, banned := range []string{"\"tool_name\"", "\"brief\"", "\"tool_input\"", "\"tool_output\"", "\"is_error\""} {
			if strings.Contains(line, banned) {
				t.Errorf("tool_mark must not carry %s; got: %s", banned, line)
			}
		}
		var e entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("unmarshal %q: %v", line, err)
		}
		c := e.Count
		if c == 0 {
			c = 1 // omitted count == 1
		}
		descriptions[e.Description] = c
	}
	if descriptions["run tests"] != 2 {
		t.Errorf("run tests count=%d, want 2 (two adjacent same-description calls collapsed)", descriptions["run tests"])
	}
	if descriptions["run lint"] != 1 {
		t.Errorf("run lint count=%d, want 1 (single occurrence)", descriptions["run lint"])
	}
}

// TestConversationOnly_TokenEstimateSane verifies TokensInEstimate and
// TokensOutEstimate are populated and reflect a real reduction.
// Failure prevented: telemetry reports zero token counts, breaking the
// cost dashboard that downstream beads (#1, #2) depend on.
func TestConversationOnly_TokenEstimateSane(t *testing.T) {
	mk := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return string(b)
	}
	bigInput, _ := json.Marshal(map[string]any{"command": strings.Repeat("echo x; ", 200)})
	tool := mk(map[string]any{
		"type": "tool", "tool_name": "Bash", "tool_input": string(bigInput),
	})
	input := strings.Join([]string{
		mk(map[string]any{"type": "user", "content": "go"}),
		tool, tool, tool, tool, tool,
		"",
	}, "\n")

	var out bytes.Buffer
	stats, err := Compress(strings.NewReader(input), &out)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if stats.TokensInEstimate <= 0 {
		t.Errorf("TokensInEstimate=%d, want >0", stats.TokensInEstimate)
	}
	if stats.TokensOutEstimate <= 0 {
		t.Errorf("TokensOutEstimate=%d, want >0", stats.TokensOutEstimate)
	}
	if stats.TokensOutEstimate >= stats.TokensInEstimate {
		t.Errorf("expected token reduction; in=%d out=%d", stats.TokensInEstimate, stats.TokensOutEstimate)
	}
	saved, pct := stats.TokenReduction()
	if saved <= 0 || pct <= 0 {
		t.Errorf("TokenReduction reports no savings: saved=%d pct=%.1f", saved, pct)
	}
}

func TestConversationOnly_ReducesBytesSubstantially(t *testing.T) {
	// Build a session where tool entries dominate (realistic).
	var b strings.Builder
	b.WriteString(`{"metadata":{"v":"1"},"type":"header"}` + "\n")
	userLine, _ := json.Marshal(map[string]any{"type": "user", "content": "do the thing"})
	b.Write(userLine)
	b.WriteString("\n")
	asstLine, _ := json.Marshal(map[string]any{"type": "assistant", "content": "working on it"})
	b.Write(asstLine)
	b.WriteString("\n")
	// 20 tool entries, each with ~1KB of tool_input (plausible for Agent/Edit calls).
	bigInput, _ := json.Marshal(map[string]any{"command": strings.Repeat("echo x; ", 200)})
	for i := 0; i < 20; i++ {
		toolLine, _ := json.Marshal(map[string]any{
			"type":       "tool",
			"tool_name":  "Bash",
			"tool_input": string(bigInput),
		})
		b.Write(toolLine)
		b.WriteString("\n")
	}

	var out bytes.Buffer
	stats, err := Compress(strings.NewReader(b.String()), &out)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	_, pct := stats.Reduction()
	if pct < 80 {
		t.Errorf("expected >80%% reduction in ConversationOnly mode, got %.1f%%", pct)
	}
	if stats.ToolsMarked != 20 {
		t.Errorf("expected ToolsMarked=20, got %d", stats.ToolsMarked)
	}
}

// ---- Regression tests for review fixes ----

// TestCompress_HandlesOversizedLine guards against the previous 8 MiB
// bufio.Scanner token limit. A single entry > 8 MiB would previously have
// failed the whole stream; now it streams through via bufio.Reader.ReadBytes.
func TestCompress_HandlesOversizedLine(t *testing.T) {
	bigContent := strings.Repeat("x", 10*1024*1024) // 10 MiB
	entryJSON, _ := json.Marshal(map[string]any{"type": "assistant", "content": bigContent})
	input := string(entryJSON) + "\n"

	var out bytes.Buffer
	stats, err := Compress(strings.NewReader(input), &out)
	if err != nil {
		t.Fatalf("Compress on 10 MiB line failed: %v", err)
	}
	if stats.EntriesIn != 1 || stats.EntriesOut != 1 {
		t.Errorf("expected 1/1 entries, got in=%d out=%d", stats.EntriesIn, stats.EntriesOut)
	}
}

// TestCompress_ReturnsFlushError catches a bug where deferred bw.Flush()
// silently swallowed write failures and made a truncated optimized file look
// like a success.
type failWriter struct{}

func (failWriter) Write(p []byte) (int, error) { return 0, fmt.Errorf("disk full") }

func TestCompress_ReturnsFlushError(t *testing.T) {
	// Write enough data that bufio.Writer won't flush until the final call.
	var b strings.Builder
	for i := 0; i < 10; i++ {
		line, _ := json.Marshal(map[string]any{"type": "assistant", "content": strings.Repeat("y", 100)})
		b.Write(line)
		b.WriteString("\n")
	}
	_, err := Compress(strings.NewReader(b.String()), failWriter{})
	if err == nil {
		t.Fatal("expected error when writer fails, got nil")
	}
}

// TestCompressLossless_PreservesUnknownTopLevelFields locks in the contract
// that ModeLossless is actually lossless. Previously, re-marshaling through
// the fixed entry struct silently dropped keys (like "seq") not on the struct.
func TestCompressLossless_PreservesUnknownTopLevelFields(t *testing.T) {
	// An assistant entry with a "seq" field (written by cmd/ox/agent_session).
	raw, _ := json.Marshal(map[string]any{
		"type":    "assistant",
		"content": "hello \x1b[31mred\x1b[0m world", // force a content mutation so lossless re-encodes
		"seq":     42,
		"extra":   map[string]any{"nested": true},
	})
	input := string(raw) + "\n"

	var out bytes.Buffer
	_, err := CompressWith(strings.NewReader(input), &out, Options{Mode: ModeLossless})
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := got["seq"]; !ok {
		t.Errorf("lossless dropped unknown top-level field 'seq'; got: %v", got)
	}
	if _, ok := got["extra"]; !ok {
		t.Errorf("lossless dropped unknown top-level field 'extra'; got: %v", got)
	}
	// Content was mutated (ANSI stripped), so that field should reflect change.
	if s, _ := got["content"].(string); strings.Contains(s, "\x1b") {
		t.Errorf("expected ANSI stripped from content, got: %q", s)
	}
}

// TestTruncateLargeBody_ClampsHugeKeepLines guards against a panic when a
// caller provides a LargeReadKeepLines larger than half the body.
func TestTruncateLargeBody_ClampsHugeKeepLines(t *testing.T) {
	// 50 lines, Options.LargeReadKeepLines = 1000 (absurd).
	lines := make([]string, 50)
	for i := range lines {
		lines[i] = "row"
	}
	body, _ := json.Marshal(strings.Join(lines, "\n"))
	input := `{"type":"tool","tool_name":"Read","tool_output":` + string(body) + `}` + "\n"

	var out bytes.Buffer
	// This MUST NOT panic.
	_, err := CompressWith(strings.NewReader(input), &out, Options{
		Mode:               ModeLossless,
		LargeReadMaxLines:  10,
		LargeReadKeepLines: 1000,
	})
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
}

// ---- Stats helpers ----

func TestStats_Reduction(t *testing.T) {
	s := Stats{BytesIn: 1000, BytesOut: 250}
	saved, pct := s.Reduction()
	if saved != 750 {
		t.Errorf("saved: got %d want 750", saved)
	}
	if pct != 75.0 {
		t.Errorf("pct: got %f want 75.0", pct)
	}

	empty := Stats{}
	if _, pct := empty.Reduction(); pct != 0 {
		t.Errorf("empty pct: got %f want 0", pct)
	}
}

func TestStats_Add(t *testing.T) {
	a := Stats{EntriesIn: 10, BytesIn: 1000, BytesOut: 500, ANSIStripped: 3}
	b := Stats{EntriesIn: 5, BytesIn: 500, BytesOut: 100, ANSIStripped: 2}
	a.Add(b)
	if a.EntriesIn != 15 || a.BytesIn != 1500 || a.BytesOut != 600 || a.ANSIStripped != 5 {
		t.Errorf("Add mismatch: %+v", a)
	}
}

func TestLRU_EvictsOldest(t *testing.T) {
	l := newLRU(2)
	l.put("a", 1)
	l.put("b", 2)
	l.put("c", 3)

	if _, ok := l.get("a"); ok {
		t.Error("expected a evicted")
	}
	if v, ok := l.get("b"); !ok || v != 2 {
		t.Errorf("expected b=2, got %d ok=%v", v, ok)
	}
	l.put("d", 4)
	if _, ok := l.get("c"); ok {
		t.Error("expected c evicted after b was touched")
	}
}
