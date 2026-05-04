// Package tokenopt reduces the token footprint of session raw.jsonl streams
// before they are handed to a summarization LLM. Deterministic, streaming,
// single-pass, no LLM calls.
//
// The one public entry point is [Compress]. It reads a raw.jsonl stream from r,
// applies a fixed set of transforms, and writes the compressed stream to w.
// Memory stays bounded (a small dedup set of content hashes) regardless of
// session size.
//
// This package is intentionally self-contained — it depends only on the
// standard library and a small LRU — so it can be used by the CLI, the
// sessionsummary package, and a future server-side distiller with no
// coupling. No persistence, no sidecar manifest: the raw.jsonl on disk is
// the audit trail.
package tokenopt

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
)

// Stats reports what Compress did. Zero values are meaningful (no matches).
type Stats struct {
	EntriesIn         int
	EntriesOut        int // differs from EntriesIn in ModeConversationOnly where tool entries collapse to markers
	BytesIn           int64
	BytesOut          int64
	ToolsMarked       int // ModeConversationOnly: count of tool entries replaced with compact markers
	ToolsBatched      int // ModeConversationOnly: count of adjacent identical tool_marks collapsed into prior with count++
	SystemDropped     int // ModeConversationOnly: count of system entries dropped
	HeaderDropped     int // ModeConversationOnly: count of header entries dropped (header carries metadata the LLM doesn't need; daemon stamps it from stored.Meta directly)
	ANSIStripped      int // ModeLossless: entries with ANSI sequences removed
	ProgressCollapsed int // ModeLossless: entries with \r progress frames collapsed
	ImagesElided      int // ModeLossless: base64 image payloads replaced
	LargeReadsElided  int // ModeLossless: large Read tool_result bodies truncated
	RemindersDeduped  int // ModeLossless: <system-reminder> blocks replaced with a ref
	ToolResultsRefd   int // ModeLossless: tool_result bodies replaced with a tool_ref

	// Token estimates use a ~4 chars/token heuristic from internal/tokens.
	// They exist so callers can log a token-reduction number alongside the
	// byte-reduction one. Tokens are the actual LLM cost driver; bytes are
	// a proxy. Swap the estimator (or wire in a real BPE tokenizer) without
	// changing any caller — the field shape stays stable.
	TokensInEstimate  int64
	TokensOutEstimate int64
}

// Reduction returns bytes saved and percentage. Safe to call when BytesIn is 0.
func (s Stats) Reduction() (saved int64, pct float64) {
	saved = s.BytesIn - s.BytesOut
	if s.BytesIn == 0 {
		return saved, 0
	}
	return saved, float64(saved) / float64(s.BytesIn) * 100
}

// TokenReduction returns estimated tokens saved and percentage. The cost
// dashboard wants this — bytes are a proxy, tokens are the actual LLM
// cost. Code-heavy sessions tokenize at a different ratio than prose-heavy
// ones, so byte-percentage trends mislead operators on cost.
func (s Stats) TokenReduction() (saved int64, pct float64) {
	saved = s.TokensInEstimate - s.TokensOutEstimate
	if s.TokensInEstimate == 0 {
		return saved, 0
	}
	return saved, float64(saved) / float64(s.TokensInEstimate) * 100
}

// Add accumulates other into s. Useful for aggregating across many sessions
// (e.g., a daemon summarizing a nightly batch).
func (s *Stats) Add(other Stats) {
	s.EntriesIn += other.EntriesIn
	s.EntriesOut += other.EntriesOut
	s.BytesIn += other.BytesIn
	s.BytesOut += other.BytesOut
	s.ToolsMarked += other.ToolsMarked
	s.ToolsBatched += other.ToolsBatched
	s.SystemDropped += other.SystemDropped
	s.HeaderDropped += other.HeaderDropped
	s.ANSIStripped += other.ANSIStripped
	s.ProgressCollapsed += other.ProgressCollapsed
	s.ImagesElided += other.ImagesElided
	s.LargeReadsElided += other.LargeReadsElided
	s.RemindersDeduped += other.RemindersDeduped
	s.ToolResultsRefd += other.ToolResultsRefd
	s.TokensInEstimate += other.TokensInEstimate
	s.TokensOutEstimate += other.TokensOutEstimate
}

