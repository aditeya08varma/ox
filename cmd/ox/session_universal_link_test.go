package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. Universal conversation URL builder ---

func TestBuildConversationURL(t *testing.T) {
	cfg := &config.ProjectConfig{RepoID: "repo_01abc", Endpoint: "https://sageox.ai"}
	tests := []struct {
		name      string
		cfg       *config.ProjectConfig
		sessionID string
		expected  string
	}{
		{
			name:      "valid ses_ UUIDv7",
			cfg:       cfg,
			sessionID: "ses_01890a5d-ac96-774b-bcce-b302099a8057",
			expected:  "https://sageox.ai/c/ses_01890a5d-ac96-774b-bcce-b302099a8057",
		},
		{
			// legacy sessions derive a deterministic UUIDv5 EffectiveSessionID —
			// their /c/ links must build identically
			name:      "legacy ses_ UUIDv5 accepted",
			cfg:       cfg,
			sessionID: "ses_74738ff5-5367-5958-9aee-98fffdcd1876",
			expected:  "https://sageox.ai/c/ses_74738ff5-5367-5958-9aee-98fffdcd1876",
		},
		{name: "empty id (old-binary recording)", cfg: cfg, sessionID: "", expected: ""},
		{name: "agent id is not a session id", cfg: cfg, sessionID: "Ox7f3a", expected: ""},
		{name: "alt-format manual id rejected", cfg: cfg, sessionID: "manual", expected: ""},
		{name: "nil config", cfg: nil, sessionID: "ses_01890a5d-ac96-774b-bcce-b302099a8057", expected: ""},
		{
			name:      "normalizes www prefix",
			cfg:       &config.ProjectConfig{RepoID: "repo_01abc", Endpoint: "https://www.sageox.ai"},
			sessionID: "ses_01890a5d-ac96-774b-bcce-b302099a8057",
			expected:  "https://sageox.ai/c/ses_01890a5d-ac96-774b-bcce-b302099a8057",
		},
		{
			// unlike buildSessionURL, the conversation link must not require a
			// repo id — it is the durable identity-only URL
			name:      "no repo id still builds",
			cfg:       &config.ProjectConfig{Endpoint: "https://sageox.ai"},
			sessionID: "ses_01890a5d-ac96-774b-bcce-b302099a8057",
			expected:  "https://sageox.ai/c/ses_01890a5d-ac96-774b-bcce-b302099a8057",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, buildConversationURL(tt.cfg, tt.sessionID))
		})
	}
}

// --- B. Commit trailer prefers /c/, falls back for legacy recordings ---

// runCommitMsgHookOn writes subject to a msg file, runs the production
// trailer injector, and returns the mutated message.
func runCommitMsgHookOn(t *testing.T, subject string) string {
	t.Helper()
	msgFile := filepath.Join(t.TempDir(), "COMMIT_EDITMSG")
	require.NoError(t, os.WriteFile(msgFile, []byte(subject+"\n"), 0o644))

	prevSource, prevFile := hooksCommitMsgSource, hooksCommitMsgFile
	t.Cleanup(func() {
		hooksCommitMsgSource = prevSource
		hooksCommitMsgFile = prevFile
	})
	hooksCommitMsgSource = ""
	hooksCommitMsgFile = msgFile
	require.NoError(t, runHooksCommitMsg(nil, nil))

	content, err := os.ReadFile(msgFile)
	require.NoError(t, err)
	return string(content)
}

// TestCommitTrailer_UsesConversationURLWhenStartMinted verifies the
// prepare-commit-msg hook emits the durable /c/<ses_id> form when the
// recording carries a start-minted ID.
// Failure prevented: trailers keep pointing at the mutable name-based URL,
// so renames/collisions orphan the provenance link.
func TestCommitTrailer_UsesConversationURLWhenStartMinted(t *testing.T) {
	projectRoot, sessionName, agentID := trailerRewriteEnv(t)

	// stamp a start-minted ID into the live recording state
	sesID := "ses_01890a5d-ac96-774b-bcce-b302099a8057"
	statePath := filepath.Join(projectRoot, "sessions", sessionName, ".recording.json")
	raw, err := os.ReadFile(statePath)
	require.NoError(t, err)
	var state session.RecordingState
	require.NoError(t, json.Unmarshal(raw, &state))
	require.Equal(t, agentID, state.AgentID)
	state.SessionID = sesID
	updated, err := json.Marshal(state)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(statePath, updated, 0o644))

	msg := runCommitMsgHookOn(t, "feat: add thing")
	assert.Contains(t, msg, "SageOx-Session: https://sageox.ai/c/"+sesID)
	assert.NotContains(t, msg, "/sessions/"+sessionName+"/view",
		"must not emit the name-based URL when the durable ID exists")
}

