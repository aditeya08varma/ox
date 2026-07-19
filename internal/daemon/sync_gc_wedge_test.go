package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/ledger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. ledgerSyncWedged detection ---
//
// checkAndRunGC's existing triggers (interval exceeded, full-clone upgrade)
// never catch a wedged ledger on their own — a wedge can persist
// indefinitely without ever exceeding the GC interval. ledgerSyncWedged is
// the missing third trigger. Failure prevented: a ledger stuck ahead+behind
// forever with no automated recovery path, exactly the reported incident.
//
// These use a plain (non-ledger-structured) bare+clone fixture — ledgerSyncWedged
// is pure git plumbing and doesn't care about sessions/ or sparse checkout.

func TestLedgerSyncWedged_FreshUnpushedCommit_NotWedged(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	isolateCredentials(t)
	s := gcTestScheduler(t)

	_, cloneDir := gcInitBareAndClone(t, t.TempDir())
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "new.txt"), []byte("x"), 0o644))
	require.NoError(t, exec.Command("git", "-C", cloneDir, "add", "-A").Run())
	require.NoError(t, exec.Command("git", "-C", cloneDir, "commit", "-m", "unpushed").Run())

	wedged, _, count := s.ledgerSyncWedged(context.Background(), cloneDir)
	assert.False(t, wedged, "a fresh unpushed commit is not a wedge — normal push/pull will resolve it")
	assert.Equal(t, 1, count)
}

func TestLedgerSyncWedged_AheadOnly_NotWedged(t *testing.T) {
	// ahead but never behind: a plain push (gcPushUnpushedCommits) resolves
	// this on its own, regardless of age — must not be treated as wedged.
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	isolateCredentials(t)
	s := gcTestScheduler(t)

	_, cloneDir := gcInitBareAndClone(t, t.TempDir())
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "new.txt"), []byte("x"), 0o644))
	require.NoError(t, exec.Command("git", "-C", cloneDir, "add", "-A").Run())
	require.NoError(t, exec.Command("git", "-C", cloneDir, "commit", "-m", "unpushed").Run())
	backdateCommitTimestamp(t, cloneDir, -4*time.Hour)

	wedged, _, _ := s.ledgerSyncWedged(context.Background(), cloneDir)
	assert.False(t, wedged, "ahead-only, never behind, must not be wedged regardless of age")
}

func TestLedgerSyncWedged_BehindOnly_NotWedged(t *testing.T) {
	// behind but never ahead: an ordinary pull resolves this — must not be
	// treated as wedged.
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	isolateCredentials(t)
	s := gcTestScheduler(t)

	bareDir, cloneDir := gcInitBareAndClone(t, t.TempDir())

	otherDir := t.TempDir()
	require.NoError(t, exec.Command("git", "clone", bareDir, otherDir).Run())
	gitConfig(t, otherDir)
	require.NoError(t, os.WriteFile(filepath.Join(otherDir, "other.txt"), []byte("x"), 0o644))
	require.NoError(t, exec.Command("git", "-C", otherDir, "add", "-A").Run())
	require.NoError(t, exec.Command("git", "-C", otherDir, "commit", "-m", "remote advance").Run())
	require.NoError(t, exec.Command("git", "-C", otherDir, "push", "origin", "HEAD:main").Run())

	wedged, _, _ := s.ledgerSyncWedged(context.Background(), cloneDir)
	assert.False(t, wedged, "behind-only (never ahead) must not be wedged — a plain pull resolves it")
}

func TestLedgerSyncWedged_GenuinelyWedged_DetectsAfterAgeThreshold(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	isolateCredentials(t)
	s := gcTestScheduler(t)

	bareDir, cloneDir := gcInitBareAndClone(t, t.TempDir())
	diverge(t, bareDir, cloneDir, "local.txt", "remote.txt")

	// too young: must not be wedged yet even though ahead+behind
	wedged, age, count := s.ledgerSyncWedged(context.Background(), cloneDir)
	assert.False(t, wedged, "ahead+behind but younger than ledgerSyncWedgeAge must not be wedged yet")
	assert.Equal(t, 1, count)
	assert.Less(t, age, ledgerSyncWedgeAge)

	// backdate the local commit past the threshold
	backdateCommitTimestamp(t, cloneDir, -4*time.Hour)

	wedged, age, count = s.ledgerSyncWedged(context.Background(), cloneDir)
	assert.True(t, wedged, "ahead+behind older than ledgerSyncWedgeAge must be detected as wedged")
	assert.GreaterOrEqual(t, age, ledgerSyncWedgeAge)
	assert.Equal(t, 1, count)
}

func TestLedgerSyncWedged_Offline_NotWedged(t *testing.T) {
	// fetch failure (remote unreachable) must never be mistaken for wedged —
	// offline is a normal, explicitly supported daemon state.
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	isolateCredentials(t)
	s := gcTestScheduler(t)

	bareDir, cloneDir := gcInitBareAndClone(t, t.TempDir())
	diverge(t, bareDir, cloneDir, "local.txt", "remote.txt")
	backdateCommitTimestamp(t, cloneDir, -4*time.Hour)

	// point origin at a nonexistent path so fetch fails
	require.NoError(t, exec.Command("git", "-C", cloneDir, "remote", "set-url", "origin", "/nonexistent/repo.git").Run())

	wedged, _, _ := s.ledgerSyncWedged(context.Background(), cloneDir)
	assert.False(t, wedged, "a fetch failure (offline) must not be classified as wedged")
}

