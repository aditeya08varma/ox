package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOMPMarkerUsesUniqueNamespace(t *testing.T) {
	assert.Contains(t, ompPrimeMarkerStart, ":omp:")
	assert.NotEqual(t, "<!-- ox:prime:start -->", ompPrimeMarkerStart)
}

func TestInstallHooksUsesNativeOMPContext(t *testing.T) {
	repo := t.TempDir()
	rootAgents := "# Project rules\n"
	require.NoError(t, os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte(rootAgents), 0o644))

	resp, err := handleInstallHooks(adapterprotocol.HookParams{RepoRoot: repo, Scope: "project"})
	require.NoError(t, err)
	assert.True(t, resp.Installed)

	data, err := os.ReadFile(filepath.Join(repo, ".omp", "AGENTS.md"))
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "ox agent prime --agent omp")
	assert.Contains(t, content, "@../AGENTS.md")
	assert.Less(t, strings.Index(content, "ox agent prime --agent omp"), strings.Index(content, "@../AGENTS.md"),
		"OMP identity must be established before imported generic prime instructions")

	unchanged, err := os.ReadFile(filepath.Join(repo, "AGENTS.md"))
	require.NoError(t, err)
	assert.Equal(t, rootAgents, string(unchanged))
}

func TestInstallHooksPreservesExistingNativeContext(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".omp"), 0o755))
	native := "# Existing OMP rules\n"
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".omp", "AGENTS.md"), []byte(native), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("# Root rules\n"), 0o644))

	_, err := handleInstallHooks(adapterprotocol.HookParams{RepoRoot: repo, Scope: "project"})
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(repo, ".omp", "AGENTS.md"))
	require.NoError(t, err)
	content := string(data)
	assert.True(t, strings.HasPrefix(content, ompPrimeMarkerStart))
	assert.Contains(t, content, native)
	assert.NotContains(t, content, "@../AGENTS.md",
		"an existing native context file intentionally owns OMP discovery")
}

func TestInstallHooksIsIdempotent(t *testing.T) {
	repo := t.TempDir()
	params := adapterprotocol.HookParams{RepoRoot: repo, Scope: "project"}
	_, err := handleInstallHooks(params)
	require.NoError(t, err)
	_, err = handleInstallHooks(params)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(repo, ".omp", "AGENTS.md"))
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(data), ompPrimeMarkerStart))
}

func TestUninstallHooksRemovesOwnedScaffold(t *testing.T) {
	repo := t.TempDir()
	params := adapterprotocol.HookParams{RepoRoot: repo, Scope: "project"}
	_, err := handleInstallHooks(params)
	require.NoError(t, err)
	_, err = handleUninstallHooks(params)
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(repo, ".omp", "AGENTS.md"))
	assert.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(filepath.Join(repo, ".omp"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestUninstallHooksPreservesUserContent(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".omp"), 0o755))
	native := "# Existing OMP rules\n"
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".omp", "AGENTS.md"), []byte(native), 0o644))
	params := adapterprotocol.HookParams{RepoRoot: repo, Scope: "project"}
	_, err := handleInstallHooks(params)
	require.NoError(t, err)
	_, err = handleUninstallHooks(params)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(repo, ".omp", "AGENTS.md"))
	require.NoError(t, err)
	assert.Equal(t, native, string(data))
}

func TestInstallHooksRejectsUserScope(t *testing.T) {
	resp, err := handleInstallHooks(adapterprotocol.HookParams{RepoRoot: t.TempDir(), Scope: "user"})
	require.Error(t, err)
	assert.False(t, resp.Installed)
}
