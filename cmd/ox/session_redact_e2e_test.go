package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file is the end-to-end, customer-facing proof of the redaction promise:
// a credential a coworker records into a session NEVER reaches the shared Ledger
// remote. It is the executable mirror of
// tests/acceptance/features/redaction/secrets-never-leak.feature.
//
// Why this lens is not covered elsewhere: every existing secret test asserts on
// LOCAL bytes (a re-scan came back clean, a working-tree file was rewritten) and
// stops before `git push`. The pre-push gate deliberately NEVER blocks — it
// auto-redacts or quarantines and returns nil — so "the secret didn't leave the
// machine" depends entirely on the redaction landing before the push. That push
// leg, with a live secret present, is the untested seam. These tests drive the
// real pushLedger pipeline to a real bare remote and assert on the remote's
// object database — the only surface a teammate can actually read.
//
// The canary AKIAIOSFODNN7EXAMPLE is AWS's own published non-secret example key
// (fires the aws_access_key detector, safe to commit — used throughout this repo).

// assertRemoteObjectsCleanOf fails if the secret appears in ANY object reachable
// in the bare remote's database — not just the HEAD tree. A secret amended out
// of the tip but still pushed in an earlier pack is still readable by every
// teammate; scanning every object is the only honest oracle. Mirrors the
// object-scan oracle in TestDraftPublish_NoTurnContentInAnyGitObject.
func assertRemoteObjectsCleanOf(t *testing.T, barePath, secret string) {
	t.Helper()
	cmd := exec.Command("sh", "-c", "git cat-file --batch-all-objects --batch")
	cmd.Dir = barePath
	out, err := cmd.CombinedOutput()
	require.NoError(t, err)
	assert.NotContains(t, string(out), secret,
		"a credential reached a git object in the shared Ledger remote — every teammate who pulls can read it")
}

// commitSession stages and commits a session dir as a local holding commit,
// exactly as the finalize path does, WITHOUT pushing — so the pre-push gate sees
// the secret in the push range on the next push.
func commitSession(t *testing.T, ledgerPath string, sessionNames ...string) {
	t.Helper()
	args := []string{"add", "--sparse", "--", "sessions/.gitignore"}
	for _, n := range sessionNames {
		args = append(args, "sessions/"+n)
	}
	runGit(t, ledgerPath, args...)
	runGit(t, ledgerPath, "commit", "--no-verify", "-m", "session: holding commit")
}

