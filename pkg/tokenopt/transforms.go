package tokenopt

import (
	"container/list"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// state holds streaming dedup bookkeeping for a single Compress invocation.
type state struct {
	opts        Options
	reminders   map[string]int // sha → first seq
	toolResults *lruMap        // sha → first seq (bounded)
	reminderRe  *regexp.Regexp // matches <system-reminder> ... </system-reminder>
	ansiRe      *regexp.Regexp // matches ANSI CSI/SGR sequences
	base64ImgRe *regexp.Regexp // matches data:image/...;base64,<payload>
}

func newState(opts Options) *state {
	return &state{
		opts:        opts,
		reminders:   make(map[string]int),
		toolResults: newLRU(opts.ToolResultLRUSize),
		// <system-reminder>...</system-reminder> — inline or multi-line (non-greedy)
		reminderRe: regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`),
		// Standard ANSI escapes: ESC [ ... letter. Covers SGR, cursor ops, etc.
		ansiRe: regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`),
		// data URI image payloads (the high-value case — screenshots in tool_results)
		base64ImgRe: regexp.MustCompile(`data:image/[a-zA-Z.+-]+;base64,[A-Za-z0-9+/=\s]+`),
	}
}

// transform applies the streaming pipeline to a single jsonl line. Returns the
// bytes to emit (if emit=true) or signals the entry should be dropped.
// On parse failure the line passes through untouched.
func (s *state) transform(line []byte, seq int, stats *Stats) ([]byte, bool, error) {
	var e entry
	if err := json.Unmarshal(line, &e); err != nil {
		return line, true, nil
	}

	// Header and user turns are always preserved verbatim in every mode.
	if e.Type == "user" || e.Type == "header" {
		return line, true, nil
	}

	if s.opts.Mode == ModeConversationOnly {
		return s.transformConversationOnly(&e, line, stats)
	}
	return s.transformLossless(line, &e, seq, stats)
}

// transformConversationOnly keeps assistant turns verbatim, replaces tool
// entries with compact markers, and drops system entries.
func (s *state) transformConversationOnly(e *entry, line []byte, stats *Stats) ([]byte, bool, error) {
	switch e.Type {
	case "assistant":
		// Pass through verbatim — assistant prose IS the narrative.
		return line, true, nil
	case "tool":
		marker := entry{
			Type:     "tool_mark",
			ToolName: e.ToolName,
			Brief:    briefForTool(e.ToolName, e.ToolInput),
			IsError:  e.IsError,
		}
		out, err := json.Marshal(&marker)
		if err != nil {
			return line, true, nil
		}
		stats.ToolsMarked++
		return out, true, nil
	case "system":
		stats.SystemDropped++
		return nil, false, nil
	default:
		// Unknown types: preserve to stay safe.
		return line, true, nil
	}
}

// transformLossless is the content-preserving pipeline. Preserves unknown
// top-level fields on the entry by round-tripping through map[string]any,
// mutating only the specific fields we know how to transform.
func (s *state) transformLossless(line []byte, e *entry, seq int, stats *Stats) ([]byte, bool, error) {
	// Mutate local copies on the typed struct so we can reuse the existing
	// transform helpers, then write the mutations back into a generic map
	// that preserves any top-level keys we don't know about (e.g. "seq",
	// future schema extensions).
	origContent := e.Content
	origToolOutput := e.ToolOutput

	if e.Content != "" {
		e.Content, stats.ANSIStripped, stats.ProgressCollapsed = s.normalizeText(e.Content, stats.ANSIStripped, stats.ProgressCollapsed)
	}
	if e.ToolOutput != "" {
		e.ToolOutput, stats.ANSIStripped, stats.ProgressCollapsed = s.normalizeText(e.ToolOutput, stats.ANSIStripped, stats.ProgressCollapsed)
	}

	if e.Content != "" {
		before := e.Content
		e.Content = s.elideBase64Images(e.Content)
		if before != e.Content {
			stats.ImagesElided++
		}
	}
	if e.ToolOutput != "" {
		before := e.ToolOutput
		e.ToolOutput = s.elideBase64Images(e.ToolOutput)
		if before != e.ToolOutput {
			stats.ImagesElided++
		}
	}

	if e.Content != "" && strings.Contains(e.Content, "<system-reminder>") {
		newContent, deduped := s.dedupReminders(e.Content, seq)
		e.Content = newContent
		stats.RemindersDeduped += deduped
	}

	if e.ToolName == "Read" {
		if e.ToolOutput != "" {
			if truncated, ok := s.truncateLargeBody(e.ToolOutput, "Read"); ok {
				e.ToolOutput = truncated
				stats.LargeReadsElided++
			}
		} else if e.Content != "" {
			if truncated, ok := s.truncateLargeBody(e.Content, "Read"); ok {
				e.Content = truncated
				stats.LargeReadsElided++
			}
		}
	}

	// Tool-result content dedup via bounded LRU. When a hit occurs we replace
	// the entry wholesale with a compact tool_ref, so unknown-field
	// preservation doesn't apply (the output shape changes by design).
	if body := toolResultBody(e); body != "" && len(body) >= s.opts.ToolResultMinBytes {
		h := hashContent([]byte(body))
		if firstSeq, hit := s.toolResults.get(h); hit {
			stats.ToolResultsRefd++
			return emitToolRef(e, h, firstSeq, body), true, nil
		}
		s.toolResults.put(h, seq)
	}

	// No mutations? Return the original bytes verbatim — avoids the tiny
	// JSON round-trip diff (key reordering, whitespace) that otherwise noise-
	// inflates stats.BytesOut.
	if e.Content == origContent && e.ToolOutput == origToolOutput {
		return line, true, nil
	}

	// Decode the original line into a generic map, overwrite only the fields
	// we mutated, re-encode. Keys we didn't touch (including unknown ones
	// like "seq" or future schema additions) survive untouched.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		// Malformed after earlier successful struct-decode shouldn't happen,
		// but if it does, fall through to struct-based re-encode.
		out, err := json.Marshal(e)
		if err != nil {
			return nil, false, fmt.Errorf("marshal: %w", err)
		}
		return out, true, nil
	}
	if e.Content != origContent {
		b, _ := json.Marshal(e.Content)
		raw["content"] = b
	}
	if e.ToolOutput != origToolOutput {
		b, _ := json.Marshal(e.ToolOutput)
		raw["tool_output"] = b
	}
	out, err := json.Marshal(raw)
	if err != nil {
		return nil, false, fmt.Errorf("marshal: %w", err)
	}
	return out, true, nil
}

