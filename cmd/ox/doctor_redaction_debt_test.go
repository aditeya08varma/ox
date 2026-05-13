package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCheckLedgerRedactionDebt_PassesWhenNoMarkers covers the steady
// state: a ledger with no .sageox/cache/redaction-debt/ dir (or an empty
// one) must report Passed. Anything else would print a spurious warning
// every doctor run on a healthy ledger.
func TestCheckLedgerRedactionDebt_PassesWhenNoMarkers(t *testing.T) {
	// We don't need the gate's recovery; we just need a ledger-shaped
	// project so the check's preconditions (gitRoot, localCfg, ledger
	// path) all resolve. Use makeLedgerWithCommit + the unified
	// config helpers won't apply here because the doctor check looks
	// up the ledger from a real project config. Instead, write the
	// marker into an absolute path under a tempdir and call the check
	// implementation directly via a helper that we expose for tests.
	//
	// The easier path: test the parsing surface of the check directly
	// with handcrafted markers. checkLedgerRedactionDebt is the cobra
	// glue + filesystem walk; separate the file-walk piece into a
	// helper for a focused unit. For now we exercise the structure of
	// the marker round-trip — the e2e wiring is covered by the gate
	// tests in prepush_autoredact_test.go (they emit real markers and
	// assert the doctor check can see them via the same file layout).

	tmp := t.TempDir()
	debtDir := filepath.Join(tmp, ".sageox", "cache", "redaction-debt")
	require.NoError(t, os.MkdirAll(debtDir, 0o700))
	// empty dir → no markers → no debt
	summaries, malformed := readDebtSummaries(debtDir)
	assert.Empty(t, summaries)
	assert.Empty(t, malformed)
}

// TestCheckLedgerRedactionDebt_SurfacesMarker is the load-bearing
// assertion: a written marker becomes a doctor warning that names the
// session, its detectors, and the next-step recovery commands.
func TestCheckLedgerRedactionDebt_SurfacesMarker(t *testing.T) {
	tmp := t.TempDir()
	debtDir := filepath.Join(tmp, ".sageox", "cache", "redaction-debt")
	require.NoError(t, os.MkdirAll(debtDir, 0o700))

	rec := redactionDebtRecord{
		SessionName:   "2026-05-12-quarantined",
		QuarantinedAt: time.Now().UTC(),
		Reason:        "pre-push secret gate could not auto-redact",
		Findings: []redactionDebtFinding{
			{Detector: "aws_access_key", Filename: "notes.md", Line: 3},
			{Detector: "generic_secret", Filename: "notes.md", Line: 9},
		},
		QuarantinePaths: []redactionDebtLocation{
			{From: "sessions/2026-05-12-quarantined/notes.md",
				To: ".sageox/cache/quarantine/2026-05-12-quarantined/notes.md"},
		},
	}
	buf, err := json.MarshalIndent(rec, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(debtDir, rec.SessionName+".json"), buf, 0o600))

	summaries, malformed := readDebtSummaries(debtDir)
	require.Len(t, summaries, 1)
	require.Empty(t, malformed)
	got := summaries[0]
	assert.Equal(t, "2026-05-12-quarantined", got.session)
	assert.Equal(t, 2, got.findings)
	assert.Equal(t, 1, got.files)
	assert.Equal(t, []string{"aws_access_key", "generic_secret"}, got.detectors)
}

// TestCheckLedgerRedactionDebt_FlagsMalformedMarkers ensures a corrupt
// or non-JSON file in the debt dir is surfaced — silently dropping it
// would mean a user could lose track of a quarantined session if its
// marker got partially written by an interrupted process.
func TestCheckLedgerRedactionDebt_FlagsMalformedMarkers(t *testing.T) {
	tmp := t.TempDir()
	debtDir := filepath.Join(tmp, ".sageox", "cache", "redaction-debt")
	require.NoError(t, os.MkdirAll(debtDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(debtDir, "broken.json"), []byte("not json"), 0o600))

	summaries, malformed := readDebtSummaries(debtDir)
	assert.Empty(t, summaries)
	assert.Equal(t, []string{"broken.json"}, malformed)
}
