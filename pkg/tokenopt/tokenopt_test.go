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
	// header + user + assistant + tool_mark (system dropped) = 4
	if len(lines) != 4 {
		t.Fatalf("expected 4 output lines, got %d: %v", len(lines), lines)
	}
	if stats.ToolsMarked != 1 {
		t.Errorf("expected ToolsMarked=1, got %d", stats.ToolsMarked)
	}
	if stats.SystemDropped != 1 {
		t.Errorf("expected SystemDropped=1, got %d", stats.SystemDropped)
	}
	if stats.EntriesOut != 4 {
		t.Errorf("expected EntriesOut=4, got %d", stats.EntriesOut)
	}

	// Assistant passes through verbatim.
	var asst entry
	_ = json.Unmarshal([]byte(lines[2]), &asst)
	if asst.Content != "I'll run make lint first" {
		t.Errorf("assistant content not verbatim: %q", asst.Content)
	}

	// Tool entry becomes tool_mark with a brief.
	var mark entry
	_ = json.Unmarshal([]byte(lines[3]), &mark)
	if mark.Type != "tool_mark" || mark.ToolName != "Bash" || mark.Brief != "make lint" {
		t.Errorf("tool_mark mismatch: %+v", mark)
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

func TestBriefForTool(t *testing.T) {
	cases := []struct {
		name       string
		tool       string
		input      string
		wantSubstr string
	}{
		{"bash command", "Bash", `{"command":"make lint","description":"x"}`, "make lint"},
		{"read file_path", "Read", `{"file_path":"/a/b.go","limit":50}`, "/a/b.go"},
		{"grep pattern", "Grep", `{"pattern":"foo.*bar","path":"."}`, "foo.*bar"},
		{"agent description", "Agent", `{"description":"fix bugs","prompt":"long..."}`, "fix bugs"},
		{"unknown tool falls back to first string", "WeirdTool", `{"x":"hello","y":1}`, "hello"},
		{"truncates long briefs", "Bash", `{"command":"` + strings.Repeat("x", 200) + `"}`, "…"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := briefForTool(c.tool, c.input)
			if !strings.Contains(got, c.wantSubstr) {
				t.Errorf("got %q, want substring %q", got, c.wantSubstr)
			}
		})
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

// TestBriefForTool_DeterministicForUnknownTool verifies unknown-tool briefs
// don't flip based on Go's randomized map iteration.
func TestBriefForTool_DeterministicForUnknownTool(t *testing.T) {
	input := `{"z_last":"zeta","a_first":"alpha","m_mid":"mu"}`
	// Many invocations — same input must produce same brief every time.
	first := briefForTool("UnknownTool", input)
	for i := 0; i < 50; i++ {
		got := briefForTool("UnknownTool", input)
		if got != first {
			t.Fatalf("non-deterministic: iter %d got %q, first was %q", i, got, first)
		}
	}
	// Sorted-key order means "a_first" wins.
	if first != "alpha" {
		t.Errorf("expected sorted-key 'alpha' to be selected, got %q", first)
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
