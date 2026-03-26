package gitserver

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/manifest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateTeamContextClone_CoreFilesPresent(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SOUL.md"), []byte("# Soul"), 0644))

	// should not warn when at least one core file exists
	ValidateTeamContextClone(dir, nil)
}

func TestValidateTeamContextClone_NoCoreFiles(t *testing.T) {
	dir := t.TempDir()
	// empty dir — warns but does not error
	ValidateTeamContextClone(dir, nil)
}

func TestValidateTeamContextClone_WithMemoryDir(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "TEAM.md"), []byte("# Team"), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "memory"), 0755))

	ValidateTeamContextClone(dir, nil)
}

func TestValidateTeamContextClone_DeniedPathExists(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("# Memory"), 0644))

	// create a path that should have been denied
	deniedDir := filepath.Join(dir, "secrets")
	require.NoError(t, os.MkdirAll(deniedDir, 0755))

	cfg := &manifest.ManifestConfig{
		Denies: []string{"secrets/"},
	}

	// should warn about denied path but not error
	ValidateTeamContextClone(dir, cfg)

	// verify the path still exists (validation is read-only)
	_, err := os.Stat(deniedDir)
	assert.NoError(t, err, "validation should not remove denied paths")
}

func TestValidateTeamContextClone_DeniedPathNotPresent(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SOUL.md"), []byte("# Soul"), 0644))

	cfg := &manifest.ManifestConfig{
		Denies: []string{"secrets/", "private/"},
	}

	// should not warn when denied paths don't exist
	ValidateTeamContextClone(dir, cfg)
}
