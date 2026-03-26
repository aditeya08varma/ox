package gitserver

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateAgentsMDContent(t *testing.T) {
	tests := []struct {
		name     string
		opts     *AgentsMDOptions
		contains []string // substrings that must appear
	}{
		{
			name: "nil opts uses defaults",
			opts: nil,
			contains: []string{
				"SageOx Repository",
				"**Ledger**",
				"(not linked)",
				"(personal)",
			},
		},
		{
			name: "ledger repo type",
			opts: &AgentsMDOptions{
				RepoURL:  "https://git.example.com/org/project.git",
				TeamID:   "team_abc123",
				Endpoint: "https://test.sageox.ai",
				RepoType: "ledger",
			},
			contains: []string{
				"**Ledger**",
				"https://git.example.com/org/project.git",
				"team_abc123",
				"https://test.sageox.ai",
			},
		},
		{
			name: "team-context repo type",
			opts: &AgentsMDOptions{
				RepoURL:  "https://git.example.com/teams/alpha.git",
				TeamID:   "team_xyz",
				Endpoint: "https://sageox.ai",
				RepoType: "team-context",
			},
			contains: []string{
				"**Team Context**",
				"https://git.example.com/teams/alpha.git",
				"team_xyz",
			},
		},
		{
			name: "empty repo type defaults to ledger",
			opts: &AgentsMDOptions{
				RepoType: "",
			},
			contains: []string{
				"**Ledger**",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := generateAgentsMDContent(tt.opts)
			for _, s := range tt.contains {
				assert.Contains(t, content, s, "missing expected substring")
			}
		})
	}
}

func TestCreateAgentsMD_SkipsExisting(t *testing.T) {
	dir := t.TempDir()
	agentsPath := filepath.Join(dir, "AGENTS.md")

	// pre-create the file
	require.NoError(t, os.WriteFile(agentsPath, []byte("existing content"), 0644))

	err := CreateAgentsMD(context.Background(), dir, nil)
	require.NoError(t, err)

	// verify original content preserved
	content, err := os.ReadFile(agentsPath)
	require.NoError(t, err)
	assert.Equal(t, "existing content", string(content))
}

func TestCreateAgentsMD_WritesFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	require.NoError(t, exec.Command("git", "init", "--initial-branch=main", dir).Run())
	require.NoError(t, exec.Command("git", "-C", dir, "config", "user.name", "test").Run())
	require.NoError(t, exec.Command("git", "-C", dir, "config", "user.email", "test@test.com").Run())

	opts := &AgentsMDOptions{
		RepoURL:  "https://git.example.com/org/repo.git",
		TeamID:   "team_test",
		Endpoint: "https://test.sageox.ai",
		RepoType: "ledger",
	}
	err := CreateAgentsMD(context.Background(), dir, opts)
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "**Ledger**")
	assert.Contains(t, string(content), "team_test")
}

func TestCommitAgentsMD_NothingToCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	require.NoError(t, exec.Command("git", "init", "--initial-branch=main", dir).Run())
	require.NoError(t, exec.Command("git", "-C", dir, "config", "user.name", "test").Run())
	require.NoError(t, exec.Command("git", "-C", dir, "config", "user.email", "test@test.com").Run())

	// write AGENTS.md and commit it manually first
	require.NoError(t, os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("test"), 0644))
	require.NoError(t, exec.Command("git", "-C", dir, "add", "AGENTS.md").Run())
	require.NoError(t, exec.Command("git", "-C", dir, "commit", "-m", "initial").Run())

	// calling commit again should be a no-op (nothing to commit, exit 1)
	err := commitAgentsMD(context.Background(), dir)
	assert.NoError(t, err, "should handle nothing-to-commit gracefully")
}
