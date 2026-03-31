package config

import (
	"log/slog"
	"os"
	"time"
)

// ResolveTimezone determines the effective timezone for date grouping.
// Priority: OX_TIMEZONE env > project config > team config > UTC default.
//
// This follows the same resolution pattern as ResolveSessionRecording:
// env var overrides everything, then project config, then team config,
// then a sensible default.
func ResolveTimezone(projectRoot string) *time.Location {
	// 1. Check OX_TIMEZONE env var — highest priority (for pipelines/automation)
	if envTZ := os.Getenv(EnvTimezone); envTZ != "" {
		if loc, err := time.LoadLocation(envTZ); err == nil {
			return loc
		}
		slog.Warn("invalid OX_TIMEZONE env var, falling through to config", "value", envTZ)
	}

	// 2. Check project config (.sageox/config.json)
	if projectRoot != "" && IsInitialized(projectRoot) {
		projectCfg, err := LoadProjectConfig(projectRoot)
		if err == nil && projectCfg != nil && projectCfg.Timezone != "" {
			if loc, err := time.LoadLocation(projectCfg.Timezone); err == nil {
				return loc
			}
			slog.Warn("invalid timezone in project config, falling through", "value", projectCfg.Timezone)
		}
	}

	// 3. Check team config (config.toml in team context)
	if projectRoot != "" {
		if tz := loadTeamTimezone(projectRoot); tz != "" {
			if loc, err := time.LoadLocation(tz); err == nil {
				return loc
			}
		}
	}

	// 4. Default: UTC
	return time.UTC
}

// loadTeamTimezone loads the timezone setting from team context.
// Returns empty string if no team context or no setting configured.
func loadTeamTimezone(projectRoot string) string {
	tc := FindRepoTeamContext(projectRoot)
	if tc == nil || tc.Path == "" {
		return ""
	}

	teamCfg, err := LoadTeamConfig(tc.Path)
	if err != nil || teamCfg == nil {
		return ""
	}

	return teamCfg.Timezone
}

// IsValidTimezone checks whether tz is a valid IANA timezone name.
// Empty string and "Local" are rejected — Go's time.LoadLocation accepts both,
// but "Local" is a Go-internal pseudonym that resolves differently per machine,
// and timezone config is shared across coworkers via .sageox/config.json or team config.
func IsValidTimezone(tz string) bool {
	if tz == "" || tz == "Local" {
		return false
	}
	_, err := time.LoadLocation(tz)
	return err == nil
}
