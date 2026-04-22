package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// aiderPrimeMarkerStart / End are the Aider-specific block markers. Each
// adapter uses a unique pair so installs/uninstalls don't collide across
// adapters. Aider writes CONVENTIONS.md (not AGENTS.md), so today's risk
// of collision is low, but the consistency simplifies shared helpers and
// the #527 doctor repair logic. See #527.
const aiderPrimeMarkerStart = "<!-- ox:prime:aider:start -->"
const aiderPrimeMarkerEnd = "<!-- ox:prime:aider:end -->"

// aiderLegacyPrimeMarkerStart / End are the pre-#527 generic markers.
// Kept for backward-compat detection: check matches both; uninstall
// removes both. New installs only emit the unique markers.
const aiderLegacyPrimeMarkerStart = "<!-- ox:prime:start -->"
const aiderLegacyPrimeMarkerEnd = "<!-- ox:prime:end -->"

// aiderPrimeBlock is the content injected into CONVENTIONS.md for Aider.
// Aider loads CONVENTIONS.md via the --read flag in .aider.conf.yml.
//
// NOTE: the prime command is intentionally adapter-agnostic — no hardcoded
// AGENT_ENV=<adapter> prefix. Instruction files can leak across agents
// (e.g. via shared symlinks), and a hardcoded AGENT_ENV in the block
// poisons sessions running a different coding agent. Runtime detection
// in agentx.CurrentAgent handles agent identification correctly. See #527.
var aiderPrimeBlock = aiderPrimeMarkerStart + "\n" +
	"## SageOx Team Context\n" +
	"\n" +
	"This project uses [SageOx](https://sageox.ai) for team context. Run the following command at the start of every session to load team knowledge:\n" +
	"\n" +
	"```bash\n" +
	"ox agent prime\n" +
	"```\n" +
	"\n" +
	"This provides architectural decisions, coding conventions, and session history from your team.\n" +
	aiderPrimeMarkerEnd

// aiderBlockAlreadyPresent reports whether CONVENTIONS.md already carries
// an Aider prime block under either the current or legacy marker pair.
// Legacy markers are recognized so pre-#527 installations are treated as
// "installed" and we don't stack a second block on top of them.
func aiderBlockAlreadyPresent(content string) bool {
	return strings.Contains(content, aiderPrimeMarkerStart) ||
		strings.Contains(content, aiderLegacyPrimeMarkerStart)
}

func handleInstallHooks(p adapterprotocol.HookParams) (*adapterprotocol.InstallHooksResponse, error) {
	if p.Scope == "user" {
		return &adapterprotocol.InstallHooksResponse{
			Installed: false,
		}, fmt.Errorf("aider does not support user-level hooks (no user-level CONVENTIONS.md)")
	}

	convPath := resolveConventionsPath(p.RepoRoot)

	existing, err := os.ReadFile(convPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to read CONVENTIONS.md: %w", err)
	}

	content := string(existing)

	// already installed — idempotent (current or legacy markers)
	if aiderBlockAlreadyPresent(content) {
		return &adapterprotocol.InstallHooksResponse{
			Installed:    true,
			FilesWritten: []string{convPath},
		}, nil
	}

	var newContent string
	if content == "" {
		newContent = aiderPrimeBlock + "\n"
	} else {
		newContent = strings.TrimRight(content, "\n") + "\n\n" + aiderPrimeBlock + "\n"
	}

	if err := fileutil.AtomicWriteBytes(convPath, []byte(newContent), 0644); err != nil {
		return nil, fmt.Errorf("failed to write CONVENTIONS.md: %w", err)
	}

	return &adapterprotocol.InstallHooksResponse{
		Installed:    true,
		FilesWritten: []string{convPath},
	}, nil
}

func handleCheckHooks(p adapterprotocol.HookParams) (*adapterprotocol.CheckHooksResponse, error) {
	if p.Scope == "user" {
		return &adapterprotocol.CheckHooksResponse{
			Installed: false,
			Scope:     p.Scope,
		}, nil
	}

	convPath := resolveConventionsPath(p.RepoRoot)
	data, err := os.ReadFile(convPath)
	if err != nil {
		return &adapterprotocol.CheckHooksResponse{
			Installed: false,
			Scope:     p.Scope,
		}, nil
	}

	installed := aiderBlockAlreadyPresent(string(data))
	return &adapterprotocol.CheckHooksResponse{
		Installed: installed,
		Scope:     p.Scope,
		HookFiles: []string{convPath},
	}, nil
}

func handleUninstallHooks(p adapterprotocol.HookParams) (*adapterprotocol.UninstallHooksResponse, error) {
	if p.Scope == "user" {
		return &adapterprotocol.UninstallHooksResponse{Uninstalled: true}, nil
	}

	convPath := resolveConventionsPath(p.RepoRoot)
	data, err := os.ReadFile(convPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &adapterprotocol.UninstallHooksResponse{Uninstalled: true}, nil
		}
		return nil, fmt.Errorf("failed to read CONVENTIONS.md: %w", err)
	}

	content := string(data)

	// remove both the current-marker block and any legacy-marker block —
	// a pre-#527 installation may carry the generic <!-- ox:prime:start -->
	// pair that we no longer emit.
	cleaned := removePrimeBlock(content, aiderPrimeMarkerStart, aiderPrimeMarkerEnd)
	cleaned = removePrimeBlock(cleaned, aiderLegacyPrimeMarkerStart, aiderLegacyPrimeMarkerEnd)

	if cleaned == content {
		return &adapterprotocol.UninstallHooksResponse{Uninstalled: true}, nil
	}

	if strings.TrimSpace(cleaned) == "" {
		if err := os.Remove(convPath); err != nil {
			return nil, fmt.Errorf("failed to remove empty CONVENTIONS.md: %w", err)
		}
	} else {
		if err := fileutil.AtomicWriteBytes(convPath, []byte(cleaned), 0644); err != nil {
			return nil, fmt.Errorf("failed to write CONVENTIONS.md: %w", err)
		}
	}

	return &adapterprotocol.UninstallHooksResponse{
		Uninstalled:   true,
		FilesModified: []string{convPath},
	}, nil
}

// removePrimeBlock strips one start...end block (inclusive) from content,
// collapsing surrounding blank lines so no orphan whitespace remains.
// Returns content unchanged if either marker is absent.
func removePrimeBlock(content, startMarker, endMarker string) string {
	startIdx := strings.Index(content, startMarker)
	if startIdx == -1 {
		return content
	}
	endIdx := strings.Index(content, endMarker)
	if endIdx == -1 {
		return content
	}
	endIdx += len(endMarker)

	before := strings.TrimRight(content[:startIdx], "\n")
	after := strings.TrimLeft(content[endIdx:], "\n")

	switch {
	case before == "" && after == "":
		return ""
	case before == "":
		return after + "\n"
	case after == "":
		return before + "\n"
	default:
		return before + "\n\n" + after + "\n"
	}
}

func resolveConventionsPath(repoRoot string) string {
	if repoRoot == "" {
		log.Println("WARN: resolveConventionsPath called with empty repoRoot, falling back to cwd")
		repoRoot, _ = os.Getwd()
	}
	return filepath.Join(repoRoot, "CONVENTIONS.md")
}
