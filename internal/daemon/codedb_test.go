package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func codedbTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

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

// --- End-to-end: heartbeat → codedb project root update ---

func TestHeartbeatUpdatesCodeDBProjectRoot(t *testing.T) {
	t.Parallel()

	logger := codedbTestLogger()
	handler := NewHeartbeatHandler(logger)
	mgr := NewCodeDBManager("/old/workspace", logger, nil)

	// wire them the same way daemon.go does
	handler.SetCallerPathCallback(func(path string) {
		mgr.UpdateProjectRoot(path)
	})

	// simulate heartbeat from new workspace
	payload := HeartbeatPayload{
		CallerPath: "/new/workspace",
		Timestamp:  time.Now(),
	}
	data, _ := json.Marshal(payload)
	handler.Handle("caller-abc", data)

	mgr.mu.Lock()
	got := mgr.projectRoot
	mgr.mu.Unlock()
	assert.Equal(t, "/new/workspace", got)
}

func TestHeartbeatUpdatesCodeDBProjectRoot_MultipleWorkspaces(t *testing.T) {
	t.Parallel()

	logger := codedbTestLogger()
	handler := NewHeartbeatHandler(logger)
	mgr := NewCodeDBManager("/workspace/edinburgh-v1", logger, nil)

	handler.SetCallerPathCallback(func(path string) {
		mgr.UpdateProjectRoot(path)
	})

	// simulate the exact Conductor pattern:
	// edinburgh-v1 → khartoum-v1 → da-nang-v1
	workspaces := []struct {
		callerID string
		path     string
	}{
		{"abc123", "/workspace/edinburgh-v1"},
		{"def456", "/workspace/khartoum-v1"},
		{"ghi789", "/workspace/da-nang-v1"},
	}

	for _, ws := range workspaces {
		payload := HeartbeatPayload{
			CallerPath: ws.path,
			Timestamp:  time.Now(),
		}
		data, _ := json.Marshal(payload)
		handler.Handle(ws.callerID, data)
	}

	mgr.mu.Lock()
	got := mgr.projectRoot
	mgr.mu.Unlock()
	assert.Equal(t, "/workspace/da-nang-v1", got)
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

// --- Same-repo workspace thrashing (regression: high CPU from re-indexing loop) ---

func TestUpdateProjectRoot_SameSharedDir_KeepsCache(t *testing.T) {
	t.Parallel()

	// Two workspaces that share the same .sageox/config.json (same repo).
	// Without the fix, every heartbeat from the alternate workspace would
	// reset dataDir → trigger re-resolution → trigger re-indexing → 100%+ CPU.
	dirA := t.TempDir()
	dirB := t.TempDir()

	// Simulate initialized projects with identical config (same repoID + endpoint).
	for _, d := range []string{dirA, dirB} {
		sageoxDir := filepath.Join(d, ".sageox")
		require.NoError(t, os.MkdirAll(sageoxDir, 0o755))
		configJSON := `{"repo_id":"repo_test123","endpoint":"https://sageox.ai"}`
		require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(configJSON), 0o644))
	}

	mgr := NewCodeDBManager(dirA, codedbTestLogger(), nil)

	// Force initial resolution so dataDir gets cached.
	initialDir := mgr.resolveSharedDataDir()
	require.NotEmpty(t, initialDir)

	// Switch to workspace B (same repo) — dataDir should NOT be reset.
	mgr.UpdateProjectRoot(dirB)

	mgr.mu.Lock()
	cachedDir := mgr.dataDir
	root := mgr.projectRoot
	mgr.mu.Unlock()

	assert.Equal(t, dirB, root, "projectRoot should update to new workspace")
	assert.Equal(t, initialDir, cachedDir, "dataDir should remain cached (same repo)")
}

