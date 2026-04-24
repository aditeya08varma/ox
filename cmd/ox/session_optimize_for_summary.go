package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/sageox/ox/pkg/tokenopt"
)

// writeOptimizedJSONLForSummary compresses raw.jsonl via tokenopt.ModeConversationOnly
// and writes the result to the ledger's .sageox/cache/tokenopt/ directory. Per
// .claude/rules/ledger-cache.md, this is the canonical location for local-only
// derived data: gitignored, per-machine, persists across worktrees, never
// synced or committed. See also internal/lfs.ContentFiles — optimized files
// are deliberately NOT on that allowlist, so they never become LFS blobs.
//
// On any error this returns "" and the caller should fall back to rawPath.
// The original raw.jsonl is never modified; this is purely additive.
func writeOptimizedJSONLForSummary(rawPath, ledgerPath, sessionName string) string {
	if rawPath == "" || ledgerPath == "" || sessionName == "" {
		return ""
	}
	in, err := os.Open(rawPath)
	if err != nil {
		slog.Debug("tokenopt: open raw.jsonl failed", "path", rawPath, "error", err)
		return ""
	}
	defer in.Close()

	cacheDir := filepath.Join(ledgerPath, ".sageox", "cache", "tokenopt")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		slog.Debug("tokenopt: mkdir cache dir failed", "path", cacheDir, "error", err)
		return ""
	}
	optPath := filepath.Join(cacheDir, sessionName+".jsonl")

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
