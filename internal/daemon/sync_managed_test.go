package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/ledger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. sessions/*/meta.json conflict classification ---
//
// sessions/ is hard-denied from auto-resolve by design (see
// internal/manifest/auto_resolve.go), so a genuine content conflict there
// can never be tier-1/2/3 resolved — pullManagedRepo must classify it as
// IssueTypeSessionConflictWedge, not the generic IssueTypeDiverged ordinary
// lag also produces. Without this discrimination, sync.go has no way to
// escalate severity for a truly stuck conflict versus a repo that's simply
// behind and will resolve on its own next cycle.

// makeSessionMetaConflictClone builds a bare remote plus a local clone where
// both sides modified the same sessions/<id>/meta.json content differently:
// the local clone has an unpushed commit, and the remote has since advanced
// past it on the same file. Running `git pull --rebase` against this clone
// reproduces the exact conflict shape the bug report described. Returns the
// local clone path.
func makeSessionMetaConflictClone(t *testing.T) string {
	t.Helper()
	isolateCredentials(t)

	bareDir := filepath.Join(t.TempDir(), "origin.git")
	out, err := runGitOut(t, t.TempDir(), "init", "--bare", "--initial-branch=main", bareDir)
	require.NoError(t, err, out)

	writeMeta := func(dir, content string) {
		metaPath := filepath.Join(dir, "sessions", "2026-07-19T00-00-test-Ox0001", "meta.json")
		require.NoError(t, os.MkdirAll(filepath.Dir(metaPath), 0o755))
		require.NoError(t, os.WriteFile(metaPath, []byte(content), 0o644))
	}
	commitAndPush := func(dir, msg string) {
		out, err := runGitOut(t, dir, "add", "-A")
		require.NoError(t, err, out)
		out, err = runGitOut(t, dir, "commit", "-m", msg)
		require.NoError(t, err, out)
		out, err = runGitOut(t, dir, "push", "origin", "HEAD:main")
		require.NoError(t, err, out)
	}

	// seed the remote with an initial version of the session's meta.json
	seedDir := t.TempDir()
	out, err = runGitOut(t, t.TempDir(), "clone", bareDir, seedDir)
	require.NoError(t, err, out)
	writeMeta(seedDir, `{"stop_reason":"initial"}`)
	commitAndPush(seedDir, "seed session")

	// local clone under test: modifies meta.json, commits, but never pushes
	localDir := t.TempDir()
	out, err = runGitOut(t, t.TempDir(), "clone", bareDir, localDir)
	require.NoError(t, err, out)
	writeMeta(localDir, `{"stop_reason":"local-in-flight"}`)
	out, err = runGitOut(t, localDir, "add", "-A")
	require.NoError(t, err, out)
	out, err = runGitOut(t, localDir, "commit", "-m", "local: finalize session")
	require.NoError(t, err, out)

	// a second writer advances the remote on the same file, so localDir is
	// simultaneously ahead (its own unpushed commit) and behind (remote
	// moved) on sessions/2026-07-19T00-00-test-Ox0001/meta.json.
	remoteWriterDir := t.TempDir()
	out, err = runGitOut(t, t.TempDir(), "clone", bareDir, remoteWriterDir)
	require.NoError(t, err, out)
	writeMeta(remoteWriterDir, `{"stop_reason":"remote-writer"}`)
	commitAndPush(remoteWriterDir, "remote: finalize session")

	return localDir
}

func TestPullManagedRepo_SessionMetaConflict_ClassifiesAsSessionConflictWedge(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	localDir := makeSessionMetaConflictClone(t)

	s := newTestScheduler(t.TempDir())
	result := s.pullManagedRepo(context.Background(), ManagedRepoPullOpts{
		RepoPath:     localDir,
		RepoName:     "ledger",
		ResolveRules: ledger.DefaultResolveRules, // real production rules: sessions/ stays hard-denied
		Logger:       discardLogger(),
	})

	require.NotNil(t, result.Err, "the conflicting rebase must surface as a pull error, not a silent success")
	require.NotNil(t, result.Issue, "a sessions/ content conflict must produce a classified issue")
	assert.Equal(t, IssueTypeSessionConflictWedge, result.Issue.Type,
		"must be classified distinctly from generic IssueTypeDiverged so sync.go can escalate it by elapsed time")
	assert.Equal(t, "ledger", result.Issue.Repo)
	assert.False(t, result.AutoResolved, "sessions/ must never auto-resolve — it's hard-denied by design")

	// the rebase must actually be cleared (AuditAndAbort ran), not left wedged
	// mid-rebase — that's a separate, already-covered failure mode
	// (sync_rebase_recovery_test.go); this test only proves classification.
	assert.NoDirExists(t, filepath.Join(localDir, ".git", "rebase-merge"),
		"AuditAndAbort should have cleared the rebase-merge dir")
}

// --- B. Severity escalation by elapsed time ---
//
// escalateSessionConflictSeverity must escalate purely as a function of how
// long the (Type, Repo) issue has existed, using IssueTracker.SetIssue's
// existing Since-preservation — not a separate timer that resets on daemon
// restart (internal/daemon/workspace_registry.go's in-memory SyncFailures
// counter has exactly that fragility, which is why this doesn't reuse it).
// Failure prevented: a genuinely stuck conflict retrying forever at
// SeverityWarning, indistinguishable from a conflict that just started.

func TestEscalateSessionConflictSeverity_EscalatesByElapsedTime(t *testing.T) {
	cases := []struct {
		name         string
		age          time.Duration
		wantSeverity string
		wantConfirm  bool
	}{
		{"fresh_no_prior_issue", 0, SeverityWarning, false},
		{"under_6h", 5 * time.Hour, SeverityWarning, false},
		{"exactly_6h", 6 * time.Hour, SeverityError, true},
		{"under_24h", 23 * time.Hour, SeverityError, true},
		{"exactly_24h", 24 * time.Hour, SeverityCritical, true},
		{"well_past_24h", 72 * time.Hour, SeverityCritical, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tracker := NewIssueTracker()
			if tc.age > 0 {
				tracker.SetIssue(DaemonIssue{
					Type:     IssueTypeSessionConflictWedge,
					Repo:     "ledger",
					Severity: SeverityWarning,
					Summary:  "seed",
					Since:    time.Now().Add(-tc.age),
				})
			}

			issue := DaemonIssue{
				Type:    IssueTypeSessionConflictWedge,
				Repo:    "ledger",
				Summary: "1 session(s) have unresolvable meta.json conflicts",
			}
			escalateSessionConflictSeverity(tracker, &issue)

			assert.Equal(t, tc.wantSeverity, issue.Severity)
			assert.Equal(t, tc.wantConfirm, issue.RequiresConfirm)
		})
	}
}

// TestEscalateSessionConflictSeverity_PreservesSinceAcrossCycles proves the
// full loop a real sync cycle drives: classify, escalate, SetIssue, repeat —
// Since must anchor to the FIRST detection, not reset on every retry (which
// would keep a permanently-stuck conflict stuck at SeverityWarning forever).
func TestEscalateSessionConflictSeverity_PreservesSinceAcrossCycles(t *testing.T) {
	tracker := NewIssueTracker()

	firstIssue := DaemonIssue{Type: IssueTypeSessionConflictWedge, Repo: "ledger", Summary: "1 session(s) have unresolvable meta.json conflicts"}
	escalateSessionConflictSeverity(tracker, &firstIssue)
	tracker.SetIssue(firstIssue)

	firstSeen, ok := tracker.GetIssue(IssueTypeSessionConflictWedge, "ledger")
	require.True(t, ok)
	assert.Equal(t, SeverityWarning, firstSeen.Severity)

	// simulate the issue having existed for 7 hours by backdating Since directly
	tracker.SetIssue(DaemonIssue{
		Type:     IssueTypeSessionConflictWedge,
		Repo:     "ledger",
		Severity: firstSeen.Severity,
		Summary:  firstSeen.Summary,
		Since:    firstSeen.Since.Add(-7 * time.Hour),
	})

	// next sync cycle re-detects the identical, still-unresolved conflict
	secondIssue := DaemonIssue{Type: IssueTypeSessionConflictWedge, Repo: "ledger", Summary: "1 session(s) have unresolvable meta.json conflicts"}
	escalateSessionConflictSeverity(tracker, &secondIssue)
	tracker.SetIssue(secondIssue)

	assert.Equal(t, SeverityError, secondIssue.Severity, "7h-old conflict must have escalated past the 6h threshold")
	assert.True(t, secondIssue.RequiresConfirm)

	final, ok := tracker.GetIssue(IssueTypeSessionConflictWedge, "ledger")
	require.True(t, ok)
	assert.Equal(t, SeverityError, final.Severity, "escalated severity must actually persist in the tracker, not just the local variable")
}
