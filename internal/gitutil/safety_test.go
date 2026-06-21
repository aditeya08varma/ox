package gitutil

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHasLockFiles(t *testing.T) {
	t.Run("no lock files", func(t *testing.T) {
		gitDir := filepath.Join(t.TempDir(), ".git")
		require.NoError(t, os.MkdirAll(gitDir, 0755))

		assert.Empty(t, HasLockFiles(gitDir))
	})

	t.Run("all lock types present", func(t *testing.T) {
		gitDir := filepath.Join(t.TempDir(), ".git")
		require.NoError(t, os.MkdirAll(gitDir, 0755))

		for _, lock := range knownLockFiles {
			require.NoError(t, os.WriteFile(filepath.Join(gitDir, lock), []byte{}, 0644))
		}

		found := HasLockFiles(gitDir)
		assert.Len(t, found, len(knownLockFiles))
		for _, lock := range knownLockFiles {
			assert.Contains(t, found, lock)
		}
	})

	t.Run("partial locks", func(t *testing.T) {
		gitDir := filepath.Join(t.TempDir(), ".git")
		require.NoError(t, os.MkdirAll(gitDir, 0755))

		// create all, then remove one
		for _, lock := range knownLockFiles {
			require.NoError(t, os.WriteFile(filepath.Join(gitDir, lock), []byte{}, 0644))
		}
		require.NoError(t, os.Remove(filepath.Join(gitDir, "index.lock")))

		found := HasLockFiles(gitDir)
		assert.Len(t, found, len(knownLockFiles)-1)
		assert.NotContains(t, found, "index.lock")
	})

	t.Run("nonexistent directory", func(t *testing.T) {
		found := HasLockFiles("/nonexistent/path/.git")
		assert.Empty(t, found)
	})
}

func TestRemoveStaleLockFiles(t *testing.T) {
	t.Run("removes old locks", func(t *testing.T) {
		gitDir := filepath.Join(t.TempDir(), ".git")
		require.NoError(t, os.MkdirAll(gitDir, 0755))

		lockPath := filepath.Join(gitDir, "index.lock")
		require.NoError(t, os.WriteFile(lockPath, []byte{}, 0644))
		// backdate to look old
		oldTime := time.Now().Add(-(StaleLockAge + time.Second))
		require.NoError(t, os.Chtimes(lockPath, oldTime, oldTime))

		removed, errs := RemoveStaleLockFiles(gitDir)
		assert.Empty(t, errs)
		assert.Equal(t, []string{"index.lock"}, removed)
		assert.Empty(t, HasLockFiles(gitDir))
	})

	t.Run("preserves fresh locks", func(t *testing.T) {
		gitDir := filepath.Join(t.TempDir(), ".git")
		require.NoError(t, os.MkdirAll(gitDir, 0755))

		require.NoError(t, os.WriteFile(filepath.Join(gitDir, "index.lock"), []byte{}, 0644))
		// do NOT backdate — file is brand new

		removed, errs := RemoveStaleLockFiles(gitDir)
		assert.Empty(t, errs)
		assert.Empty(t, removed)
		assert.NotEmpty(t, HasLockFiles(gitDir))
	})

	t.Run("no locks present", func(t *testing.T) {
		gitDir := filepath.Join(t.TempDir(), ".git")
		require.NoError(t, os.MkdirAll(gitDir, 0755))

		removed, errs := RemoveStaleLockFiles(gitDir)
		assert.Empty(t, errs)
		assert.Empty(t, removed)
	})

	t.Run("nonexistent directory", func(t *testing.T) {
		removed, errs := RemoveStaleLockFiles("/nonexistent/.git")
		assert.Empty(t, errs) // missing files are not errors
		assert.Empty(t, removed)
	})
}

func TestIsRebaseInProgress(t *testing.T) {
	t.Run("clean repo", func(t *testing.T) {
		repo := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0755))

		assert.False(t, IsRebaseInProgress(repo))
	})

	t.Run("rebase-merge exists", func(t *testing.T) {
		repo := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git", "rebase-merge"), 0755))

		assert.True(t, IsRebaseInProgress(repo))
	})

	t.Run("rebase-apply exists", func(t *testing.T) {
		repo := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git", "rebase-apply"), 0755))

		assert.True(t, IsRebaseInProgress(repo))
	})

	t.Run("both exist", func(t *testing.T) {
		repo := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git", "rebase-merge"), 0755))
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git", "rebase-apply"), 0755))

		assert.True(t, IsRebaseInProgress(repo))
	})
}

