package gitserver

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHasNonOauth2Userinfo(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{"oauth2 username", "https://oauth2:token@host.com/repo.git", false},
		{"deploy token", "https://deploy-token:abc@host.com/repo.git", true},
		{"bare URL (no userinfo)", "https://host.com/repo.git", false},
		{"unparseable URL", "://invalid", false},
		{"username only", "https://admin@host.com/repo.git", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, hasNonOauth2Userinfo(tt.url))
		})
	}
}

func TestGetBareRemoteURL(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	tests := []struct {
		name      string
		remoteURL string
		want      string
	}{
		{
			name:      "strips oauth2 credentials",
			remoteURL: "https://oauth2:secret@git.sageox.ai/team/ledger.git",
			want:      "https://git.sageox.ai/team/ledger.git",
		},
		{
			name:      "bare URL unchanged",
			remoteURL: "https://git.sageox.ai/team/ledger.git",
			want:      "https://git.sageox.ai/team/ledger.git",
		},
		{
			name:      "SSH URL unchanged",
			remoteURL: "git@git.sageox.ai:team/ledger.git",
			want:      "git@git.sageox.ai:team/ledger.git",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := setupGitRepoWithRemote(t, tt.remoteURL)
			got, err := GetBareRemoteURL(dir)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGetBareRemoteURL_NoRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// git repo without a remote
	dir := t.TempDir()
	require.NoError(t, exec.Command("git", "init", "--initial-branch=main", dir).Run())

	_, err := GetBareRemoteURL(dir)
	assert.Error(t, err, "should error when no remote exists")
}

func TestExtractPATFromRemote_NonOauth2Username(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// deploy-token style: has userinfo but username is not oauth2
	dir := setupGitRepoWithRemote(t, "https://deploy-token:secret@git.example.com/repo.git")
	pat, remoteURL, err := extractPATFromRemote(dir)
	require.NoError(t, err)
	assert.Empty(t, pat, "should not extract PAT from non-oauth2 username")
	assert.Contains(t, remoteURL, "deploy-token")
}

func TestExtractPATFromRemote_UsernameNoPassword(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// oauth2 username but no password
	dir := setupGitRepoWithRemote(t, "https://oauth2@git.example.com/repo.git")
	pat, _, err := extractPATFromRemote(dir)
	require.NoError(t, err)
	assert.Empty(t, pat, "should not extract PAT when no password set")
}
