package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

func TestCleanupLegacyClaudeCommandsPreservesUserFiles(t *testing.T) {
	repo := t.TempDir()
	commands := filepath.Join(repo, ".claude", "commands")
	if err := os.MkdirAll(commands, 0o755); err != nil {
		t.Fatal(err)
	}
	managed := filepath.Join(commands, "ox-plan.md")
	user := filepath.Join(commands, "ox-recap.md")
	if err := os.WriteFile(managed, agentx.StampedContent([]byte("managed\n"), "1.0.0", "ox"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(user, []byte("user owned\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	desired := skillmanager.DesiredSkills{Bundles: []skillmanager.BundleRef{{ID: "core"}}, Targets: []string{"claude-project"}}
	targets := []adapterprotocol.SkillTarget{{
		Key: "claude-project", Root: ".claude/skills",
		Format: adapterprotocol.SkillFormatAgentSkillsV1, Scope: adapterprotocol.SkillScopeProject,
		LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
	}}
	cleanupLegacyClaudeCommands(repo, desired, targets)
	if _, err := os.Stat(managed); !os.IsNotExist(err) {
		t.Fatalf("managed legacy command remains: %v", err)
	}
	if _, err := os.Stat(user); err != nil {
		t.Fatalf("user command was removed: %v", err)
	}
}
