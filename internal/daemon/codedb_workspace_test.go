package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- UpdateProjectRoot ---

func TestUpdateProjectRoot_UpdatesPath(t *testing.T) {
	t.Parallel()
	mgr := NewCodeDBManager("/old/workspace", codedbTestLogger(), nil)

	mgr.UpdateProjectRoot("/new/workspace")

	mgr.mu.Lock()
	got := mgr.projectRoot
	mgr.mu.Unlock()
	assert.Equal(t, "/new/workspace", got)
}

func TestUpdateProjectRoot_IgnoresEmptyPath(t *testing.T) {
	t.Parallel()
	mgr := NewCodeDBManager("/original", codedbTestLogger(), nil)

	mgr.UpdateProjectRoot("")

	mgr.mu.Lock()
	got := mgr.projectRoot
	mgr.mu.Unlock()
	assert.Equal(t, "/original", got)
}

// TestUpdateProjectRoot_NoSwitchWhenCurrentExists is the regression test for the
// "worktree oscillation" bug: two simultaneously active sessions (e.g., main
// worktree + Conductor worktree) caused the daemon to flip projectRoot on every
// heartbeat, triggering a full dirty-index rebuild (~79ms) and occasionally a
// full git re-index (~10s) on every 60s scheduler tick.
// The fix: only switch if the current path no longer exists on disk.
func TestUpdateProjectRoot_NoSwitchWhenCurrentExists(t *testing.T) {
	t.Parallel()

	// both worktrees exist simultaneously
	mainWorktree := t.TempDir()
	conductorWorktree := t.TempDir()

	mgr := NewCodeDBManager(mainWorktree, codedbTestLogger(), nil)

	// simulate heartbeat from a simultaneously active Conductor workspace
	mgr.UpdateProjectRoot(conductorWorktree)

	mgr.mu.Lock()
	got := mgr.projectRoot
	mgr.mu.Unlock()

	// must NOT switch — mainWorktree still exists
	assert.Equal(t, mainWorktree, got, "should not switch while current path still exists")
}

func TestUpdateProjectRoot_IgnoresSamePath(t *testing.T) {
	t.Parallel()
	mgr := NewCodeDBManager("/same/path", codedbTestLogger(), nil)

	mgr.UpdateProjectRoot("/same/path")

	mgr.mu.Lock()
	got := mgr.projectRoot
	mgr.mu.Unlock()
	assert.Equal(t, "/same/path", got)
}

func TestUpdateProjectRoot_ConcurrentUpdates(t *testing.T) {
	t.Parallel()
	mgr := NewCodeDBManager("/initial", codedbTestLogger(), nil)

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			mgr.UpdateProjectRoot("/workspace-" + string(rune('a'+n%26)))
		}(i)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent UpdateProjectRoot deadlocked")
	}

	// should have one of the paths, not be corrupted
	mgr.mu.Lock()
	got := mgr.projectRoot
	mgr.mu.Unlock()
	assert.NotEmpty(t, got)
}

func TestUpdateProjectRoot_RaceWithStats(t *testing.T) {
	t.Parallel()
	mgr := NewCodeDBManager("/initial", codedbTestLogger(), nil)

	var wg sync.WaitGroup
	// writers
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mgr.UpdateProjectRoot("/new/path")
		}()
	}
	// readers
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = mgr.Stats()
		}()
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent UpdateProjectRoot + Stats deadlocked")
	}
}

// --- CallerPath callback wiring ---

func TestCallerPathCallback_FiresOnNewPath(t *testing.T) {
	t.Parallel()
	handler := NewHeartbeatHandler(codedbTestLogger())

	var received []string
	var mu sync.Mutex
	handler.SetCallerPathCallback(func(path string) {
		mu.Lock()
		received = append(received, path)
		mu.Unlock()
	})

	// heartbeat with caller path
	payload := HeartbeatPayload{
		CallerPath: "/workspace/alpha",
		Timestamp:  time.Now(),
	}
	data, _ := json.Marshal(payload)
	handler.Handle("caller-1", data)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, received, 1)
	assert.Equal(t, "/workspace/alpha", received[0])
}

func TestCallerPathCallback_FiresOnEveryHeartbeat(t *testing.T) {
	t.Parallel()
	handler := NewHeartbeatHandler(codedbTestLogger())

	var count int
	var mu sync.Mutex
	handler.SetCallerPathCallback(func(path string) {
		mu.Lock()
		count++
		mu.Unlock()
	})

	// same path sent twice — callback fires both times because the daemon
	// can't know if the path became invalid and was re-created
	for range 3 {
		payload := HeartbeatPayload{
			CallerPath: "/workspace/same",
			Timestamp:  time.Now(),
		}
		data, _ := json.Marshal(payload)
		handler.Handle("caller-1", data)
	}

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 3, count)
}

