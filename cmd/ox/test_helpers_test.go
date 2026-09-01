package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// skipIntegration skips tests that intentionally exercise process, git, or
// filesystem boundaries. It lives in an untagged helper file because both
// fast and full test files use it; hiding the helper behind !short makes the
// fast package fail to compile before tests can declare their own tier.
func skipIntegration(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
}

// testGitRepo initializes a temporary git repository for integration tests.
func testGitRepo(t *testing.T) string {
	t.Helper()
	skipIntegration(t)

	tmpDir := t.TempDir()
	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.name", "Test User"},
		{"git", "config", "user.email", "test@example.com"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = tmpDir
		cmd.Env = isolatedGitEnv(tmpDir)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "failed: %v: %s", args, string(out))
	}

	readmePath := filepath.Join(tmpDir, "README.md")
	require.NoError(t, os.WriteFile(readmePath, []byte("# Test\n"), 0o644), "failed to create README")
	cmd := exec.Command("git", "add", "README.md")
	cmd.Dir = tmpDir
	cmd.Env = isolatedGitEnv(tmpDir)
	require.NoError(t, cmd.Run(), "failed to git add")
	cmd = exec.Command("git", "commit", "-m", "Initial commit")
	cmd.Dir = tmpDir
	cmd.Env = isolatedGitEnv(tmpDir)
	require.NoError(t, cmd.Run(), "failed to git commit")
	return tmpDir
}

// runIsolatedGit always pins cmd.Dir and scrubs per-user git configuration.
// Tests must never alter the host's git identity.
func runIsolatedGit(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = isolatedGitEnv(dir)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// isolatedGitEnv removes repository-selection variables inherited from the
// developer shell before applying the test repository's hermetic settings.
func isolatedGitEnv(dir string) []string {
	overrides := map[string]string{
		"HOME":                dir,
		"XDG_CONFIG_HOME":     filepath.Join(dir, ".config"),
		"GIT_CONFIG_GLOBAL":   "/dev/null",
		"GIT_CONFIG_SYSTEM":   "/dev/null",
		"GIT_AUTHOR_NAME":     "Test",
		"GIT_AUTHOR_EMAIL":    "test@example.com",
		"GIT_COMMITTER_NAME":  "Test",
		"GIT_COMMITTER_EMAIL": "test@example.com",
	}
	blocked := map[string]bool{
		"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_COMMON_DIR": true, "GIT_INDEX_FILE": true,
	}
	for key := range overrides {
		blocked[key] = true
	}
	baseEnv := os.Environ() // safe: repository selectors are filtered below before any test subprocess starts
	env := make([]string, 0, len(baseEnv)+len(overrides))
	for _, entry := range baseEnv {
		key, _, _ := strings.Cut(entry, "=")
		if !blocked[key] {
			env = append(env, entry)
		}
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

func mustRunGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	out, err := runIsolatedGit(t, dir, args...)
	require.NoErrorf(t, err, "git %s: %s", strings.Join(args, " "), out)
}

// initGitRepo creates a minimal git repo in the given directory. Keep this
// helper available to every tier; callers decide whether their scenario is
// fast enough to run under testing.Short.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.name", "Dev"},
		{"git", "config", "user.email", "dev@example.com"},
		{"git", "config", "commit.gpgsign", "false"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = isolatedGitEnv(dir)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "failed: %v: %s", args, string(out))
	}

	readme := filepath.Join(dir, "README.md")
	require.NoError(t, os.WriteFile(readme, []byte("repo"), 0o644))
	cmd := exec.Command("git", "add", "README.md")
	cmd.Dir = dir
	cmd.Env = isolatedGitEnv(dir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git add: %s", string(out))
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = dir
	cmd.Env = isolatedGitEnv(dir)
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git commit: %s", string(out))
}

func TestIsolatedGitEnvDropsRepositorySelectors(t *testing.T) {
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE"} {
		t.Setenv(key, "/must/not/leak")
	}

	env := isolatedGitEnv(t.TempDir())
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		require.NotContains(t, []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE"}, key)
	}
}
