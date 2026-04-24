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
	EntriesOut        int // always equals EntriesIn; nothing is dropped, only rewritten
	BytesIn           int64
	BytesOut          int64
	ANSIStripped      int // count of entries where ANSI sequences were removed
	ProgressCollapsed int // count of entries where \r progress frames were collapsed
	ImagesElided      int // count of base64 image payloads replaced
	LargeReadsElided  int // count of large Read tool_result bodies truncated
	RemindersDeduped  int // count of <system-reminder> blocks replaced with a ref
	ToolResultsRefd   int // count of tool_result bodies replaced with a tool_ref
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
		slog.Int("entries", s.EntriesIn),
		slog.Int64("bytes_in", s.BytesIn),
		slog.Int64("bytes_out", s.BytesOut),
		slog.Float64("reduction_pct", pct),
		slog.Int("ansi_stripped", s.ANSIStripped),
		slog.Int("progress_collapsed", s.ProgressCollapsed),
		slog.Int("images_elided", s.ImagesElided),
		slog.Int("large_reads_elided", s.LargeReadsElided),
		slog.Int("reminders_deduped", s.RemindersDeduped),
		slog.Int("tool_results_refd", s.ToolResultsRefd),
	)
}

// Options configures a Compress run. Zero value is a reasonable default.
type Options struct {
	// LargeReadMaxLines is the line threshold above which a Read tool_result
	// body is truncated to head+tail. Defaults to 120.
	LargeReadMaxLines int

	// LargeReadKeepLines is how many lines to keep from the start and end when
	// a large Read body is truncated. Defaults to 40 (so 80 lines retained).
	LargeReadKeepLines int

	// ToolResultLRUSize caps the content-hash LRU for tool_result dedup.
	// Defaults to 1024 unique payloads (~32KB of state).
	ToolResultLRUSize int

	// ToolResultMinBytes is the minimum content size worth deduping. Small
	// payloads aren't worth the ref overhead. Defaults to 512.
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
// Guarantees:
//   - Single pass over r. Entries emit in order.
//   - Constant memory relative to session size (bounded by dedup LRU).
//   - User turns are always preserved verbatim.
//   - Header entries are always preserved verbatim.
//   - No entry is dropped; entries are rewritten in place.
func Compress(r io.Reader, w io.Writer) (Stats, error) {
	return CompressWith(r, w, Options{})
}

// CompressWith is Compress with tunable options.
func CompressWith(r io.Reader, w io.Writer, opts Options) (Stats, error) {
	opts.withDefaults()

	var stats Stats
	state := newState(opts)

	scanner := bufio.NewScanner(r)
	// Session entries can carry large tool_result bodies (~MB). Raise the
	// per-line buffer ceiling to 8MB — still bounded, still streaming.
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)

	bw := bufio.NewWriter(w)
	defer bw.Flush()

	seq := 0
	for scanner.Scan() {
		seq++
		line := scanner.Bytes()
		stats.EntriesIn++
		stats.BytesIn += int64(len(line)) + 1 // +1 for newline

		out, err := state.transform(line, seq, &stats)
		if err != nil {
			return stats, fmt.Errorf("entry %d: %w", seq, err)
		}

		if _, err := bw.Write(out); err != nil {
			return stats, err
		}
		if _, err := bw.Write([]byte{'\n'}); err != nil {
			return stats, err
		}
		stats.EntriesOut++
		stats.BytesOut += int64(len(out)) + 1
	}
	if err := scanner.Err(); err != nil {
		return stats, err
	}
	return stats, nil
}

// entry mirrors the jsonl shape loosely. Unknown fields round-trip via Extra.
type entry struct {
	Type       string          `json:"type,omitempty"`
	Content    string          `json:"content,omitempty"`
	Timestamp  string          `json:"timestamp,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	ToolInput  string          `json:"tool_input,omitempty"`
	ToolOutput string          `json:"tool_output,omitempty"`
	IsError    bool            `json:"is_error,omitempty"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`

	// ref fields emitted when we replace a payload with a pointer to an
	// earlier occurrence. Only one of FirstSeq/RefHash is nonzero per entry.
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
