package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type sessionUploadFixture struct {
	projectRoot string
	ledgerPath  string
	sessionName string
	rawContent  []byte
	result      *agentSessionResult
	state       *session.RecordingState
	refs        map[string]lfs.FileRef
}

func (f sessionUploadFixture) orphan() orphanedSession {
	return orphanedSession{
		SessionName: f.sessionName,
		CachePath:   f.state.SessionPath,
		Meta: &session.StoreMeta{
			SessionID: f.state.SessionID,
			AgentID:   f.state.AgentID,
			AgentType: f.state.AdapterName,
			Username:  "testuser",
			CreatedAt: f.state.StartedAt,
		},
		EntryCount: f.result.EntryCount,
	}
}

func newSessionUploadFixture(t *testing.T) sessionUploadFixture {
	t.Helper()
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	projectRoot := t.TempDir()
	ledgerPath := t.TempDir()
	cachePath := filepath.Join(t.TempDir(), "2026-09-01T03-00-testuser-OxLifecycle")
	require.NoError(t, os.MkdirAll(cachePath, 0o755))
	rawContent := []byte("{\"type\":\"header\",\"metadata\":{\"agent_id\":\"OxLifecycle\"}}\n" +
		"{\"type\":\"user\",\"content\":\"preserve me\"}\n" +
		"{\"type\":\"assistant\",\"content\":\"preserved\"}\n" +
		"{\"type\":\"footer\",\"entry_count\":2}\n")
	rawPath := filepath.Join(cachePath, ledgerFileRaw)
	require.NoError(t, os.WriteFile(rawPath, rawContent, 0o600))

	sessionName := filepath.Base(cachePath)
	return sessionUploadFixture{
		projectRoot: projectRoot,
		ledgerPath:  ledgerPath,
		sessionName: sessionName,
		rawContent:  rawContent,
		result: &agentSessionResult{
			RawPath:     rawPath,
			EntryCount:  2,
			SessionName: sessionName,
			Summary:     "A durable local summary",
		},
		state: &session.RecordingState{
			AgentID:     "OxLifecycle",
			AdapterName: "claude-code",
			SessionID:   "ses_019d0000-0000-7000-8000-000000000007",
			SessionPath: cachePath,
			StartedAt:   time.Date(2026, 9, 1, 3, 0, 0, 0, time.UTC),
			Title:       "Lifecycle recovery",
		},
		refs: map[string]lfs.FileRef{ledgerFileRaw: lfs.NewFileRef(rawContent)},
	}
}

func scriptedSessionUploadEffects(calls *[]string, refs map[string]lfs.FileRef, failAt string) sessionUploadEffects {
	mark := func(name string) error {
		*calls = append(*calls, name)
		if failAt == name {
			return errors.New(name + " failed")
		}
		return nil
	}
	return sessionUploadEffects{
		uploadLFS: func(_, _ string) (map[string]lfs.FileRef, error) {
			if err := mark("upload_lfs"); err != nil {
				return nil, err
			}
			return refs, nil
		},
		commitInitial: func(_, _ string) error { return mark("commit_initial") },
		commitRetry: func(_, _ string, _ bool) error {
			return mark("commit_retry")
		},
		commitPointerRewrite: func(_, _ string, paths []string) error {
			if len(paths) == 0 {
				return errors.New("pointer commit received no paths")
			}
			return mark("commit_pointers")
		},
		reconcilePlans: func(_ string, _ []string, _, _ string) {
			_ = mark("reconcile_plans")
		},
		finalizeLinkage: func(_, _ string, _ *lfs.SessionMeta, _ string) []api.PRLinkMiss {
			_ = mark("finalize_linkage")
			return nil
		},
	}
}

func assertSessionBytesPreserved(t *testing.T, fixture sessionUploadFixture) {
	t.Helper()
	cacheBytes, err := os.ReadFile(fixture.result.RawPath)
	require.NoError(t, err)
	assert.Equal(t, fixture.rawContent, cacheBytes, "the cache remains the authoritative retry source")

	ledgerRaw := filepath.Join(fixture.ledgerPath, "sessions", fixture.sessionName, ledgerFileRaw)
	ledgerBytes, err := os.ReadFile(ledgerRaw)
	require.NoError(t, err)
	assert.Equal(t, fixture.rawContent, ledgerBytes, "pre-push failures must leave real bytes, never a pointer")
	assert.False(t, lfs.IsPointerFile(ledgerRaw))
}