// diverge makes cloneDir simultaneously ahead (one unpushed local commit
// adding localFile) and behind (a second writer pushed remoteFile to
// bareDir after cloneDir last synced) — the shape a wedged sync produces,
// without any content conflict (different files) so capture/restore has a
// clean case to prove works before layering an irreconcilable conflict on.
func diverge(t *testing.T, bareDir, cloneDir, localFile, remoteFile string) {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, localFile), []byte("local content"), 0o644))
	require.NoError(t, exec.Command("git", "-C", cloneDir, "add", "-A").Run())
	require.NoError(t, exec.Command("git", "-C", cloneDir, "commit", "-m", "local: "+localFile).Run())

	remoteWriterDir := t.TempDir()
	require.NoError(t, exec.Command("git", "clone", bareDir, remoteWriterDir).Run())
	gitConfig(t, remoteWriterDir)
	require.NoError(t, os.WriteFile(filepath.Join(remoteWriterDir, remoteFile), []byte("remote content"), 0o644))
	require.NoError(t, exec.Command("git", "-C", remoteWriterDir, "add", "-A").Run())
	require.NoError(t, exec.Command("git", "-C", remoteWriterDir, "commit", "-m", "remote: "+remoteFile).Run())
	require.NoError(t, exec.Command("git", "-C", remoteWriterDir, "push", "origin", "HEAD:main").Run())
}

// backdateCommitTimestamp rewrites HEAD's author+committer date so
// ledgerSyncWedged's %ct-based age computation reads a genuinely old
// commit, without a real-time sleep.
func backdateCommitTimestamp(t *testing.T, repo string, delta time.Duration) {
	t.Helper()
	newDate := time.Now().Add(delta).Format(time.RFC3339)
	cmd := exec.Command("git", "-C", repo, "commit", "--amend", "--no-edit", "--date="+newDate)
	cmd.Env = append(os.Environ(), "GIT_COMMITTER_DATE="+newDate) // safe: git subprocess, not ox
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

// --- B. capture-and-restore on a genuinely diverged ledger ---
//
// checkAndRunGC's existing GC path explicitly skips (gcSkippedDirty) exactly
// the state a wedged ledger produces: unpushed local commits that a plain
// push can't land because the remote diverged. Failure prevented: GC being
// structurally unable to rescue the one scenario it exists for.

func TestGC_CaptureUnpushedOnDiverge_Succeeds(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	isolateCredentials(t)

	bareDir := setupLedgerBareRepo(t)
	cloneURL := "file://" + bareDir
	projectDir := setupProjectWithConfig(t, "")
	s := newTestScheduler(projectDir)

	ledgerDir := filepath.Join(t.TempDir(), "ledger")
	require.NoError(t, ledger.CloneWithSparseCheckout(ledgerDir, cloneURL))
	gitConfig(t, ledgerDir)

	diverge(t, bareDir, ledgerDir, filepath.Join("sessions", "local.txt"), filepath.Join("sessions", "remote.txt"))

	ws := WorkspaceState{
		ID:       "ledger",
		Type:     WorkspaceTypeLedger,
		Path:     ledgerDir,
		CloneURL: cloneURL,
		Exists:   true,
	}
	registry := s.WorkspaceRegistry()
	registry.mu.Lock()
	registry.ledger = &ws
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	// sanity: an ordinary (non-diverge-aware) reclone must still skip,
	// proving this fixture genuinely reproduces today's bug before
	// asserting the fix. The failed push mutates nothing locally, so
	// ledgerDir is safe to reuse for the real assertion below.
	plainResult := s.runBlueGreenGC(context.Background(), ws)
	require.Equal(t, gcSkippedDirty, plainResult, "sanity check: an ordinary reclone must skip on a diverged push (this is the bug being fixed)")

	result := s.runBlueGreenGCOpts(context.Background(), ws, true)
	require.Equal(t, gcSuccess, result, "diverge-aware reclone must succeed instead of skipping")

	// the remote's content must be present (reclone actually happened)
	assert.FileExists(t, filepath.Join(ledgerDir, "sessions", "remote.txt"))

	// the local unpushed commit's content must be recovered...
	assert.FileExists(t, filepath.Join(ledgerDir, "sessions", "local.txt"))

	// ...but as UNCOMMITTED working-tree changes — the daemon never commits
	// (.claude/rules/daemon-git.md). git status must show it, not git log.
	statusOut, err := exec.Command("git", "-C", ledgerDir, "status", "--porcelain").CombinedOutput()
	require.NoError(t, err, string(statusOut))
	assert.Contains(t, string(statusOut), "local.txt", "recovered content must land as an uncommitted change, not a daemon-authored commit")
}
