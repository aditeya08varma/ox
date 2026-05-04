package tokenopt

import (
	"container/list"
	"encoding/json"
	"fmt"
	"regexp"
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

	// pending and pendingCount are the in-flight tool_mark held for
	// adjacency-batching. When the next entry is another tool_mark with
	// matching tool_name + brief + is_error, pendingCount increments and
	// the duplicate is silently absorbed. When the next entry is anything
	// else (assistant, user, different tool_mark), Compress flushes
	// pending — emitting it once with `count` set if pendingCount > 1.
	//
	// Invariant: pending is non-nil iff pendingCount > 0.
	pending      *entry
	pendingCount int
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

// transform applies the streaming pipeline to a single jsonl line. Returns
// the bytes to emit (when action == actEmit), or signals drop / hold.
// On JSON parse failure the line passes through unchanged.
//
// Pending tool_mark batching: in ModeConversationOnly, transform may set
// state.pending and return actHold. The Compress outer loop is responsible
// for flushing pending on actEmit (so adjacency in the output stream matches
// adjacency in the agent's behavior) and at EOF.
func (s *state) transform(line []byte, seq int, stats *Stats) ([]byte, emitAction, error) {
	var e entry
	if err := json.Unmarshal(line, &e); err != nil {
		return line, actEmit, nil
	}

	// User turns are sacred — intent signal, never altered. Always emit
	// verbatim in every mode.
	if e.Type == "user" {
		return line, actEmit, nil
	}

	// Header was historically preserved verbatim. In ModeConversationOnly
	// we drop it: the summarizer's schema (title, summary, key_actions,
	// aha_moments, chapter_titles, agent_summary, ...) needs none of the
	// header fields (agent_type, agent_version, model, ox_username,
	// ox_version, created_at). The daemon's writeMetaAndUploadLFS stamps
	// those into meta.json directly from stored.Meta — the LLM never
	// reads or writes them. ~50 tokens once per session, free win.
	//
	// In ModeLossless we still preserve the header to stay true to the
	// "every entry survives" contract for replay/debug consumers.
	if e.Type == "header" {
		if s.opts.Mode == ModeConversationOnly {
			stats.HeaderDropped++
			return nil, actDrop, nil
		}
		return line, actEmit, nil
	}

	if s.opts.Mode == ModeConversationOnly {
		return s.transformConversationOnly(&e, line, stats)
	}
	out, emit, err := s.transformLossless(line, &e, seq, stats)
	if !emit {
		return nil, actDrop, err
	}
	return out, actEmit, err
}

// transformConversationOnly keeps assistant turns verbatim, replaces tool
// entries with bare-marker placeholders, and drops system entries.
//
// tool_mark shape today is intentionally minimal: just `{type:"tool_mark"}`,
// optionally `{type:"tool_mark", count:N}` when adjacent calls collapse.
// No tool_name, no brief, no is_error.
//
// Reasoning: the brief was a 120-char-per-call tax (6–11K input tokens on
// real sessions) that paid back unevenly. Most of what the brief carried —
// the bash command, the file path, the grep pattern — is also in the
// surrounding assistant prose ("I'll edit foo.go and run tests"). What the
// summarizer actually needs from these entries is *narrative scaffolding*:
// "the agent paused and acted between these two messages, N times." A bare
// `tool_mark` (with `count` when batched) preserves that signal at <1% of
// the previous cost. If `quality_score` / `judge_overall` regresses on
// tool-heavy sessions when this lands, the next step is option 3 from the
// design discussion: a single tail digest of tool_name → count.
func (s *state) transformConversationOnly(e *entry, line []byte, stats *Stats) ([]byte, emitAction, error) {
	switch e.Type {
	case "assistant":
		// Pass through verbatim — assistant prose IS the narrative.
		return line, actEmit, nil
	case "tool":
		// Always the same shape — no per-call detail. Every tool entry
		// produces an identical mark, which means adjacency batching
		// collapses every contiguous run of tool calls between assistant
		// turns into a single `{type:"tool_mark", count:N}` entry.
		mark := entry{Type: "tool_mark"}
		stats.ToolsMarked++

		if s.pending != nil {
			// All marks are identical now, so every continuation is a
			// batchable duplicate. The pendingMatches check is kept as a
			// no-op call site for symmetry with other modes / future
			// variants that may reintroduce per-call distinctions.
			if pendingMatches(s.pending, &mark) {
				s.pendingCount++
				stats.ToolsBatched++
				return nil, actHold, nil
			}
			// Unreachable today (all marks match), but kept for safety:
			// flush old pending, hold new one. Same shape as the original
			// implementation so future modes can reintroduce a distinction.
			oldFlushed := s.marshalPending()
			s.pending = &mark
			s.pendingCount = 1
			return oldFlushed, actEmit, nil
		}

		s.pending = &mark
		s.pendingCount = 1
		return nil, actHold, nil
	case "system":
		stats.SystemDropped++
		return nil, actDrop, nil
	default:
		// Unknown types: preserve to stay safe.
		return line, actEmit, nil
	}
}

// pendingMatches reports whether two tool_marks are batchable. Today every
// tool_mark has the same shape (`{type:"tool_mark"}`), so this returns true
// for any two tool→tool transitions. Kept as a function (rather than inlined
// `true`) to make the batching policy explicit and to leave a single place
// to reintroduce per-call distinctions if the digest experiment regresses.
func pendingMatches(a, b *entry) bool {
	return a.Type == b.Type
}

// marshalPending serializes state.pending with its accumulated count into
// the bytes Compress should write. Caller is responsible for clearing
// pending afterwards (or via flushPending which does both).
func (s *state) marshalPending() []byte {
	if s.pending == nil {
		return nil
	}
	out := *s.pending
	if s.pendingCount > 1 {
		out.Count = s.pendingCount
	}
	bytes, err := json.Marshal(&out)
	if err != nil {
		// Fall back to an unbatched marshal of just the type/name —
		// losing the count is preferable to losing the entry entirely.
		return []byte(`{"type":"tool_mark","tool_name":"` + out.ToolName + `"}`)
	}
	return bytes
}

// flushPending returns the bytes for the held tool_mark (if any) and
// clears state. Returns nil when nothing is pending. Called from Compress
// at EOF and before any actEmit.
func (s *state) flushPending(stats *Stats) []byte {
	if s.pending == nil {
		return nil
	}
	out := s.marshalPending()
	s.pending = nil
	s.pendingCount = 0
	_ = stats // reserved for future per-flush counters
	return out
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