func TestCallerPathCallback_NotFiredWithoutCallerPath(t *testing.T) {
	t.Parallel()
	handler := NewHeartbeatHandler(codedbTestLogger())

	called := false
	handler.SetCallerPathCallback(func(path string) {
		called = true
	})

	// heartbeat without CallerPath
	payload := HeartbeatPayload{
		RepoPath:  "/some/repo",
		Timestamp: time.Now(),
	}
	data, _ := json.Marshal(payload)
	handler.Handle("caller-1", data)

	assert.False(t, called)
}

func TestCallerPathCallback_NotFiredWithoutCallerID(t *testing.T) {
	t.Parallel()
	handler := NewHeartbeatHandler(codedbTestLogger())

	called := false
	handler.SetCallerPathCallback(func(path string) {
		called = true
	})

	// heartbeat with path but no caller ID — caller tracking block is skipped
	payload := HeartbeatPayload{
		CallerPath: "/workspace/orphan",
		Timestamp:  time.Now(),
	}
	data, _ := json.Marshal(payload)
	handler.Handle("", data)

	assert.False(t, called)
}

// --- Workspace lifecycle edge cases ---
// These test the patterns that lead to the original bug: daemon holds a path
// that stops existing mid-flight.

func TestCodeDBManager_DeletedProjectRoot_StatsDoesNotPanic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	// simulate Conductor deleting the workspace
	require.NoError(t, os.RemoveAll(dir))

	// Stats should return gracefully, not panic
	stats := mgr.Stats()
	assert.False(t, stats.IndexExists)
	assert.Empty(t, stats.LastError)
}

func TestCodeDBManager_DeletedThenRecreatedProjectRoot(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	// delete workspace
	require.NoError(t, os.RemoveAll(dir))

	// new workspace at different path
	newDir := t.TempDir()
	mgr.UpdateProjectRoot(newDir)

	mgr.mu.Lock()
	got := mgr.projectRoot
	mgr.mu.Unlock()
	assert.Equal(t, newDir, got)
}

func TestCodeDBManager_RapidWorkspaceSwitches(t *testing.T) {
	t.Parallel()

	mgr := NewCodeDBManager("/initial", codedbTestLogger(), nil)

	// simulate Conductor rapidly creating/deleting workspaces
	paths := []string{
		"/workspace/alpha",
		"/workspace/bravo",
		"/workspace/charlie",
		"/workspace/delta",
	}
	for _, p := range paths {
		mgr.UpdateProjectRoot(p)
	}

	mgr.mu.Lock()
	got := mgr.projectRoot
	mgr.mu.Unlock()
	assert.Equal(t, "/workspace/delta", got, "should have the last path")
}

// --- resolveSharedDataDir with deleted project root ---

func TestCodeDBManager_ResolveSharedDataDir_MissingProjectRoot(t *testing.T) {
	t.Parallel()

	// project root that doesn't exist — resolveSharedDataDir should fall back
	// to legacy path without panicking
	mgr := NewCodeDBManager("/does/not/exist", codedbTestLogger(), nil)
	dir := mgr.resolveSharedDataDir()
	assert.NotEmpty(t, dir, "should return a fallback path even with missing project root")
}

// --- Index fails fast on deleted worktree ---

func TestIndex_DeletedProjectRoot_FailsFast(t *testing.T) {
	t.Parallel()

	// create then immediately delete a directory to simulate a Conductor worktree removal
	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)
	require.NoError(t, os.RemoveAll(dir))

	ctx := context.Background()
	_, err := mgr.Index(ctx, CodeIndexPayload{}, nil)

	require.Error(t, err, "Index must fail when projectRoot no longer exists")
	assert.Contains(t, err.Error(), "no longer exists")

	// indexing flag must be cleared even on this error path
	mgr.mu.Lock()
	still := mgr.indexing
	mgr.mu.Unlock()
	assert.False(t, still, "indexing flag must be cleared after early exit")
}

// --- Symlink edge case ---

func TestUpdateProjectRoot_Symlink(t *testing.T) {
	t.Parallel()

	realDir := t.TempDir()
	linkDir := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(realDir, linkDir))

	mgr := NewCodeDBManager(realDir, codedbTestLogger(), nil)

	// simulate Conductor deleting the old workspace — only then should we switch
	require.NoError(t, os.RemoveAll(realDir))

	// update to symlink path — should accept it (let git resolve the real path)
	mgr.UpdateProjectRoot(linkDir)

	mgr.mu.Lock()
	got := mgr.projectRoot
	mgr.mu.Unlock()
	assert.Equal(t, linkDir, got)
}

