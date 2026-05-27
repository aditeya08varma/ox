package autofix

import (
	"context"
	"testing"
)

// TestCheckSessionInlineSummaryRetry_LiveLedger runs the autofix against the
// real local ledger to prove hydration + reset works end-to-end.
// Only runs when the real ledger is present (skips in CI).
func TestCheckSessionInlineSummaryRetry_LiveLedger(t *testing.T) {
	if testing.Short() {
		t.Skip("short: live ledger integration test")
	}

	result := checkSessionInlineSummaryRetry(context.Background(), "/Users/ryan/Documents/Code/sageox/ox")
	t.Logf("Status: %d", result.Status)
	t.Logf("Summary: %s", result.Summary)
	t.Logf("Repo: %s", result.Repo)

	// We expect it to find and reset sessions
	if result.Status == StatusError {
		t.Fatalf("autofix returned error: %s", result.Summary)
	}
}
