package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sandboxSessionCache redirects paths.CacheDir() into a fresh temp dir for
// the life of the test, so session store fixtures never touch the
// developer's real ~/.cache/sageox. XDG_CACHE_HOME is set directly (rather
// than cleared to fall back through HOME) because paths.getHomeDir() caches
// os.UserHomeDir() behind a sync.Once for the life of the test binary --
// relying on a HOME override alone would only take effect if this happened
// to be the first test in the package to resolve a path, which isn't
// guaranteed.
func sandboxSessionCache(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

// newTestSessionListCmd builds an isolated 'session list' command with its
// own root (carrying the persistent --json flag runSessionList reads via
// cmd.Root()), instead of reusing the package-level sessionListCmd/rootCmd
// singletons that every other test in this package's binary also mutates.
func newTestSessionListCmd() *cobra.Command {
	root := &cobra.Command{Use: "ox"}
	root.PersistentFlags().Bool("json", false, "")

	cmd := &cobra.Command{Use: "list"}
	cmd.Flags().Int("limit", 10, "")
	cmd.Flags().Bool("all", false, "")
	cmd.Flags().String("repo", "", "")
	root.AddCommand(cmd)

	return cmd
}

// --- newSessionStoreForRepoPath ---

func TestNewSessionStoreForRepoPath_ValidRepo(t *testing.T) {
	sandboxSessionCache(t)
	dir := createInitializedProjectWithConfig(t, &config.ProjectConfig{RepoID: "repo_test_valid_0001"})

	store, resolved, err := newSessionStoreForRepoPath(dir)
	require.NoError(t, err)
	require.NotNil(t, store)

	wantAbs, err := filepath.Abs(dir)
	require.NoError(t, err)
	assert.Equal(t, wantAbs, resolved)
}

// TestNewSessionStoreForRepoPath_NoSageoxDir_HardErrors proves the top risk
// flagged for this feature: an unconfigured --repo path must hard-error, not
// silently resolve to the shared base sessions directory that
// paths.SessionCacheDir returns for an empty repo ID.
func TestNewSessionStoreForRepoPath_NoSageoxDir_HardErrors(t *testing.T) {
	sandboxSessionCache(t)
	dir := t.TempDir() // no .sageox/ at all

	store, resolved, err := newSessionStoreForRepoPath(dir)
	require.Error(t, err)
	assert.Nil(t, store)
	assert.Empty(t, resolved)
}

// TestNewSessionStoreForRepoPath_SageoxDirNoConfig_HardErrors covers the
// specific shape LoadProjectConfig treats as "present but uninitialized": a
// .sageox/ directory that exists with no config.json/config.yaml inside.
// getRepoIDOrDefault (the cwd path used by newSessionStore) papers over this
// with the shared "default" bucket; newSessionStoreForRepoPath must not.
func TestNewSessionStoreForRepoPath_SageoxDirNoConfig_HardErrors(t *testing.T) {
	sandboxSessionCache(t)
	dir := createInitializedProject(t) // .sageox/ exists, no config.json

	store, _, err := newSessionStoreForRepoPath(dir)
	require.Error(t, err)
	assert.Nil(t, store)
}

// --- runSessionList --repo ---

// TestRunSessionList_RepoFlag_ListsTargetNotCwd is the core cross-repo
// scenario this feature exists for: a caller sitting in repo A wants
// sessions for repo B without cd-ing there. Failure mode prevented: --repo
// being ignored, or silently listing the cwd's sessions instead of the
// named repo's.
func TestRunSessionList_RepoFlag_ListsTargetNotCwd(t *testing.T) {
	sandboxSessionCache(t)
	t.Setenv(config.EnvProjectRoot, "")

	dirA := createInitializedProjectWithConfig(t, &config.ProjectConfig{RepoID: "repo_test_cwd_a"})
	dirB := createInitializedProjectWithConfig(t, &config.ProjectConfig{RepoID: "repo_test_target_b"})

	createSessionInDir(t, session.GetContextPath("repo_test_cwd_a"), "2026-01-01T00-00-testuser-OxAAAA")
	createSessionInDir(t, session.GetContextPath("repo_test_target_b"), "2026-01-02T00-00-testuser-OxBBBB")

	t.Chdir(dirA) // cwd is repo A; --repo below asks for repo B

	cmd := newTestSessionListCmd()
	require.NoError(t, cmd.Flags().Set("repo", dirB))
	require.NoError(t, cmd.Flags().Set("all", "true"))
	require.NoError(t, cmd.Root().PersistentFlags().Set("json", "true"))
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err := runSessionList(cmd, nil)
	require.NoError(t, err)

	var out sessionListOutput
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out))

	assert.Equal(t, "repo_test_target_b", out.RepoID, "must resolve repo id from --repo path, not cwd")
	assert.Equal(t, filepath.Base(dirB), out.RepoName)
	require.Len(t, out.Sessions, 1, "must list only the --repo target's session")
	assert.Equal(t, "2026-01-02T00-00-testuser-OxBBBB", out.Sessions[0].Name)
}

// TestRunSessionList_RepoFlag_MissingConfigHardErrors proves --repo does NOT
// fall back to the shared bucket paths.SessionCacheDir("") returns for an
// empty repo ID -- that fallback would leak unrelated sessions under the
// name of the repo the caller asked for.
func TestRunSessionList_RepoFlag_MissingConfigHardErrors(t *testing.T) {
	sandboxSessionCache(t)
	t.Setenv(config.EnvProjectRoot, "")

	// plant a session in the shared bucket. If --repo ever silently fell
	// back to it (the bug this test exists to prevent), it would show up
	// in the output asserted below.
	createSessionInDir(t, session.GetContextPath(""), "2026-01-03T00-00-eve-OxLEAK")

	unconfigured := createInitializedProject(t) // .sageox/ exists, no config.json

	cmd := newTestSessionListCmd()
	require.NoError(t, cmd.Flags().Set("repo", unconfigured))
	require.NoError(t, cmd.Root().PersistentFlags().Set("json", "true"))
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err := runSessionList(cmd, nil)
	require.Error(t, err)
	assert.Empty(t, buf.String(), "hard error must produce no output, including no leaked shared-bucket session")
}

// TestRunSessionList_NoRepoFlag_UsesCwd is the regression baseline: omitting
// --repo must behave exactly as it did before this feature existed.
func TestRunSessionList_NoRepoFlag_UsesCwd(t *testing.T) {
	sandboxSessionCache(t)
	t.Setenv(config.EnvProjectRoot, "")

	dir := createInitializedProjectWithConfig(t, &config.ProjectConfig{RepoID: "repo_test_cwd_only"})
	createSessionInDir(t, session.GetContextPath("repo_test_cwd_only"), "2026-01-04T00-00-testuser-OxCCCC")

	t.Chdir(dir)

	cmd := newTestSessionListCmd()
	require.NoError(t, cmd.Flags().Set("all", "true"))
	require.NoError(t, cmd.Root().PersistentFlags().Set("json", "true"))
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err := runSessionList(cmd, nil)
	require.NoError(t, err)

	var out sessionListOutput
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out))
	assert.Equal(t, "repo_test_cwd_only", out.RepoID)
	require.Len(t, out.Sessions, 1)
	assert.Equal(t, "2026-01-04T00-00-testuser-OxCCCC", out.Sessions[0].Name)
}
