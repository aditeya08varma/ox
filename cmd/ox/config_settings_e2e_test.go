package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file is the end-to-end, customer-facing proof that an `ox config`
// setting a coworker changes actually changes what they experience downstream —
// not that a resolver returns a value in isolation, but that driving the real
// `ox config set` command changes the real command output the customer sees.
// It is the executable mirror of tests/acceptance/features/config/*.feature.
//
// The lens: `ox config get` / resolver unit tests already prove the value round-
// trips. What was untested is the JOIN — set the real setting, then run the real
// consuming flow, and observe the difference the customer would. That join is
// where "I turned it off but it still happened" bugs live.

// initedProjectForConfigE2E builds a real initialized project (git repo +
// .sageox) and isolates user-level config so the host's real ~/.sageox cannot
// bleed into the resolved value. cwd is set to the project so the real commands
// resolve it the way they do for a coworker standing in their repo.
func initedProjectForConfigE2E(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("short: real git + config file writes")
	}
	if _, err := os.Stat("/dev/null"); err != nil {
		t.Skip("needs a real filesystem")
	}
	root := t.TempDir()
	runGit(t, root, "init")
	sageox := filepath.Join(root, ".sageox")
	require.NoError(t, os.MkdirAll(sageox, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(sageox, "config.json"),
		[]byte(`{"config_version":"2","repo_id":"repo_config_e2e"}`), 0644))

	// Isolate user config: OX_USER_CONFIG points at a path that does not exist,
	// so LoadUserConfig contributes nothing and the resolved value is exactly
	// (repo config) over (default) — no host bleed-through.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("OX_USER_CONFIG", filepath.Join(home, "user-config.yaml"))
	t.Setenv("SAGEOX_AGENT_ID", "") // human commit path: attribute unconditionally
	t.Chdir(root)
	return root
}

// oxConfigSetRepo drives the REAL `ox config set <key> <value> --repo` command
// (flag parsing, attribution case-preservation, file write) — not SetConfigValue
// in isolation.
func oxConfigSetRepo(t *testing.T, key, value string) {
	t.Helper()
	require.NoError(t, configSetCmd.Flags().Set("repo", "true"))
	t.Cleanup(func() { _ = configSetCmd.Flags().Set("repo", "false") })
	require.NoError(t, runConfigSet(configSetCmd, []string{key, value}))
	require.NoError(t, configSetCmd.Flags().Set("repo", "false"))
}

// TestConfigE2E_AttributionCommitControlsTheCommitTrailer proves the customer
// promise: what a coworker sets for attribution.commit is exactly what lands in
// their real commit message — including turning it off entirely.
//
// The observable is the commit message the prepare-commit-msg hook produces —
// the artifact a teammate reads in `git log`. Failure prevented: a coworker who
// disabled attribution still ships the SageOx trailer (or a custom trailer is
// silently ignored). Red-first: hardcode the hook to the default attribution
// (ignore config) and the "off" and "custom" assertions fail.
func TestConfigE2E_AttributionCommitControlsTheCommitTrailer(t *testing.T) {
	initedProjectForConfigE2E(t)

	writeMsg := func() string {
		f := filepath.Join(t.TempDir(), "COMMIT_EDITMSG")
		require.NoError(t, os.WriteFile(f, []byte("feat: ship a thing\n"), 0644))
		return f
	}
	runCommitMsgHook := func(msgFile string) string {
		hooksCommitMsgFile = msgFile
		hooksCommitMsgSource = ""
		t.Cleanup(func() { hooksCommitMsgFile = ""; hooksCommitMsgSource = "" })
		require.NoError(t, runHooksCommitMsg(hooksCommitMsgCmd, nil))
		out, err := os.ReadFile(msgFile)
		require.NoError(t, err)
		return string(out)
	}

	// Given the default config: an ox-guided commit carries the SageOx trailer.
	assert.Contains(t, runCommitMsgHook(writeMsg()),
		"Co-Authored-By: SageOx <ox@sageox.ai>",
		"with default config, a commit carries the SageOx co-author trailer")

	// When the coworker disables attribution via the real command...
	oxConfigSetRepo(t, "attribution.commit", "")
	// Then the trailer disappears from the real commit message entirely.
	assert.NotContains(t, runCommitMsgHook(writeMsg()), "Co-Authored-By",
		`ox config set attribution.commit "" removes the trailer from real commits`)

	// When the coworker sets a custom attribution via the real command...
	oxConfigSetRepo(t, "attribution.commit", "Co-Authored-By: Devon <devon@example.com>")
	// Then exactly that trailer lands, replacing (not stacking) the default.
	got := runCommitMsgHook(writeMsg())
	assert.Contains(t, got, "Co-Authored-By: Devon <devon@example.com>",
		"a custom attribution.commit is exactly what lands in the commit message")
	assert.NotContains(t, got, "SageOx <ox@sageox.ai>",
		"the custom value replaces the default trailer, it does not stack on top")
}