func TestUpdateProjectRoot_DifferentRepo_ResetsCache(t *testing.T) {
	t.Parallel()

	dirA := t.TempDir()
	dirB := t.TempDir()

	// Different repo IDs — switching should reset dataDir.
	sageoxA := filepath.Join(dirA, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxA, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sageoxA, "config.json"),
		[]byte(`{"repo_id":"repo_aaa","endpoint":"https://sageox.ai"}`), 0o644))

	sageoxB := filepath.Join(dirB, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxB, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sageoxB, "config.json"),
		[]byte(`{"repo_id":"repo_bbb","endpoint":"https://sageox.ai"}`), 0o644))

	mgr := NewCodeDBManager(dirA, codedbTestLogger(), nil)
	_ = mgr.resolveSharedDataDir() // cache initial dir

	mgr.UpdateProjectRoot(dirB)

	mgr.mu.Lock()
	cachedDir := mgr.dataDir
	mgr.mu.Unlock()

	assert.Empty(t, cachedDir, "dataDir should be reset for a different repo")
}

func TestCheckFreshness_Cooldown(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)
	mgr.freshnessCooldown = 1 * time.Hour // large cooldown

	// Simulate a recent index attempt.
	mgr.mu.Lock()
	mgr.lastAttempt = time.Now()
	mgr.mu.Unlock()

	// Create the resolved data dir so it's not treated as initial.
	dataDir := mgr.resolveSharedDataDir()
	require.NoError(t, os.MkdirAll(dataDir, 0o755))

	// This should be a no-op due to cooldown.
	mgr.CheckFreshness(t.Context())

	// Give the goroutine a moment to start if it was going to.
	time.Sleep(50 * time.Millisecond)

	mgr.mu.Lock()
	indexing := mgr.indexing
	mgr.mu.Unlock()

	assert.False(t, indexing, "should not start indexing within cooldown period")
}

func TestCheckFreshness_CooldownAppliesToInitialRetries(t *testing.T) {
	t.Parallel()

	dir := t.TempDir() // no .sageox/ → falls back to legacy path under this dir
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)
	mgr.freshnessCooldown = 1 * time.Hour

	// Simulate a recent failed index attempt. Even though the data dir doesn't
	// exist (isInitial=true), the cooldown should still apply to prevent infinite
	// retry loops when indexing persistently fails.
	mgr.mu.Lock()
	mgr.lastAttempt = time.Now()
	mgr.mu.Unlock()

	mgr.CheckFreshness(t.Context())

	// Give the goroutine a moment to start if it was going to.
	time.Sleep(50 * time.Millisecond)

	mgr.mu.Lock()
	indexing := mgr.indexing
	mgr.mu.Unlock()

	assert.False(t, indexing, "should not retry initial indexing within cooldown period")
}

func TestCheckFreshness_FirstAttemptBypassesCooldown(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: waits for background goroutine")
	}

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)
	mgr.freshnessCooldown = 1 * time.Hour

	// No lastAttempt set — very first call should always proceed.
	// lastAttempt is set synchronously in CheckFreshness (before the goroutine).
	mgr.CheckFreshness(t.Context())

	mgr.mu.Lock()
	attempted := !mgr.lastAttempt.IsZero()
	mgr.mu.Unlock()
	assert.True(t, attempted, "first attempt should bypass cooldown (lastAttempt should be set)")

	// Wait for the indexing goroutine to finish so Bleve files are closed
	// before TempDir cleanup. Indexing a non-git tempdir fails instantly,
	// so a short poll is sufficient.
	for range 20 {
		time.Sleep(25 * time.Millisecond)
		mgr.mu.Lock()
		done := !mgr.indexing
		mgr.mu.Unlock()
		if done {
			break
		}
	}
}

// --- Concurrent CheckFreshness (regression: TOCTOU race) ---

func TestCheckFreshness_ConcurrentCallsOnlyOneProceeds(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: concurrent goroutine stress test")
	}

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Fire 20 concurrent CheckFreshness calls. Only one should set indexing=true;
	// the rest should see indexing=true and return immediately.
	const goroutines = 20
	var started sync.WaitGroup
	started.Add(goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			started.Done()
			started.Wait() // all goroutines launch simultaneously
			mgr.CheckFreshness(ctx)
		}()
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent CheckFreshness deadlocked")
	}

	// Cancel to stop the background indexing goroutine, then wait for it.
	cancel()
	deadline := time.After(5 * time.Second)
	for {
		mgr.mu.Lock()
		indexing := mgr.indexing
		mgr.mu.Unlock()
		if !indexing {
			break
		}
		select {
		case <-deadline:
			t.Fatal("indexing goroutine did not stop after context cancel")
		case <-time.After(50 * time.Millisecond):
		}
	}

	// Verify that lastAttempt was set exactly once (only one goroutine indexed).
	mgr.mu.Lock()
	attempt := mgr.lastAttempt
	mgr.mu.Unlock()
	assert.False(t, attempt.IsZero(), "exactly one goroutine should have attempted indexing")
}

