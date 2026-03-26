package gitserver

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepoNameFromURL_EdgeCases(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"host-only HTTPS URL", "https://git.example.com", ""},
		{"trailing slash", "https://git.example.com/", ""},
		{"SSH with nested path", "git@host:org/sub/repo.git", "repo"},
		{"SSH with no colon path", "git@host", ""},
		{"URL with port", "https://git.example.com:8443/org/repo.git", "repo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := repoNameFromURL(tt.url)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDefaultCheckoutPath_EmptyWorkDir(t *testing.T) {
	// when workDir is empty, uses os.Getwd() — result is cwd parent + repoName
	got := DefaultCheckoutPath("ledger", "")
	assert.NotEmpty(t, got)
	assert.True(t, strings.HasSuffix(got, "ledger"),
		"expected path to end with 'ledger', got %s", got)
}

func TestCacheFilesTracked(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := setupGitRepoWithSageox(t)

	// no cache files tracked initially
	assert.False(t, CacheFilesTracked(dir), "should report false when no cache files tracked")

	// commit a cache file
	cacheDir := filepath.Join(dir, ".sageox", "cache")
	require.NoError(t, os.MkdirAll(cacheDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "sync-state.json"), []byte("{}"), 0644))
	runGit(t, dir, "add", "-f", ".sageox/cache/sync-state.json")
	runGit(t, dir, "commit", "-m", "accidentally commit cache file")

	assert.True(t, CacheFilesTracked(dir), "should report true when cache files are tracked")
}

func TestCacheFilesTracked_NotGitRepo(t *testing.T) {
	dir := t.TempDir()
	// not a git repo — should return false without error
	assert.False(t, CacheFilesTracked(dir))
}

func TestWriteAskpassScript_TokenWithSingleQuotes(t *testing.T) {
	// single quotes in the token must be escaped properly
	token := "token'with'quotes"
	path, err := writeAskpassScript(token)
	require.NoError(t, err)
	defer os.Remove(path)

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	// the script should contain escaped quotes, not break shell syntax
	assert.Contains(t, string(content), "#!/bin/sh")

	// verify the script actually runs and outputs the token
	out, err := exec.Command("sh", path).Output()
	require.NoError(t, err)
	assert.Equal(t, token, strings.TrimSpace(string(out)),
		"askpass script should output the original token")
}

func TestWriteAskpassScript_EmptyToken(t *testing.T) {
	path, err := writeAskpassScript("")
	require.NoError(t, err)
	defer os.Remove(path)

	out, err := exec.Command("sh", path).Output()
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(string(out)))
}
