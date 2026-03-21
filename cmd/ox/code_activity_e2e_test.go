//go:build integration

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/codedb/store"
	"github.com/sageox/ox/internal/testguard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCodeActivityE2E runs the real ox binary against a populated CodeDB.
// 5 subtests: default output, --json, empty window, invalid --since, no CodeDB.
func TestCodeActivityE2E(t *testing.T) {
	oxBin := buildOxBinary(t)

	t.Run("A_default_output", func(t *testing.T) {
		repoDir, envVars := setupActivityTestProject(t, oxBin)

		output, exitCode, _ := testguard.RunOx(t, oxBin, repoDir, envVars,
			"code", "activity", "--since", "7d")

		assert.Equal(t, 0, exitCode, "exit code should be 0, output: %s", output)

		// Parse as flat JSON array
		var elements []map[string]any
		require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(output)), &elements),
			"output must be valid JSON array, got: %s", output)

		// Should have 3 elements: 1 PR + 1 issue + 1 commit
		assert.Len(t, elements, 3, "should have 3 elements")

		// Verify type discriminators
		types := make(map[string]int)
		for _, elem := range elements {
			if t, ok := elem["type"].(string); ok {
				types[t]++
			}
		}
		assert.Equal(t, 1, types["pull_request"], "should have 1 PR")
		assert.Equal(t, 1, types["issue"], "should have 1 issue")
		assert.Equal(t, 1, types["commit"], "should have 1 commit")

		// PR should use "description" not "body"
		for _, elem := range elements {
			if elem["type"] == "pull_request" {
				_, hasDescription := elem["description"]
				assert.True(t, hasDescription, "PR must use 'description' key")
				_, hasBody := elem["body"]
				assert.False(t, hasBody, "PR must NOT have 'body' key")
				// No related_issue or files_changed
				_, hasRelated := elem["related_issue"]
				assert.False(t, hasRelated, "PR must NOT have 'related_issue' key")
				_, hasFiles := elem["files_changed"]
				assert.False(t, hasFiles, "PR must NOT have 'files_changed' key")
			}
		}
	})

	t.Run("B_pretty_printed", func(t *testing.T) {
		repoDir, envVars := setupActivityTestProject(t, oxBin)

		output, exitCode, _ := testguard.RunOx(t, oxBin, repoDir, envVars,
			"code", "activity", "--since", "7d", "--pretty")

		assert.Equal(t, 0, exitCode, "exit code should be 0, output: %s", output)

		// --pretty flag should produce indented output
		trimmed := strings.TrimSpace(output)
		assert.True(t, strings.Contains(trimmed, "\n"), "pretty-printed output should have newlines")
		assert.True(t, strings.Contains(trimmed, "  "), "pretty-printed output should have indentation")

		// Should still be valid JSON
		var elements []map[string]any
		require.NoError(t, json.Unmarshal([]byte(trimmed), &elements))
		assert.Len(t, elements, 3)
	})

	t.Run("C_empty_window", func(t *testing.T) {
		repoDir := testGitRepoForActivity(t)
		// Create a CodeDB with data that's very old (30+ days ago)
		codeDBDir := filepath.Join(repoDir, ".sageox", "cache", "codedb")
		require.NoError(t, os.MkdirAll(codeDBDir, 0755))

		s, err := store.Open(codeDBDir)
		require.NoError(t, err)

		oldTime := time.Now().UTC().Add(-60 * 24 * time.Hour).Unix() // 60 days ago
		_, err = s.Exec(`INSERT INTO pull_requests
			(number, title, body, author, state, labels, created_at, updated_at, url)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			99, "Old PR", "Ancient code", "alice", "merged", "[]",
			oldTime, oldTime, "https://github.com/org/repo/pull/99")
		require.NoError(t, err)
		require.NoError(t, s.Close())

		envVars := []string{"OX_NO_DAEMON=1"}
		// Use --since 1h: data is 60 days old, nothing in 1-hour window
		output, exitCode, _ := testguard.RunOx(t, oxBin, repoDir, envVars,
			"code", "activity", "--since", "1h")

		assert.Equal(t, 0, exitCode, "exit code should be 0 for empty results, output: %s", output)
		assert.Equal(t, "[]", strings.TrimSpace(output), "empty results should be []")
	})

	t.Run("D_invalid_since", func(t *testing.T) {
		repoDir, envVars := setupActivityTestProject(t, oxBin)

		output, exitCode, _ := testguard.RunOx(t, oxBin, repoDir, envVars,
			"code", "activity", "--since", "banana")

		assert.NotEqual(t, 0, exitCode, "exit code should be non-zero for invalid --since")
		assert.Contains(t, strings.ToLower(output), "invalid", "error should mention invalid format")
	})

	t.Run("E_no_codedb", func(t *testing.T) {
		// Fresh project with no CodeDB
		repoDir := testGitRepoForActivity(t)

		envVars := []string{
			"OX_NO_DAEMON=1",
		}

		output, exitCode, _ := testguard.RunOx(t, oxBin, repoDir, envVars,
			"code", "activity", "--since", "7d")

		assert.NotEqual(t, 0, exitCode, "exit code should be non-zero when no CodeDB")
		lower := strings.ToLower(output)
		assert.True(t,
			strings.Contains(lower, "ox code index") || strings.Contains(lower, "no code index"),
			"error should mention 'ox code index', got: %s", output)
	})
}

// testGitRepoForActivity creates a git repo with .sageox/ initialized.
func testGitRepoForActivity(t *testing.T) string {
	t.Helper()

	tmpDir := t.TempDir()

	// init git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	require.NoError(t, cmd.Run(), "failed to init git repo")

	// configure git user (scoped to temp dir)
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = tmpDir
	require.NoError(t, cmd.Run())

	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = tmpDir
	require.NoError(t, cmd.Run())

	// create initial commit
	readmePath := filepath.Join(tmpDir, "README.md")
	require.NoError(t, os.WriteFile(readmePath, []byte("# Test\n"), 0644))

	cmd = exec.Command("git", "add", "README.md")
	cmd.Dir = tmpDir
	require.NoError(t, cmd.Run())

	cmd = exec.Command("git", "commit", "-m", "initial commit")
	cmd.Dir = tmpDir
	require.NoError(t, cmd.Run())

	// create .sageox/ directory
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	return tmpDir
}

// setupActivityTestProject creates a git repo + CodeDB populated with test data.
// Returns the repo dir and env vars for testguard.RunOx.
func setupActivityTestProject(t *testing.T, _ string) (string, []string) {
	t.Helper()

	repoDir := testGitRepoForActivity(t)

	// Create CodeDB at the fallback path: .sageox/cache/codedb/
	codeDBDir := filepath.Join(repoDir, ".sageox", "cache", "codedb")
	require.NoError(t, os.MkdirAll(codeDBDir, 0755))

	s, err := store.Open(codeDBDir)
	require.NoError(t, err)

	now := time.Now().UTC()
	yesterday := now.Add(-24 * time.Hour)

	yesterdayUnix := yesterday.Unix()
	nowUnix := now.Unix()

	// Insert a merged PR #42 with 2 comments (1 review, 1 discussion) and 2 commits
	res, err := s.Exec(`INSERT INTO pull_requests
		(number, title, body, author, state, labels, created_at, updated_at, merged_at, url)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		42, "Add rate limiting", "Implements rate limiting", "alice", "merged", "[]",
		yesterdayUnix, nowUnix, nowUnix, "https://github.com/org/repo/pull/42")
	require.NoError(t, err)
	prID, _ := res.LastInsertId()

	// Review comment (has path)
	_, err = s.Exec(`INSERT INTO pr_comments (pr_id, author, body, path, line, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		prID, "bob", "Looks good", "api/handler.go", 42, yesterdayUnix)
	require.NoError(t, err)

	// Discussion comment (no path)
	_, err = s.Exec(`INSERT INTO pr_comments (pr_id, author, body, path, line, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		prID, "bob", "LGTM", nil, nil, nowUnix)
	require.NoError(t, err)

	// PR commits
	_, err = s.Exec(`INSERT INTO pr_commits (pr_id, sha) VALUES (?, ?)`, prID, "commit1")
	require.NoError(t, err)
	_, err = s.Exec(`INSERT INTO pr_commits (pr_id, sha) VALUES (?, ?)`, prID, "commit2")
	require.NoError(t, err)

	// Ensure repos table has an entry for commit inserts
	_, _ = s.Exec("INSERT OR IGNORE INTO repos (id, name, path) VALUES (1, 'test', '/tmp/test')")

	// Insert commit metadata for commit1
	_, err = s.Exec(`INSERT INTO commits (repo_id, hash, author, message, timestamp)
		VALUES (1, ?, ?, ?, ?)`,
		"commit1", "alice", "feat: add rate limiter", yesterdayUnix)
	require.NoError(t, err)

	// Insert standalone issue #30
	_, err = s.Exec(`INSERT INTO issues
		(number, title, body, author, state, labels, created_at, updated_at, url)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		30, "Bug report", "Something is broken", "carol", "open", "[]",
		yesterdayUnix, nowUnix, "https://github.com/org/repo/issues/30")
	require.NoError(t, err)

	issueID := int64(1) // first issue inserted
	_, err = s.Exec(`INSERT INTO issue_comments (issue_id, author, body, created_at)
		VALUES (?, ?, ?, ?)`,
		issueID, "dave", "Can reproduce", yesterdayUnix)
	require.NoError(t, err)

	// Insert standalone commit (not linked to any PR)
	_, err = s.Exec(`INSERT INTO commits (repo_id, hash, author, message, timestamp)
		VALUES (1, ?, ?, ?, ?)`,
		"standalone_abc", "eve", "chore: update deps", yesterdayUnix)
	require.NoError(t, err)

	// Close store so binary can open it
	require.NoError(t, s.Close())

	envVars := []string{
		"OX_NO_DAEMON=1",
	}

	return repoDir, envVars
}
