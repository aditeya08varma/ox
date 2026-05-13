package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented across this file (ox-y3ok): the pre-push secret gate
// must never block the push. Auto-redact what it can, quarantine what it
// can't, but always return nil so unrelated commits and other sessions
// keep flowing.

// seedSessionMeta writes a minimal valid meta.json at sessions/<name>/.
// Required because the canonical RedactionPass writer
// (writeRedactionPassesPerSession → lfs.MutateSessionMeta) refuses to
// synthesize a meta.json from scratch — see the comment at meta.go.
func seedSessionMeta(t *testing.T, sessionDir, sessionName string) {
	t.Helper()
	meta := lfs.SessionMeta{
		Version:     "1.0",
		SessionName: sessionName,
		AgentID:     "test-agent",
		AgentType:   "claude-code",
		CreatedAt:   time.Now().UTC(),
	}
	buf, err := json.MarshalIndent(meta, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "meta.json"), buf, 0o644))
}

// TestRunPrePushSecretGate_AutoRedactsJSONLAndAllowsPush is the happy
// path of the recovery pipeline: a JSONL session file with a planted
// canary gets rewritten through the canonical chokepoint, a
// RedactionPass lands in meta.json, the holding commit is amended,
// and the gate returns nil so the push proceeds.
//
// Failure prevented: a single session with a chokepoint-recoverable
// finding holds up the entire push — defeating both the "never block"
// directive and the canonical chokepoint's idempotency guarantee.
func TestRunPrePushSecretGate_AutoRedactsJSONLAndAllowsPush(t *testing.T) {
	t.Setenv("OX_ALLOW_SECRETS", "") // ensure no override
	const sessionName = "2026-05-12-autoredact"
	const canary = "AKIAIOSFODNN7EXAMPLE"
	rec := `{"type":"text","value":"` + canary + `"}` + "\n"

	work := makeLedgerWithCommit(t, map[string]string{
		filepath.ToSlash(filepath.Join("sessions", sessionName, "raw.jsonl")): rec,
	})
	seedSessionMeta(t, filepath.Join(work, "sessions", sessionName), sessionName)
	mustGit(t, work, "add", "--sparse", filepath.Join("sessions", sessionName, "meta.json"))
	mustGit(t, work, "commit", "--amend", "--no-edit", "--no-verify")

	err := runPrePushSecretGate(context.Background(), work)
	require.NoError(t, err, "gate must NEVER block the push")

	// raw.jsonl on disk must no longer contain the canary bytes.
	rawAbs := filepath.Join(work, "sessions", sessionName, "raw.jsonl")
	bytes, readErr := os.ReadFile(rawAbs)
	require.NoError(t, readErr)
	assert.NotContains(t, string(bytes), canary,
		"canary still on disk; auto-redact did not run or did not scrub")

	// Re-scan must come back clean — the chokepoint and the scanner
	// share a detector catalog; if redact says it cleared, scan must
	// agree, otherwise we'd loop forever on a future iteration.
	rescan, scanErr := scanPrePushForSecrets(context.Background(), work)
	require.NoError(t, scanErr)
	assert.Empty(t, rescan.Findings, "scanner still finds the canary after auto-redact: %v", rescan.Findings)

	// meta.json must carry a RedactionPass entry recording the catalog
	// identity and the per-line finding metadata (file/line/detector).
	metaBuf, err := os.ReadFile(filepath.Join(work, "sessions", sessionName, "meta.json"))
	require.NoError(t, err)
	var meta lfs.SessionMeta
	require.NoError(t, json.Unmarshal(metaBuf, &meta))
	require.NotEmpty(t, meta.Redactions, "meta.json missing RedactionPass entry")
	pass := meta.Redactions[len(meta.Redactions)-1]
	assert.NotEmpty(t, pass.PassID)
	assert.NotEmpty(t, pass.CatalogVersion)
	assert.NotEmpty(t, pass.CatalogHash)
	assert.GreaterOrEqual(t, len(pass.Entries), 1, "expected at least one per-line entry")
	for _, e := range pass.Entries {
		assert.Equal(t, "raw.jsonl", e.File)
		assert.NotEmpty(t, e.Detector)
		// MUST NEVER carry matched bytes — ox-zyg7.
		assert.NotContains(t, e.Detector, canary)
	}

	// The amend should have updated HEAD with the redacted bytes —
	// `git diff HEAD~..HEAD --stat` should show raw.jsonl changed.
	// (We don't strictly assert content here; the disk-bytes check above
	// covers the load-bearing claim.)
}