// --- C. Stats merge ---

// TestStats_LedgerFieldsPopulated verifies Stats() reports ledger index info
// after a ledger index build.
// Failure prevented: ox status not showing ledger index info.
func TestStats_LedgerFieldsPopulated(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	// simulate successful ledger index build
	mgr.mu.Lock()
	mgr.ledgerStats = CodeDBStats{
		IndexExists: true,
		Commits:     15,
		Symbols:     200,
		Comments:    50,
	}
	mgr.mu.Unlock()

	stats := mgr.Stats()
	assert.True(t, stats.LedgerExists, "LedgerExists must be true after ledger index build")
	assert.Equal(t, 15, stats.LedgerCommits, "LedgerCommits must reflect ledger index stats")
	assert.False(t, stats.LedgerIndexingNow, "LedgerIndexingNow must be false when not building")
}

// TestUpdateProjectRoot_DuringLedgerIndexBuild_NoPanic verifies workspace switch during
// ledger index build doesn't corrupt state.
// Failure prevented: Conductor switching workspaces while ledger index is building.
func TestUpdateProjectRoot_DuringLedgerIndexBuild_NoPanic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	newDir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	mgr.ledgerTestHook = func() {
		select {
		case entered <- struct{}{}:
		default:
		}
		// simulate Conductor deleting old workspace then switching
		// UpdateProjectRoot only switches when the old root is gone
		_ = os.RemoveAll(dir)
		mgr.UpdateProjectRoot(newDir)
		<-release
	}

	ctx := context.Background()
	go mgr.BuildLedgerIndex(ctx, dir)

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("ledgerTestHook did not run")
	}
	close(release)
	waitForLedgerIndexingDone(t, mgr)

	mgr.mu.Lock()
	got := mgr.projectRoot
	mgr.mu.Unlock()
	assert.Equal(t, newDir, got, "projectRoot should reflect the workspace switch")
}

// TestDoIndex_DirtyOverlayToLedger_FailureDoesNotBlockWorktreeIndex verifies that
// if the dirty overlay redirect to ledger index dir fails (e.g. ledger index dir missing),
// the main worktree indexing still completes normally.
// Failure prevented: ledger index dir disappearance blocks all worktree indexing.
func TestDoIndex_DirtyOverlayToLedger_FailureDoesNotBlockWorktreeIndex(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// create .git so the ledger-not-cloned guard in doIndex passes
	// (resolveSharedDataDir falls back to <dir>/.sageox/cache/codedb, which
	// triggers ledgerRootForDataDir → checks <dir>/.git)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	// set a ledger index dir that doesn't exist — the dirty overlay redirect should
	// silently fail without affecting the main index
	mgr.mu.Lock()
	mgr.ledgerDataDir = filepath.Join(dir, "nonexistent-ledger")
	mgr.mu.Unlock()

	// Index will fail because there's no valid git repo, but the important thing is
	// it fails at the git step, NOT at the ledger dirty overlay step
	ctx := context.Background()
	_, err := mgr.Index(ctx, CodeIndexPayload{}, nil)

	// should fail because no valid git repo — NOT because of ledger dir issues
	require.Error(t, err)
	assert.Contains(t, err.Error(), "index local", "error should be from git indexing, not ledger dirty overlay")

	// indexing flag must be released
	mgr.mu.Lock()
	flag := mgr.indexing
	mgr.mu.Unlock()
	assert.False(t, flag, "indexing flag must be released")
}

// TestStats_LedgerOverridesEvenWhenWorktreeIndexMissing verifies that Stats()
// reports ledger index availability even when no worktree index exists.
// Failure prevented: fresh install shows "no index" when ledger index is actually ready.
func TestStats_LedgerOverridesEvenWhenWorktreeIndexMissing(t *testing.T) {
	t.Parallel()

	// non-existent project root — no worktree index
	mgr := NewCodeDBManager("/does/not/exist", codedbTestLogger(), nil)

	// simulate successful ledger index
	mgr.mu.Lock()
	mgr.ledgerStats = CodeDBStats{
		IndexExists: true,
		Commits:     25,
		Symbols:     300,
	}
	mgr.mu.Unlock()

	stats := mgr.Stats()
	assert.True(t, stats.LedgerExists, "ledger index must be reported even without worktree index")
	assert.Equal(t, 25, stats.LedgerCommits)
	// worktree index fields should be empty/false
	assert.False(t, stats.IndexExists, "worktree index should not exist")
}
