package config

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPRVisualsResolutionHonorsUserRepoTeamAndDefaults(t *testing.T) {
	t.Setenv("OX_USER_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))

	// No preferences means the GitHub-compatible default: rich visuals in light theme.
	assert.True(t, PRVisualsRich(""))
	assert.Equal(t, PRVisualsThemeLight, PRVisualsTheme(""))

	projectRoot := CreateInitializedProjectWithConfig(t, &ProjectConfig{TeamID: "team_visuals"})
	teamPath := t.TempDir()
	require.NoError(t, SaveLocalConfig(projectRoot, &LocalConfig{TeamContexts: []TeamContext{{
		TeamID: "team_visuals",
		Path:   teamPath,
	}}}))

	teamRich := false
	teamTheme := PRVisualsThemeDark
	require.NoError(t, SaveTeamConfig(teamPath, &TeamConfig{PRVisuals: &PRVisualsConfig{
		Rich:  &teamRich,
		Theme: &teamTheme,
	}}))
	assert.False(t, PRVisualsRich(projectRoot), "team policy should apply when higher scopes are unset")
	assert.Equal(t, PRVisualsThemeDark, PRVisualsTheme(projectRoot))

	repoCfg, err := LoadProjectConfig(projectRoot)
	require.NoError(t, err)
	repoRich := true
	repoTheme := PRVisualsThemeLight
	repoCfg.PRVisuals = &PRVisualsConfig{Rich: &repoRich, Theme: &repoTheme}
	require.NoError(t, SaveProjectConfig(projectRoot, repoCfg))
	assert.True(t, PRVisualsRich(projectRoot), "repo should override team")
	assert.Equal(t, PRVisualsThemeLight, PRVisualsTheme(projectRoot))

	userRich := false
	userTheme := PRVisualsThemeDark
	require.NoError(t, SaveUserConfig(&UserConfig{PRVisuals: &PRVisualsConfig{
		Rich:  &userRich,
		Theme: &userTheme,
	}}))
	assert.False(t, PRVisualsRich(projectRoot), "personal preference should override repo")
	assert.Equal(t, PRVisualsThemeDark, PRVisualsTheme(projectRoot))
}

func TestPRVisualsConfigIsEmpty(t *testing.T) {
	assert.True(t, (*PRVisualsConfig)(nil).IsEmpty())
	assert.True(t, (&PRVisualsConfig{}).IsEmpty())
	rich := true
	assert.False(t, (&PRVisualsConfig{Rich: &rich}).IsEmpty())
}
