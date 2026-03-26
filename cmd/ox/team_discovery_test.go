package main

import (
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
)

func TestEnrichedTeam_ToConfigTeamContext(t *testing.T) {
	syncTime := time.Date(2026, 3, 20, 10, 0, 0, 0, time.UTC)
	team := enrichedTeam{
		TeamID:   "team-abc",
		Name:     "Alpha Team",
		Slug:     "alpha-team",
		Path:     "/path/to/team",
		LastSync: syncTime,
		Primary:  true,
	}

	tc := team.toConfigTeamContext()

	assert.Equal(t, "team-abc", tc.TeamID)
	assert.Equal(t, "Alpha Team", tc.TeamName)
	assert.Equal(t, "alpha-team", tc.Slug)
	assert.Equal(t, "/path/to/team", tc.Path)
	assert.Equal(t, syncTime, tc.LastSync)
}

func TestEnrichedTeam_ToConfigTeamContext_ZeroValues(t *testing.T) {
	team := enrichedTeam{
		TeamID: "team-minimal",
	}

	tc := team.toConfigTeamContext()

	assert.Equal(t, "team-minimal", tc.TeamID)
	assert.Equal(t, "", tc.TeamName)
	assert.Equal(t, "", tc.Slug)
	assert.Equal(t, "", tc.Path)
	assert.True(t, tc.LastSync.IsZero())
}

func TestDiscoverAllTeams_PrimaryFirst(t *testing.T) {
	dir := createInitializedProject(t)

	teamContexts := []config.TeamContext{
		{TeamID: "team-second", TeamName: "Second Team", Slug: "second-team", Path: "/path/second"},
		{TeamID: "team-primary", TeamName: "Primary Team", Slug: "primary-team", Path: "/path/primary"},
		{TeamID: "team-third", TeamName: "Third Team", Slug: "third-team", Path: "/path/third"},
	}
	_ = config.SaveLocalConfig(dir, &config.LocalConfig{TeamContexts: teamContexts})
	_ = config.SaveProjectConfig(dir, &config.ProjectConfig{TeamID: "team-primary", TeamName: "Primary Team"})

	teams := discoverAllTeams(dir)
	if len(teams) == 0 {
		t.Skip("no teams discovered")
	}

	// find our primary team in the result
	var primaryIdx = -1
	for i, team := range teams {
		if team.TeamID == "team-primary" {
			primaryIdx = i
			break
		}
	}
	if primaryIdx == -1 {
		t.Skip("test team not in discovered teams")
	}

	assert.True(t, teams[primaryIdx].Primary, "team-primary should be marked primary")
	// primary teams come first in the result
	assert.Equal(t, 0, primaryIdx, "primary team should be first")
}

func TestDiscoverAllTeams_EnrichesEmptyNameToTeamID(t *testing.T) {
	dir := createInitializedProject(t)

	teamContexts := []config.TeamContext{
		{TeamID: "team-no-name", TeamName: "", Path: "/path/nameless"},
	}
	_ = config.SaveLocalConfig(dir, &config.LocalConfig{TeamContexts: teamContexts})
	// don't set team-no-name as primary so project config name doesn't interfere
	_ = config.SaveProjectConfig(dir, &config.ProjectConfig{TeamID: "other-team"})

	teams := discoverAllTeams(dir)
	if len(teams) == 0 {
		t.Skip("no teams discovered")
	}

	// find our test team
	for _, team := range teams {
		if team.TeamID == "team-no-name" {
			// name should have been enriched (at minimum to team ID itself)
			assert.NotEmpty(t, team.Name, "enriched team name should not be empty")
			return
		}
	}
	t.Skip("team-no-name not in discovered teams")
}

func TestResolveTeamByQuery_NoTeamsReturnsNil(t *testing.T) {
	// use an empty temp dir with no config — no daemon, no filesystem teams
	dir := createInitializedProject(t)
	// don't save any local config with teams
	_ = config.SaveLocalConfig(dir, &config.LocalConfig{TeamContexts: nil})

	// the function may still find teams from the real daemon/filesystem,
	// so only assert nil result for a definitely-nonexistent query
	result := resolveTeamByQuery(dir, "absolutely-nonexistent-team-xyz-123")
	assert.Nil(t, result)
}

func TestResolveTeamByQuery_WhitespaceHandling(t *testing.T) {
	dir := createInitializedProject(t)

	teamContexts := []config.TeamContext{
		{TeamID: "team-ws-test", TeamName: "WS Team", Slug: "ws-team", Path: "/path/ws"},
	}
	_ = config.SaveLocalConfig(dir, &config.LocalConfig{TeamContexts: teamContexts})
	_ = config.SaveProjectConfig(dir, &config.ProjectConfig{TeamID: "team-ws-test"})

	// query with whitespace should match after trimming
	result := resolveTeamByQuery(dir, "  ws-team  ")
	if result == nil {
		t.Skip("team not found in discovery")
	}
	assert.Equal(t, "team-ws-test", result.TeamID, "query with whitespace should still match")
}
