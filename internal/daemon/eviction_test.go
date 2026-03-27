package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTestRegistry writes a registry file with the given entries into the XDG runtime dir.
func writeTestRegistry(t *testing.T, tmpDir string, entries map[string]DaemonInfo) {
	t.Helper()
	reg := &Registry{Daemons: entries}
	data, err := json.MarshalIndent(reg, "", "  ")
	require.NoError(t, err)
	regDir := filepath.Join(tmpDir, "sageox", "daemon")
	require.NoError(t, os.MkdirAll(regDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(regDir, "registry.json"), data, 0600))
}

func TestSuperseded_NotSuperseded_WhenOurPIDRegistered(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	wsID := CurrentWorkspaceID()
	writeTestRegistry(t, tmpDir, map[string]DaemonInfo{
		wsID: {WorkspaceID: wsID, PID: os.Getpid()},
	})

	assert.False(t, superseded(), "should not be superseded when our PID is registered")
}

func TestSuperseded_Superseded_WhenDifferentPIDRegistered(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	wsID := CurrentWorkspaceID()
	writeTestRegistry(t, tmpDir, map[string]DaemonInfo{
		wsID: {WorkspaceID: wsID, PID: os.Getpid() + 99999},
	})

	assert.True(t, superseded(), "should be superseded when different PID is registered")
}

func TestSuperseded_NotSuperseded_WhenEntryMissing(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	// empty registry — entry missing could be a race, don't evict
	writeTestRegistry(t, tmpDir, map[string]DaemonInfo{})

	assert.False(t, superseded(), "should not evict on missing entry (could be registry race)")
}

func TestSuperseded_NotSuperseded_WhenRegistryMissing(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	// no registry file at all — can't determine, assume not superseded
	assert.False(t, superseded(), "should not evict when registry is missing")
}