func TestUploadSessionToLedger_PreservesPriorStateAtFallibleBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name      string
		failAt    string
		wantCalls []string
		wantRefs  bool
	}{
		{
			name:      "LFS upload failure",
			failAt:    "upload_lfs",
			wantCalls: []string{"upload_lfs"},
		},
		{
			name:      "git sync failure after durable manifest",
			failAt:    "commit_initial",
			wantCalls: []string{"upload_lfs", "commit_initial"},
			wantRefs:  true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newSessionUploadFixture(t)
			var calls []string
			effects := scriptedSessionUploadEffects(&calls, fixture.refs, tc.failAt)

			err := uploadSessionToLedgerWithEffects(
				fixture.projectRoot, fixture.result, fixture.state,
				fixture.ledgerPath, fixture.sessionName, effects,
			)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.failAt+" failed")
			assert.Equal(t, tc.wantCalls, calls, "no later phase may run after a failed boundary")
			assertSessionBytesPreserved(t, fixture)

			meta, readErr := lfs.ReadSessionMeta(filepath.Join(fixture.ledgerPath, "sessions", fixture.sessionName))
			require.NoError(t, readErr, "metadata must be durable before external publication")
			assert.Equal(t, fixture.state.SessionID, meta.SessionID)
			assert.Equal(t, lfs.LinkageStatusStaged, meta.LinkageStatus)
			if tc.wantRefs {
				assert.Equal(t, fixture.refs, meta.Files)
			} else {
				assert.Empty(t, meta.Files)
			}
		})
	}
}

func TestUploadSessionToLedger_FullSuccessRunsOnlyPostPushEffects(t *testing.T) {
	fixture := newSessionUploadFixture(t)
	var calls []string
	effects := scriptedSessionUploadEffects(&calls, fixture.refs, "")
	effects.finalizeLinkage = func(_, _ string, _ *lfs.SessionMeta, _ string) []api.PRLinkMiss {
		calls = append(calls, "finalize_linkage")
		return []api.PRLinkMiss{{
			PRURL:        "https://github.com/sageox/ox/pull/999",
			ExpectedLine: "SageOx-Session: https://sageox.ai/c/ses_test",
		}}
	}

	err := uploadSessionToLedgerWithEffects(
		fixture.projectRoot, fixture.result, fixture.state,
		fixture.ledgerPath, fixture.sessionName, effects,
	)

	require.NoError(t, err)
	assert.Equal(t, []string{
		"upload_lfs", "commit_initial", "reconcile_plans", "commit_pointers", "finalize_linkage",
	}, calls)
	ledgerRaw := filepath.Join(fixture.ledgerPath, "sessions", fixture.sessionName, ledgerFileRaw)
	assert.True(t, lfs.IsPointerFile(ledgerRaw), "real bytes become pointers only after the durable push")
	cacheBytes, readErr := os.ReadFile(fixture.result.RawPath)
	require.NoError(t, readErr)
	assert.Equal(t, fixture.rawContent, cacheBytes, "the local source survives through post-push processing")
	require.Len(t, fixture.result.PRLinkMisses, 1)
	assert.Contains(t, fixture.result.PRLinkMisses[0], "pull/999")
}

func TestUploadSessionToLedger_GitignoreFailureStopsBeforePush(t *testing.T) {
	fixture := newSessionUploadFixture(t)
	gitignorePath := filepath.Join(fixture.ledgerPath, "sessions", ".gitignore")
	require.NoError(t, os.MkdirAll(gitignorePath, 0o755))
	var calls []string
	effects := scriptedSessionUploadEffects(&calls, fixture.refs, "")

	err := uploadSessionToLedgerWithEffects(
		fixture.projectRoot, fixture.result, fixture.state,
		fixture.ledgerPath, fixture.sessionName, effects,
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "ensure .gitignore")
	assert.Equal(t, []string{"upload_lfs"}, calls, "push and all post-push effects must remain unreachable")
	assertSessionBytesPreserved(t, fixture)
	meta, readErr := lfs.ReadSessionMeta(filepath.Join(fixture.ledgerPath, "sessions", fixture.sessionName))
	require.NoError(t, readErr)
	assert.Equal(t, fixture.refs, meta.Files, "the uploaded manifest remains durable for doctor retry")
}

