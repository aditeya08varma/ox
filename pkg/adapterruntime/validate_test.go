package adapterruntime

import (
	"os"
	"path/filepath"
	"testing"
)

// initSageox creates .sageox/config.json in tmpDir for ValidateRepoRoot tests.
func initSageox(t *testing.T, tmpDir string) {
	t.Helper()
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// --- A. ValidateRepoRoot ---

// TestValidateRepoRoot_RejectsEmpty verifies empty string is rejected.
// Failure prevented: adapter receives "" as repo-root, hashes to garbage path.
func TestValidateRepoRoot_RejectsEmpty(t *testing.T) {
	err := ValidateRepoRoot("")
	if err == nil {
		t.Fatal("expected error for empty root")
	}
}

// TestValidateRepoRoot_RejectsDot verifies "." is rejected.
// Failure prevented: adapter receives "." as repo-root, resolves to wrong directory.
func TestValidateRepoRoot_RejectsDot(t *testing.T) {
	err := ValidateRepoRoot(".")
	if err == nil {
		t.Fatal("expected error for dot root")
	}
}

// TestValidateRepoRoot_RejectsRelative verifies relative paths are rejected.
// Failure prevented: adapter receives "some/relative/path" which depends on cwd.
func TestValidateRepoRoot_RejectsRelative(t *testing.T) {
	err := ValidateRepoRoot("some/relative/path")
	if err == nil {
		t.Fatal("expected error for relative path")
	}
}

// TestValidateRepoRoot_RejectsNoSageox verifies absolute path without .sageox/ is rejected.
// Failure prevented: adapter runs against a directory that isn't an ox project.
func TestValidateRepoRoot_RejectsNoSageox(t *testing.T) {
	tmpDir := t.TempDir()
	tmpDir, _ = filepath.EvalSymlinks(tmpDir)

	err := ValidateRepoRoot(tmpDir)
	if err == nil {
		t.Fatal("expected error for directory without .sageox/")
	}
}

// TestValidateRepoRoot_AcceptsValid verifies a proper ox project root passes.
func TestValidateRepoRoot_AcceptsValid(t *testing.T) {
	tmpDir := t.TempDir()
	tmpDir, _ = filepath.EvalSymlinks(tmpDir)
	initSageox(t, tmpDir)

	err := ValidateRepoRoot(tmpDir)
	if err != nil {
		t.Fatalf("expected no error for valid root, got: %v", err)
	}
}

// --- A2. ValidateRepoRoot edge cases ---

// TestValidateRepoRoot_PathWithSpaces verifies paths containing spaces pass.
// Failure prevented: projects in directories with spaces silently fail session lookup.
func TestValidateRepoRoot_PathWithSpaces(t *testing.T) {
	tmpDir := filepath.Join(t.TempDir(), "my project")
	tmpDir, _ = filepath.EvalSymlinks(filepath.Dir(tmpDir))
	tmpDir = filepath.Join(tmpDir, "my project")
	initSageox(t, tmpDir)
	if err := ValidateRepoRoot(tmpDir); err != nil {
		t.Fatalf("path with spaces should pass: %v", err)
	}
}

// TestValidateRepoRoot_TrailingSlash verifies trailing slash is accepted.
func TestValidateRepoRoot_TrailingSlash(t *testing.T) {
	tmpDir := t.TempDir()
	tmpDir, _ = filepath.EvalSymlinks(tmpDir)
	initSageox(t, tmpDir)
	if err := ValidateRepoRoot(tmpDir + "/"); err != nil {
		t.Fatalf("trailing slash should pass: %v", err)
	}
}

// TestValidateRepoRoot_SageoxIsFile verifies .sageox as a regular file is rejected.
// Failure prevented: interrupted `ox init` leaves a .sageox file; adapter treats it as valid.
func TestValidateRepoRoot_SageoxIsFile(t *testing.T) {
	tmpDir := t.TempDir()
	tmpDir, _ = filepath.EvalSymlinks(tmpDir)
	if err := os.WriteFile(filepath.Join(tmpDir, ".sageox"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := ValidateRepoRoot(tmpDir)
	if err == nil {
		t.Fatal("expected error when .sageox is a file, not directory")
	}
}

// TestValidateRepoRoot_SymlinkedSageox verifies .sageox as a symlink to a real dir passes.
func TestValidateRepoRoot_SymlinkedSageox(t *testing.T) {
	tmpDir := t.TempDir()
	tmpDir, _ = filepath.EvalSymlinks(tmpDir)

	realDir := filepath.Join(tmpDir, "real-sageox")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, filepath.Join(tmpDir, ".sageox")); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRepoRoot(tmpDir); err != nil {
		t.Fatalf("symlinked .sageox should pass: %v", err)
	}
}

// TestValidateRepoRoot_NonexistentPath verifies totally nonexistent path is rejected.
func TestValidateRepoRoot_NonexistentPath(t *testing.T) {
	err := ValidateRepoRoot("/nonexistent/path/that/does/not/exist")
	if err == nil {
		t.Fatal("expected error for nonexistent path")
	}
}

// TestValidateRepoRoot_TildePath verifies ~ is not treated as absolute.
func TestValidateRepoRoot_TildePath(t *testing.T) {
	err := ValidateRepoRoot("~/repo")
	if err == nil {
		t.Fatal("expected error for tilde path")
	}
}

// --- B. ValidateSessionID ---

func TestValidateSessionID(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{name: "empty", id: "", wantErr: false},
		{name: "valid uuid", id: "550e8400-e29b-41d4-a716-446655440000", wantErr: false},
		{name: "valid alphanumeric", id: "session-abc-123", wantErr: false},
		{name: "path traversal", id: "../../../etc/passwd", wantErr: true},
		{name: "double dot", id: "foo..bar", wantErr: true},
		{name: "forward slash", id: "foo/bar", wantErr: true},
		{name: "backslash", id: "foo\\bar", wantErr: true},
		{name: "absolute path", id: "/etc/passwd", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSessionID(tt.id)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSessionID(%q) error = %v, wantErr %v", tt.id, err, tt.wantErr)
			}
		})
	}
}
