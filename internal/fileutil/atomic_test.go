package fileutil

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- AtomicWriteBytes ---

// TestAtomicWriteBytes_RoundTrip verifies the happy path: bytes go in,
// the exact same bytes come out, and the file has the requested mode.
// Failure prevented: regressing the helper that backs every instruction-file
// write (AGENTS.md, CONVENTIONS.md, CLAUDE_ENV_FILE) to leave partial or
// wrongly-permissioned content.
func TestAtomicWriteBytes_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target.md")

	want := []byte("<!-- ox:prime-check -->\n# hi\n")
	if err := AtomicWriteBytes(path, want, 0644); err != nil {
		t.Fatalf("AtomicWriteBytes: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("content mismatch: got %q, want %q", got, want)
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := st.Mode() & os.ModePerm; perm != 0644 {
		t.Errorf("perm = %o, want 0644", perm)
	}
}

// TestAtomicWriteBytes_OverwritesExisting verifies rewrite semantics:
// an existing file is replaced wholesale, not appended to.
// Failure prevented: a regression that leaves stale content behind
// (the exact failure mode we're defending against in adapter hooks).
func TestAtomicWriteBytes_OverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target")

	if err := os.WriteFile(path, []byte("original"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := AtomicWriteBytes(path, []byte("replaced"), 0644); err != nil {
		t.Fatalf("AtomicWriteBytes: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "replaced" {
		t.Errorf("overwrite failed: got %q", got)
	}
}

// TestAtomicWriteBytes_NoStrayTempFiles guards cleanup: every successful
// write leaves only the target, no .tmp-* siblings.
// Failure prevented: leaking temp files into user repos (and making the
// next scan of AGENTS.md-class markers pick them up as real content).
func TestAtomicWriteBytes_NoStrayTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target")

	if err := AtomicWriteBytes(path, []byte("data"), 0644); err != nil {
		t.Fatalf("AtomicWriteBytes: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("unexpected temp file left in dir: %s", e.Name())
		}
	}
}

// TestAtomicWriteBytes_ParentDirMissing confirms the helper surfaces
// errors rather than silently creating files in unexpected locations.
// Failure prevented: a misconfigured caller getting an empty return
// and believing the write landed when the parent directory was removed
// between stat and call.
func TestAtomicWriteBytes_ParentDirMissing(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist", "target")

	err := AtomicWriteBytes(missing, []byte("data"), 0644)
	if err == nil {
		t.Fatalf("expected error for missing parent dir, got nil")
	}
}

func TestAtomicWriteJSON_Success(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")

	data := map[string]string{"key": "value"}
	if err := AtomicWriteJSON(path, data, 0600); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}

	var got map[string]string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["key"] != "value" {
		t.Errorf("expected value, got %q", got["key"])
	}
}

func TestAtomicWriteJSON_NoPartialOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fail.json")

	// channels cannot be marshaled to JSON
	err := AtomicWriteJSON(path, make(chan int), 0600)
	if err == nil {
		t.Fatal("expected error for unencodable value")
	}

	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Error("file should not exist after failed write")
	}

	// also verify no temp files left behind
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		t.Errorf("leftover file: %s", e.Name())
	}
}

func TestAtomicWriteJSON_Permissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "perms.json")

	if err := AtomicWriteJSON(path, "test", 0600); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("expected 0600, got %o", info.Mode().Perm())
	}
}
