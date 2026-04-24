package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session/pipeline"
	"github.com/sageox/ox/pkg/tokenopt"
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

// TestSanityCheckOptimized covers the gate that refuses to hand the
// summarizer a suspiciously wrong compressed file. Failure prevented:
// a silent tokenopt bug (or future regression) eating user/assistant
// turns and producing a blank or near-blank summary.
func TestSanityCheckOptimized(t *testing.T) {
	dir := t.TempDir()
	goodPath := filepath.Join(dir, "good.jsonl")
	if err := os.WriteFile(goodPath, []byte(`{"type":"user","content":"x"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	emptyPath := filepath.Join(dir, "empty.jsonl")
	if err := os.WriteFile(emptyPath, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name       string
		path       string
		stats      tokenopt.Stats
		wantReason string
	}{
		{
			name:       "empty input is legitimate",
			path:       goodPath,
			stats:      tokenopt.Stats{EntriesIn: 0, EntriesOut: 0},
			wantReason: "",
		},
		{
			name:       "happy path",
			path:       goodPath,
			stats:      tokenopt.Stats{EntriesIn: 10, EntriesOut: 8, SystemDropped: 2},
			wantReason: "",
		},
		{
			name:       "zero-byte file rejected",
			path:       emptyPath,
			stats:      tokenopt.Stats{EntriesIn: 10, EntriesOut: 10},
			wantReason: "empty",
		},
		{
			name:       "zero-entry output rejected",
			path:       goodPath,
			stats:      tokenopt.Stats{EntriesIn: 10, EntriesOut: 0},
			wantReason: "zero entries",
		},
		{
			name:       "entry-count drift rejected (eats user/assistant turns)",
			path:       goodPath,
			stats:      tokenopt.Stats{EntriesIn: 100, EntriesOut: 50, SystemDropped: 5},
			wantReason: "entry-count mismatch",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sanityCheckOptimized(c.path, c.stats)
			if c.wantReason == "" && got != "" {
				t.Errorf("expected pass, got reason: %s", got)
			}
			if c.wantReason != "" && !strings.Contains(got, c.wantReason) {
				t.Errorf("got %q, want reason containing %q", got, c.wantReason)
			}
		})
	}
}

// TestPruneSummaryInputCache_EnforcesFileCount verifies LRU eviction kicks in
// when the file count exceeds the budget. Oldest mtime first.
func TestPruneSummaryInputCache_EnforcesFileCount(t *testing.T) {
	dir := t.TempDir()
	// Create 5 files with increasing mtimes.
	for i := 0; i < 5; i++ {
		p := filepath.Join(dir, fmt.Sprintf("session-%02d.jsonl", i))
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		mt := time.Now().Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}

	// Budget: 2 files. Expect 3 oldest evicted.
	if err := pruneSummaryInputCache(dir, 1024*1024, 2); err != nil {
		t.Fatalf("prune: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("expected 2 files after prune, got %d", len(entries))
	}
	// The two survivors should be the newest.
	names := []string{entries[0].Name(), entries[1].Name()}
	sort.Strings(names)
	if names[0] != "session-03.jsonl" || names[1] != "session-04.jsonl" {
		t.Errorf("wrong files survived: %v (expected session-03, session-04)", names)
	}
}

// TestPruneSummaryInputCache_EnforcesByteBudget verifies LRU eviction kicks
// in when the total size exceeds the byte budget.
func TestPruneSummaryInputCache_EnforcesByteBudget(t *testing.T) {
	dir := t.TempDir()
	// Three 100-byte files, oldest-first mtimes.
	payload := make([]byte, 100)
	for i := 0; i < 3; i++ {
		p := filepath.Join(dir, fmt.Sprintf("s%d.jsonl", i))
		if err := os.WriteFile(p, payload, 0o644); err != nil {
			t.Fatal(err)
		}
		mt := time.Now().Add(time.Duration(i) * time.Hour)
		_ = os.Chtimes(p, mt, mt)
	}
	// Budget 150 bytes — only one 100-byte file fits.
	if err := pruneSummaryInputCache(dir, 150, 100); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 file under 150-byte budget, got %d", len(entries))
	}
	if entries[0].Name() != "s2.jsonl" {
		t.Errorf("expected newest file to survive, got %s", entries[0].Name())
	}
}

// TestWriteOptimizedJSONLForSummary_RespectsEnvDisable exercises the
// OX_SUMMARY_INPUT_OPTIMIZE escape hatch. If a user (or the --no-optimize
// flag) disables optimization, writeOptimizedJSONLForSummary must return ""
// without creating any files so the summarizer falls back to raw.jsonl.
func TestWriteOptimizedJSONLForSummary_RespectsEnvDisable(t *testing.T) {
	srcDir := t.TempDir()
	ledgerDir := t.TempDir()
	rawPath := filepath.Join(srcDir, "raw.jsonl")
	if err := os.WriteFile(rawPath, []byte(`{"type":"user","content":"x"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, v := range []string{"off", "OFF", "0", "false", "no"} {
		t.Run("disabled="+v, func(t *testing.T) {
			t.Setenv("OX_SUMMARY_INPUT_OPTIMIZE", v)
			got := writeOptimizedJSONLForSummary(rawPath, ledgerDir, "session-disabled-"+v)
			if got != "" {
				t.Errorf("expected empty fallback with env=%q, got %q", v, got)
			}
			// No file should have been created.
			if _, err := os.Stat(filepath.Join(ledgerDir, ".sageox", "cache", "summary-input", "session-disabled-"+v+".jsonl")); !os.IsNotExist(err) {
				t.Errorf("expected no file written when disabled, got err=%v", err)
			}
		})
	}

	t.Run("enabled by default when env unset", func(t *testing.T) {
		t.Setenv("OX_SUMMARY_INPUT_OPTIMIZE", "")
		got := writeOptimizedJSONLForSummary(rawPath, ledgerDir, "session-enabled")
		if got == "" {
			t.Errorf("expected optimization to run when env unset, got empty path")
		}
	})
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