// TestRebaseAge pins the fresh-vs-stale distinction the daemon relies on to
// decide whether to auto-recover a wedged rebase.
// Failure prevented: without an age signal the daemon either skips a
// pre-existing wedge forever (stranding every new session — the original bug)
// or aborts a rebase its own pull just started seconds ago.
func TestRebaseAge(t *testing.T) {
	t.Run("no rebase in progress", func(t *testing.T) {
		repo := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0755))

		_, inProgress := RebaseAge(repo)
		assert.False(t, inProgress)
	})

	// A non-ENOENT stat error (here ENOTDIR: .git is a file, not a dir) is NOT
	// proof the repo is clean. RebaseAge must conservatively report in-progress
	// so the caller skips rather than pulling on a possibly-wedged repo, and
	// fresh (age 0) so it never auto-aborts on a guess.
	t.Run("non-not-exist stat error treated as in-progress fresh", func(t *testing.T) {
		repo := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(repo, ".git"), []byte("not a dir"), 0644))

		age, inProgress := RebaseAge(repo)
		assert.True(t, inProgress)
		assert.Equal(t, time.Duration(0), age)
	})

	t.Run("fresh rebase reports small age", func(t *testing.T) {
		repo := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git", "rebase-merge"), 0755))

		age, inProgress := RebaseAge(repo)
		assert.True(t, inProgress)
		// just-created dir → well under any sane stale threshold
		assert.Less(t, age, time.Minute)
	})

	t.Run("stale rebase reports large age via backdated mtime", func(t *testing.T) {
		repo := t.TempDir()
		dir := filepath.Join(repo, ".git", "rebase-merge")
		require.NoError(t, os.MkdirAll(dir, 0755))
		// backdate the dir mtime to simulate a wedge abandoned an hour ago
		old := time.Now().Add(-time.Hour)
		require.NoError(t, os.Chtimes(dir, old, old))

		age, inProgress := RebaseAge(repo)
		assert.True(t, inProgress)
		assert.Greater(t, age, 30*time.Minute)
	})

	// rebase-apply (git am-style / older interactive rebases) hits the same
	// loop; a typo skipping it would silently bypass stale recovery for that
	// backend, so pin its stale path independently.
	t.Run("stale rebase-apply backend reports large age", func(t *testing.T) {
		repo := t.TempDir()
		dir := filepath.Join(repo, ".git", "rebase-apply")
		require.NoError(t, os.MkdirAll(dir, 0755))
		old := time.Now().Add(-time.Hour)
		require.NoError(t, os.Chtimes(dir, old, old))

		age, inProgress := RebaseAge(repo)
		assert.True(t, inProgress)
		assert.Greater(t, age, 30*time.Minute)
	})

	// Blind-spot guard: a partial/failed `rebase --abort` can bump the dir's
	// own mtime to "now" while the rebase metadata files keep their original
	// (old) mtime. Age must follow the OLDEST entry so a genuinely stuck wedge
	// still reads as stale instead of looking fresh for another threshold.
	t.Run("fresh dir mtime but old entry still reads stale", func(t *testing.T) {
		repo := t.TempDir()
		dir := filepath.Join(repo, ".git", "rebase-merge")
		require.NoError(t, os.MkdirAll(dir, 0755))
		oldFile := filepath.Join(dir, "onto")
		require.NoError(t, os.WriteFile(oldFile, []byte("deadbeef\n"), 0644))
		old := time.Now().Add(-time.Hour)
		require.NoError(t, os.Chtimes(oldFile, old, old)) // metadata file is old
		now := time.Now()
		require.NoError(t, os.Chtimes(dir, now, now)) // dir mtime bumped to "now"

		age, inProgress := RebaseAge(repo)
		assert.True(t, inProgress)
		assert.Greater(t, age, 30*time.Minute, "age must follow the oldest entry, not the bumped dir mtime")
	})
}

