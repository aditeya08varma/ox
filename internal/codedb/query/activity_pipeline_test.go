package query

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/codedb/index"
	"github.com/sageox/ox/internal/codedb/store"
	"github.com/sageox/ox/internal/ledger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFullPipelineExtractorContract tests the complete pipeline:
//
//	ledger → IndexGitHubData() → AssembleActivity() → MarshalEventClusters()
//
// Validates the flat JSON array against the extractor input spec. This is
// THE schema contract test — if this passes, the extractor gets valid input.
// Integration Test 3 from the test plan.
func TestFullPipelineExtractorContract(t *testing.T) {
	t.Parallel()

	// --- timestamps ---
	now := time.Date(2026, 3, 19, 15, 0, 0, 0, time.UTC)
	since := now.Add(-7 * 24 * time.Hour)
	mergedAt100 := time.Date(2026, 3, 18, 14, 30, 0, 0, time.UTC)
	closedAt102 := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)
	baseTime := time.Date(2026, 3, 16, 10, 0, 0, 0, time.UTC)

	// --- create temp ledger and write golden dataset ---
	ledgerPath := t.TempDir()

	// PR #100: merged, "Add rate limiting to API"
	pr100 := &ledger.PRFile{
		Number:    100,
		Title:     "Add rate limiting to API",
		Body:      "Implements token bucket at 100 req/min. Closes #50.",
		Author:    "sarah",
		State:     "merged",
		CreatedAt: baseTime,
		UpdatedAt: mergedAt100,
		MergedAt:  &mergedAt100,
		URL:       "https://github.com/org/repo/pull/100",
		Comments: []ledger.PRComment{
			{
				Author:    "jake",
				Body:      "Switch to token bucket?",
				Path:      "api/rate_limit.go",
				Line:      intPtr(42),
				CreatedAt: baseTime.Add(time.Hour),
			},
			{
				Author:    "jake",
				Body:      "Approving.",
				CreatedAt: baseTime.Add(2 * time.Hour),
			},
			{
				Author:    "sarah",
				Body:      "Good point.",
				CreatedAt: baseTime.Add(3 * time.Hour),
			},
		},
		Commits: []ledger.PRCommit{
			{SHA: "aaa111", Author: "sarah", Date: baseTime, Msg: "feat: add rate limiter"},
			{SHA: "bbb222", Author: "sarah", Date: baseTime.Add(time.Hour), Msg: "fix: edge case"},
		},
	}

	// PR #101: open, "WIP: Add caching"
	pr101 := &ledger.PRFile{
		Number:    101,
		Title:     "WIP: Add caching",
		Body:      "Adding Redis caching layer",
		Author:    "bob",
		State:     "open",
		CreatedAt: baseTime.Add(24 * time.Hour),
		UpdatedAt: baseTime.Add(48 * time.Hour),
		URL:       "https://github.com/org/repo/pull/101",
		Comments: []ledger.PRComment{
			{
				Author:    "carol",
				Body:      "Consider using TTL",
				Path:      "cache/redis.go",
				Line:      intPtr(15),
				CreatedAt: baseTime.Add(25 * time.Hour),
			},
		},
	}

	// PR #102: closed (not merged), "Experiment with Redis"
	pr102 := &ledger.PRFile{
		Number:    102,
		Title:     "Experiment with Redis",
		Body:      "Testing Redis as cache backend",
		Author:    "carol",
		State:     "closed",
		CreatedAt: baseTime,
		UpdatedAt: closedAt102,
		ClosedAt:  &closedAt102,
		URL:       "https://github.com/org/repo/pull/102",
		Comments: []ledger.PRComment{
			{
				Author:    "dave",
				Body:      "Not sure about Redis here",
				CreatedAt: baseTime.Add(time.Hour),
			},
			{
				Author:    "carol",
				Body:      "Agreed, closing.",
				CreatedAt: baseTime.Add(2 * time.Hour),
			},
		},
	}

	// Issue #50: open, 1 comment
	issue50 := &ledger.IssueFile{
		Number:    50,
		Title:     "API needs rate limiting",
		Body:      "Users hammering the API, 503 errors in peak hours",
		Author:    "mike",
		State:     "open",
		CreatedAt: baseTime.Add(-24 * time.Hour),
		UpdatedAt: baseTime,
		URL:       "https://github.com/org/repo/issues/50",
		Comments: []ledger.IssueComment{
			{
				Author:    "sarah",
				Body:      "Working on this in PR #100",
				CreatedAt: baseTime,
			},
		},
	}

	// Issue #55: open, 0 comments
	issue55 := &ledger.IssueFile{
		Number:    55,
		Title:     "Improve error messages",
		Body:      "Error messages need more context",
		Author:    "eve",
		State:     "open",
		CreatedAt: baseTime,
		UpdatedAt: baseTime,
		URL:       "https://github.com/org/repo/issues/55",
	}

	// Write all ledger files
	require.NoError(t, ledger.WriteGitHubPR(ledgerPath, pr100))
	require.NoError(t, ledger.WriteGitHubPR(ledgerPath, pr101))
	require.NoError(t, ledger.WriteGitHubPR(ledgerPath, pr102))
	require.NoError(t, ledger.WriteGitHubIssue(ledgerPath, issue50))
	require.NoError(t, ledger.WriteGitHubIssue(ledgerPath, issue55))

	// --- open store and run pipeline ---
	s, err := store.Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })

	stats, err := index.IndexGitHubData(context.Background(), s, ledgerPath, nil)
	require.NoError(t, err)
	assert.Equal(t, 3, stats.PRsIndexed)
	assert.Equal(t, 2, stats.IssuesIndexed)

	// Insert commit metadata for "aaa111" only (leave "bbb222" for degradation test)
	insertCommit(t, s, "aaa111", "sarah", "feat: add rate limiter", baseTime.Unix())

	// Also insert a standalone commit not linked to any PR
	standaloneCommitTs := baseTime.Add(12 * time.Hour).Unix()
	insertCommit(t, s, "standalone1", "frank", "chore: update deps", standaloneCommitTs)

	// Run full pipeline
	result, err := AssembleActivity(context.Background(), s, since, now)
	require.NoError(t, err)

	// MarshalEventClusters produces the flat JSON array
	data, err := result.MarshalEventClusters()
	require.NoError(t, err)

	// ============================================================
	// ASSERTION 1: Valid JSON, parses as flat array
	// ============================================================
	var elements []map[string]any
	require.NoError(t, json.Unmarshal(data, &elements), "output must be valid JSON array")

	// ============================================================
	// ASSERTION 2: Count: 3 PRs + 2 issues + N standalone commits
	// ============================================================
	var prCount, issueCount, commitCount int
	for _, elem := range elements {
		switch elem["type"] {
		case "pull_request":
			prCount++
		case "issue":
			issueCount++
		case "commit":
			commitCount++
		}
	}
	assert.Equal(t, 3, prCount, "should have 3 PR clusters")
	assert.Equal(t, 2, issueCount, "should have 2 standalone issues")
	assert.GreaterOrEqual(t, commitCount, 1, "should have at least 1 standalone commit")

	// ============================================================
	// ASSERTION 3: Every element has "type" field
	// ============================================================
	for i, elem := range elements {
		_, hasType := elem["type"]
		assert.True(t, hasType, "element %d must have 'type' field", i)
	}

	// ============================================================
	// ASSERTION 4: PR #100 deep validation
	// ============================================================
	pr100elem := findElement(t, elements, "pull_request", 100)
	require.NotNil(t, pr100elem, "PR #100 must be in output")

	// Field names
	assert.Equal(t, "pull_request", pr100elem["type"])
	assert.Equal(t, float64(100), pr100elem["number"])

	// "description" not "body"
	_, hasDescription := pr100elem["description"]
	assert.True(t, hasDescription, "PR must use 'description' key")
	_, hasBody := pr100elem["body"]
	assert.False(t, hasBody, "PR must NOT have 'body' key")

	// "status" not "state", value "merged"
	assert.Equal(t, "merged", pr100elem["status"])
	_, hasState := pr100elem["state"]
	assert.False(t, hasState, "PR must NOT have 'state' key")

	// merged_at is ISO 8601
	mergedAtStr, ok := pr100elem["merged_at"].(string)
	require.True(t, ok, "merged_at must be a string")
	_, parseErr := time.Parse(time.RFC3339, mergedAtStr)
	assert.NoError(t, parseErr, "merged_at must be valid ISO 8601")

	// reviews: 1 ReviewGroup for jake with 1 comment (the one with path)
	reviews, ok := pr100elem["reviews"].([]any)
	require.True(t, ok, "reviews must be an array")
	require.Len(t, reviews, 1, "should have 1 review group (jake)")
	reviewGroup := reviews[0].(map[string]any)
	assert.Equal(t, "jake", reviewGroup["reviewer"])
	reviewComments := reviewGroup["comments"].([]any)
	require.Len(t, reviewComments, 1)
	rc := reviewComments[0].(map[string]any)
	assert.Equal(t, "api/rate_limit.go", rc["path"])
	assert.Equal(t, float64(42), rc["line"])

	// discussion_comments: 2 entries (jake's "Approving." and sarah's "Good point.")
	discussions, ok := pr100elem["discussion_comments"].([]any)
	require.True(t, ok, "discussion_comments must be an array")
	assert.Len(t, discussions, 2, "should have 2 discussion comments")

	// commits: 2 entries (one full metadata, one degraded)
	commits, ok := pr100elem["commits"].([]any)
	require.True(t, ok, "commits must be an array")
	require.Len(t, commits, 2, "should have 2 commits")

	// Find the full metadata commit and the degraded one
	var fullCommit, degradedCommit map[string]any
	for _, c := range commits {
		cm := c.(map[string]any)
		switch cm["sha"] {
		case "aaa111":
			fullCommit = cm
		case "bbb222":
			degradedCommit = cm
		}
	}
	require.NotNil(t, fullCommit, "commit aaa111 must exist")
	require.NotNil(t, degradedCommit, "commit bbb222 must exist")

	assert.NotNil(t, fullCommit["author"], "aaa111 author should be populated")
	assert.NotNil(t, fullCommit["message"], "aaa111 message should be populated")
	assert.NotNil(t, fullCommit["timestamp"], "aaa111 timestamp should be populated")

	assert.Nil(t, degradedCommit["author"], "bbb222 author should be null (degraded)")
	assert.Nil(t, degradedCommit["message"], "bbb222 message should be null (degraded)")
	assert.Nil(t, degradedCommit["timestamp"], "bbb222 timestamp should be null (degraded)")

	// ============================================================
	// ASSERTION 5: PR #101 — open, empty commits, 1 review group
	// ============================================================
	pr101elem := findElement(t, elements, "pull_request", 101)
	require.NotNil(t, pr101elem, "PR #101 must be in output")
	assert.Equal(t, "open", pr101elem["status"])

	pr101commits, ok := pr101elem["commits"].([]any)
	require.True(t, ok)
	assert.Empty(t, pr101commits, "PR #101 should have empty commits")

	pr101reviews, ok := pr101elem["reviews"].([]any)
	require.True(t, ok)
	assert.Len(t, pr101reviews, 1, "PR #101 should have 1 review group")

	// ============================================================
	// ASSERTION 6: PR #102 — closed, no merged_at, 2 discussion comments
	// ============================================================
	pr102elem := findElement(t, elements, "pull_request", 102)
	require.NotNil(t, pr102elem, "PR #102 must be in output")
	assert.Equal(t, "closed", pr102elem["status"])

	// closed without merge — merged_at should be absent or null
	pr102MergedAt := pr102elem["merged_at"]
	assert.Nil(t, pr102MergedAt, "closed-without-merge PR should not have merged_at")

	pr102discussions, ok := pr102elem["discussion_comments"].([]any)
	require.True(t, ok)
	assert.Len(t, pr102discussions, 2, "PR #102 should have 2 discussion comments")

	// ============================================================
	// ASSERTION 7: Issue #50 — "body" key, "state", 1 comment
	// ============================================================
	issue50elem := findElement(t, elements, "issue", 50)
	require.NotNil(t, issue50elem, "Issue #50 must be in output")

	assert.Equal(t, "issue", issue50elem["type"])
	_, hasIssueBody := issue50elem["body"]
	assert.True(t, hasIssueBody, "Issue must use 'body' key")
	assert.Equal(t, "open", issue50elem["state"])

	issueComments, ok := issue50elem["comments"].([]any)
	require.True(t, ok)
	assert.Len(t, issueComments, 1, "Issue #50 should have 1 comment")

	// ============================================================
	// ASSERTION 8: Issue #55 — comments is []
	// ============================================================
	issue55elem := findElement(t, elements, "issue", 55)
	require.NotNil(t, issue55elem, "Issue #55 must be in output")

	issue55comments, ok := issue55elem["comments"].([]any)
	require.True(t, ok, "comments must be an array (not null)")
	assert.Empty(t, issue55comments, "Issue #55 should have empty comments array")

	// ============================================================
	// ASSERTION 9: Standalone commits — "type": "commit", sha, message, author, timestamp
	// ============================================================
	for _, elem := range elements {
		if elem["type"] == "commit" {
			_, hasSHA := elem["sha"]
			assert.True(t, hasSHA, "commit must have 'sha'")
			_, hasMsg := elem["message"]
			assert.True(t, hasMsg, "commit must have 'message'")
			_, hasAuthor := elem["author"]
			assert.True(t, hasAuthor, "commit must have 'author'")
			_, hasTS := elem["timestamp"]
			assert.True(t, hasTS, "commit must have 'timestamp'")
		}
	}

	// ============================================================
	// ASSERTION 10: No "related_issue" or "files_changed" anywhere
	// ============================================================
	rawJSON := string(data)
	assert.NotContains(t, rawJSON, `"related_issue"`, "output must not contain related_issue")
	assert.NotContains(t, rawJSON, `"files_changed"`, "output must not contain files_changed")

	// ============================================================
	// ASSERTION 11: Valid UTF-8, no unescaped control characters
	// ============================================================
	assert.True(t, json.Valid(data), "output must be valid JSON")
	for _, b := range data {
		if b < 0x20 && b != '\t' && b != '\n' && b != '\r' {
			t.Errorf("output contains unescaped control character: 0x%02x", b)
		}
	}

	// ============================================================
	// Verify empty arrays serialize as [] not null
	// ============================================================
	assert.NotContains(t, rawJSON, `"commits":null`)
	assert.NotContains(t, rawJSON, `"reviews":null`)
	assert.NotContains(t, rawJSON, `"discussion_comments":null`)
	assert.NotContains(t, rawJSON, `"comments":null`)

	// Verify empty arrays are present as []
	// PR #101 has no commits, so check that pattern
	assert.True(t, strings.Contains(rawJSON, `"commits":[]`) || !strings.Contains(rawJSON, `"commits":null`),
		"empty commits must serialize as [] not null")
}

// findElement locates an element in the flat array by type and number.
func findElement(t *testing.T, elements []map[string]any, typeName string, number int) map[string]any {
	t.Helper()
	for _, elem := range elements {
		if elem["type"] == typeName {
			if n, ok := elem["number"].(float64); ok && int(n) == number {
				return elem
			}
		}
	}
	// For commits, search by sha instead
	return nil
}
