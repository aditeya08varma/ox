package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
)

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugGuidanceFiles,
		Name:        "Guidance files",
		Category:    "Team Context",
		FixLevel:    FixLevelAuto,
		Description: "Ensures distill guidance files are in memory/guidance/ and migrates legacy DISTILL.md",
		Run:         checkGuidanceFiles,
	})
}

// checkGuidanceFiles detects whether the old root-level DISTILL.md needs migration
// and whether the base guidance files exist. On fix, migrates and seeds as needed.
func checkGuidanceFiles(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Guidance files", "not in git repo", "")
	}

	tc := config.FindRepoTeamContext(gitRoot)
	if tc == nil {
		return SkippedCheck("Guidance files", "no team context", "")
	}

	result := checkGuidanceFilesForPath(tc.Path, fix)

	// push team context after seeding/migrating guidance files
	if fix && result.passed {
		ep := endpoint.GetForProject(gitRoot)
		if err := pushTeamContext(context.Background(), tc.Path, ep); err != nil {
			slog.Warn("failed to push team context after guidance fix", "error", err)
			result = WarningCheck("Guidance files", "seeded but push failed", err.Error())
		}
	}

	return result
}

// checkGuidanceFilesForPath is the testable core of the guidance check.
// It checks a specific team context path for guidance file presence and migration needs.
func checkGuidanceFilesForPath(tcPath string, fix bool) checkResult {
	oldDistill := filepath.Join(tcPath, "DISTILL.md")
	newDistill := filepath.Join(tcPath, "memory", "guidance", "DISTILL.md")
	newExtract := filepath.Join(tcPath, "memory", "guidance", "EXTRACT.md")

	// check for legacy DISTILL.md needing migration
	_, oldErr := os.Stat(oldDistill)
	_, newDistillErr := os.Stat(newDistill)
	needsMigration := oldErr == nil && newDistillErr != nil

	// check for missing base guidance files (exclude migration case — that's a separate issue)
	missingDistill := newDistillErr != nil && !needsMigration
	_, extractErr := os.Stat(newExtract)
	missingExtract := extractErr != nil

	if !needsMigration && !missingDistill && !missingExtract {
		return PassedCheck("Guidance files", "memory/guidance/ configured")
	}

	if !fix {
		var issues []string
		if needsMigration {
			issues = append(issues, "DISTILL.md at root needs migration to memory/guidance/")
		}
		if missingDistill {
			issues = append(issues, "memory/guidance/DISTILL.md missing")
		}
		if missingExtract {
			issues = append(issues, "memory/guidance/EXTRACT.md missing")
		}
		detail := "  " + strings.Join(issues, "\n  ") + "\n  Run `ox doctor --fix` to resolve"
		return WarningCheck("Guidance files",
			fmt.Sprintf("%d issue(s)", len(issues)),
			detail)
	}

	// fix: migrate and seed
	if needsMigration {
		if _, err := migrateDistillGuidelines(tcPath); err != nil {
			// Don't fall through to seeding — that would replace custom content with defaults
			return WarningCheck("Guidance files", "migration failed",
				fmt.Sprintf("Failed to migrate DISTILL.md: %s\n  Move it manually to memory/guidance/DISTILL.md", err))
		}
		slog.Info("migrated DISTILL.md to memory/guidance/")
	}

	if err := seedGuidanceFiles(tcPath); err != nil {
		slog.Warn("failed to seed guidance files", "error", err)
		return WarningCheck("Guidance files", "seed failed", err.Error())
	}

	return PassedCheck("Guidance files", "memory/guidance/ configured")
}
