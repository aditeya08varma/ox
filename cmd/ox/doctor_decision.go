package main

import (
	"fmt"
	"strings"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/decision"
)

// CheckSlugDecisionPaths is the slug for the decision-paths config check.
const CheckSlugDecisionPaths = "decision-paths"

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugDecisionPaths,
		Name:        "Decision record paths",
		Category:    "Project Health",
		FixLevel:    FixLevelCheckOnly,
		Description: "Validates the committed decision.paths config and that each configured path yields decision records",
		Run: func(fix bool) checkResult {
			return checkDecisionPaths()
		},
	})
}

// checkDecisionPaths validates the decision.paths block: schema errors are
// failures; configured paths that match zero DR files are warnings (a typo'd
// path silently disables enrichment, so surface it). No config → skip; the
// zero-config default dirs need no doctoring.
func checkDecisionPaths() checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Decision record paths", "not in git repo", "")
	}
	cfg, err := config.LoadProjectConfig(gitRoot)
	if err != nil || cfg == nil || cfg.Decision.IsEmpty() {
		return SkippedCheck("Decision record paths", "no decision.paths configured (default discovery applies)", "")
	}

	if err := config.ValidateDecisionConfig(cfg.Decision); err != nil {
		return checkResult{
			name:    "Decision record paths",
			passed:  false,
			message: err.Error(),
		}
	}

	corpus := decision.LoadCorpus(gitRoot, cfg.Decision)
	if len(corpus) == 0 {
		return checkResult{
			name:    "Decision record paths",
			passed:  false,
			warning: true,
			message: fmt.Sprintf("decision.paths (%s) matched no decision records — check for typos", strings.Join(cfg.Decision.Paths, ", ")),
		}
	}
	return checkResult{
		name:    "Decision record paths",
		passed:  true,
		message: fmt.Sprintf("%d decision records across %d configured path(s)", len(corpus), len(cfg.Decision.Paths)),
	}
}
