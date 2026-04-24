package tokenopt

import (
	"bytes"
	"encoding/json"
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
	stats, err := Compress(strings.NewReader(input), &out)
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
	stats, err := Compress(strings.NewReader(input), &out)
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
	stats, err := Compress(strings.NewReader(input), &out)
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
	stats, err := Compress(strings.NewReader(input), &out)
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
	stats, err := Compress(strings.NewReader(input), &out)
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
	stats, err := Compress(strings.NewReader(input), &out)
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
	stats, err := Compress(strings.NewReader(input), &out)
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
	stats, err := Compress(strings.NewReader(input), &out)
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
	stats, err := Compress(strings.NewReader(input), &out)
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
