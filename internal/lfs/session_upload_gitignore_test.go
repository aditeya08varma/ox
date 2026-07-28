package lfs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEnsureSessionsGitignore_ExcludesNeedsSummary pins the intent that the
// machine-local .needs-summary marker never enters git. Its payload is absolute
// local paths, so committing it leaks the writer's home directory into a shared
// repo and guarantees a conflict that can never merge cleanly: the cloud
// summarizer deletes the marker on completion while a local writer modifies it.
func TestEnsureSessionsGitignore_ExcludesNeedsSummary(t *testing.T) {
	sessionsDir := t.TempDir()
	require.NoError(t, EnsureSessionsGitignore(sessionsDir))

	data, err := os.ReadFile(filepath.Join(sessionsDir, ".gitignore"))
	require.NoError(t, err)
	assert.Contains(t, string(data), ".needs-summary",
		"marker holds absolute local paths and must never be committed")
}

// TestEnsureSessionsGitignore_RewritesLegacy ensures a ledger created before the
// exclusion existed is repaired in place — the 111 already-tracked markers on a
// real ledger got there through the legacy content.
func TestEnsureSessionsGitignore_RewritesLegacy(t *testing.T) {
	sessionsDir := t.TempDir()
	legacy := "# LFS pointer files and meta.json are committed to git.\n"
	path := filepath.Join(sessionsDir, ".gitignore")
	require.NoError(t, os.WriteFile(path, []byte(legacy), 0644))

	require.NoError(t, EnsureSessionsGitignore(sessionsDir))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), ".needs-summary")
}

// TestEnsureSessionsGitignore_Idempotent guards against the rewrite-every-call
// churn that would dirty the ledger worktree on every sync cycle.
func TestEnsureSessionsGitignore_Idempotent(t *testing.T) {
	sessionsDir := t.TempDir()
	path := filepath.Join(sessionsDir, ".gitignore")

	require.NoError(t, EnsureSessionsGitignore(sessionsDir))
	first, err := os.ReadFile(path)
	require.NoError(t, err)

	require.NoError(t, EnsureSessionsGitignore(sessionsDir))
	second, err := os.ReadFile(path)
	require.NoError(t, err)

	assert.Equal(t, string(first), string(second))
}
