package query

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sageox/ox/internal/codedb/index"
	"github.com/sageox/ox/internal/codedb/store"
	"github.com/sageox/ox/internal/ledger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIndexQueryBoundary tests the full data path:
//
//	ledger JSON → IndexGitHubData() → CodeDB → AssembleActivity()
//
// It catches mismatches between how the indexer writes data and how the
// query layer reads it. This is Integration Test 1 from the test plan.
func TestIndexQueryBoundary(t *testing.T) {
	t.Parallel()

	// --- timestamps ---
	now := time.Date(2026, 3, 18, 15, 0, 0, 0, time.UTC)
	yesterday := now.Add(-24 * time.Hour)
	twoDaysAgo := now.Add(-48 * time.Hour)
	since := now.Add(-7 * 24 * time.Hour)

	mergedAt := yesterday
	closedAt := twoDaysAgo

	// --- create temp ledger and write test data ---
	ledgerPath := t.TempDir()

	// PR #10: merged, 2 comments (one review with path+line, one discussion), 2 commits
	pr10 := &ledger.PRFile{
		Number:    10,
		Title:     "Add rate limiting",
		Body:      "Closes #5",
		Author:    "alice",
		State:     "merged",
		CreatedAt: twoDaysAgo,
		UpdatedAt: yesterday,
		MergedAt:  &mergedAt,
		URL:       "https://github.com/org/repo/pull/10",
		Comments: []ledger.PRComment{
			{
				Author:    "reviewer1",
				Body:      "Looks good, but needs caching",
				Path:      "api/handler.go",
				Line:      intPtr(42),
				CreatedAt: twoDaysAgo.Add(time.Hour),
			},
			{
				Author:    "reviewer1",
				Body:      "Approving.",
				CreatedAt: twoDaysAgo.Add(2 * time.Hour),
			},
		},
		Commits: []ledger.PRCommit{
			{SHA: "aaa111", Author: "alice", Date: twoDaysAgo, Msg: "feat: add rate limiter"},
			{SHA: "bbb222", Author: "alice", Date: yesterday, Msg: "fix: rate limit edge case"},
		},
	}

	// PR #11: open, 1 discussion comment, 0 commits
	pr11 := &ledger.PRFile{
		Number:    11,
		Title:     "WIP: Add caching",
		Body:      "Work in progress",
		Author:    "bob",
		State:     "open",
		CreatedAt: twoDaysAgo,
		UpdatedAt: yesterday,
		URL:       "https://github.com/org/repo/pull/11",
		Comments: []ledger.PRComment{
			{
				Author:    "bob",
				Body:      "Still working on this",
				CreatedAt: yesterday,
			},
		},
	}

	// PR #12: closed (not merged), 2 discussion comments
	pr12 := &ledger.PRFile{
		Number:    12,
		Title:     "Experiment with Redis",
		Body:      "Trying Redis for caching",
		Author:    "carol",
		State:     "closed",
		CreatedAt: twoDaysAgo,
		UpdatedAt: yesterday,
		ClosedAt:  &closedAt,
		URL:       "https://github.com/org/repo/pull/12",
		Comments: []ledger.PRComment{
			{
				Author:    "dave",
				Body:      "Not sure about Redis for this",
				CreatedAt: twoDaysAgo.Add(time.Hour),
			},
			{
				Author:    "carol",
				Body:      "Agreed, closing this",
				CreatedAt: twoDaysAgo.Add(2 * time.Hour),
			},
		},
	}

	// Issue #5: open, 1 comment
	issue5 := &ledger.IssueFile{
		Number:    5,
		Title:     "Need rate limiting",
		Body:      "Users are hammering the API",
		Author:    "dave",
		State:     "open",
		CreatedAt: twoDaysAgo,
		UpdatedAt: yesterday,
		URL:       "https://github.com/org/repo/issues/5",
		Comments: []ledger.IssueComment{
			{
				Author:    "eve",
				Body:      "This is urgent",
				CreatedAt: twoDaysAgo.Add(time.Hour),
			},
		},
	}

	// Issue #8: open, 0 comments
	issue8 := &ledger.IssueFile{
		Number:    8,
		Title:     "Improve error messages",
		Body:      "Error messages are unclear",
		Author:    "eve",
		State:     "open",
		CreatedAt: yesterday,
		UpdatedAt: yesterday,
		URL:       "https://github.com/org/repo/issues/8",
	}

	// Write all ledger files
	require.NoError(t, ledger.WriteGitHubPR(ledgerPath, pr10))
	require.NoError(t, ledger.WriteGitHubPR(ledgerPath, pr11))
	require.NoError(t, ledger.WriteGitHubPR(ledgerPath, pr12))
	require.NoError(t, ledger.WriteGitHubIssue(ledgerPath, issue5))
	require.NoError(t, ledger.WriteGitHubIssue(ledgerPath, issue8))

	// --- open store and run IndexGitHubData ---
	s, err := store.Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })

	stats, err := index.IndexGitHubData(context.Background(), s, ledgerPath, nil)
	require.NoError(t, err)
	assert.Equal(t, 3, stats.PRsIndexed, "should index 3 PRs")
	assert.Equal(t, 2, stats.IssuesIndexed, "should index 2 issues")

	// --- CRITICAL: manually INSERT commit metadata for "aaa111" ---
	// IndexGitHubData only populates pr_commits(pr_id, sha).
	// Commit metadata comes from git history indexing (separate process).
	// Insert for aaa111 (full metadata path), leave bbb222 unmatched (degradation).
	commitTs := twoDaysAgo.Unix()
	insertCommit(t, s, "aaa111", "sarah", "feat: add rate limiter", commitTs)

	// --- run AssembleActivity ---
	result, err := AssembleActivity(context.Background(), s, since, now)
	require.NoError(t, err)

	// --- assertions ---

	// 3 PR clusters
	require.Len(t, result.PRClusters, 3, "expected 3 PR clusters")

	// 2 standalone issues
	require.Len(t, result.StandaloneIssues, 2, "expected 2 standalone issues")

	// PRs are ordered DESC by activity, find each by number
	prByNumber := make(map[int]PRCluster)
	for _, pr := range result.PRClusters {
		prByNumber[pr.Number] = pr
	}

	// --- PR #10 assertions ---
	pr10result, ok := prByNumber[10]
	require.True(t, ok, "PR #10 should be in results")

	assert.Equal(t, "Add rate limiting", pr10result.Title)
	assert.Equal(t, "Closes #5", pr10result.Description, "should use Description (body)")
	assert.Equal(t, "alice", pr10result.Author)
	assert.Equal(t, "merged", pr10result.Status, "should use Status (state)")
	require.NotNil(t, pr10result.MergedAt)
	assert.NotEmpty(t, *pr10result.MergedAt)
	assert.Equal(t, "https://github.com/org/repo/pull/10", pr10result.URL)

	// Reviews: 1 ReviewGroup (reviewer1 has one comment with path)
	require.Len(t, pr10result.Reviews, 1, "should have 1 review group")
	assert.Equal(t, "reviewer1", pr10result.Reviews[0].Reviewer)
	require.Len(t, pr10result.Reviews[0].Comments, 1, "reviewer1 should have 1 review comment")
	assert.Equal(t, "Looks good, but needs caching", pr10result.Reviews[0].Comments[0].Body)
	require.NotNil(t, pr10result.Reviews[0].Comments[0].Path)
	assert.Equal(t, "api/handler.go", *pr10result.Reviews[0].Comments[0].Path)
	require.NotNil(t, pr10result.Reviews[0].Comments[0].Line)
	assert.Equal(t, 42, *pr10result.Reviews[0].Comments[0].Line)

	// Discussion comments: 1 entry (reviewer1's "Approving." has no path)
	require.Len(t, pr10result.DiscussionComments, 1, "should have 1 discussion comment")
	assert.Equal(t, "reviewer1", pr10result.DiscussionComments[0].Author)
	assert.Equal(t, "Approving.", pr10result.DiscussionComments[0].Body)

	// Commits: 2 entries — aaa111 full metadata, bbb222 degraded
	require.Len(t, pr10result.Commits, 2, "should have 2 commits")

	commitBySHA := make(map[string]CommitEntry)
	for _, c := range pr10result.Commits {
		commitBySHA[c.SHA] = c
	}

	aaa := commitBySHA["aaa111"]
	assert.Equal(t, "aaa111", aaa.SHA)
	require.NotNil(t, aaa.Author, "aaa111 should have full metadata")
	assert.Equal(t, "sarah", *aaa.Author)
	require.NotNil(t, aaa.Message)
	assert.Equal(t, "feat: add rate limiter", *aaa.Message)
	require.NotNil(t, aaa.Timestamp)

	bbb := commitBySHA["bbb222"]
	assert.Equal(t, "bbb222", bbb.SHA)
	assert.Nil(t, bbb.Author, "bbb222 should have degraded metadata (no commits row)")
	assert.Nil(t, bbb.Message, "bbb222 message should be nil")
	assert.Nil(t, bbb.Timestamp, "bbb222 timestamp should be nil")

	// --- PR #11 assertions ---
	pr11result, ok := prByNumber[11]
	require.True(t, ok, "PR #11 should be in results")

	assert.Equal(t, "open", pr11result.Status)
	assert.Empty(t, pr11result.Commits, "PR #11 should have no commits")
	assert.Len(t, pr11result.Commits, 0)
	require.Len(t, pr11result.DiscussionComments, 1, "PR #11 should have 1 discussion comment")
	assert.Equal(t, "bob", pr11result.DiscussionComments[0].Author)
	assert.Empty(t, pr11result.Reviews, "PR #11 should have no reviews")

	// --- PR #12 assertions ---
	pr12result, ok := prByNumber[12]
	require.True(t, ok, "PR #12 should be in results")

	assert.Equal(t, "closed", pr12result.Status)
	require.Len(t, pr12result.DiscussionComments, 2, "PR #12 should have 2 discussion comments")

	// --- Issue #5 assertions ---
	issueByNumber := make(map[int]StandaloneIssue)
	for _, issue := range result.StandaloneIssues {
		issueByNumber[issue.Number] = issue
	}

	issue5result, ok := issueByNumber[5]
	require.True(t, ok, "Issue #5 should be in results")

	assert.Equal(t, "Need rate limiting", issue5result.Title)
	assert.Equal(t, "Users are hammering the API", issue5result.Body)
	assert.Equal(t, "dave", issue5result.Author)
	assert.Equal(t, "open", issue5result.State)
	require.Len(t, issue5result.Comments, 1, "Issue #5 should have 1 comment")
	assert.Equal(t, "eve", issue5result.Comments[0].Author)
	assert.Equal(t, "This is urgent", issue5result.Comments[0].Body)

	// --- Issue #8 assertions ---
	issue8result, ok := issueByNumber[8]
	require.True(t, ok, "Issue #8 should be in results")

	assert.Empty(t, issue8result.Comments, "Issue #8 should have empty comments")
	assert.Len(t, issue8result.Comments, 0, "Issue #8 comments should serialize as []")

	// --- Metadata assertions ---
	assert.Equal(t, 3, result.Metadata.PRCount)
	assert.Equal(t, 2, result.Metadata.IssueCount)

	// --- Verify no forbidden keys via JSON round-trip ---
	data, err := result.MarshalEventClusters()
	require.NoError(t, err)

	var elements []map[string]any
	require.NoError(t, json.Unmarshal(data, &elements))

	for _, elem := range elements {
		_, hasRelated := elem["related_issue"]
		assert.False(t, hasRelated, "should not have related_issue key")
		_, hasFiles := elem["files_changed"]
		assert.False(t, hasFiles, "should not have files_changed key")
	}
}
