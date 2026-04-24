package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/sageox/ox/pkg/tokenopt"
)

// writeOptimizedJSONLForSummary compresses raw.jsonl into a sibling
// raw.optimized.jsonl using tokenopt.ModeConversationOnly. The optimized
// stream keeps user + assistant turns verbatim, replaces tool entries with
// compact markers, and drops system entries — the shape a summarizer LLM
// actually needs. Reduces input tokens typically 50-80% on real sessions.
//
// On any error this returns "" (and the caller should fall back to the raw
// path). The original raw.jsonl is never modified; this is purely additive.
func writeOptimizedJSONLForSummary(rawPath string) string {
	if rawPath == "" {
		return ""
	}
	in, err := os.Open(rawPath)
	if err != nil {
		slog.Debug("tokenopt: open raw.jsonl failed", "path", rawPath, "error", err)
		return ""
	}
	defer in.Close()

	optPath := filepath.Join(filepath.Dir(rawPath), "raw.optimized.jsonl")
	out, err := os.Create(optPath)
	if err != nil {
		slog.Debug("tokenopt: create optimized file failed", "path", optPath, "error", err)
		return ""
	}
	// Close explicitly so the file is flushed before any subsequent reads.
	stats, compressErr := tokenopt.Compress(in, out)
	if cerr := out.Close(); cerr != nil && compressErr == nil {
		compressErr = cerr
	}
	if compressErr != nil {
		slog.Warn("tokenopt: compress failed, summary will use raw.jsonl", "error", compressErr)
		_ = os.Remove(optPath)
		return ""
	}

	slog.Info("tokenopt", "path", optPath, "stats", stats)
	_, pct := stats.Reduction()
	fmt.Fprintf(os.Stderr, "session summary input optimized: %d→%d entries, %d→%d bytes (%.1f%% smaller)\n",
		stats.EntriesIn, stats.EntriesOut, stats.BytesIn, stats.BytesOut, pct)
	return optPath
}