func TestUploadSessionToLedger_ReadOnlyIsNotHiddenByRecoveryWrapping(t *testing.T) {
	fixture := newSessionUploadFixture(t)
	var calls []string
	effects := scriptedSessionUploadEffects(&calls, fixture.refs, "")
	effects.uploadLFS = func(_, _ string) (map[string]lfs.FileRef, error) {
		calls = append(calls, "upload_lfs")
		return nil, api.ErrReadOnly
	}

	err := uploadSessionToLedgerWithEffects(
		fixture.projectRoot, fixture.result, fixture.state,
		fixture.ledgerPath, fixture.sessionName, effects,
	)

	assert.ErrorIs(t, err, api.ErrReadOnly)
	assert.Equal(t, api.ErrReadOnly, err, "the caller relies on the sentinel for membership guidance")
	assert.Equal(t, []string{"upload_lfs"}, calls)
	assertSessionBytesPreserved(t, fixture)
}

func TestSessionUploadOrchestration_FailedUploadThenRetryIsIdempotent(t *testing.T) {
	fixture := newSessionUploadFixture(t)
	var failedCalls []string
	failedEffects := scriptedSessionUploadEffects(&failedCalls, fixture.refs, "commit_initial")
	require.Error(t, uploadSessionToLedgerWithEffects(
		fixture.projectRoot, fixture.result, fixture.state,
		fixture.ledgerPath, fixture.sessionName, failedEffects,
	))
	assertSessionBytesPreserved(t, fixture)

	orphan := fixture.orphan()

	var retryCalls []string
	retryEffects := scriptedSessionUploadEffects(&retryCalls, fixture.refs, "")
	require.NoError(t, retrySessionUploadWithEffects(
		fixture.projectRoot, fixture.ledgerPath, orphan, retryEffects,
	))
	assert.Equal(t, []string{"upload_lfs", "commit_retry", "commit_pointers"}, retryCalls)

	sessionDir := filepath.Join(fixture.ledgerPath, "sessions", fixture.sessionName)
	firstMeta, err := lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	assert.Equal(t, fixture.state.SessionID, firstMeta.SessionID,
		"recovery must preserve the ID already made durable by the failed stop")
	assert.True(t, lfs.IsPointerFile(filepath.Join(sessionDir, ledgerFileRaw)))
	cacheBytes, err := os.ReadFile(fixture.result.RawPath)
	require.NoError(t, err)
	assert.Equal(t, fixture.rawContent, cacheBytes)

	// A second doctor pass after a crash between remote push and cache pruning
	// is safe: it replays from cache, retains identity, and converges again.
	retryCalls = nil
	require.NoError(t, retrySessionUploadWithEffects(
		fixture.projectRoot, fixture.ledgerPath, orphan, retryEffects,
	))
	assert.Equal(t, []string{"upload_lfs", "commit_retry", "commit_pointers"}, retryCalls)
	secondMeta, err := lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	assert.Equal(t, firstMeta.SessionID, secondMeta.SessionID)
	assert.Equal(t, firstMeta.Files, secondMeta.Files)
	assert.True(t, lfs.IsPointerFile(filepath.Join(sessionDir, ledgerFileRaw)))
}

