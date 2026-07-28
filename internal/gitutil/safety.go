// Package gitutil provides shared git safety primitives used by both the daemon
// (pull/fetch) and CLI (push/commit) code paths.
package gitutil

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MinFetchHeadAge is the minimum age of FETCH_HEAD before we'll fetch again.
// Prevents redundant fetches if another process fetched recently.
const MinFetchHeadAge = 30 * time.Second

// knownLockFiles are git lock files that indicate a crashed or in-progress git process.
var knownLockFiles = []string{
	"index.lock",
	"shallow.lock",
	"config.lock",
	"HEAD.lock",
}

// knownLockGlobs are lock-file patterns whose exact name isn't predictable.
// git writes .git/next-index-<pid>.lock while rebuilding the index; a process
// killed mid-write leaves one behind, and because the pid varies it can never
// be enumerated in knownLockFiles.
//
// Deliberately NOT a broad "*.lock": that would sweep packed-refs.lock and
// ref-transaction locks, which belong to operations we must not interfere with.
var knownLockGlobs = []string{
	"next-index-*.lock",
}

// lockFilesIn returns the .git-relative names of every lock file present,
// covering both the fixed names and the pid-suffixed globs. Shared by
// HasLockFiles and RemoveStaleLockFiles so the two can never drift — a lock
// that one detects but the other cannot clear is a permanent wedge, which is
// exactly how a ledger sat blocked for three months on a stray
// next-index-13088.lock.
func lockFilesIn(gitDir string) []string {
	var found []string
	for _, lock := range knownLockFiles {
		if _, err := os.Stat(filepath.Join(gitDir, lock)); err == nil {
			found = append(found, lock)
		}
	}
	for _, pattern := range knownLockGlobs {
		matches, err := filepath.Glob(filepath.Join(gitDir, pattern))
		if err != nil {
			continue // malformed pattern — treat as no match
		}
		for _, m := range matches {
			found = append(found, filepath.Base(m))
		}
	}
	return found
}

// HasLockFiles checks .git/ for stale lock files that block git operations.
// Returns the names of lock files found (empty slice = safe to proceed).
func HasLockFiles(gitDir string) []string {
	return lockFilesIn(gitDir)
}

// StaleLockAge is how old a git lock file must be before we consider it abandoned.
// Git operations normally hold locks for milliseconds to a few seconds, but a slow
// git pull --rebase on a large repo over a poor network can hold index.lock for
// several minutes. 5 minutes is conservative enough to cover legitimate operations
// while still recovering from crashed processes.
const StaleLockAge = 5 * time.Minute

// RemoveStaleLockFiles removes git lock files older than StaleLockAge.
// Safe to call at daemon startup or before pull operations — only removes
// files that no running git process could still be holding.
// Returns the names of files removed and any removal errors encountered.
func RemoveStaleLockFiles(gitDir string) (removed []string, errs []error) {
	for _, lock := range lockFilesIn(gitDir) {
		path := filepath.Join(gitDir, lock)
		info, err := os.Stat(path)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, fmt.Errorf("stat %s: %w", lock, err))
			}
			continue
		}
		if time.Since(info.ModTime()) < StaleLockAge {
			continue // recent enough to be from an active process
		}
		if err := os.Remove(path); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", lock, err))
		} else {
			removed = append(removed, lock)
		}
	}
	return removed, errs
}

// IsRebaseInProgress checks whether the repo is stuck in a broken rebase state.
// Returns true if .git/rebase-merge or .git/rebase-apply exists.
func IsRebaseInProgress(repoPath string) bool {
	gitDir := filepath.Join(repoPath, ".git")
	for _, dir := range []string{"rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(filepath.Join(gitDir, dir)); err == nil {
			return true
		}
	}
	return false
}

