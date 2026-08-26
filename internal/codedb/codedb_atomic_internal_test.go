package codedb

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A failed promotion must never destroy the previous healthy cache. With the
// naive "remove finalDir, then rename staging on top" order, an injected rename
// failure loses both. The move-aside → rename → restore-on-failure sequence
// keeps the previous cache intact. White-box so it can inject renameDir.
func TestBuildCodeDBAtomic_PromotionFailurePreservesPreviousCache(t *testing.T) {
	finalDir := filepath.Join(t.TempDir(), "codedb")
	if err := os.MkdirAll(finalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(finalDir, "GOOD")
	if err := os.WriteFile(good, []byte("healthy"), 0o600); err != nil {
		t.Fatal(err)
	}

	orig := renameDir
	defer func() { renameDir = orig }()
	// Fail only the promoting rename (source is the staging dir); let the
	// move-aside and restore renames run normally.
	renameDir = func(oldpath, newpath string) error {
		if strings.Contains(filepath.Base(oldpath), ".building-") {
			return fmt.Errorf("injected promotion failure")
		}
		return orig(oldpath, newpath)
	}

	err := BuildCodeDBAtomic(context.Background(), finalDir, func(ctx context.Context, db *DB) error {
		return nil
	})
	if err == nil {
		t.Fatal("expected BuildCodeDBAtomic to report the promotion failure")
	}

	// The previous healthy cache must have been restored, byte-for-byte.
	b, readErr := os.ReadFile(good)
	if readErr != nil {
		t.Fatalf("previous cache must be restored after a failed promotion, GOOD missing: %v", readErr)
	}
	if string(b) != "healthy" {
		t.Fatalf("previous cache corrupted after failed promotion, got %q", string(b))
	}
	// No staging or backup dirs left behind.
	if leftovers, _ := filepath.Glob(finalDir + ".*"); len(leftovers) != 0 {
		t.Fatalf("expected no staging/backup leftovers after failed promotion, found %v", leftovers)
	}
}
