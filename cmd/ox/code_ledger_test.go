package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveLedgerCodeDBDir_Exists(t *testing.T) {
	projectRoot := t.TempDir()
	xdgData := t.TempDir()

	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_DATA_HOME", xdgData)

	// create .sageox/config.json with repo_id and endpoint
	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	cfg := map[string]string{
		"repo_id":  "repo_01abc123",
		"endpoint": "https://sageox.ai",
	}
	cfgData, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), cfgData, 0644))

	// compute and create the ledger index dir on disk with metadata.db
	ledgerDir := paths.CodeDBLedgerDir("repo_01abc123", "https://sageox.ai")
	require.NotEmpty(t, ledgerDir, "CodeDBLedgerDir must return a non-empty path")
	require.NoError(t, os.MkdirAll(ledgerDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(ledgerDir, "metadata.db"), []byte("fake"), 0644))

	got := resolveLedgerCodeDBDir(projectRoot)
	assert.Equal(t, ledgerDir, got)
}

func TestResolveLedgerCodeDBDir_EmptyDir_NotValid(t *testing.T) {
	projectRoot := t.TempDir()
	xdgData := t.TempDir()

	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_DATA_HOME", xdgData)

	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	cfg := map[string]string{
		"repo_id":  "repo_01abc123",
		"endpoint": "https://sageox.ai",
	}
	cfgData, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), cfgData, 0644))

	// create ledger dir but WITHOUT metadata.db (simulates failed BuildLedgerIndex)
	ledgerDir := paths.CodeDBLedgerDir("repo_01abc123", "https://sageox.ai")
	require.NoError(t, os.MkdirAll(ledgerDir, 0755))

	got := resolveLedgerCodeDBDir(projectRoot)
	assert.Empty(t, got, "empty ledger dir without metadata.db should not be treated as valid")
}

func TestResolveLedgerCodeDBDir_Missing_FallsBack(t *testing.T) {
	projectRoot := t.TempDir()
	xdgData := t.TempDir()

	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_DATA_HOME", xdgData)

	// create .sageox/config.json but do NOT create the ledger index dir on disk
	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	cfg := map[string]string{
		"repo_id":  "repo_01abc123",
		"endpoint": "https://sageox.ai",
	}
	cfgData, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), cfgData, 0644))

	got := resolveLedgerCodeDBDir(projectRoot)
	assert.Empty(t, got, "should return empty when ledger index dir does not exist on disk")
}

func TestResolveLedgerCodeDBDir_NoProjectConfig_FallsBack(t *testing.T) {
	// bare temp dir with no .sageox/ — not a SageOx project
	projectRoot := t.TempDir()

	got := resolveLedgerCodeDBDir(projectRoot)
	assert.Empty(t, got, "should return empty for non-SageOx project without panicking")
}

// --- resolvePreferredCodeDBDir preference logic ---

func TestResolvePreferredCodeDBDir_PrefersShared(t *testing.T) {
	projectRoot := t.TempDir()
	xdgData := t.TempDir()

	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_DATA_HOME", xdgData)

	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	cfg := map[string]string{
		"repo_id":  "repo_01abc123",
		"endpoint": "https://sageox.ai",
	}
	cfgData, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), cfgData, 0644))

	// create BOTH shared and ledger dirs on disk, with metadata.db in shared
	sharedDir := paths.CodeDBSharedDir("repo_01abc123", "https://sageox.ai")
	ledgerDir := paths.CodeDBLedgerDir("repo_01abc123", "https://sageox.ai")
	require.NoError(t, os.MkdirAll(sharedDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(sharedDir, "metadata.db"), []byte("fake"), 0644))
	require.NoError(t, os.MkdirAll(ledgerDir, 0755))

	got, useLedger := resolvePreferredCodeDBDir(projectRoot)
	assert.Equal(t, sharedDir, got, "should prefer shared CodeDB when it exists")
	assert.False(t, useLedger, "should not use ledger when shared exists")
}

func TestResolvePreferredCodeDBDir_FallsBackToLedger(t *testing.T) {
	projectRoot := t.TempDir()
	xdgData := t.TempDir()

	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_DATA_HOME", xdgData)

	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	cfg := map[string]string{
		"repo_id":  "repo_01abc123",
		"endpoint": "https://sageox.ai",
	}
	cfgData, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), cfgData, 0644))

	// create ONLY ledger dir with metadata.db — shared does not have an index
	ledgerDir := paths.CodeDBLedgerDir("repo_01abc123", "https://sageox.ai")
	require.NoError(t, os.MkdirAll(ledgerDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(ledgerDir, "metadata.db"), []byte("fake"), 0644))

	got, useLedger := resolvePreferredCodeDBDir(projectRoot)
	assert.Equal(t, ledgerDir, got, "should fall back to ledger when shared does not exist")
	assert.True(t, useLedger, "should report ledger fallback")
}

func TestResolvePreferredCodeDBDir_NeitherExists(t *testing.T) {
	projectRoot := t.TempDir()
	xdgData := t.TempDir()

	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_DATA_HOME", xdgData)

	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	cfg := map[string]string{
		"repo_id":  "repo_01abc123",
		"endpoint": "https://sageox.ai",
	}
	cfgData, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), cfgData, 0644))

	// neither shared nor ledger dir exists
	sharedDir := paths.CodeDBSharedDir("repo_01abc123", "https://sageox.ai")

	got, useLedger := resolvePreferredCodeDBDir(projectRoot)
	assert.Equal(t, sharedDir, got, "should return shared path even if it doesn't exist")
	assert.False(t, useLedger, "should not report ledger fallback")
}
