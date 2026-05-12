//go:build !short

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initTeamContextGitDir creates a temp directory with git init and user config.
func initTeamContextGitDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "team-ctx")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
	for _, kv := range [][2]string{{"user.name", "test"}, {"user.email", "test@test.com"}} {
		c := exec.Command("git", "config", kv[0], kv[1])
		c.Dir = dir
		require.NoError(t, c.Run())
	}
	return dir
}

func TestCheckGuidanceFilesForPath_MissingFiles(t *testing.T) {
	tcPath := initTeamContextGitDir(t)

	result := checkGuidanceFilesForPath(tcPath, false)

	assert.True(t, result.warning, "should warn when guidance files are missing")
	assert.Contains(t, result.message, "2 issue(s)")
}

func TestCheckGuidanceFilesForPath_FixCreatesFiles(t *testing.T) {
	tcPath := initTeamContextGitDir(t)

	result := checkGuidanceFilesForPath(tcPath, true)

	assert.True(t, result.passed, "should pass after fix")
	assert.False(t, result.warning)

	// verify files were created
	guidanceDir := filepath.Join(tcPath, "memory", "guidance")
	extractPath := filepath.Join(guidanceDir, "EXTRACT.md")
	distillPath := filepath.Join(guidanceDir, "DISTILL.md")
	assert.FileExists(t, extractPath)
	assert.FileExists(t, distillPath)

	// verify content
	extractData, err := os.ReadFile(extractPath)
	require.NoError(t, err)
	assert.Contains(t, string(extractData), "Extraction Guidance")

	distillData, err := os.ReadFile(distillPath)
	require.NoError(t, err)
	assert.Contains(t, string(distillData), "Distillation Guidance")
}

func TestCheckGuidanceFilesForPath_PassesWhenPresent(t *testing.T) {
	tcPath := initTeamContextGitDir(t)

	// create guidance files
	guidanceDir := filepath.Join(tcPath, "memory", "guidance")
	require.NoError(t, os.MkdirAll(guidanceDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(guidanceDir, "EXTRACT.md"), []byte("custom extract"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(guidanceDir, "DISTILL.md"), []byte("custom distill"), 0o644))

	result := checkGuidanceFilesForPath(tcPath, false)

	assert.True(t, result.passed, "should pass when both files exist")
	assert.False(t, result.warning)
}

func TestCheckGuidanceFilesForPath_DetectsLegacyMigration(t *testing.T) {
	tcPath := initTeamContextGitDir(t)

	// create old-style DISTILL.md at root
	require.NoError(t, os.WriteFile(filepath.Join(tcPath, "DISTILL.md"), []byte("legacy guidelines"), 0o644))

	result := checkGuidanceFilesForPath(tcPath, false)

	assert.True(t, result.warning, "should warn about migration needed")
	assert.Contains(t, result.detail, "migration")
}

func TestCheckGuidanceFilesForPath_FixMigratesLegacy(t *testing.T) {
	tcPath := initTeamContextGitDir(t)

	// commit old DISTILL.md so git operations work
	oldContent := "legacy team guidelines\ntrack security decisions"
	require.NoError(t, os.WriteFile(filepath.Join(tcPath, "DISTILL.md"), []byte(oldContent), 0o644))
	addCmd := exec.Command("git", "add", "DISTILL.md")
	addCmd.Dir = tcPath
	require.NoError(t, addCmd.Run())
	commitCmd := exec.Command("git", "commit", "-m", "add DISTILL.md")
	commitCmd.Dir = tcPath
	require.NoError(t, commitCmd.Run())

	result := checkGuidanceFilesForPath(tcPath, true)

	assert.True(t, result.passed, "should pass after migration fix")

	// verify legacy content was migrated
	guidanceDir := filepath.Join(tcPath, "memory", "guidance")
	newDistillPath := filepath.Join(guidanceDir, "DISTILL.md")
	assert.FileExists(t, newDistillPath)
	data, err := os.ReadFile(newDistillPath)
	require.NoError(t, err)
	assert.Equal(t, oldContent, string(data), "migrated file should preserve original content")

	// verify old file was removed
	assert.NoFileExists(t, filepath.Join(tcPath, "DISTILL.md"))

	// verify EXTRACT.md was also seeded
	assert.FileExists(t, filepath.Join(guidanceDir, "EXTRACT.md"))
}

func TestCheckGuidanceFilesForPath_FixDoesNotOverwrite(t *testing.T) {
	tcPath := initTeamContextGitDir(t)

	// create custom guidance files
	guidanceDir := filepath.Join(tcPath, "memory", "guidance")
	require.NoError(t, os.MkdirAll(guidanceDir, 0o755))
	customContent := "my custom guidelines"
	require.NoError(t, os.WriteFile(filepath.Join(guidanceDir, "EXTRACT.md"), []byte(customContent), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(guidanceDir, "DISTILL.md"), []byte(customContent), 0o644))

	result := checkGuidanceFilesForPath(tcPath, true)

	assert.True(t, result.passed)

	// verify custom content was NOT overwritten
	data, err := os.ReadFile(filepath.Join(guidanceDir, "EXTRACT.md"))
	require.NoError(t, err)
	assert.Equal(t, customContent, string(data), "existing content should not be overwritten")
}

func TestCheckGuidanceFiles_NoTeamContext(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	result := checkGuidanceFiles(false)

	assert.True(t, result.skipped, "should skip when no team context")
}

// TestCheckGuidanceFiles_FixDoesNotPush is the regression test for ox-ydb0.
// Background: a previous version of checkGuidanceFiles called pushTeamContext
// whenever fix=true. Because the check is FixLevelAuto, shouldFix returns true
// even when the user did not pass --fix, which meant every run of `ox doctor`
// shelled out to `git push` against the team context remote. That:
//   - made a read-only diagnostic command mutate the remote,
//   - hung the test package under high parallelism on real network I/O,
//   - blocked fast-tier CI on any machine with a daemon-discovered team context.
//
// Push propagation is the daemon's job; this check is local-only by contract.
// If anyone reintroduces a push from inside checkGuidanceFiles, this test wedges
// on the broken `origin` URL set below and fails the suite quickly.
func TestCheckGuidanceFiles_FixDoesNotPush(t *testing.T) {
	tcPath := initTeamContextGitDir(t)

	// add an unreachable origin so any attempted push would either fail fast
	// or block; either way, the test budget catches it.
	c := exec.Command("git", "remote", "add", "origin", "https://invalid.test.localhost:1/never.git")
	c.Dir = tcPath
	require.NoError(t, c.Run())

	done := make(chan checkResult, 1)
	go func() { done <- checkGuidanceFilesForPath(tcPath, true) }()

	select {
	case result := <-done:
		assert.True(t, result.passed, "guidance check should pass after local seed")
		assert.False(t, result.warning, "guidance check should not warn — push must not be attempted")
	case <-time.After(5 * time.Second):
		t.Fatal("guidance check exceeded 5s budget — a network push was likely reintroduced")
	}
}
