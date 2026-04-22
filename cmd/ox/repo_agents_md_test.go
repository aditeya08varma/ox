package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestRepoAgentsMD_NoAdapterEnvHardcodes guards this repo's own AGENTS.md
// against re-introduction of the #527 bug pattern: an adapter-specific
// prime block hardcoding AGENT_ENV=<adapter> inside a file that Claude Code
// (or any other agent) may read via the AGENTS.md/CLAUDE.md symlink.
//
// Failure prevented: a committed AGENTS.md that mis-routes Claude Code
// sessions through the Pi/Amp/Aider adapter, producing wrong agent_type
// rows in agent_instances.jsonl and poisoning CLAUDE_ENV_FILE with a
// bogus AGENT_ENV for every subsequent Bash tool invocation.
//
// Scope: the repo-root AGENTS.md (and its CLAUDE.md symlink, if present).
// The hook shell commands in internal/constants/agent.go legitimately
// carry AGENT_ENV because they run in subprocess contexts; this test
// does not look at those.
func TestRepoAgentsMD_NoAdapterEnvHardcodes(t *testing.T) {
	root := findOxRepoRootForTest(t)

	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		path := filepath.Join(root, name)
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}

		forbidden := []string{
			"AGENT_ENV=pi",
			"AGENT_ENV=amp",
			"AGENT_ENV=aider",
			"AGENT_ENV=codex",
			"AGENT_ENV=gemini",
			"AGENT_ENV=droid",
			"AGENT_ENV=opencode",
		}
		content := string(data)
		for _, lit := range forbidden {
			if strings.Contains(content, lit) {
				t.Errorf("%s contains forbidden literal %q — see #527: adapter-specific AGENT_ENV hardcodes in AGENTS.md poison cross-agent sessions", name, lit)
			}
		}
	}
}

// findOxRepoRootForTest walks up from the current test file's directory
// until it finds a go.mod, returning that directory. Fails the test if
// not found. Named distinctly from production findRepoRoot() in doctor_git.go.
func findOxRepoRootForTest(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod found walking up from %s", filepath.Dir(thisFile))
		}
		dir = parent
	}
}