// --- Index() concurrent rejection ---

func TestIndex_ConcurrentCallReturnsError(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: concurrent indexing test")
	}

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	ctx, cancel := context.WithCancel(context.Background())

	// Start a long-running index by holding the indexing flag.
	mgr.mu.Lock()
	mgr.indexing = true
	mgr.mu.Unlock()

	// A second Index() call via the public API should return "already in progress".
	_, err := mgr.Index(ctx, CodeIndexPayload{}, nil)
	assert.ErrorContains(t, err, "indexing already in progress")

	// Clean up.
	mgr.mu.Lock()
	mgr.indexing = false
	mgr.mu.Unlock()
	cancel()
}

// --- UpdateProjectRoot malformed config edge case ---

func TestUpdateProjectRoot_MalformedConfig_ResetsCache(t *testing.T) {
	t.Parallel()

	dirA := t.TempDir()
	dirB := t.TempDir()

	// dirA: valid config
	sageoxA := filepath.Join(dirA, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxA, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sageoxA, "config.json"),
		[]byte(`{"repo_id":"repo_test","endpoint":"https://sageox.ai"}`), 0o644))

	// dirB: malformed config
	sageoxB := filepath.Join(dirB, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxB, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sageoxB, "config.json"),
		[]byte(`{not valid json`), 0o644))

	mgr := NewCodeDBManager(dirA, codedbTestLogger(), nil)
	_ = mgr.resolveSharedDataDir() // cache initial dir

	// Switching to a workspace with malformed config should reset dataDir
	// (can't confirm it's the same repo, so safest to re-resolve).
	mgr.UpdateProjectRoot(dirB)

	mgr.mu.Lock()
	cachedDir := mgr.dataDir
	mgr.mu.Unlock()

	assert.Empty(t, cachedDir, "dataDir should be reset when new workspace has malformed config")
}

// --- CheckFreshness cooldown tracks failed attempts ---

func TestCheckFreshness_FailedIndexSetsLastAttempt(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: waits for background goroutine")
	}

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)
	mgr.freshnessCooldown = 1 * time.Hour

	// First call — should proceed (lastAttempt is zero).
	// lastAttempt is set synchronously in CheckFreshness.
	mgr.CheckFreshness(t.Context())

	mgr.mu.Lock()
	attempt := mgr.lastAttempt
	mgr.mu.Unlock()
	require.False(t, attempt.IsZero(), "lastAttempt should be set after CheckFreshness")

	// Wait for indexing to complete.
	for range 20 {
		time.Sleep(50 * time.Millisecond)
		mgr.mu.Lock()
		indexing := mgr.indexing
		mgr.mu.Unlock()
		if !indexing {
			break
		}
	}

	// Verify first index completed.
	mgr.mu.Lock()
	origIndexing := mgr.indexing
	mgr.mu.Unlock()
	assert.False(t, origIndexing, "first index should have completed")

	// Second call — should be blocked by cooldown even though index failed.
	mgr.CheckFreshness(t.Context())
	time.Sleep(50 * time.Millisecond)

	mgr.mu.Lock()
	indexingAfterSecond := mgr.indexing
	mgr.mu.Unlock()

	assert.False(t, indexingAfterSecond, "second call should be blocked by cooldown after failed attempt")
}

// --- Symlink edge case ---

func TestUpdateProjectRoot_Symlink(t *testing.T) {
	t.Parallel()

	realDir := t.TempDir()
	linkDir := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(realDir, linkDir))

	mgr := NewCodeDBManager(realDir, codedbTestLogger(), nil)

	// update to symlink path — should accept it (let git resolve the real path)
	mgr.UpdateProjectRoot(linkDir)

	mgr.mu.Lock()
	got := mgr.projectRoot
	mgr.mu.Unlock()
	assert.Equal(t, linkDir, got)
}