// LogValue implements slog.LogValuer. Enables callers (CLI, daemon) to emit
// single-line key=value compression telemetry with:
//
//	slog.Info("token_optimize", "stats", stats)
func (s Stats) LogValue() slog.Value {
	_, bytePct := s.Reduction()
	_, tokPct := s.TokenReduction()
	return slog.GroupValue(
		slog.Int("entries_in", s.EntriesIn),
		slog.Int("entries_out", s.EntriesOut),
		slog.Int64("bytes_in", s.BytesIn),
		slog.Int64("bytes_out", s.BytesOut),
		slog.Float64("byte_reduction_pct", bytePct),
		slog.Int64("tokens_in_est", s.TokensInEstimate),
		slog.Int64("tokens_out_est", s.TokensOutEstimate),
		slog.Float64("token_reduction_pct", tokPct),
		slog.Int("tools_marked", s.ToolsMarked),
		slog.Int("tools_batched", s.ToolsBatched),
		slog.Int("system_dropped", s.SystemDropped),
		slog.Int("header_dropped", s.HeaderDropped),
		slog.Int("ansi_stripped", s.ANSIStripped),
		slog.Int("progress_collapsed", s.ProgressCollapsed),
		slog.Int("images_elided", s.ImagesElided),
		slog.Int("large_reads_elided", s.LargeReadsElided),
		slog.Int("reminders_deduped", s.RemindersDeduped),
		slog.Int("tool_results_refd", s.ToolResultsRefd),
	)
}

// Mode controls how aggressively Compress reduces the stream.
type Mode int

const (
	// ModeConversationOnly (default) emits user + assistant entries verbatim,
	// drops header and system entries, and replaces every tool entry with a
	// bare `{type:"tool_mark"}` marker (or `{type:"tool_mark", count:N}`
	// when adjacent tool entries collapse). The marker's only job is to tell
	// the summarizer "the agent acted between these two messages, N times" —
	// no tool_name, no input gist, no output. Optimal for downstream
	// summarization: the summarizer needs the conversation arc, not tool I/O,
	// and assistant prose already names the concrete actions ("I'll edit X").
	ModeConversationOnly Mode = 0

	// ModeLossless keeps every entry but applies content-level transforms
	// (ANSI strip, progress collapse, image elision, large-Read truncation,
	// system-reminder + tool_result dedup). Use when a downstream consumer
	// still needs tool details (replay, debugging, contract-testing).
	ModeLossless Mode = 1
)

// Options configures a Compress run. Zero value is a reasonable default
// (ModeConversationOnly).
type Options struct {
	// Mode selects the compression strategy. Defaults to ModeConversationOnly.
	Mode Mode

	// LargeReadMaxLines is the line threshold above which a Read tool_result
	// body is truncated to head+tail. ModeLossless only. Defaults to 120.
	LargeReadMaxLines int

	// LargeReadKeepLines is how many lines to keep from the start and end when
	// a large Read body is truncated. ModeLossless only. Defaults to 40.
	LargeReadKeepLines int

	// ToolResultLRUSize caps the content-hash LRU for tool_result dedup.
	// ModeLossless only. Defaults to 1024 unique payloads.
	ToolResultLRUSize int

	// ToolResultMinBytes is the minimum content size worth deduping.
	// ModeLossless only. Defaults to 512.
	ToolResultMinBytes int
}

func (o *Options) withDefaults() {
	if o.LargeReadMaxLines == 0 {
		o.LargeReadMaxLines = 120
	}
	if o.LargeReadKeepLines == 0 {
		o.LargeReadKeepLines = 40
	}
	if o.ToolResultLRUSize == 0 {
		o.ToolResultLRUSize = 1024
	}
	if o.ToolResultMinBytes == 0 {
		o.ToolResultMinBytes = 512
	}
}

// Compress reads raw.jsonl entries from r, applies streaming transforms, and
// writes compressed jsonl to w. Returns Stats describing what was done.
//
// Guarantees (both modes):
//   - Single pass over r. Surviving entries emit in original order.
//   - Constant memory relative to session size.
//   - User turns and header entries are preserved verbatim, always.
//
// Mode-specific behavior:
//   - ModeConversationOnly (default): assistant turns verbatim; tool entries
//     collapse to compact tool_mark entries with a brief gist; system entries
//     are dropped. Typical reduction 50–80% on real sessions.
//   - ModeLossless: every entry preserved; content-level transforms only
//     (ANSI strip, image elision, dedup, etc.).
func Compress(r io.Reader, w io.Writer) (Stats, error) {
	return CompressWith(r, w, Options{})
}

