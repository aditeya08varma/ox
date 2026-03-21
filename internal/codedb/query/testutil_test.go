package query

import (
	"testing"

	"github.com/sageox/ox/internal/codedb/store"
	"github.com/stretchr/testify/require"
)

// openTestStore creates a fresh in-memory CodeDB store for testing.
func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// insertPR inserts a pull request and returns its database ID.
func insertPR(t *testing.T, s *store.Store, number int, title, body, author, state string, createdAt, updatedAt, mergedAt *int64, url string) int64 {
	t.Helper()
	res, err := s.Exec(`INSERT INTO pull_requests
		(number, title, body, author, state, labels, created_at, updated_at, merged_at, url)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		number, title, body, author, state, "[]", createdAt, updatedAt, mergedAt, url)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return id
}

// insertComment inserts a PR comment (review or discussion depending on path).
func insertComment(t *testing.T, s *store.Store, prID int64, author, body string, path *string, line *int, createdAt int64) {
	t.Helper()
	_, err := s.Exec(`INSERT INTO pr_comments (pr_id, author, body, path, line, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		prID, author, body, path, line, createdAt)
	require.NoError(t, err)
}

// insertCommit inserts a git commit into the commits table.
func insertCommit(t *testing.T, s *store.Store, hash, author, message string, timestamp int64) {
	t.Helper()
	// ensure a repo exists
	_, _ = s.Exec("INSERT OR IGNORE INTO repos (id, name, path) VALUES (1, 'test', '/tmp/test')")
	_, err := s.Exec(`INSERT INTO commits (repo_id, hash, author, message, timestamp)
		VALUES (1, ?, ?, ?, ?)`,
		hash, author, message, timestamp)
	require.NoError(t, err)
}

// insertPRCommit links a commit SHA to a PR in the pr_commits join table.
func insertPRCommit(t *testing.T, s *store.Store, prID int64, sha string) {
	t.Helper()
	_, err := s.Exec(`INSERT INTO pr_commits (pr_id, sha) VALUES (?, ?)`, prID, sha)
	require.NoError(t, err)
}

// insertIssue inserts an issue and returns its database ID.
func insertIssue(t *testing.T, s *store.Store, number int, title, body, author, state string, createdAt, updatedAt *int64, url string) int64 {
	t.Helper()
	res, err := s.Exec(`INSERT INTO issues
		(number, title, body, author, state, labels, created_at, updated_at, url)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		number, title, body, author, state, "[]", createdAt, updatedAt, url)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return id
}

// insertIssueComment inserts a comment on an issue.
func insertIssueComment(t *testing.T, s *store.Store, issueID int64, author, body string, createdAt int64) {
	t.Helper()
	_, err := s.Exec(`INSERT INTO issue_comments (issue_id, author, body, created_at)
		VALUES (?, ?, ?, ?)`,
		issueID, author, body, createdAt)
	require.NoError(t, err)
}

// int64Ptr returns a pointer to the given int64.
func int64Ptr(v int64) *int64 { return &v }

// strPtr returns a pointer to the given string.
func strPtr(v string) *string { return &v }

// intPtr returns a pointer to the given int.
func intPtr(v int) *int { return &v }
