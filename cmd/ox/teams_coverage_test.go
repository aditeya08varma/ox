package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestEnrichedTeamsToEntries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		teams []enrichedTeam
		want  []teamEntry
	}{
		{
			name:  "empty slice",
			teams: []enrichedTeam{},
			want:  []teamEntry{},
		},
		{
			name: "single team with sync time",
			teams: []enrichedTeam{
				{
					TeamID:   "team_abc",
					Name:     "Alpha Team",
					Slug:     "alpha-team",
					Path:     "/data/teams/team_abc",
					LastSync: time.Now().Add(-2 * time.Hour),
					Primary:  true,
				},
			},
			want: []teamEntry{
				{
					TeamID:  "team_abc",
					Name:    "Alpha Team",
					Slug:    "alpha-team",
					Primary: true,
					Path:    "/data/teams/team_abc",
					// LastSync will be "2h ago" or similar
				},
			},
		},
		{
			name: "team with zero sync time shows never",
			teams: []enrichedTeam{
				{
					TeamID: "team_xyz",
					Name:   "Unsynced",
					Slug:   "unsynced",
				},
			},
			want: []teamEntry{
				{
					TeamID:   "team_xyz",
					Name:     "Unsynced",
					Slug:     "unsynced",
					LastSync: "never",
				},
			},
		},
		{
			name: "multiple teams preserves order",
			teams: []enrichedTeam{
				{TeamID: "team_1", Name: "First", Slug: "first"},
				{TeamID: "team_2", Name: "Second", Slug: "second", Primary: true},
				{TeamID: "team_3", Name: "Third", Slug: "third"},
			},
			want: []teamEntry{
				{TeamID: "team_1", Name: "First", Slug: "first", LastSync: "never"},
				{TeamID: "team_2", Name: "Second", Slug: "second", Primary: true, LastSync: "never"},
				{TeamID: "team_3", Name: "Third", Slug: "third", LastSync: "never"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := enrichedTeamsToEntries(tt.teams)
			assert.Equal(t, len(tt.want), len(got))

			for i, want := range tt.want {
				assert.Equal(t, want.TeamID, got[i].TeamID)
				assert.Equal(t, want.Name, got[i].Name)
				assert.Equal(t, want.Slug, got[i].Slug)
				assert.Equal(t, want.Primary, got[i].Primary)
				assert.Equal(t, want.Path, got[i].Path)

				if want.LastSync == "never" {
					assert.Equal(t, "never", got[i].LastSync)
				} else if want.LastSync != "" {
					// for non-zero sync times, just verify it's not "never"
					assert.NotEqual(t, "never", got[i].LastSync)
				}
			}
		})
	}
}

func TestEnrichedTeamsToEntries_RecentSync(t *testing.T) {
	t.Parallel()

	// a sync from 30 seconds ago should produce something like "just now" or "30s ago"
	teams := []enrichedTeam{
		{
			TeamID:   "team_recent",
			Name:     "Recent",
			Slug:     "recent",
			LastSync: time.Now().Add(-30 * time.Second),
		},
	}

	entries := enrichedTeamsToEntries(teams)
	assert.Len(t, entries, 1)
	assert.NotEqual(t, "never", entries[0].LastSync)
	// should be a short duration string
	assert.NotEmpty(t, entries[0].LastSync)
}

func TestEnrichedTeamsToEntries_OldSync(t *testing.T) {
	t.Parallel()

	teams := []enrichedTeam{
		{
			TeamID:   "team_old",
			Name:     "Old Sync",
			Slug:     "old-sync",
			LastSync: time.Now().Add(-48 * time.Hour),
		},
	}

	entries := enrichedTeamsToEntries(teams)
	assert.Len(t, entries, 1)
	assert.NotEqual(t, "never", entries[0].LastSync)
}
