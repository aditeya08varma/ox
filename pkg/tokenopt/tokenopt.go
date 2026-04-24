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
	SystemDropped     int // ModeConversationOnly: count of system entries dropped
	ANSIStripped      int // ModeLossless: entries with ANSI sequences removed
	ProgressCollapsed int // ModeLossless: entries with \r progress frames collapsed
	ImagesElided      int // ModeLossless: base64 image payloads replaced
	LargeReadsElided  int // ModeLossless: large Read tool_result bodies truncated
	RemindersDeduped  int // ModeLossless: <system-reminder> blocks replaced with a ref
	ToolResultsRefd   int // ModeLossless: tool_result bodies replaced with a tool_ref
}

// Reduction returns bytes saved and percentage. Safe to call when BytesIn is 0.
func (s Stats) Reduction() (saved int64, pct float64) {
	saved = s.BytesIn - s.BytesOut
	if s.BytesIn == 0 {
		return saved, 0
	}
	return saved, float64(saved) / float64(s.BytesIn) * 100
}

// Add accumulates other into s. Useful for aggregating across many sessions
// (e.g., a daemon summarizing a nightly batch).
func (s *Stats) Add(other Stats) {
	s.EntriesIn += other.EntriesIn
	s.EntriesOut += other.EntriesOut
	s.BytesIn += other.BytesIn
	s.BytesOut += other.BytesOut
	s.ToolsMarked += other.ToolsMarked
	s.SystemDropped += other.SystemDropped
	s.ANSIStripped += other.ANSIStripped
	s.ProgressCollapsed += other.ProgressCollapsed
	s.ImagesElided += other.ImagesElided
	s.LargeReadsElided += other.LargeReadsElided
	s.RemindersDeduped += other.RemindersDeduped
	s.ToolResultsRefd += other.ToolResultsRefd
}

// LogValue implements slog.LogValuer. Enables callers (CLI, daemon) to emit
// single-line key=value compression telemetry with:
//
//	slog.Info("token_optimize", "stats", stats)
func (s Stats) LogValue() slog.Value {
	_, pct := s.Reduction()
	return slog.GroupValue(
		slog.Int("entries_in", s.EntriesIn),
		slog.Int("entries_out", s.EntriesOut),
		slog.Int64("bytes_in", s.BytesIn),
		slog.Int64("bytes_out", s.BytesOut),
		slog.Float64("reduction_pct", pct),
		slog.Int("tools_marked", s.ToolsMarked),
		slog.Int("system_dropped", s.SystemDropped),
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
	// ModeConversationOnly (default) emits header + user + assistant entries
	// verbatim and replaces every tool/system entry with a compact marker
	// (name + brief gist of the input). Optimal for downstream summarization:
	// the summarizer needs the conversation arc, not tool I/O.
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

			out, emit, terr := state.transform(entryBytes, seq, &stats)
			if terr != nil {
				return stats, fmt.Errorf("entry %d: %w", seq, terr)
			}
			if emit {
				if _, werr := bw.Write(out); werr != nil {
					return stats, werr
				}
				if _, werr := bw.Write([]byte{'\n'}); werr != nil {
					return stats, werr
				}
				stats.EntriesOut++
				stats.BytesOut += int64(len(out)) + 1
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return stats, err
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

	// Brief is emitted in ModeConversationOnly on tool_mark entries: a compact
	// gist of the tool input (e.g., the bash command, file path, grep pattern).
	Brief string `json:"brief,omitempty"`

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
