package ledger

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// mockFetcher implements GitHubFetcher for testing.
type mockFetcher struct {
	prs       []FetchedPR
	issues    []FetchedIssue
	comments  map[int][]FetchedComment  // keyed by issue/PR number
	prCommits map[int][]FetchedPRCommit // keyed by PR number
	prRL      *FetchRateLimit
	issueRL   *FetchRateLimit
	err       error
}

func (m *mockFetcher) ListPullRequests(_ context.Context, _, _ string, _ ListPRsOptions) ([]FetchedPR, *FetchRateLimit, error) {
	return m.prs, m.prRL, m.err
}

func (m *mockFetcher) ListIssues(_ context.Context, _, _ string, _ ListIssuesOptions) ([]FetchedIssue, *FetchRateLimit, error) {
	return m.issues, m.issueRL, m.err
}

func (m *mockFetcher) ListPRComments(_ context.Context, _, _ string, number int) ([]FetchedComment, error) {
	return m.comments[number], m.err
}

func (m *mockFetcher) ListIssueComments(_ context.Context, _, _ string, number int) ([]FetchedComment, error) {
	return m.comments[number], m.err
}

func (m *mockFetcher) ListPRCommits(_ context.Context, _, _ string, number int) ([]FetchedPRCommit, error) {
	if m.prCommits != nil {
		return m.prCommits[number], m.err
	}
	return nil, m.err
}

func TestSyncPRs_NewPRs(t *testing.T) {
	ledgerPath := t.TempDir()
	logger := slog.Default()

	now := time.Now().UTC().Truncate(time.Second)
	fetcher := &mockFetcher{
		prs: []FetchedPR{
			{
				Number:    100,
				Title:     "Add feature X",
				Body:      "description",
				State:     "open",
				Author:    "alice",
				Labels:    []string{"enhancement"},
				CreatedAt: now,
				UpdatedAt: now,
				HTMLURL:   "https://github.com/org/repo/pull/100",
			},
			{
				Number:    101,
				Title:     "Fix bug Y",
				State:     "merged",
				Author:    "bob",
				CreatedAt: now,
				UpdatedAt: now,
				MergedAt:  &now,
				MergeSHA:  "abc123",
				HTMLURL:   "https://github.com/org/repo/pull/101",
			},
		},
		comments: map[int][]FetchedComment{
			100: {{Author: "carol", Body: "LGTM", CreatedAt: now}},
			101: {{Author: "dave", Body: "Looks good", Path: "main.go", CreatedAt: now}},
		},
	}

	result, err := SyncPRs(context.Background(), fetcher, ledgerPath, "org", "repo", 30, logger)
	if err != nil {
		t.Fatalf("SyncPRs: %v", err)
	}

	if result.PRTotal != 2 {
		t.Errorf("expected 2 PRs, got %d", result.PRTotal)
	}
	if result.PRCreated != 2 {
		t.Errorf("expected 2 created, got %d", result.PRCreated)
	}
	if result.PRUpdated != 0 {
		t.Errorf("expected 0 updated, got %d", result.PRUpdated)
	}

	// verify files were written
	files, err := ListGitHubDataFiles(ledgerPath, "pr")
	if err != nil {
		t.Fatalf("ListGitHubDataFiles: %v", err)
	}
	if len(files) != 2 {
		t.Errorf("expected 2 PR files, got %d", len(files))
	}

	// verify sync state was persisted
	state, err := ReadGitHubTypeSyncState(ledgerPath, "pr")
	if err != nil {
		t.Fatalf("ReadGitHubTypeSyncState: %v", err)
	}
	if state.Count != 2 {
		t.Errorf("expected count 2, got %d", state.Count)
	}
	if len(state.KnownStates) != 2 {
		t.Errorf("expected 2 known states, got %d", len(state.KnownStates))
	}
	if state.KnownStates[100] != "open" {
		t.Errorf("expected PR 100 state 'open', got %q", state.KnownStates[100])
	}
	if state.KnownStates[101] != "merged" {
		t.Errorf("expected PR 101 state 'merged', got %q", state.KnownStates[101])
	}
}

