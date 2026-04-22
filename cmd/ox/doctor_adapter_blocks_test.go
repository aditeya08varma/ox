package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupGitRepoWithFiles builds a throwaway git repo at t.TempDir, writes
// the given files, and cd's into it. Restores cwd on cleanup. checkAdapterPrimeBlocks
// relies on findGitRoot which uses cwd, hence the chdir.
func setupGitRepoWithFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()

	init := exec.Command("git", "init", "-q")
	init.Dir = dir
	require.NoError(t, init.Run(), "git init")

	for name, content := range files {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte(content), 0644))
	}

	prev, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(prev) })

	return dir
}

// --- Detection ---

// TestCheckAdapterPrimeBlocks_NoBlocks_Passes confirms a clean repo with
// only universal markers passes the check silently.
// Failure prevented: false-positive noise in doctor output.
func TestCheckAdapterPrimeBlocks_NoBlocks_Passes(t *testing.T) {
	setupGitRepoWithFiles(t, map[string]string{
		"AGENTS.md": "<!-- ox:prime-check -->\n# Agent\n<!-- ox:prime --> prime\n",
	})

	res := checkAdapterPrimeBlocks(false)
	assert.True(t, res.passed, "expected pass, got %+v", res)
	assert.False(t, res.warning, "clean repo should not warn")
}

// TestCheckAdapterPrimeBlocks_FlagsPiBlock detects a current-marker Pi
// block and warns without modifying the file when fix=false.
// Failure prevented: silent contamination of Claude Code sessions via Pi
// block in shared AGENTS.md (the #527 pattern).
func TestCheckAdapterPrimeBlocks_FlagsPiBlock(t *testing.T) {
	dir := setupGitRepoWithFiles(t, map[string]string{
		"AGENTS.md": "<!-- ox:prime-check -->\n\n<!-- ox:prime:pi:start -->\n## Pi block\nox agent prime\n<!-- ox:prime:pi:end -->\n<!-- ox:prime --> footer\n",
	})

	res := checkAdapterPrimeBlocks(false)
	assert.True(t, res.warning, "expected warning, got %+v", res)
	assert.Contains(t, res.message, "adapter block")
	assert.Contains(t, res.detail, "Pi")

	// file must not be modified in check-only mode
	data, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "<!-- ox:prime:pi:start -->",
		"check without --fix must not mutate the file")
}

// TestCheckAdapterPrimeBlocks_FlagsLegacyGenericBlock recognizes the
// pre-#527 <!-- ox:prime:start --> pair as a legacy adapter block.
// Failure prevented: repos with pre-#527 installations silently carrying
// broken blocks after upgrade.
func TestCheckAdapterPrimeBlocks_FlagsLegacyGenericBlock(t *testing.T) {
	setupGitRepoWithFiles(t, map[string]string{
		"AGENTS.md": "<!-- ox:prime-check -->\n\n<!-- ox:prime:start -->\nAGENT_ENV=pi ox agent prime\n<!-- ox:prime:end -->\n<!-- ox:prime --> footer\n",
	})

	res := checkAdapterPrimeBlocks(false)
	assert.True(t, res.warning, "expected warning for legacy block, got %+v", res)
	assert.Contains(t, res.detail, "generic",
		"detail should label ambiguous pre-#527 block")
	assert.Contains(t, res.detail, "AGENT_ENV",
		"detail should highlight the active poisoning")
}

// TestCheckAdapterPrimeBlocks_SymlinkAmplifier surfaces the AGENTS.md ↔
// CLAUDE.md symlink as an amplifier in the finding detail.
// Failure prevented: users fix AGENTS.md but don't realize CLAUDE.md
// pointed at the same physical file (or vice versa).
func TestCheckAdapterPrimeBlocks_SymlinkAmplifier(t *testing.T) {
	dir := setupGitRepoWithFiles(t, map[string]string{
		"AGENTS.md": "<!-- ox:prime-check -->\n\n<!-- ox:prime:pi:start -->\npi block\n<!-- ox:prime:pi:end -->\n",
	})
	// create CLAUDE.md as a symlink to AGENTS.md (the #527 configuration)
	require.NoError(t, os.Symlink("AGENTS.md", filepath.Join(dir, "CLAUDE.md")))

	res := checkAdapterPrimeBlocks(false)
	assert.True(t, res.warning)
	assert.Contains(t, res.detail, "symlinked",
		"detail should call out the symlink amplifier")
}