// RebaseAge returns how long a rebase has been in progress, based on the
// mtime of the .git/rebase-merge or .git/rebase-apply directory. The bool is
// false if no rebase is in progress (age is then meaningless).
//
// Used to distinguish a transient, in-flight rebase (seconds old — leave it
// alone) from a genuinely wedged one abandoned by a prior crash or a rebase
// that stopped at an "edit"/conflict and was never continued (minutes/days
// old — safe to auto-recover). A fresh rebase that the daemon's own pull just
// started must NOT be aborted out from under itself.
func RebaseAge(repoPath string) (time.Duration, bool) {
	gitDir := filepath.Join(repoPath, ".git")
	for _, dir := range []string{"rebase-merge", "rebase-apply"} {
		p := filepath.Join(gitDir, dir)
		info, err := os.Stat(p)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue // this backend's dir is absent; check the other
			}
			// A genuine stat failure (permission, I/O) is NOT proof the repo is
			// clean. Treat it conservatively as an in-progress rebase so the
			// caller skips rather than pulling on a possibly-wedged repo, and
			// report it as fresh (age 0) so we never auto-abort on a guess.
			return 0, true
		}
		// Use the OLDEST mtime among the dir and its entries. The rebase
		// metadata files are written once at rebase start and not rewritten
		// while it sits stuck, but a partial/failed `rebase --abort` can bump
		// the directory's own mtime to "now". Keying off the oldest entry keeps
		// a wedge stuck after a failed abort reading as stale on the next cycle,
		// instead of masking it as "fresh" for another staleRebaseThreshold.
		oldest := info.ModTime()
		if entries, derr := os.ReadDir(p); derr == nil {
			for _, e := range entries {
				if fi, ferr := e.Info(); ferr == nil && fi.ModTime().Before(oldest) {
					oldest = fi.ModTime()
				}
			}
		}
		return time.Since(oldest), true
	}
	return 0, false
}

// IsSafeForGitOps combines lock file and rebase state checks into a single
// pre-flight check. Returns nil if safe to proceed, or an error describing
// why the repo is blocked.
func IsSafeForGitOps(repoPath string) error {
	gitDir := filepath.Join(repoPath, ".git")

	// Sweep abandoned locks before reporting them as blocking. Without this the
	// push path is asymmetric with the pull path (which already sweeps before
	// every pull), so a lock left by a crashed git blocks every future push
	// forever with no self-heal. Gated on StaleLockAge, so a lock held by a
	// live git process is never touched.
	if removed, errs := RemoveStaleLockFiles(gitDir); len(removed) > 0 || len(errs) > 0 {
		slog.Info("git lock sweep", "repo", repoPath, "removed", strings.Join(removed, ","), "errors", len(errs))
	}

	if locks := HasLockFiles(gitDir); len(locks) > 0 {
		return fmt.Errorf("git lock files detected: %s (remove stale locks or wait for in-progress operation)", strings.Join(locks, ", "))
	}

	if IsRebaseInProgress(repoPath) {
		return fmt.Errorf("repo in broken rebase state (run 'git rebase --abort' in %s)", repoPath)
	}

	return nil
}

// StripLFSConfig removes lfs.repositoryformatversion from local git config.
// This config is set by git-lfs when filter.lfs.required=true is global, but
// it causes HTTP 403 on push to GitLab when the server-side ALB doesn't
// expect LFS-aware clients. Safe to call on any repo — no-op if not set.
func StripLFSConfig(repoPath string) {
	configPath := filepath.Join(repoPath, ".git", "config")
	if _, err := os.Stat(configPath); err != nil {
		return
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return
	}

	content := string(data)
	if !strings.Contains(content, "[lfs]") {
		return
	}

	// remove the [lfs] section and its contents
	var lines []string
	inLFS := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "[lfs]" {
			inLFS = true
			continue
		}
		if inLFS {
			if strings.HasPrefix(trimmed, "[") {
				inLFS = false
			} else {
				continue
			}
		}
		lines = append(lines, line)
	}

	cleaned := strings.Join(lines, "\n")
	if cleaned != content {
		_ = os.WriteFile(configPath, []byte(cleaned), 0644)
	}
}

// FetchHeadAge returns how long ago FETCH_HEAD was last modified.
// Returns (0, false) if FETCH_HEAD doesn't exist or can't be read.
func FetchHeadAge(repoPath string) (time.Duration, bool) {
	fetchHead := filepath.Join(repoPath, ".git", "FETCH_HEAD")
	info, err := os.Stat(fetchHead)
	if err != nil {
		return 0, false
	}
	return time.Since(info.ModTime()), true
}
