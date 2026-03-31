package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveTimezone_NoConfig(t *testing.T) {
	// With no project root, should return UTC
	loc := ResolveTimezone("")
	if loc != time.UTC {
		t.Errorf("expected UTC, got %s", loc)
	}
}

func TestResolveTimezone_NonInitializedProject(t *testing.T) {
	tmpDir := t.TempDir()
	loc := ResolveTimezone(tmpDir)
	if loc != time.UTC {
		t.Errorf("expected UTC for non-initialized project, got %s", loc)
	}
}

func TestResolveTimezone_EnvVar(t *testing.T) {
	t.Setenv("OX_TIMEZONE", "America/New_York")

	loc := ResolveTimezone("")
	expected, _ := time.LoadLocation("America/New_York")
	if loc.String() != expected.String() {
		t.Errorf("expected %s, got %s", expected, loc)
	}
}

func TestResolveTimezone_InvalidEnvVar_FallsThrough(t *testing.T) {
	t.Setenv("OX_TIMEZONE", "Invalid/NotATimezone")

	loc := ResolveTimezone("")
	if loc != time.UTC {
		t.Errorf("expected UTC for invalid env var, got %s", loc)
	}
}

func TestResolveTimezone_ProjectConfig(t *testing.T) {
	tmpDir := CreateInitializedProject(t)

	cfg, err := LoadProjectConfig(tmpDir)
	if err != nil {
		t.Fatalf("failed to load project config: %v", err)
	}
	cfg.Timezone = "Europe/London"
	if err := SaveProjectConfig(tmpDir, cfg); err != nil {
		t.Fatalf("failed to save project config: %v", err)
	}

	loc := ResolveTimezone(tmpDir)
	expected, _ := time.LoadLocation("Europe/London")
	if loc.String() != expected.String() {
		t.Errorf("expected %s, got %s", expected, loc)
	}
}

func TestResolveTimezone_EnvOverridesProjectConfig(t *testing.T) {
	t.Setenv("OX_TIMEZONE", "Asia/Tokyo")

	tmpDir := CreateInitializedProject(t)
	cfg, err := LoadProjectConfig(tmpDir)
	if err != nil {
		t.Fatalf("failed to load project config: %v", err)
	}
	cfg.Timezone = "Europe/London"
	if err := SaveProjectConfig(tmpDir, cfg); err != nil {
		t.Fatalf("failed to save project config: %v", err)
	}

	loc := ResolveTimezone(tmpDir)
	expected, _ := time.LoadLocation("Asia/Tokyo")
	if loc.String() != expected.String() {
		t.Errorf("expected %s (from env), got %s", expected, loc)
	}
}

func TestResolveTimezone_InvalidProjectConfig_FallsThrough(t *testing.T) {
	tmpDir := CreateInitializedProject(t)

	cfg, err := LoadProjectConfig(tmpDir)
	if err != nil {
		t.Fatalf("failed to load project config: %v", err)
	}
	cfg.Timezone = "Invalid/NotReal"
	if err := SaveProjectConfig(tmpDir, cfg); err != nil {
		t.Fatalf("failed to save project config: %v", err)
	}

	loc := ResolveTimezone(tmpDir)
	if loc != time.UTC {
		t.Errorf("expected UTC for invalid project timezone, got %s", loc)
	}
}

func TestIsValidTimezone(t *testing.T) {
	tests := []struct {
		tz    string
		valid bool
	}{
		{"UTC", true},
		{"America/New_York", true},
		{"Europe/London", true},
		{"Asia/Tokyo", true},
		{"US/Pacific", true},
		{"Local", false}, // Go pseudonym — resolves differently per machine
		{"", false},      // empty string is not a valid user-facing timezone
		{"Invalid/NotATimezone", false},
		{"NotATimezone", false},
		{"America/FakeCity", false},
	}

	for _, tt := range tests {
		t.Run(tt.tz, func(t *testing.T) {
			got := IsValidTimezone(tt.tz)
			if got != tt.valid {
				t.Errorf("IsValidTimezone(%q) = %v, want %v", tt.tz, got, tt.valid)
			}
		})
	}
}

func TestResolveTimezone_TeamConfig(t *testing.T) {
	// Create a fake team context directory with config.toml
	teamDir := t.TempDir()
	teamCfg := &TeamConfig{Timezone: "Australia/Sydney"}
	if err := SaveTeamConfig(teamDir, teamCfg); err != nil {
		t.Fatalf("failed to save team config: %v", err)
	}

	// Verify loadTeamTimezone works when given a path directly
	// (We can't easily test via ResolveTimezone because FindRepoTeamContext
	// requires a full local config setup, but we test the helper.)
	cfg, err := LoadTeamConfig(teamDir)
	if err != nil {
		t.Fatalf("failed to load team config: %v", err)
	}
	if cfg.Timezone != "Australia/Sydney" {
		t.Errorf("expected Australia/Sydney, got %s", cfg.Timezone)
	}

	// Verify the timezone is loadable
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		t.Fatalf("failed to load location: %v", err)
	}
	if loc.String() != "Australia/Sydney" {
		t.Errorf("expected Australia/Sydney, got %s", loc)
	}
}

func TestTeamConfig_TimezoneRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()

	original := &TeamConfig{
		SessionRecording: "auto",
		Timezone:         "America/Chicago",
	}
	if err := SaveTeamConfig(tmpDir, original); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := LoadTeamConfig(tmpDir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Timezone != "America/Chicago" {
		t.Errorf("expected America/Chicago, got %s", loaded.Timezone)
	}
}

func TestProjectConfig_TimezoneRoundTrip(t *testing.T) {
	tmpDir := CreateInitializedProject(t)

	cfg, err := LoadProjectConfig(tmpDir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cfg.Timezone = "Pacific/Auckland"
	if err := SaveProjectConfig(tmpDir, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := LoadProjectConfig(tmpDir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if loaded.Timezone != "Pacific/Auckland" {
		t.Errorf("expected Pacific/Auckland, got %s", loaded.Timezone)
	}
}

func TestProjectConfig_TimezoneOmittedWhenEmpty(t *testing.T) {
	tmpDir := CreateInitializedProject(t)

	cfg, err := LoadProjectConfig(tmpDir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cfg.Timezone = "" // explicitly empty
	if err := SaveProjectConfig(tmpDir, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Read raw JSON and verify timezone key is not present
	data, err := os.ReadFile(filepath.Join(tmpDir, ".sageox", "config.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(data), `"timezone"`) {
		t.Error("expected timezone to be omitted from JSON when empty")
	}
}