func TestSyncPRs_IncrementalUpdate(t *testing.T) {
	ledgerPath := t.TempDir()
	logger := slog.Default()

	now := time.Now().UTC().Truncate(time.Second)

	// first sync: create PR 100 as open
	fetcher := &mockFetcher{
		prs: []FetchedPR{{
			Number:    100,
			Title:     "Feature",
			State:     "open",
			Author:    "alice",
			CreatedAt: now,
			UpdatedAt: now,
		}},
		comments: map[int][]FetchedComment{},
	}

	result1, err := SyncPRs(context.Background(), fetcher, ledgerPath, "org", "repo", 30, logger)
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if result1.PRCreated != 1 {
		t.Errorf("first sync: expected 1 created, got %d", result1.PRCreated)
	}

	// second sync: PR 100 now merged (state transition)
	merged := now.Add(time.Hour)
	fetcher2 := &mockFetcher{
		prs: []FetchedPR{{
			Number:    100,
			Title:     "Feature",
			State:     "closed",
			Author:    "alice",
			CreatedAt: now,
			UpdatedAt: merged,
			MergedAt:  &merged,
			MergeSHA:  "def456",
		}},
		comments: map[int][]FetchedComment{
			100: {{Author: "bob", Body: "merged!", CreatedAt: merged}},
		},
	}

	result2, err := SyncPRs(context.Background(), fetcher2, ledgerPath, "org", "repo", 30, logger)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if result2.PRUpdated != 1 {
		t.Errorf("second sync: expected 1 updated, got %d", result2.PRUpdated)
	}
	if result2.PRCreated != 0 {
		t.Errorf("second sync: expected 0 created, got %d", result2.PRCreated)
	}

	// verify state was updated
	state, err := ReadGitHubTypeSyncState(ledgerPath, "pr")
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state.KnownStates[100] != "merged" {
		t.Errorf("expected state 'merged', got %q", state.KnownStates[100])
	}
}

func TestSyncIssues_NewIssues(t *testing.T) {
	ledgerPath := t.TempDir()
	logger := slog.Default()

	now := time.Now().UTC().Truncate(time.Second)
	fetcher := &mockFetcher{
		issues: []FetchedIssue{
			{
				Number:    50,
				Title:     "Bug report",
				Body:      "something is broken",
				State:     "open",
				Author:    "alice",
				Labels:    []string{"bug"},
				CreatedAt: now,
				UpdatedAt: now,
				HTMLURL:   "https://github.com/org/repo/issues/50",
			},
		},
		comments: map[int][]FetchedComment{
			50: {{Author: "bob", Body: "confirmed", CreatedAt: now}},
		},
	}

	result, err := SyncIssues(context.Background(), fetcher, ledgerPath, "org", "repo", 30, logger)
	if err != nil {
		t.Fatalf("SyncIssues: %v", err)
	}

	if result.IssueTotal != 1 {
		t.Errorf("expected 1 issue, got %d", result.IssueTotal)
	}
	if result.IssueCreated != 1 {
		t.Errorf("expected 1 created, got %d", result.IssueCreated)
	}

	files, err := ListGitHubDataFiles(ledgerPath, "issue")
	if err != nil {
		t.Fatalf("ListGitHubDataFiles: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("expected 1 issue file, got %d", len(files))
	}
}

func TestSyncPRs_MergedPRGetsCommits(t *testing.T) {
	ledgerPath := t.TempDir()
	logger := slog.Default()

	now := time.Now().UTC().Truncate(time.Second)
	fetcher := &mockFetcher{
		prs: []FetchedPR{
			{
				Number:    200,
				Title:     "Merged PR with commits",
				State:     "closed",
				Author:    "alice",
				CreatedAt: now,
				UpdatedAt: now,
				MergedAt:  &now,
				MergeSHA:  "merge123",
				HTMLURL:   "https://github.com/org/repo/pull/200",
			},
		},
		comments: map[int][]FetchedComment{},
		prCommits: map[int][]FetchedPRCommit{
			200: {
				{SHA: "aaa111", Author: "alice", Date: now.Add(-2 * time.Hour), Msg: "first commit"},
				{SHA: "bbb222", Author: "alice", Date: now.Add(-1 * time.Hour), Msg: "second commit"},
			},
		},
	}

	result, err := SyncPRs(context.Background(), fetcher, ledgerPath, "org", "repo", 30, logger)
	if err != nil {
		t.Fatalf("SyncPRs: %v", err)
	}

	if result.PRCreated != 1 {
		t.Errorf("expected 1 created, got %d", result.PRCreated)
	}

	// verify the PR file contains commits
	files, err := ListGitHubDataFiles(ledgerPath, "pr")
	if err != nil {
		t.Fatalf("ListGitHubDataFiles: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 PR file, got %d", len(files))
	}

	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read PR file: %v", err)
	}

	var pr PRFile
	if err := json.Unmarshal(data, &pr); err != nil {
		t.Fatalf("unmarshal PR: %v", err)
	}

	if len(pr.Commits) != 2 {
		t.Errorf("expected 2 commits, got %d", len(pr.Commits))
	}
	if pr.Commits[0].SHA != "aaa111" {
		t.Errorf("expected first commit SHA 'aaa111', got %q", pr.Commits[0].SHA)
	}
	if pr.Commits[1].Msg != "second commit" {
		t.Errorf("expected second commit message 'second commit', got %q", pr.Commits[1].Msg)
	}
}

