package read

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
)

// TestReadIntegration_DailyEndToEnd wires the reader against the real
// config.FindRepoTeamContext resolver. Failure prevented: the reader
// silently disagrees with the canonical team-context lookup when a real
// project is pointed at a real memory/daily directory.
func TestReadIntegration_DailyEndToEnd(t *testing.T) {
	// Two temp dirs: a project root (so FindRepoTeamContext can load
	// ProjectConfig + LocalConfig) and a separate team-context root
	// holding memory/daily/*.md fixtures.
	projectRoot := t.TempDir()
	teamRoot := t.TempDir()

	// .sageox/ is the minimum for SaveLocalConfig + SaveProjectConfig to
	// consider the project initialized. We replicate the two-line helper
	// from internal/config/testhelper_test.go here because it is in a
	// _test.go file and cannot be imported across packages.
	if err := os.MkdirAll(filepath.Join(projectRoot, ".sageox"), 0o755); err != nil {
		t.Fatalf("mkdir .sageox: %v", err)
	}

	if err := config.SaveProjectConfig(projectRoot, &config.ProjectConfig{
		ProjectID:   "proj_read_e2e",
		WorkspaceID: "ws_read_e2e",
		TeamID:      "team_read_e2e",
		TeamName:    "Read E2E",
	}); err != nil {
		t.Fatalf("SaveProjectConfig: %v", err)
	}

	if err := config.SaveLocalConfig(projectRoot, &config.LocalConfig{
		TeamContexts: []config.TeamContext{{
			TeamID:   "team_read_e2e",
			TeamName: "Read E2E",
			Slug:     "read-e2e",
			Path:     teamRoot,
		}},
	}); err != nil {
		t.Fatalf("SaveLocalConfig: %v", err)
	}

	// Stage two daily files with deterministic mtimes.
	apr12mtime := time.Date(2026, 4, 12, 10, 0, 0, 0, time.UTC)
	apr12laterMtime := time.Date(2026, 4, 12, 14, 0, 0, 0, time.UTC)
	mustWriteDaily(t, teamRoot, "2026-04-12-019c8a3f.md", sampleDailyBody, apr12mtime)
	mustWriteDaily(t, teamRoot, "2026-04-12-019c87aa.md", sampleDailyBody, apr12laterMtime)

	// Canonical resolution path: the reader consumes exactly what
	// FindRepoTeamContext returns, so if the real config layer changes
	// where team contexts live, this test fails loudly.
	tc := config.FindRepoTeamContext(projectRoot)
	if tc == nil {
		t.Fatalf("FindRepoTeamContext returned nil; local config did not register team")
	}
	if tc.Path != teamRoot {
		t.Fatalf("FindRepoTeamContext.Path = %q, want %q", tc.Path, teamRoot)
	}

	q := ReadQuery{
		Since: time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 4, 12, 23, 0, 0, 0, time.UTC),
		Layer: LayerDaily,
		Teams: []TeamRef{{Slug: tc.Slug, Path: tc.Path}},
	}
	entries, meta, err := ListEntries(context.Background(), q)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	wantIDs := []string{
		"2026-04-12-019c8a3f", // older mtime first
		"2026-04-12-019c87aa",
	}
	for i, want := range wantIDs {
		if entries[i].ID != want {
			t.Fatalf("entries[%d].ID = %q, want %q", i, entries[i].ID, want)
		}
		if entries[i].Team != "read-e2e" {
			t.Fatalf("entries[%d].Team = %q, want read-e2e", i, entries[i].Team)
		}
		if filepath.IsAbs(entries[i].RelPath) {
			t.Fatalf("entries[%d].RelPath = %q must be relative, not absolute", i, entries[i].RelPath)
		}
	}
	if meta.LayerResolved != LayerDaily {
		t.Fatalf("LayerResolved = %q", meta.LayerResolved)
	}
}