func TestRetrySessionUpload_PointerCommitFailureRemainsIncomplete(t *testing.T) {
	fixture := newSessionUploadFixture(t)
	orphan := fixture.orphan()

	var calls []string
	effects := scriptedSessionUploadEffects(&calls, fixture.refs, "commit_pointers")
	err := retrySessionUploadWithEffects(
		fixture.projectRoot, fixture.ledgerPath, orphan, effects,
	)

	require.ErrorContains(t, err, "commit LFS pointer rewrite")
	assert.Equal(t, []string{"upload_lfs", "commit_retry", "commit_pointers"}, calls)
	_, statErr := os.Stat(fixture.state.SessionPath)
	assert.NoError(t, statErr, "failed retry must leave authoritative cache available")
	require.FileExists(t, filepath.Join(fixture.state.SessionPath, sessionUploadRetryPendingFile))

	// The committed final metadata used to make the next doctor pass skip this
	// cache directory forever. The pending marker must keep it discoverable.
	orphans, scanErr := findOrphanedSessionsInDir(filepath.Dir(fixture.state.SessionPath), fixture.ledgerPath)
	require.NoError(t, scanErr)
	require.Len(t, orphans, 1)
	assert.Equal(t, fixture.sessionName, orphans[0].SessionName)

	calls = nil
	successEffects := scriptedSessionUploadEffects(&calls, fixture.refs, "")
	require.NoError(t, retrySessionUploadWithEffects(
		fixture.projectRoot, fixture.ledgerPath, orphans[0], successEffects,
	))
	assert.Equal(t, []string{"upload_lfs", "commit_retry", "commit_pointers"}, calls)
	assert.True(t, lfs.IsPointerFile(filepath.Join(
		fixture.ledgerPath, "sessions", fixture.sessionName, ledgerFileRaw,
	)))

	require.NoError(t, os.RemoveAll(orphans[0].CachePath), "doctor prunes cache only after full success")
	orphans, scanErr = findOrphanedSessionsInDir(filepath.Dir(fixture.state.SessionPath), fixture.ledgerPath)
	require.NoError(t, scanErr)
	assert.Empty(t, orphans)
}

func TestRetrySessionUpload_AcceptsLargeValidHeader(t *testing.T) {
	fixture := newSessionUploadFixture(t)

	var raw bytes.Buffer
	enc := json.NewEncoder(&raw)
	require.NoError(t, enc.Encode(map[string]any{
		"type": "header",
		"metadata": map[string]any{
			"agent_id":   fixture.state.AgentID,
			"agent_type": fixture.state.AdapterName,
			"session_id": fixture.state.SessionID,
			"model":      strings.Repeat("m", 70*1024),
		},
	}))
	require.NoError(t, enc.Encode(map[string]any{"type": "user", "content": "preserve me"}))
	require.NoError(t, enc.Encode(map[string]any{"type": "footer", "entry_count": 1}))
	require.Greater(t, bytes.IndexByte(raw.Bytes(), '\n'), 64*1024)

	fixture.rawContent = raw.Bytes()
	require.NoError(t, os.WriteFile(fixture.result.RawPath, fixture.rawContent, 0o600))
	fixture.result.EntryCount = 1
	fixture.refs = map[string]lfs.FileRef{ledgerFileRaw: lfs.NewFileRef(fixture.rawContent)}

	require.NoError(t, validateRawJSONLHeader(fixture.result.RawPath))
	var calls []string
	effects := scriptedSessionUploadEffects(&calls, fixture.refs, "")
	require.NoError(t, retrySessionUploadWithEffects(
		fixture.projectRoot, fixture.ledgerPath, fixture.orphan(), effects,
	))
	assert.Equal(t, []string{"upload_lfs", "commit_retry", "commit_pointers"}, calls)
}

func TestWriteSessionUploadRetryPending_RequiresExistingCache(t *testing.T) {
	err := writeSessionUploadRetryPending(filepath.Join(t.TempDir(), "missing"))
	require.Error(t, err)
}

func TestRetrySessionUpload_PendingMarkerFailureStopsBeforeMutation(t *testing.T) {
	fixture := newSessionUploadFixture(t)
	require.NoError(t, os.Mkdir(
		filepath.Join(fixture.state.SessionPath, sessionUploadRetryPendingFile),
		0o700,
	))

	var calls []string
	err := retrySessionUploadWithEffects(
		fixture.projectRoot,
		fixture.ledgerPath,
		fixture.orphan(),
		scriptedSessionUploadEffects(&calls, fixture.refs, ""),
	)
	require.ErrorContains(t, err, "record pending session upload retry")
	assert.Empty(t, calls, "ledger mutation must not begin without durable retry ownership")
}

func TestRetrySessionUpload_UnsafePointerPathRemainsRetryable(t *testing.T) {
	fixture := newSessionUploadFixture(t)
	fixture.refs["../escape.jsonl"] = lfs.NewFileRef([]byte("must not escape"))

	var calls []string
	err := retrySessionUploadWithEffects(
		fixture.projectRoot,
		fixture.ledgerPath,
		fixture.orphan(),
		scriptedSessionUploadEffects(&calls, fixture.refs, ""),
	)
	require.ErrorContains(t, err, "write LFS pointer files")
	require.FileExists(t, filepath.Join(fixture.state.SessionPath, sessionUploadRetryPendingFile))
}