// TestRunPrePushSecretGate_QuarantinesNonJSONLAndAllowsPush exercises
// the recovery path that auto-redact cannot service. A planted canary
// in a non-JSONL file (the chokepoint only rewrites JSONL) must be
// preserved on disk, moved to the quarantine cache, dropped from the
// holding commit, and surfaced via a debt marker — all without
// returning an error.
//
// Failure prevented: a non-JSONL session file with a finding blocks
// the push of ALL other sessions and unrelated commits, and either
// data is lost or the user has no on-disk record of what was carved out.
func TestRunPrePushSecretGate_QuarantinesNonJSONLAndAllowsPush(t *testing.T) {
	t.Setenv("OX_ALLOW_SECRETS", "")
	const sessionName = "2026-05-12-quarantine"
	const canary = "AKIAIOSFODNN7EXAMPLE"
	relMD := filepath.ToSlash(filepath.Join("sessions", sessionName, "notes.md"))

	work := makeLedgerWithCommit(t, map[string]string{
		// Also include an UNRELATED file in the same commit so we can
		// assert that the rest of the push survives.
		"unrelated.txt": "hello world\n",
		relMD:           "# Notes\n\nfound a token: " + canary + "\n",
	})
	seedSessionMeta(t, filepath.Join(work, "sessions", sessionName), sessionName)
	mustGit(t, work, "add", "--sparse", filepath.Join("sessions", sessionName, "meta.json"))
	mustGit(t, work, "commit", "--amend", "--no-edit", "--no-verify")

	err := runPrePushSecretGate(context.Background(), work)
	require.NoError(t, err, "gate must NEVER block, even on non-JSONL findings")

	// Original in-place path must be gone (moved aside).
	_, statErr := os.Stat(filepath.Join(work, relMD))
	assert.True(t, os.IsNotExist(statErr),
		"original notes.md should have been moved to quarantine; stat err=%v", statErr)

	// Quarantine path must hold the bytes.
	quarRel := filepath.Join(".sageox", "cache", "quarantine", sessionName, "notes.md")
	quarAbs := filepath.Join(work, quarRel)
	bytes, readErr := os.ReadFile(quarAbs)
	require.NoError(t, readErr, "quarantine path missing")
	assert.Contains(t, string(bytes), canary,
		"bytes must be preserved verbatim in quarantine; the point of quarantine is no data loss")

	// Debt marker must exist and parse.
	markerRel := filepath.Join(".sageox", "cache", "redaction-debt", sessionName+".json")
	markerBuf, err := os.ReadFile(filepath.Join(work, markerRel))
	require.NoError(t, err, "debt marker missing")
	var rec redactionDebtRecord
	require.NoError(t, json.Unmarshal(markerBuf, &rec))
	assert.Equal(t, sessionName, rec.SessionName)
	require.NotEmpty(t, rec.Findings, "marker missing findings")
	require.NotEmpty(t, rec.QuarantinePaths, "marker missing quarantine path mapping")
	assert.Equal(t, relMD, rec.QuarantinePaths[0].From)
	assert.Equal(t, filepath.ToSlash(quarRel), rec.QuarantinePaths[0].To)

	// The holding commit must NOT carry the quarantined path anymore.
	mustGit(t, work, "rev-parse", "HEAD")
	if mustGitCapture(t, work, "ls-tree", "--name-only", "HEAD", filepath.ToSlash(relMD)) != "" {
		t.Fatalf("notes.md still tracked at HEAD; carve-out failed")
	}
	// And the rest of the commit must survive: unrelated.txt and the
	// session's meta.json should still be in HEAD's tree.
	if mustGitCapture(t, work, "ls-tree", "--name-only", "HEAD", "unrelated.txt") == "" {
		t.Fatalf("unrelated.txt missing from HEAD; quarantine over-reached")
	}
}

// TestRunPrePushSecretGate_OverrideStillSkipsRecovery confirms
// OX_ALLOW_SECRETS=1 still short-circuits the recovery pipeline.
// Required so emergency overrides don't get auto-redacted or
// auto-quarantined — when a user explicitly says "publish as-is," the
// gate must respect that, not silently rewrite their bytes.
func TestRunPrePushSecretGate_OverrideStillSkipsRecovery(t *testing.T) {
	t.Setenv("OX_ALLOW_SECRETS", "1")
	const sessionName = "2026-05-12-override"
	const canary = "AKIAIOSFODNN7EXAMPLE"
	relJSONL := filepath.ToSlash(filepath.Join("sessions", sessionName, "raw.jsonl"))
	rec := `{"type":"text","value":"` + canary + `"}` + "\n"

	work := makeLedgerWithCommit(t, map[string]string{relJSONL: rec})
	err := runPrePushSecretGate(context.Background(), work)
	require.NoError(t, err)

	// With override, raw.jsonl must NOT have been rewritten.
	bytes, readErr := os.ReadFile(filepath.Join(work, relJSONL))
	require.NoError(t, readErr)
	assert.Contains(t, string(bytes), canary,
		"override must NOT auto-redact; bytes go to cloud as-is")
}

// mustGitCapture runs git and returns trimmed stdout. Helper for tests
// that need to assert on tree contents.
func mustGitCapture(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := captureGit(dir, args...)
	require.NoErrorf(t, err, "git %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(out)
}

func captureGit(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	return string(out), err
}