func TestSyncPRs_OpenPRDoesNotGetCommits(t *testing.T) {
	ledgerPath := t.TempDir()
	logger := slog.Default()

	now := time.Now().UTC().Truncate(time.Second)
	fetcher := &mockFetcher{
		prs: []FetchedPR{
			{
				Number:    300,
				Title:     "Open PR",
				State:     "open",
				Author:    "bob",
				CreatedAt: now,
				UpdatedAt: now,
			},
		},
		comments: map[int][]FetchedComment{},
		prCommits: map[int][]FetchedPRCommit{
			300: {{SHA: "ccc333", Author: "bob", Date: now, Msg: "wip"}},
		},
	}

	_, err := SyncPRs(context.Background(), fetcher, ledgerPath, "org", "repo", 30, logger)
	if err != nil {
		t.Fatalf("SyncPRs: %v", err)
	}

	files, _ := ListGitHubDataFiles(ledgerPath, "pr")
	data, _ := os.ReadFile(files[0])
	var pr PRFile
	json.Unmarshal(data, &pr)

	if len(pr.Commits) != 0 {
		t.Errorf("open PR should have 0 commits, got %d", len(pr.Commits))
	}
}

func TestSyncPRs_StateTransitionToMergedGetsCommits(t *testing.T) {
	ledgerPath := t.TempDir()
	logger := slog.Default()

	now := time.Now().UTC().Truncate(time.Second)

	// first sync: PR is open
	fetcher1 := &mockFetcher{
		prs: []FetchedPR{{
			Number: 400, Title: "Feature", State: "open", Author: "alice",
			CreatedAt: now, UpdatedAt: now,
		}},
		comments:  map[int][]FetchedComment{},
		prCommits: map[int][]FetchedPRCommit{},
	}
	_, err := SyncPRs(context.Background(), fetcher1, ledgerPath, "org", "repo", 30, logger)
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}

	// second sync: PR merged (state transition)
	merged := now.Add(time.Hour)
	fetcher2 := &mockFetcher{
		prs: []FetchedPR{{
			Number: 400, Title: "Feature", State: "closed", Author: "alice",
			CreatedAt: now, UpdatedAt: merged, MergedAt: &merged, MergeSHA: "xyz789",
		}},
		comments: map[int][]FetchedComment{},
		prCommits: map[int][]FetchedPRCommit{
			400: {{SHA: "ddd444", Author: "alice", Date: now, Msg: "the commit"}},
		},
	}
	_, err = SyncPRs(context.Background(), fetcher2, ledgerPath, "org", "repo", 30, logger)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}

	// verify commits were fetched on state transition
	files, _ := ListGitHubDataFiles(ledgerPath, "pr")
	data, _ := os.ReadFile(files[0])
	var pr PRFile
	json.Unmarshal(data, &pr)

	if len(pr.Commits) != 1 {
		t.Errorf("expected 1 commit after merge transition, got %d", len(pr.Commits))
	}
}