func TestIsSafeForGitOps(t *testing.T) {
	t.Run("clean repo", func(t *testing.T) {
		repo := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0755))

		assert.NoError(t, IsSafeForGitOps(repo))
	})

	t.Run("lock files present", func(t *testing.T) {
		repo := t.TempDir()
		gitDir := filepath.Join(repo, ".git")
		require.NoError(t, os.MkdirAll(gitDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(gitDir, "index.lock"), []byte{}, 0644))

		err := IsSafeForGitOps(repo)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "index.lock")
	})

	t.Run("rebase in progress", func(t *testing.T) {
		repo := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git", "rebase-merge"), 0755))

		err := IsSafeForGitOps(repo)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "rebase")
	})

	t.Run("both lock and rebase", func(t *testing.T) {
		repo := t.TempDir()
		gitDir := filepath.Join(repo, ".git")
		require.NoError(t, os.MkdirAll(filepath.Join(gitDir, "rebase-merge"), 0755))
		require.NoError(t, os.WriteFile(filepath.Join(gitDir, "index.lock"), []byte{}, 0644))

		// lock check runs first, so error should mention locks
		err := IsSafeForGitOps(repo)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "lock")
	})
}

func TestStripLFSConfig(t *testing.T) {
	t.Run("removes lfs section", func(t *testing.T) {
		repo := t.TempDir()
		gitDir := filepath.Join(repo, ".git")
		require.NoError(t, os.MkdirAll(gitDir, 0755))

		config := `[core]
	repositoryformatversion = 0
	bare = false
[lfs]
	repositoryformatversion = 0
[remote "origin"]
	url = https://example.com/repo.git
`
		require.NoError(t, os.WriteFile(filepath.Join(gitDir, "config"), []byte(config), 0644))

		StripLFSConfig(repo)

		data, err := os.ReadFile(filepath.Join(gitDir, "config"))
		require.NoError(t, err)
		assert.NotContains(t, string(data), "[lfs]")
		assert.NotContains(t, string(data), "lfs")
		assert.Contains(t, string(data), "[core]")
		assert.Contains(t, string(data), "[remote \"origin\"]")
	})

	t.Run("no-op without lfs section", func(t *testing.T) {
		repo := t.TempDir()
		gitDir := filepath.Join(repo, ".git")
		require.NoError(t, os.MkdirAll(gitDir, 0755))

		config := `[core]
	repositoryformatversion = 0
[remote "origin"]
	url = https://example.com/repo.git
`
		require.NoError(t, os.WriteFile(filepath.Join(gitDir, "config"), []byte(config), 0644))

		StripLFSConfig(repo)

		data, err := os.ReadFile(filepath.Join(gitDir, "config"))
		require.NoError(t, err)
		assert.Equal(t, config, string(data))
	})

	t.Run("no-op without .git dir", func(t *testing.T) {
		repo := t.TempDir()
		StripLFSConfig(repo) // should not panic
	})
}

func TestFetchHeadAge(t *testing.T) {
	t.Run("no FETCH_HEAD", func(t *testing.T) {
		repo := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0755))

		age, ok := FetchHeadAge(repo)
		assert.False(t, ok)
		assert.Zero(t, age)
	})

	t.Run("recent FETCH_HEAD", func(t *testing.T) {
		repo := t.TempDir()
		gitDir := filepath.Join(repo, ".git")
		require.NoError(t, os.MkdirAll(gitDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(gitDir, "FETCH_HEAD"), []byte("ref"), 0644))

		age, ok := FetchHeadAge(repo)
		assert.True(t, ok)
		assert.Less(t, age, 2*time.Second) // just created
	})

	t.Run("old FETCH_HEAD", func(t *testing.T) {
		repo := t.TempDir()
		gitDir := filepath.Join(repo, ".git")
		require.NoError(t, os.MkdirAll(gitDir, 0755))

		fetchHead := filepath.Join(gitDir, "FETCH_HEAD")
		require.NoError(t, os.WriteFile(fetchHead, []byte("ref"), 0644))
		oldTime := time.Now().Add(-5 * time.Minute)
		require.NoError(t, os.Chtimes(fetchHead, oldTime, oldTime))

		age, ok := FetchHeadAge(repo)
		assert.True(t, ok)
		assert.Greater(t, age, 4*time.Minute)
	})
}
