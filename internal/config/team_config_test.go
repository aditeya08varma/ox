package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadTeamConfig_EmptyPath(t *testing.T) {
	t.Parallel()
	cfg, err := LoadTeamConfig("")
	require.NoError(t, err)
	assert.Nil(t, cfg, "empty path should return nil without error")
}

func TestLoadTeamConfig_MissingFile(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	cfg, err := LoadTeamConfig(tmpDir)
	require.NoError(t, err, "missing file should not be an error")
	assert.Nil(t, cfg, "missing file should return nil config")
}

func TestLoadTeamConfig_ParsesKnownFields(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	raw := `session_recording = "auto"
session_notification = "heads up"
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, teamConfigFilename), []byte(raw), 0644))

	cfg, err := LoadTeamConfig(tmpDir)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, "auto", cfg.SessionRecording)
	assert.Equal(t, "heads up", cfg.SessionNotification)
}

func TestTeamConfig_PRVisualsRoundTrip(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	rich := false
	theme := PRVisualsThemeDark
	require.NoError(t, SaveTeamConfig(tmpDir, &TeamConfig{PRVisuals: &PRVisualsConfig{
		Rich:  &rich,
		Theme: &theme,
	}}))

	cfg, err := LoadTeamConfig(tmpDir)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.NotNil(t, cfg.PRVisuals)
	require.NotNil(t, cfg.PRVisuals.Rich)
	require.NotNil(t, cfg.PRVisuals.Theme)
	assert.False(t, *cfg.PRVisuals.Rich)
	assert.Equal(t, PRVisualsThemeDark, *cfg.PRVisuals.Theme)
}

// TestLoadTeamConfig_SilentlyDropsStrayTimezoneKey verifies that an older
// team config.toml carrying a top-level `timezone` key still loads cleanly
// after the field was removed from TeamConfig during the team-timezone revert.
// Unrelated fields must continue to round-trip through the loader.
//
// Failure prevented: a future change to LoadTeamConfig (e.g. switching to a
// strict decoder that rejects unknown keys) would break older team configs on
// disk without warning. Doctor's timezone-scrub auto-fix depends on the loader
// staying permissive until the scrub runs.
func TestLoadTeamConfig_SilentlyDropsStrayTimezoneKey(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	raw := `# team config header
timezone = "America/New_York"
session_recording = "auto"
session_notification = "keep going"
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, teamConfigFilename), []byte(raw), 0644))

	cfg, err := LoadTeamConfig(tmpDir)
	require.NoError(t, err, "stray timezone key must not make loader error")
	require.NotNil(t, cfg)

	assert.Equal(t, "auto", cfg.SessionRecording)
	assert.Equal(t, "keep going", cfg.SessionNotification)
}
