package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// --- GC-swap wait: bounded coordination between CLI writes and daemon reclone ---
//
// waitForGCSwap is the CLI-side half of the cross-process guard against a
// commitAndPushLedger racing the daemon's blue-green GC rename-swap
// (internal/daemon/sync_gc.go). Failure prevented: a session upload that
// lands in what becomes the old clone, silently deleted by the daemon with
// no error surfaced anywhere — a lost commit.

// TestWaitForGCSwap_NoLockFile_ReturnsImmediately verifies the common case
// (no GC swap in flight) never blocks a session upload.
// Failure prevented: every ledger write pays a needless wait/poll penalty.
func TestWaitForGCSwap_NoLockFile_ReturnsImmediately(t *testing.T) {
	ledgerPath := filepath.Join(t.TempDir(), "ledger")

	start := time.Now()
	waitForGCSwapWithBound(ledgerPath, 5*time.Second, 50*time.Millisecond)
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("expected immediate return with no lock file present, took %v", elapsed)
	}
}

// TestWaitForGCSwap_LockClearedMidWait_UnblocksBeforeBound proves the poll
// loop actually observes the lock clearing rather than either ignoring it
// (never blocking) or always running out the clock.
// Failure prevented: a session upload either races an in-progress swap
// (loop doesn't really wait) or stalls for the full bound on every swap
// even after the daemon finishes in milliseconds (loop doesn't really poll).
func TestWaitForGCSwap_LockClearedMidWait_UnblocksBeforeBound(t *testing.T) {
	ledgerPath := filepath.Join(t.TempDir(), "ledger")
	lockPath := ledgerPath + ".gc-swap-lock"
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatalf("failed to create lock file: %v", err)
	}

	const clearAfter = 150 * time.Millisecond
	go func() {
		time.Sleep(clearAfter)
		_ = os.Remove(lockPath)
	}()

	start := time.Now()
	waitForGCSwapWithBound(ledgerPath, 5*time.Second, 20*time.Millisecond)
	elapsed := time.Since(start)

	if elapsed < clearAfter {
		t.Fatalf("returned before the lock was actually removed: %v", elapsed)
	}
	if elapsed >= 5*time.Second {
		t.Fatalf("waited out the full bound instead of unblocking when the lock cleared: %v", elapsed)
	}
}

// TestWaitForGCSwap_LockPersists_ReturnsAtBound verifies a marker orphaned
// by a crashed daemon (never removed) doesn't hang the CLI forever.
// Failure prevented: a stale ".gc-swap-lock" left behind by a daemon crash
// mid-swap wedges every future session upload indefinitely.
func TestWaitForGCSwap_LockPersists_ReturnsAtBound(t *testing.T) {
	ledgerPath := filepath.Join(t.TempDir(), "ledger")
	lockPath := ledgerPath + ".gc-swap-lock"
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatalf("failed to create lock file: %v", err)
	}
	defer os.Remove(lockPath)

	const bound = 200 * time.Millisecond
	start := time.Now()
	waitForGCSwapWithBound(ledgerPath, bound, 20*time.Millisecond)
	elapsed := time.Since(start)

	if elapsed < bound {
		t.Fatalf("returned before the bound expired: %v", elapsed)
	}
	if elapsed > bound+300*time.Millisecond {
		t.Fatalf("took too long past the bound: %v", elapsed)
	}
}