// TestCommitTrailer_FallsBackToNameURLForLegacyRecording verifies a recording
// started under an older binary (no SessionID) still gets a resolvable
// trailer via the name-based URL.
// Failure prevented: mid-upgrade sessions silently losing commit provenance.
func TestCommitTrailer_FallsBackToNameURLForLegacyRecording(t *testing.T) {
	_, sessionName, _ := trailerRewriteEnv(t) // env writes state WITHOUT SessionID

	msg := runCommitMsgHookOn(t, "feat: add thing")
	assert.Contains(t, msg, "SageOx-Session: https://sageox.ai/repo/repo_01test/sessions/"+sessionName+"/view")
	assert.NotContains(t, msg, "/c/", "no /c/ URL may be invented without a start-minted ID")
}

// --- C. Prime emits the exact-literal PR directive ---

// TestOutputAgentPrimeXML_PRDirective verifies the session-context block
// carries the verbatim trailer instruction (with the real URL inline) when
// set, and stays silent when absent.
// Failure prevented: regressing to a templated "<session_url>" placeholder —
// the confabulation vector this design removed — or emitting the directive
// for non-recording sessions.
func TestOutputAgentPrimeXML_PRDirective(t *testing.T) {
	directive := "When you create a PR for this session's work, the LAST line of the PR body must be exactly:\nSageOx-Session: https://sageox.ai/c/ses_01890a5d-ac96-774b-bcce-b302099a8057\nIf this session is stopped or aborted, stop adding this line."

	render := func(s *sessionStatus) string {
		var buf bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&buf)
		output := agentPrimeOutput{AgentID: "abc123", Status: "fresh", Session: s}
		_, err := outputAgentPrimeXML(cmd, output)
		require.NoError(t, err)
		return buf.String()
	}

	withDirective := render(&sessionStatus{
		Recording:   true,
		SessionURL:  "https://sageox.ai/c/ses_01890a5d-ac96-774b-bcce-b302099a8057",
		PRDirective: directive,
	})
	assert.Contains(t, withDirective, "SageOx-Session: https://sageox.ai/c/ses_01890a5d-ac96-774b-bcce-b302099a8057")
	assert.Contains(t, withDirective, "LAST line of the PR body")
	assert.NotContains(t, withDirective, "<session_url>")

	withoutDirective := render(&sessionStatus{Recording: true})
	assert.NotContains(t, withoutDirective, "SageOx-Session:")
}

// --- D. Abort countermands the trailer directive ---

// TestAbortGuidance_CountermandsSessionTrailer verifies the abort output
// tells the agent to stop using the session link. The hook self-heals
// (recording state deleted), but the agent's in-context PR directive can
// only be revoked through this guidance.
// Failure prevented: an agent decorating post-abort PRs with a link to a
// recording that no longer exists.
func TestAbortGuidance_CountermandsSessionTrailer(t *testing.T) {
	prevCfg := cfg
	cfg = &config.Config{}
	t.Cleanup(func() { cfg = prevCfg })

	var buf bytes.Buffer
	require.NoError(t, emitAbortOutput(&buf, "OxAB01", "2026-01-01T00-00-user-OxAB01"))

	var out sessionAbortOutput
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out))
	assert.Contains(t, out.Guidance, "No further action needed", "existing anchor phrase must survive")
	assert.Contains(t, out.Guidance, "do not add SageOx-Session")
	assert.Contains(t, out.Guidance, "unsubmitted PR drafts")
}