// normalizeText strips ANSI sequences and collapses \r-based progress frames.
// Returns updated counters.
func (s *state) normalizeText(in string, ansiCount, progCount int) (string, int, int) {
	out := in
	if strings.IndexByte(out, 0x1b) >= 0 {
		stripped := s.ansiRe.ReplaceAllString(out, "")
		if stripped != out {
			ansiCount++
			out = stripped
		}
	}
	if strings.IndexByte(out, '\r') >= 0 {
		collapsed := collapseCarriageReturns(out)
		if collapsed != out {
			progCount++
			out = collapsed
		}
	}
	return out, ansiCount, progCount
}

// collapseCarriageReturns keeps only the final segment per line after \r.
// Common in progress bars: "downloading [=  ] 10%\rdownloading [==] 20%\r...".
func collapseCarriageReturns(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if idx := strings.LastIndexByte(line, '\r'); idx >= 0 {
			lines[i] = line[idx+1:]
		}
	}
	return strings.Join(lines, "\n")
}

// elideBase64Images replaces data: URIs and bare long base64 blobs with a
// compact placeholder. No MIME sniffing on bare blobs — we just note the size.
func (s *state) elideBase64Images(in string) string {
	return s.base64ImgRe.ReplaceAllStringFunc(in, func(match string) string {
		idx := strings.Index(match, ";base64,")
		if idx < 0 {
			return match
		}
		mime := strings.TrimPrefix(match[:idx], "data:")
		payload := match[idx+len(";base64,"):]
		return fmt.Sprintf("[image: %s, %d b64 chars elided]", mime, len(payload))
	})
}

// dedupReminders replaces repeated <system-reminder> blocks with compact refs.
// First occurrence stays verbatim; subsequent identical blocks become a ref.
func (s *state) dedupReminders(content string, seq int) (string, int) {
	count := 0
	out := s.reminderRe.ReplaceAllStringFunc(content, func(match string) string {
		h := hashContent([]byte(match))
		if firstSeq, seen := s.reminders[h]; seen {
			count++
			return fmt.Sprintf("<system-reminder ref=%q first_seq=%d elided />", h, firstSeq)
		}
		s.reminders[h] = seq
		return match
	})
	return out, count
}

