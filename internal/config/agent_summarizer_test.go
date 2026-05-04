package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAgentSummarizerDefaults_Inline verifies that, with no env vars and no
// user config, the resolver returns `inline`. This is the load-bearing default —
// we deliberately favor cheap-by-default over non-blocking-by-default.
// Failure prevented: a future refactor flips the default to `delegated`,
// silently 10×-ing the token bill for every user on session-stop.
func TestAgentSummarizerDefaults_Inline(t *testing.T) {
	t.Setenv("OX_SESSION_INLINE_SUMMARY", "")
	t.Setenv("SAGEOX_ASYNC_SESSION_UPLOAD", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	resolved := ResolveAgentSummarizer("")

	assert.Equal(t, AgentSummarizerInline, resolved.Mode,
		"default must be inline — see ADR-016 cost rationale")
	assert.Equal(t, AgentSummarizerSourceDefault, resolved.Source)
}

// TestAgentSummarizerEnvShim_Inline verifies that the legacy env var
// OX_SESSION_INLINE_SUMMARY=1 still forces inline, with the source labeled
// as deprecated so the resolver can log a deprecation warning.
// Failure prevented: removing the env var shim before users have migrated
// to `ox config set agent.summarizer ...`.
func TestAgentSummarizerEnvShim_Inline(t *testing.T) {
	t.Setenv("OX_SESSION_INLINE_SUMMARY", "1")
	t.Setenv("SAGEOX_ASYNC_SESSION_UPLOAD", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	resolved := ResolveAgentSummarizer("")
	assert.Equal(t, AgentSummarizerInline, resolved.Mode)
	assert.Equal(t, AgentSummarizerSourceEnvDeprecated, resolved.Source)
}

// TestAgentSummarizerEnvShim_Delegated verifies that the legacy
// SAGEOX_ASYNC_SESSION_UPLOAD=1 still forces delegated.
func TestAgentSummarizerEnvShim_Delegated(t *testing.T) {
	t.Setenv("OX_SESSION_INLINE_SUMMARY", "")
	t.Setenv("SAGEOX_ASYNC_SESSION_UPLOAD", "1")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	resolved := ResolveAgentSummarizer("")
	assert.Equal(t, AgentSummarizerDelegated, resolved.Mode)
	assert.Equal(t, AgentSummarizerSourceEnvDeprecated, resolved.Source)
}

// TestAgentSummarizerEnvShim_InlineWinsOverDelegated verifies that when both
// legacy env vars are set (a confused user state), the inline-forcing one
// takes precedence — the cheap path wins ties, matching the cheap-by-default
// philosophy.
func TestAgentSummarizerEnvShim_InlineWinsOverDelegated(t *testing.T) {
	t.Setenv("OX_SESSION_INLINE_SUMMARY", "1")
	t.Setenv("SAGEOX_ASYNC_SESSION_UPLOAD", "1")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	resolved := ResolveAgentSummarizer("")
	assert.Equal(t, AgentSummarizerInline, resolved.Mode,
		"when both legacy env vars are set, the cheap path wins")
}

// TestAgentSummarizerUserConfig_Delegated verifies that user config can
// override the default to `delegated`.
func TestAgentSummarizerUserConfig_Delegated(t *testing.T) {
	t.Setenv("OX_SESSION_INLINE_SUMMARY", "")
	t.Setenv("SAGEOX_ASYNC_SESSION_UPLOAD", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cfg := &UserConfig{AgentSummarizer: AgentSummarizerDelegated}
	require.NoError(t, SaveUserConfig(cfg))

	resolved := ResolveAgentSummarizer("")
	assert.Equal(t, AgentSummarizerDelegated, resolved.Mode)
	assert.Equal(t, AgentSummarizerSourceUserConfig, resolved.Source)
}

// TestAgentSummarizerUserConfig_Off verifies that user config can disable
// LLM summarization entirely.
func TestAgentSummarizerUserConfig_Off(t *testing.T) {
	t.Setenv("OX_SESSION_INLINE_SUMMARY", "")
	t.Setenv("SAGEOX_ASYNC_SESSION_UPLOAD", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cfg := &UserConfig{AgentSummarizer: AgentSummarizerOff}
	require.NoError(t, SaveUserConfig(cfg))

	resolved := ResolveAgentSummarizer("")
	assert.Equal(t, AgentSummarizerOff, resolved.Mode)
}

// TestAgentSummarizerEnvShim_OverridesUserConfig verifies that the legacy env
// vars take precedence over user config. This is intentional: power users
// can scope-test alternate modes without touching their persisted config.
// Failure prevented: a user setting agent.summarizer=delegated then trying
// to test inline via env var unexpectedly gets delegated.
func TestAgentSummarizerEnvShim_OverridesUserConfig(t *testing.T) {
	t.Setenv("OX_SESSION_INLINE_SUMMARY", "1")
	t.Setenv("SAGEOX_ASYNC_SESSION_UPLOAD", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cfg := &UserConfig{AgentSummarizer: AgentSummarizerDelegated}
	require.NoError(t, SaveUserConfig(cfg))

	resolved := ResolveAgentSummarizer("")
	assert.Equal(t, AgentSummarizerInline, resolved.Mode,
		"env shim must override user config so power users can scope-test modes")
	assert.Equal(t, AgentSummarizerSourceEnvDeprecated, resolved.Source)
}

func TestIsValidAgentSummarizer(t *testing.T) {
	tests := []struct {
		mode string
		want bool
	}{
		{"inline", true},
		{"delegated", true},
		{"off", true},
		{"", true}, // unset is valid (callers pick default)
		{"cloud", false},
		{"bogus", false},
		{"INLINE", false}, // case-sensitive
	}
	for _, tc := range tests {
		t.Run(tc.mode, func(t *testing.T) {
			assert.Equal(t, tc.want, IsValidAgentSummarizer(tc.mode))
		})
	}
}

// TestNormalizeAgentSummarizer_FallsBackToInline verifies that any unrecognized
// or empty value normalizes to inline — the cheap default. Important because
// upstream config readers may pass through stale or hand-edited YAML; we
// don't want a typo to silently flip the user to the expensive path.
func TestNormalizeAgentSummarizer_FallsBackToInline(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"inline", "inline"},
		{"delegated", "delegated"},
		{"off", "off"},
		{"", "inline"},      // unset → default
		{"cloud", "inline"}, // reserved → fall back, never silently activate
		{"DELEGATED", "inline"},
		{"   inline   ", "inline"}, // no fancy trimming today; if you need it, normalize at the source
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := NormalizeAgentSummarizer(tc.in)
			// the trim case is intentionally not handled — assert what we actually do:
			if tc.in == "   inline   " {
				assert.Equal(t, "inline", got, "untrimmed input falls through to default")
			} else {
				assert.Equal(t, tc.want, got)
			}
		})
	}
}
