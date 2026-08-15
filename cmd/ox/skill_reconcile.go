package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/internal/adapterstamp"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/internal/version"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// skillTargetsForAdapters resolves native target descriptors and deduplicates
// shared projections such as Codex and Gemini's .agents/skills root.
func skillTargetsForAdapters(repoRoot string, candidates []*adapters.ExternalAdapter) ([]adapterprotocol.SkillTarget, error) {
	var targets []adapterprotocol.SkillTarget
	for _, adapter := range candidates {
		if !adapter.HasCapability(adapterprotocol.CapSkillsInstaller) || adapter.Info() == nil {
			continue
		}
		targets = append(targets, adapter.Info().SkillTargets...)
	}
	return skillmanager.CanonicalizeTargets(repoRoot, targets)
}

func detectedSkillTargets(repoRoot string) ([]adapterprotocol.SkillTarget, error) {
	var candidates []*adapters.ExternalAdapter
	for _, adapter := range adapters.DiscoverExternalAdapters() {
		if adapter.HasCapability(adapterprotocol.CapSkillsInstaller) && adapter.Detect() {
			candidates = append(candidates, adapter)
		}
	}
	return skillTargetsForAdapters(repoRoot, candidates)
}

func reconcileSelectedSkills(repoRoot string, selected []adapterprotocol.SkillTarget) (*skillmanager.ReconcilePlan, error) {
	var effectiveDesired skillmanager.DesiredSkills
	plan, err := skillmanager.ReconcileUpdate(repoRoot, version.Version, func(desired skillmanager.DesiredSkills, targets []adapterprotocol.SkillTarget) (skillmanager.DesiredSkills, []adapterprotocol.SkillTarget, error) {
		targets = append(targets, selected...)
		var err error
		targets, err = skillmanager.CanonicalizeTargets(repoRoot, targets)
		if err != nil {
			return desired, nil, err
		}
		for _, id := range skills.DefaultBundleIDs() {
			desired = skillmanager.AddBundles(desired, id)
		}
		desired = skillmanager.AddTargets(desired, selected...)
		effectiveDesired = desired
		return desired, targets, nil
	})
	if err != nil {
		return nil, err
	}
	cleanupLegacyClaudeCommands(repoRoot, effectiveDesired, selected)
	return plan, nil
}

func cleanupLegacyClaudeCommands(repoRoot string, desired skillmanager.DesiredSkills, targets []adapterprotocol.SkillTarget) {
	var hasClaude bool
	for _, target := range targets {
		if target.Root == ".claude/skills" {
			hasClaude = true
			break
		}
	}
	if !hasClaude {
		return
	}
	var bundles []string
	for _, bundle := range desired.Bundles {
		bundles = append(bundles, bundle.ID)
	}
	names, err := skills.BundleNames(bundles)
	if err != nil {
		return
	}
	names = append(names, desired.Names...)
	for _, name := range names {
		path := filepath.Join(repoRoot, ".claude", "commands", name+".md")
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		if hash, _, _ := adapterstamp.ExtractStampAnywhere(data, "ox"); hash != "" {
			if err := os.Remove(path); err != nil {
				slog.Warn("skills: failed to remove superseded Claude command", "path", path, "error", err)
			}
		}
	}
}

func planCommittedSkills(repoRoot string) (*skillmanager.ReconcilePlan, error) {
	desired, targets, err := committedSkillState(repoRoot)
	if err != nil {
		return nil, err
	}
	return skillmanager.Plan(repoRoot, version.Version, desired, targets)
}

func reconcileCommittedSkills(repoRoot string) (*skillmanager.ReconcilePlan, error) {
	return skillmanager.ReconcileUpdate(repoRoot, version.Version, func(current skillmanager.DesiredSkills, currentTargets []adapterprotocol.SkillTarget) (skillmanager.DesiredSkills, []adapterprotocol.SkillTarget, error) {
		if len(current.Targets) > 0 {
			return current, currentTargets, nil
		}
		return bootstrapLegacySkillState(repoRoot, current, currentTargets)
	})
}

func committedSkillState(repoRoot string) (skillmanager.DesiredSkills, []adapterprotocol.SkillTarget, error) {
	desired, targets, err := skillmanager.LoadDesired(repoRoot)
	if err != nil {
		return skillmanager.DesiredSkills{}, nil, err
	}
	return bootstrapLegacySkillState(repoRoot, desired, targets)
}

func bootstrapLegacySkillState(repoRoot string, desired skillmanager.DesiredSkills, targets []adapterprotocol.SkillTarget) (skillmanager.DesiredSkills, []adapterprotocol.SkillTarget, error) {
	if len(desired.Targets) == 0 {
		candidates, detectErr := detectedSkillTargets(repoRoot)
		if detectErr != nil {
			return skillmanager.DesiredSkills{}, nil, detectErr
		}
		for _, target := range candidates {
			legacyBundles, legacyErr := skillmanager.LegacyBundles(repoRoot, target)
			if legacyErr != nil {
				return skillmanager.DesiredSkills{}, nil, legacyErr
			}
			if len(legacyBundles) > 0 {
				targets = append(targets, target)
				desired = skillmanager.AddTargets(desired, target)
				desired = skillmanager.AddBundles(desired, legacyBundles...)
			}
		}
		if len(desired.Targets) > 0 {
			for _, id := range skills.DefaultBundleIDs() {
				desired = skillmanager.AddBundles(desired, id)
			}
		}
	}
	return desired, targets, nil
}

func addSkillBundle(repoRoot, bundle string) (*skillmanager.ReconcilePlan, error) {
	desired, targets, err := skillmanager.LoadDesired(repoRoot)
	if err != nil {
		return nil, err
	}
	if len(desired.Targets) == 0 {
		targets, err = detectedSkillTargets(repoRoot)
		if err != nil {
			return nil, err
		}
		if len(targets) == 0 {
			return nil, fmt.Errorf("no detected AI coworker supports native Agent Skills; Attest remains available through the ox CLI")
		}
		desired = skillmanager.DefaultDesired(targets)
	}
	return skillmanager.ReconcileUpdate(repoRoot, version.Version, func(current skillmanager.DesiredSkills, currentTargets []adapterprotocol.SkillTarget) (skillmanager.DesiredSkills, []adapterprotocol.SkillTarget, error) {
		if len(current.Targets) == 0 {
			current = desired
			currentTargets = targets
		}
		current = skillmanager.AddBundles(current, bundle)
		return current, currentTargets, nil
	})
}

func uninstallManagedSkills(repoRoot string) (*skillmanager.ReconcilePlan, error) {
	// Include detected descriptors only to migrate and remove old stamped
	// projections from projects that predate the manifest.
	detected, err := detectedSkillTargets(repoRoot)
	if err != nil {
		return nil, err
	}
	return skillmanager.ReconcileUpdate(repoRoot, version.Version, func(current skillmanager.DesiredSkills, currentTargets []adapterprotocol.SkillTarget) (skillmanager.DesiredSkills, []adapterprotocol.SkillTarget, error) {
		currentTargets = append(currentTargets, detected...)
		var err error
		currentTargets, err = skillmanager.CanonicalizeTargets(repoRoot, currentTargets)
		if err != nil {
			return current, nil, err
		}
		current.Targets = nil
		return current, currentTargets, nil
	})
}
