package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. Local clone with bare remote ---

// TestLocalCloneWithBareRemote verifies the core sandbox primitive:
// clone a repo locally, create a bare remote, push to it.
// Failure prevented: sandbox setup fails silently, leaving distill
// writing to the real team context.
func TestLocalCloneWithBareRemote(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}

	// create a source repo with some content
	srcDir := t.TempDir()
	gitInit(t, srcDir)
	writeAndCommit(t, srcDir, "memory/daily/2026-03-10.md", "observation one\n", "add daily")
	writeAndCommit(t, srcDir, "memory/guidance/EXTRACT.md", "extract guidance\n", "add extract")
	writeAndCommit(t, srcDir, "SOUL.md", "# Soul\n", "add soul")

	cloneDir := filepath.Join(t.TempDir(), "clone")
	bareDir := filepath.Join(t.TempDir(), "remote.git")

	err := localCloneWithBareRemote(srcDir, cloneDir, bareDir)
	require.NoError(t, err)

	// clone should have all files
	require.FileExists(t, filepath.Join(cloneDir, "memory", "daily", "2026-03-10.md"))
	require.FileExists(t, filepath.Join(cloneDir, "memory", "guidance", "EXTRACT.md"))
	require.FileExists(t, filepath.Join(cloneDir, "SOUL.md"))

	// remote should point to bare repo, not source
	cmd := exec.Command("git", "-C", cloneDir, "remote", "get-url", "origin")
	out, err := cmd.Output()
	require.NoError(t, err)
	assert.Contains(t, string(out), "remote.git")

	// push should work (to bare)
	writeAndCommit(t, cloneDir, "memory/daily/2026-03-11.md", "observation two\n", "add day 2")
	cmd = exec.Command("git", "-C", cloneDir, "push")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "push to bare remote should succeed: %s", string(out))

	// verify bare received the push (clone from bare and check)
	verifyDir := filepath.Join(t.TempDir(), "verify")
	cmd = exec.Command("git", "clone", bareDir, verifyDir)
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "clone from bare: %s", string(out))
	require.FileExists(t, filepath.Join(verifyDir, "memory", "daily", "2026-03-11.md"))
}

// --- B. Guidance override ---

// TestOverrideGuidance verifies that sandbox guidance overrides replace
// the original files in the sandbox clone.
// Failure prevented: guidance override silently ignored, causing sandbox
// to use the original guidance and produce identical output.
func TestOverrideGuidance(t *testing.T) {
	tcDir := t.TempDir()
	guidanceDir := filepath.Join(tcDir, "memory", "guidance")
	require.NoError(t, os.MkdirAll(guidanceDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(guidanceDir, "EXTRACT.md"), []byte("original"), 0o644))

	// create override file
	overrideFile := filepath.Join(t.TempDir(), "my-extract.md")
	require.NoError(t, os.WriteFile(overrideFile, []byte("experimental v2"), 0o644))

	err := overrideGuidance(tcDir, "EXTRACT.md", overrideFile)
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(guidanceDir, "EXTRACT.md"))
	require.NoError(t, err)
	assert.Equal(t, "experimental v2", string(content))
}

// --- C. Full sandbox creation ---

// TestCreateDistillSandbox verifies the complete sandbox lifecycle:
// team context clone, ledger clone, guidance overrides, push isolation.
// Failure prevented: sandbox leaks writes to real repos.
func TestCreateDistillSandbox(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}

	// create fake "real" team context repo
	realTCDir := t.TempDir()
	gitInit(t, realTCDir)
	writeAndCommit(t, realTCDir, "SOUL.md", "# Real Soul\n", "init")
	writeAndCommit(t, realTCDir, "memory/guidance/EXTRACT.md", "real extract\n", "guidance")
	writeAndCommit(t, realTCDir, "memory/guidance/DISTILL.md", "real distill\n", "guidance")
	writeAndCommit(t, realTCDir, "memory/.discussion-facts/d1.jsonl", `{"fact":"one"}`+"\n", "fact")

	// create fake "real" ledger repo
	realLedgerDir := t.TempDir()
	gitInit(t, realLedgerDir)
	writeAndCommit(t, realLedgerDir, "sessions/s1/summary.json", `{"title":"test"}`, "session")

	// create override files
	extractOverride := filepath.Join(t.TempDir(), "extract-v2.md")
	require.NoError(t, os.WriteFile(extractOverride, []byte("experimental extract v2"), 0o644))

	realTC := &config.TeamContext{TeamID: "team_test", TeamName: "Test Team", Path: realTCDir}
	realRepos := []distillRepo{{
		ProjectRoot: "/fake/project",
		RepoID:      "repo_123",
		LedgerPath:  realLedgerDir,
		CodeDBDir:   "/fake/codedb",
	}}

	sb, cleanup, err := createDistillSandbox(realTC, realRepos, extractOverride, "")
	require.NoError(t, err)
	defer cleanup()

	// sandbox TC should exist with overridden guidance
	require.FileExists(t, filepath.Join(sb.TC.Path, "SOUL.md"))

	extractContent, err := os.ReadFile(filepath.Join(sb.TC.Path, "memory", "guidance", "EXTRACT.md"))
	require.NoError(t, err)
	assert.Equal(t, "experimental extract v2", string(extractContent))

	// original DISTILL.md should be preserved (no override)
	distillContent, err := os.ReadFile(filepath.Join(sb.TC.Path, "memory", "guidance", "DISTILL.md"))
	require.NoError(t, err)
	assert.Equal(t, "real distill\n", string(distillContent))

	// sandbox TC remote should NOT point to the real repo
	cmd := exec.Command("git", "-C", sb.TC.Path, "remote", "get-url", "origin")
	out, err := cmd.Output()
	require.NoError(t, err)
	assert.NotContains(t, string(out), realTCDir, "sandbox must not point to real TC")

	// sandbox ledger should exist
	require.Len(t, sb.Repos, 1)
	require.FileExists(t, filepath.Join(sb.Repos[0].LedgerPath, "sessions", "s1", "summary.json"))

	// CodeDB should be shared (read-only), not cloned
	assert.Equal(t, "/fake/codedb", sb.Repos[0].CodeDBDir)

	// push from sandbox TC should work (to bare remote, not real)
	writeAndCommit(t, sb.TC.Path, "memory/daily/sandbox-test.md", "sandbox output\n", "sandbox distill")
	cmd = exec.Command("git", "-C", sb.TC.Path, "push")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "sandbox push should succeed: %s", string(out))

	// real TC should NOT have the sandbox file
	_, err = os.Stat(filepath.Join(realTCDir, "memory", "daily", "sandbox-test.md"))
	assert.True(t, os.IsNotExist(err), "real TC must not have sandbox output")
}

// --- helpers ---

func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.name", "test"},
		{"config", "user.email", "test@test.local"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, string(out))
	}
}

func writeAndCommit(t *testing.T, repoDir, relPath, content, msg string) {
	t.Helper()
	absPath := filepath.Join(repoDir, relPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(absPath), 0o755))
	require.NoError(t, os.WriteFile(absPath, []byte(content), 0o644))

	cmd := exec.Command("git", "-C", repoDir, "add", relPath)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git add: %s", string(out))

	cmd = exec.Command("git", "-C", repoDir, "commit", "-m", msg)
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git commit: %s", string(out))
}