// TestRedactE2E_SecretInJSONLNeverReachesRemote is the core contract.
//
// A coworker's raw.jsonl carries a live AWS key past the write-time layer. When
// the Ledger is pushed, the pre-push gate auto-redacts the JSONL in place and
// the push proceeds — but the credential must never appear in any object on the
// remote, and the redacted slug is what teammates receive instead.
//
// Failure prevented: a session credential reaching every teammate through the
// shared Ledger. Red-first: neuter autoRedactSessionFindings (return the input
// unchanged) and this test fails — the canary reaches the bare remote.
func TestRedactE2E_SecretInJSONLNeverReachesRemote(t *testing.T) {
	f := newDraftLedgerFixture(t)
	const sessionName = "2026-01-01T00-00-testuser-OxSjsn"
	const canary = "AKIAIOSFODNN7EXAMPLE"

	// Given: a session whose raw.jsonl contains a live credential, committed to a
	// local holding commit (not yet pushed).
	dir := seedFinalizedLedgerSessionWithArtifacts(t, f.ledgerPath, sessionName)
	rawLine := `{"type":"assistant","content":"export AWS_ACCESS_KEY_ID=` + canary + `"}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(rawLine), 0644))
	commitSession(t, f.ledgerPath, sessionName)
	require.Contains(t, runGit(t, f.ledgerPath, "show", "HEAD:sessions/"+sessionName+"/raw.jsonl"), canary,
		"precondition: the credential is in the local commit the push would publish")

	// When: the coworker pushes the Ledger through the real gate.
	require.NoError(t, pushLedger(context.Background(), f.ledgerPath))

	// Then: no object on the remote carries the credential.
	assertRemoteObjectsCleanOf(t, f.barePath, canary)
	// And: teammates receive the redacted slug in its place.
	assert.Contains(t, runGit(t, f.barePath, "show", "HEAD:sessions/"+sessionName+"/raw.jsonl"), "[REDACTED_AWS_KEY]",
		"the redacted slug is what shipped to the remote")
	// And: the redaction is recorded in the session's audit trail.
	assert.Contains(t, runGit(t, f.barePath, "show", "HEAD:sessions/"+sessionName+"/meta.json"), "redactions",
		"a RedactionPass must be recorded in meta.json so the scrub is auditable")
	gitFsckClean(t, f.barePath)
}

// TestRedactE2E_SecretInSummaryQuarantinedNotPushed covers the non-JSONL path.
//
// A credential lands in a non-JSONL artifact (summary.md) that the auto-redactor
// cannot rewrite in place. The gate quarantines the file — drops it from the
// push, preserves the bytes locally, records a debt marker — and the rest of the
// push proceeds, including an UNRELATED clean session. One bad artifact must not
// leak, and must not wedge everyone else's sync.
//
// Failure prevented: a leaked summary shipping to teammates, or a single bad
// session stalling the whole Ledger push.
func TestRedactE2E_SecretInSummaryQuarantinedNotPushed(t *testing.T) {
	f := newDraftLedgerFixture(t)
	const leaky = "2026-01-01T00-00-testuser-OxLeak"
	const clean = "2026-01-01T00-00-testuser-OxClen"
	const canary = "AKIAIOSFODNN7EXAMPLE"

	// Given: a session with a credential in its (non-JSONL) summary.md, plus an
	// unrelated clean session, both in one holding commit.
	leakyDir := seedFinalizedLedgerSessionWithArtifacts(t, f.ledgerPath, leaky)
	require.NoError(t, os.WriteFile(filepath.Join(leakyDir, "summary.md"),
		[]byte("Shipped the fix. Set AWS_ACCESS_KEY_ID="+canary+" to reproduce.\n"), 0644))
	seedFinalizedLedgerSessionWithArtifacts(t, f.ledgerPath, clean)
	commitSession(t, f.ledgerPath, leaky, clean)

	// When: the coworker pushes.
	require.NoError(t, pushLedger(context.Background(), f.ledgerPath))

	// Then: no object on the remote carries the credential.
	assertRemoteObjectsCleanOf(t, f.barePath, canary)
	// And: the leaking summary was dropped from the push (quarantined out).
	assert.NotContains(t, remoteTree(t, f.barePath), "sessions/"+leaky+"/summary.md",
		"the credential-bearing summary must not reach the remote")
	// And: the bytes are preserved locally and a debt marker records the state.
	assert.FileExists(t, filepath.Join(f.ledgerPath, ".sageox", "cache", "quarantine", leaky, "summary.md"),
		"quarantined bytes must be preserved on disk for recovery")
	assert.FileExists(t, filepath.Join(f.ledgerPath, ".sageox", "cache", "redaction-debt", leaky+".json"),
		"a redaction-debt marker must record the quarantine so `ox doctor` can surface it")
	// And: the unrelated clean session shipped normally — one bad session does not wedge the sync.
	assert.Contains(t, remoteTree(t, f.barePath), "sessions/"+clean+"/meta.json",
		"an unrelated clean session must still reach the remote")
	gitFsckClean(t, f.barePath)
}

// TestRedactE2E_AllowSecretsOverrideShipsRawSecret pins the escape hatch.
//
// OX_ALLOW_SECRETS=1 is the documented, deliberately-dangerous override: the
// coworker has chosen to publish raw. This test asserts the RAW secret DOES
// reach the remote under the override — so that if a future change silently
// starts redacting even under the override (changing the contract), it is caught.
func TestRedactE2E_AllowSecretsOverrideShipsRawSecret(t *testing.T) {
	f := newDraftLedgerFixture(t)
	t.Setenv("OX_ALLOW_SECRETS", "1")
	const sessionName = "2026-01-01T00-00-testuser-OxOvrd"
	const canary = "AKIAIOSFODNN7EXAMPLE"

	dir := seedFinalizedLedgerSessionWithArtifacts(t, f.ledgerPath, sessionName)
	rawLine := `{"type":"assistant","content":"export AWS_ACCESS_KEY_ID=` + canary + `"}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(rawLine), 0644))
	commitSession(t, f.ledgerPath, sessionName)

	require.NoError(t, pushLedger(context.Background(), f.ledgerPath))

	// The override deliberately ships the raw secret. If this ever redacts, the
	// override contract changed — fail loudly so a human re-decides.
	assert.Contains(t, runGit(t, f.barePath, "show", "HEAD:sessions/"+sessionName+"/raw.jsonl"), canary,
		"OX_ALLOW_SECRETS=1 must ship the raw secret unchanged — that is the chosen, dangerous behavior")
}