// truncateLargeBody replaces the middle of oversized tool_result bodies with an
// elision marker, keeping head and tail lines. Returns (new, true) if truncated.
func (s *state) truncateLargeBody(body, tool string) (string, bool) {
	lines := strings.Split(body, "\n")
	if len(lines) <= s.opts.LargeReadMaxLines {
		return body, false
	}
	keep := s.opts.LargeReadKeepLines
	if keep < 1 {
		return body, false
	}
	// Clamp: Options is public, and a caller-provided LargeReadKeepLines
	// larger than half the body would cause head+tail to overlap and produce
	// a negative elided count. Cap it at len(lines)/2 to degrade gracefully.
	if maxKeep := len(lines) / 2; keep > maxKeep {
		if maxKeep < 1 {
			return body, false
		}
		keep = maxKeep
	}
	head := lines[:keep]
	tail := lines[len(lines)-keep:]
	elided := len(lines) - 2*keep
	marker := fmt.Sprintf("...[%d lines elided from %s output]...", elided, tool)
	var sb strings.Builder
	sb.WriteString(strings.Join(head, "\n"))
	sb.WriteString("\n")
	sb.WriteString(marker)
	sb.WriteString("\n")
	sb.WriteString(strings.Join(tail, "\n"))
	return sb.String(), true
}

// briefForTool extracts a short human-readable gist from a tool_input payload.
// Used by ModeConversationOnly to give the summarizer narrative scaffolding
// (what the agent DID between messages) without the full I/O.
//
// tool_input is itself a JSON string, so we parse it and pick the field most
// representative of the action. Falls back to a truncated view.
func briefForTool(toolName, toolInput string) string {
	const maxBrief = 120
	if toolInput == "" {
		return ""
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(toolInput), &raw); err != nil {
		return truncate(toolInput, maxBrief)
	}

	primary := map[string]string{
		"Bash":         "command",
		"Read":         "file_path",
		"Write":        "file_path",
		"Edit":         "file_path",
		"MultiEdit":    "file_path",
		"NotebookEdit": "notebook_path",
		"Glob":         "pattern",
		"Grep":         "pattern",
		"Agent":        "description",
		"Task":         "description",
		"WebFetch":     "url",
		"WebSearch":    "query",
	}
	if field, ok := primary[toolName]; ok {
		if v, ok := raw[field]; ok {
			if s, ok := v.(string); ok {
				return truncate(s, maxBrief)
			}
		}
	}

	// Unknown tool: pick the first string field in sorted-key order.
	// Sorting makes the brief deterministic; Go map iteration is randomized
	// and this package advertises deterministic output.
	keys := make([]string, 0, len(raw))
	for k, v := range raw {
		if s, ok := v.(string); ok && s != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		if s, ok := raw[k].(string); ok {
			return truncate(s, maxBrief)
		}
	}
	return truncate(toolInput, maxBrief)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

// toolResultBody returns whichever field of the entry carries a tool result
// body, or "" if this isn't a tool-result-bearing entry.
func toolResultBody(e *entry) string {
	if e.Type != "tool" {
		return ""
	}
	if e.ToolOutput != "" {
		return e.ToolOutput
	}
	// Older format: tool results sometimes land in Content.
	if e.Content != "" {
		return e.Content
	}
	return ""
}

// emitToolRef replaces a tool entry's body with a self-describing ref entry.
// Preserves tool_name, timestamp, and is_error so the summarizer still knows
// this was (e.g.) a Grep call — just a repeated one.
func emitToolRef(e *entry, hash string, firstSeq int, originalBody string) []byte {
	sample := originalBody
	if len(sample) > 80 {
		sample = sample[:80]
	}
	ref := entry{
		Type:      "tool_ref",
		Timestamp: e.Timestamp,
		ToolName:  e.ToolName,
		IsError:   e.IsError,
		RefType:   "tool_result",
		RefKind:   e.ToolName,
		RefHash:   hash,
		RefFirst:  firstSeq,
		RefSample: sample,
	}
	out, err := json.Marshal(&ref)
	if err != nil {
		// This cannot happen for our struct; fall back to original body.
		b, _ := json.Marshal(e)
		return b
	}
	return out
}

// lruMap is a minimal string→int LRU cache. Bounded by capacity.
type lruMap struct {
	cap   int
	m     map[string]*list.Element
	order *list.List
}

type lruEntry struct {
	key string
	val int
}

func newLRU(capacity int) *lruMap {
	if capacity < 1 {
		capacity = 1
	}
	return &lruMap{cap: capacity, m: make(map[string]*list.Element, capacity), order: list.New()}
}

func (l *lruMap) get(k string) (int, bool) {
	el, ok := l.m[k]
	if !ok {
		return 0, false
	}
	l.order.MoveToFront(el)
	return el.Value.(*lruEntry).val, true
}

func (l *lruMap) put(k string, v int) {
	if el, ok := l.m[k]; ok {
		el.Value.(*lruEntry).val = v
		l.order.MoveToFront(el)
		return
	}
	el := l.order.PushFront(&lruEntry{key: k, val: v})
	l.m[k] = el
	if l.order.Len() > l.cap {
		oldest := l.order.Back()
		if oldest != nil {
			l.order.Remove(oldest)
			delete(l.m, oldest.Value.(*lruEntry).key)
		}
	}
}