// CompressWith is Compress with tunable options.
func CompressWith(r io.Reader, w io.Writer, opts Options) (Stats, error) {
	opts.withDefaults()

	var stats Stats
	state := newState(opts)

	// bufio.Reader.ReadBytes('\n') instead of bufio.Scanner — scanner has a
	// token size ceiling (default 64KB, raised at most to some limit) that
	// would fail the whole stream on a single oversized entry. Oversized
	// entries (large Read bodies, base64 images) are exactly what this
	// package is designed to compress, so we must tolerate them at input.
	br := bufio.NewReader(r)
	bw := bufio.NewWriter(w)

	// emit writes out (followed by a newline) and updates byte/token/entry
	// stats. Centralized so the four call sites — transform's actEmit,
	// pending flush before a non-batchable emission, EOF flush, and the
	// (currently absent) error-recovery path — share one accounting code
	// path. Drift here is silently corrupted telemetry.
	emit := func(out []byte) error {
		if _, werr := bw.Write(out); werr != nil {
			return werr
		}
		if _, werr := bw.Write([]byte{'\n'}); werr != nil {
			return werr
		}
		stats.EntriesOut++
		stats.BytesOut += int64(len(out)) + 1
		stats.TokensOutEstimate += estimateTokens(out)
		return nil
	}

	seq := 0
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			seq++
			// Strip the trailing newline from the entry we feed to transform,
			// but keep byte accounting honest (BytesIn/BytesOut include it).
			hadNL := line[len(line)-1] == '\n'
			entryBytes := line
			if hadNL {
				entryBytes = line[:len(line)-1]
			}
			stats.EntriesIn++
			stats.BytesIn += int64(len(line))
			stats.TokensInEstimate += estimateTokens(line)

			out, action, terr := state.transform(entryBytes, seq, &stats)
			if terr != nil {
				return stats, fmt.Errorf("entry %d: %w", seq, terr)
			}

			switch action {
			case actDrop:
				// Drop quietly. Pending tool_mark (if any) STAYS pending —
				// dropping a system or header entry is not a real
				// interruption between two identical tool calls; the
				// agent's behavior is what we're modeling, and a system
				// reminder appearing mid-batch is not a behavior change.
			case actHold:
				// transform updated state.pending; nothing to write yet.
			case actEmit:
				// Anything that emits flushes any held tool_mark first so
				// adjacency in the output stream matches adjacency in the
				// agent's behavior. user/assistant turns are real
				// interruptions; do not let them appear after a batched
				// tool_mark that semantically came after them.
				if pending := state.flushPending(&stats); pending != nil {
					if werr := emit(pending); werr != nil {
						return stats, werr
					}
				}
				if werr := emit(out); werr != nil {
					return stats, werr
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return stats, err
		}
	}

	// EOF — flush any tool_mark still held in pending. Without this, a
	// session that ENDS on a run of identical tool calls would silently
	// drop the entire run from the optimized output.
	if pending := state.flushPending(&stats); pending != nil {
		if werr := emit(pending); werr != nil {
			return stats, werr
		}
	}

	// Propagate any buffered-write failure. A deferred bw.Flush() would
	// silently swallow this and let callers believe the compressed file
	// was fully written when it wasn't.
	if err := bw.Flush(); err != nil {
		return stats, fmt.Errorf("flush output: %w", err)
	}
	return stats, nil
}

// entry mirrors the jsonl shape loosely. Unknown fields round-trip via Metadata.
type entry struct {
	Type       string          `json:"type,omitempty"`
	Content    string          `json:"content,omitempty"`
	Timestamp  string          `json:"timestamp,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	ToolInput  string          `json:"tool_input,omitempty"`
	ToolOutput string          `json:"tool_output,omitempty"`
	IsError    bool            `json:"is_error,omitempty"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`

	// Count is set on tool_mark entries when adjacent tool_marks were
	// collapsed into one entry. A missing/zero count means 1 (single
	// occurrence). Lets the summarizer see "agent did N tool things
	// between these messages" without N redundant entries.
	Count int `json:"count,omitempty"`

	// ref fields for ModeLossless tool_ref entries.
	RefType   string `json:"ref_type,omitempty"`
	RefKind   string `json:"ref_kind,omitempty"`
	RefHash   string `json:"ref_hash,omitempty"`
	RefFirst  int    `json:"ref_first_seq,omitempty"`
	RefSample string `json:"ref_sample,omitempty"`
}

func hashContent(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:12]) // 12 bytes = 96 bits, plenty for in-session dedup
}

// estimateTokens is the same ~4-chars-per-token heuristic pkg/tokenstrip
// uses (and that internal/tokens.EstimateTokens documents). Vendored here
// rather than imported because pkg/tokenopt's package doc advertises
// stdlib-only dependencies. Swap for a real BPE tokenizer in one place
// when accuracy matters more than dependency hygiene.
func estimateTokens(b []byte) int64 {
	if len(b) == 0 {
		return 0
	}
	// Integer division rounds down; any non-empty input deserves at
	// least 1 token, otherwise non-empty content can produce TokensIn=0
	// and skew TokenReduction percentages.
	t := int64(len(b)) / 4
	if t == 0 {
		return 1
	}
	return t
}

// emitAction tells the Compress outer loop what to do with a transformed
// entry. Three states because tool_mark batching needs a way to "hold this
// entry, see if the next one matches" that isn't either drop or emit.
type emitAction int

const (
	// actDrop: this entry is gone from the stream (system/header).
	// Pending tool_mark (if any) is NOT flushed — see Compress for why.
	actDrop emitAction = iota

	// actEmit: emit the returned bytes. Compress flushes any pending
	// tool_mark FIRST so adjacency is preserved.
	actEmit

	// actHold: state.pending was updated; outer loop should not write
	// anything this iteration. Used exclusively by tool_mark batching.
	actHold
)
