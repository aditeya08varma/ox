package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetSessionExportFlags restores sessionExportCmd's flags to their zero
// values before each run. sessionExportCmd is a package-level singleton
// (registered once in init()), so tests must reset flag state between runs
// rather than relying on a fresh command — house style matches
// TestSessionRegenerateRedactFlagValidation in session_regenerate_test.go.
func resetSessionExportFlags(t *testing.T) {
	t.Helper()
	require.NoError(t, sessionExportCmd.Flags().Set("input", ""))
	require.NoError(t, sessionExportCmd.Flags().Set("output", ""))
	require.NoError(t, sessionExportCmd.Flags().Set("markdown", "true"))
}

// buildExportFixture writes a minimal, deterministic raw.jsonl via the
// production rewriteRawJSONL helper (session_regenerate.go), then returns
// its path for use with `session export --input`. Using --input bypasses
// the managed session store entirely, so the fixture doesn't need a repo
// config or session store setup — just a valid raw.jsonl on disk.
func buildExportFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	rawPath := filepath.Join(dir, "raw.jsonl")

	sess := &session.StoredSession{
		Meta: &session.StoreMeta{
			Version:   "1",
			CreatedAt: time.Date(2026, 1, 5, 10, 30, 0, 0, time.UTC),
			AgentID:   "OxExportTest",
			AgentType: "claude-code",
			Username:  "person-a",
		},
		Entries: []map[string]any{
			{"type": "user", "content": "what is 2+2?"},
			{"type": "assistant", "content": "4"},
		},
	}
	require.NoError(t, rewriteRawJSONL(rawPath, sess))
	return rawPath
}

// TestSessionExport_MarkdownFlagAcceptedAsNoOp verifies --markdown — an
// agent's guess by analogy with --json/--text elsewhere in the CLI — is
// accepted instead of failing with "unknown flag", and that its value has
// zero effect on the generated output: markdown is the only export format
// there is, so true/false/omitted must all produce byte-identical files.
// Failure prevented: (1) the flag not being registered at all (the
// original bug report), and (2) a future change accidentally wiring the
// flag to real branching logic, which byte-identical comparison across all
// three invocations would catch immediately.
func TestSessionExport_MarkdownFlagAcceptedAsNoOp(t *testing.T) {
	rawPath := buildExportFixture(t)
	outDir := t.TempDir()

	run := func(markdownArg string) []byte {
		resetSessionExportFlags(t)
		cmd := sessionExportCmd
		require.NoError(t, cmd.Flags().Set("input", rawPath))
		outPath := filepath.Join(outDir, "out-"+markdownArg+".md")
		require.NoError(t, cmd.Flags().Set("output", outPath))
		if markdownArg != "omitted" {
			require.NoError(t, cmd.Flags().Set("markdown", markdownArg))
		}

		err := cmd.RunE(cmd, nil)
		require.NoError(t, err)

		content, err := os.ReadFile(outPath)
		require.NoError(t, err)
		return content
	}

	omitted := run("omitted")
	explicitTrue := run("true")
	explicitFalse := run("false")

	assert.NotEmpty(t, omitted, "export should produce non-empty markdown")
	assert.Equal(t, omitted, explicitTrue, "--markdown=true must match flag omitted")
	assert.Equal(t, omitted, explicitFalse, "--markdown=false must match flag omitted")
}
