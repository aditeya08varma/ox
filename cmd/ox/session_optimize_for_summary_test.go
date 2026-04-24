package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session/pipeline"
)

// TestOptimizedJSONL_NotInLFSAllowlist guards against someone adding
// tokenopt output files to the LFS upload list. These are local-only
// derived data and must never become LFS blobs (bandwidth, storage,
// cross-user consistency).
//
// Failure prevented: a future contributor adding "raw.optimized.jsonl"
// or similar to internal/lfs.ContentFiles, which would ship every user's
// tokenopt cache to LFS blob storage.
func TestOptimizedJSONL_NotInLFSAllowlist(t *testing.T) {
	banned := []string{
		"raw.optimized.jsonl",
		"optimized.jsonl",
		"tokenopt.jsonl",
	}
	for _, name := range lfs.ContentFiles {
		for _, b := range banned {
			if name == b || strings.Contains(name, "optimized") || strings.Contains(name, "tokenopt") {
				t.Errorf("LFS ContentFiles contains %q — tokenopt output must not be LFS-tracked; it lives in ledger .sageox/cache/ and is local-only", name)
			}
			_ = b
		}
	}
}

// TestOptimizedJSONL_NotCopiedToLedger verifies that CopySessionToLedger
// does not pick up a tokenopt-style optimized file even if one exists in
// the source dir alongside raw.jsonl. Current behavior: CopySessionToLedger
// copies named files explicitly, not via directory walk, so this is safe
// today. This test locks that behavior in.
//
// Failure prevented: a future refactor changing CopySessionToLedger to
// walk the source dir and inadvertently shipping local-only tokenopt
// output to the ledger / remote.
func TestOptimizedJSONL_NotCopiedToLedger(t *testing.T) {
	srcDir := t.TempDir()
	ledgerDir := t.TempDir()
	sessionName := "2026-04-23T00-00-test-Oxabcd"

	// Write raw.jsonl + a decoy tokenopt output next to it.
	rawPath := filepath.Join(srcDir, "raw.jsonl")
	if err := os.WriteFile(rawPath, []byte(`{"type":"user","content":"x"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	decoy := filepath.Join(srcDir, "raw.optimized.jsonl")
	if err := os.WriteFile(decoy, []byte("LEAKED\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := &pipeline.Result{RawPath: rawPath, EntryCount: 1}
	if err := pipeline.CopySessionToLedger(pipeline.OSFileSystem{}, result, ledgerDir, sessionName); err != nil {
		t.Fatalf("CopySessionToLedger: %v", err)
	}

	destDir := filepath.Join(ledgerDir, "sessions", sessionName)
	entries, err := os.ReadDir(destDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), "optimized") || strings.Contains(e.Name(), "tokenopt") {
			t.Errorf("tokenopt output %q leaked to ledger dir %s", e.Name(), destDir)
		}
	}
}

// TestWriteOptimizedJSONLForSummary_WritesToLedgerCache verifies the
// compressed file lands in the ledger cache dir (.sageox/cache/tokenopt/)
// and NOT next to raw.jsonl in the session cache.
func TestWriteOptimizedJSONLForSummary_WritesToLedgerCache(t *testing.T) {
	srcDir := t.TempDir()
	ledgerDir := t.TempDir()

	rawPath := filepath.Join(srcDir, "raw.jsonl")
	content := `{"type":"header"}` + "\n" +
		`{"type":"user","content":"hello"}` + "\n" +
		`{"type":"assistant","content":"hi"}` + "\n" +
		`{"type":"tool","tool_name":"Bash","tool_input":"{\"command\":\"ls\"}"}` + "\n"
	if err := os.WriteFile(rawPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	sessionName := "2026-04-23T00-00-test-Oxabcd"
	optPath := writeOptimizedJSONLForSummary(rawPath, ledgerDir, sessionName)
	if optPath == "" {
		t.Fatal("expected non-empty optPath")
	}

	wantDir := filepath.Join(ledgerDir, ".sageox", "cache", "summary-input")
	if filepath.Dir(optPath) != wantDir {
		t.Errorf("optimized path %q not in expected cache dir %q", optPath, wantDir)
	}
	if !strings.HasSuffix(optPath, sessionName+".jsonl") {
		t.Errorf("expected path to end with <session>.jsonl, got %q", optPath)
	}
	// Verify nothing got written next to raw.jsonl.
	if _, err := os.Stat(filepath.Join(srcDir, "raw.optimized.jsonl")); !os.IsNotExist(err) {
		t.Errorf("optimized file must not be written next to raw.jsonl (would be seen by other pipeline code)")
	}
}

// TestWriteOptimizedJSONLForSummary_FallsBackOnMissingLedger returns ""
// when ledger path cannot be resolved. Caller then uses raw.jsonl directly.
func TestWriteOptimizedJSONLForSummary_FallsBackOnMissingLedger(t *testing.T) {
	if got := writeOptimizedJSONLForSummary("/tmp/nonexistent", "", "session-01"); got != "" {
		t.Errorf("expected empty fallback, got %q", got)
	}
	if got := writeOptimizedJSONLForSummary("", "/tmp/ledger", "session-01"); got != "" {
		t.Errorf("expected empty fallback on missing rawPath, got %q", got)
	}
	if got := writeOptimizedJSONLForSummary("/tmp/raw.jsonl", "/tmp/ledger", ""); got != "" {
		t.Errorf("expected empty fallback on missing sessionName, got %q", got)
	}
}