func TestBackfillPRCommits(t *testing.T) {
	ledgerPath := t.TempDir()
	logger := slog.Default()

	now := time.Now().UTC().Truncate(time.Second)

	// write a merged PR without commits (simulating pre-feature data)
	pr := &PRFile{
		Number:      500,
		Title:       "Old merged PR",
		State:       "merged",
		Author:      "alice",
		CreatedAt:   now,
		UpdatedAt:   now,
		MergedAt:    &now,
		MergeCommit: "old123",
	}
	if err := WriteGitHubPR(ledgerPath, pr); err != nil {
		t.Fatalf("write PR: %v", err)
	}

	// also write an open PR (should not be backfilled)
	openPR := &PRFile{
		Number:    501,
		Title:     "Open PR",
		State:     "open",
		Author:    "bob",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := WriteGitHubPR(ledgerPath, openPR); err != nil {
		t.Fatalf("write open PR: %v", err)
	}

	fetcher := &mockFetcher{
		prCommits: map[int][]FetchedPRCommit{
			500: {
				{SHA: "eee555", Author: "alice", Date: now, Msg: "backfilled commit"},
			},
		},
	}

	backfilled, err := BackfillPRCommits(context.Background(), fetcher, ledgerPath, "org", "repo", logger)
	if err != nil {
		t.Fatalf("BackfillPRCommits: %v", err)
	}

	if backfilled != 1 {
		t.Errorf("expected 1 backfilled, got %d", backfilled)
	}

	// verify the PR file now has commits
	files, _ := ListGitHubDataFiles(ledgerPath, "pr")
	for _, f := range files {
		data, _ := os.ReadFile(f)
		var p PRFile
		json.Unmarshal(data, &p)
		if p.Number == 500 {
			if len(p.Commits) != 1 {
				t.Errorf("expected 1 commit for PR 500, got %d", len(p.Commits))
			}
			if p.Commits[0].SHA != "eee555" {
				t.Errorf("expected commit SHA 'eee555', got %q", p.Commits[0].SHA)
			}
		}
		if p.Number == 501 {
			if len(p.Commits) != 0 {
				t.Errorf("open PR should not have commits, got %d", len(p.Commits))
			}
		}
	}
}

func TestBackfillPRCommits_AlreadyHasCommits(t *testing.T) {
	ledgerPath := t.TempDir()
	logger := slog.Default()

	now := time.Now().UTC().Truncate(time.Second)

	// write a merged PR that already has commits
	pr := &PRFile{
		Number:      600,
		Title:       "Already has commits",
		State:       "merged",
		Author:      "alice",
		CreatedAt:   now,
		UpdatedAt:   now,
		MergedAt:    &now,
		MergeCommit: "fff666",
		Commits: []PRCommit{
			{SHA: "existing", Author: "alice", Date: now, Msg: "existing"},
		},
	}
	if err := WriteGitHubPR(ledgerPath, pr); err != nil {
		t.Fatalf("write PR: %v", err)
	}

	fetcher := &mockFetcher{
		prCommits: map[int][]FetchedPRCommit{
			600: {{SHA: "new", Author: "alice", Date: now, Msg: "should not replace"}},
		},
	}

	backfilled, err := BackfillPRCommits(context.Background(), fetcher, ledgerPath, "org", "repo", logger)
	if err != nil {
		t.Fatalf("BackfillPRCommits: %v", err)
	}

	if backfilled != 0 {
		t.Errorf("expected 0 backfilled (already has commits), got %d", backfilled)
	}
}

func TestPRFile_BackwardCompat_NoCommitsField(t *testing.T) {
	// Simulate old JSON without commits field
	oldJSON := `{
		"number": 700,
		"title": "Old PR",
		"body": "",
		"author": "alice",
		"state": "merged",
		"created_at": "2026-01-01T00:00:00Z",
		"updated_at": "2026-01-01T00:00:00Z",
		"merge_commit": "ggg777",
		"url": "https://github.com/org/repo/pull/700"
	}`

	var pr PRFile
	if err := json.Unmarshal([]byte(oldJSON), &pr); err != nil {
		t.Fatalf("unmarshal old JSON: %v", err)
	}

	if pr.Number != 700 {
		t.Errorf("expected number 700, got %d", pr.Number)
	}
	if pr.Commits != nil {
		t.Errorf("expected nil commits from old JSON, got %v", pr.Commits)
	}
}

func TestSyncPRs_ContextCancellation(t *testing.T) {
	ledgerPath := t.TempDir()
	logger := slog.Default()

	now := time.Now().UTC().Truncate(time.Second)
	fetcher := &mockFetcher{
		prs: []FetchedPR{
			{Number: 1, State: "open", Author: "a", CreatedAt: now, UpdatedAt: now},
			{Number: 2, State: "open", Author: "b", CreatedAt: now, UpdatedAt: now},
		},
		comments: map[int][]FetchedComment{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := SyncPRs(ctx, fetcher, ledgerPath, "org", "repo", 30, logger)
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
}

func TestCommitAndPushGitHubData_NothingToCommit(t *testing.T) {
	// create a git repo with nothing to stage
	ledgerPath := t.TempDir()
	initGitRepo(t, ledgerPath)

	result := &SyncResult{PRTotal: 0, IssueTotal: 0}
	pushCalled := false
	pushFn := func(_ context.Context, _ string) error {
		pushCalled = true
		return nil
	}

	// with 0 total items, CommitAndPushGitHubData is not called by the caller
	// but let's test the "nothing to commit" path
	err := CommitAndPushGitHubData(context.Background(), ledgerPath, "org", "repo", result, pushFn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// push should not be called since there's nothing to commit
	if pushCalled {
		t.Error("push should not be called when nothing to commit")
	}
}

func TestCommitAndPushGitHubData_WithData(t *testing.T) {
	ledgerPath := t.TempDir()
	initGitRepo(t, ledgerPath)

	// write a PR file
	now := time.Now().UTC().Truncate(time.Second)
	pr := &PRFile{
		Number:    42,
		Title:     "Test PR",
		State:     "open",
		Author:    "test",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := WriteGitHubPR(ledgerPath, pr); err != nil {
		t.Fatalf("WriteGitHubPR: %v", err)
	}

	result := &SyncResult{PRTotal: 1, PRCreated: 1}
	pushCalled := false
	pushFn := func(_ context.Context, _ string) error {
		pushCalled = true
		return nil
	}

	err := CommitAndPushGitHubData(context.Background(), ledgerPath, "org", "repo", result, pushFn)
	if err != nil {
		t.Fatalf("CommitAndPushGitHubData: %v", err)
	}
	if !pushCalled {
		t.Error("push should have been called")
	}
}

// initGitRepo creates a minimal git repo for testing.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	cmds := [][]string{
		{"git", "-C", dir, "init", "--initial-branch=main"},
		{"git", "-C", dir, "config", "user.name", "test"},
		{"git", "-C", dir, "config", "user.email", "test@test.com"},
	}
	// create initial commit
	readmePath := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readmePath, []byte("# test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmds = append(cmds,
		[]string{"git", "-C", dir, "add", "README.md"},
		[]string{"git", "-C", dir, "commit", "-m", "initial"},
	)
	for _, c := range cmds {
		out, err := runCmd(c[0], c[1:]...)
		if err != nil {
			t.Fatalf("%v failed: %s: %v", c, out, err)
		}
	}
}

func runCmd(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