// --- Fix path ---

// TestCheckAdapterPrimeBlocks_FixRemovesBlock confirms --fix strips the
// adapter block and leaves the universal ox:prime-check / ox:prime
// markers intact.
// Failure prevented: the fix regresses by removing universal markers,
// breaking priming everywhere.
func TestCheckAdapterPrimeBlocks_FixRemovesBlock(t *testing.T) {
	dir := setupGitRepoWithFiles(t, map[string]string{
		"AGENTS.md": "<!-- ox:prime-check -->\nheader body\n\n<!-- ox:prime:amp:start -->\namp block\n<!-- ox:prime:amp:end -->\n\n<!-- ox:prime --> footer\n",
	})

	res := checkAdapterPrimeBlocks(true)
	assert.True(t, res.passed, "fix mode should pass, got %+v", res)

	data, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	require.NoError(t, err)
	got := string(data)
	assert.NotContains(t, got, "<!-- ox:prime:amp:start -->",
		"adapter block should be removed")
	assert.Contains(t, got, "<!-- ox:prime-check -->",
		"universal header marker must survive the fix")
	assert.Contains(t, got, "<!-- ox:prime -->",
		"universal footer marker must survive the fix")
}

// TestCheckAdapterPrimeBlocks_FixSweepsAllMarkerGenerations removes
// current, legacy-in-process, and legacy-generic blocks in one pass.
// Failure prevented: partial cleanup leaving some adapter blocks behind.
func TestCheckAdapterPrimeBlocks_FixSweepsAllMarkerGenerations(t *testing.T) {
	dir := setupGitRepoWithFiles(t, map[string]string{
		"AGENTS.md": strings.Join([]string{
			"<!-- ox:prime-check -->",
			"",
			"<!-- ox:prime:pi:start -->", "pi", "<!-- ox:prime:pi:end -->",
			"",
			"<!-- ox:pi-prime:start -->", "legacy inproc", "<!-- ox:pi-prime:end -->",
			"",
			"<!-- ox:prime:start -->", "legacy generic", "<!-- ox:prime:end -->",
			"",
			"<!-- ox:prime --> footer",
		}, "\n") + "\n",
	})

	res := checkAdapterPrimeBlocks(true)
	assert.True(t, res.passed)

	data, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	require.NoError(t, err)
	got := string(data)
	for _, lit := range []string{
		"<!-- ox:prime:pi:start -->",
		"<!-- ox:pi-prime:start -->",
		"<!-- ox:prime:start -->",
	} {
		assert.NotContains(t, got, lit,
			"every marker generation should be swept: %s", lit)
	}
	assert.Contains(t, got, "<!-- ox:prime-check -->", "universal header survives")
	assert.Contains(t, got, "<!-- ox:prime -->", "universal footer survives")
}

// TestCheckAdapterPrimeBlocks_DedupesSymlinkedFiles confirms we don't
// double-process when AGENTS.md and CLAUDE.md are the same physical file.
// Failure prevented: duplicate findings or redundant writes on symlinked
// instruction files.
func TestCheckAdapterPrimeBlocks_DedupesSymlinkedFiles(t *testing.T) {
	dir := setupGitRepoWithFiles(t, map[string]string{
		"AGENTS.md": "<!-- ox:prime:pi:start -->\npi\n<!-- ox:prime:pi:end -->\n",
	})
	require.NoError(t, os.Symlink("AGENTS.md", filepath.Join(dir, "CLAUDE.md")))

	res := checkAdapterPrimeBlocks(false)
	assert.True(t, res.warning)
	// The message should reference AGENTS.md once (not both AGENTS.md and CLAUDE.md)
	assert.Equal(t, 1, strings.Count(res.message, "AGENTS.md"),
		"AGENTS.md must appear exactly once in the summary (symlink dedup)")
	assert.NotContains(t, res.message, "CLAUDE.md",
		"symlinked CLAUDE.md should not appear as a separate file in findings")
}
