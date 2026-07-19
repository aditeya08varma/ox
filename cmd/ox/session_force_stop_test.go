package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetSessionForceStopFlags restores sessionForceStopCmd's flags to their
// zero values before each test. sessionForceStopCmd is a package-level
// singleton (registered once in init()), so tests must reset flag state
// rather than relying on a fresh command — same house style as
// TestSessionRegenerateRedactFlagValidation in session_regenerate_test.go.
func resetSessionForceStopFlags(t *testing.T) {
	t.Helper()
	require.NoError(t, sessionForceStopCmd.Flags().Set("agent-id", ""))
	require.NoError(t, sessionForceStopCmd.Flags().Set("current", "false"))
}

// createStoppableRecording writes a minimal active recording with an empty
// AdapterName. runAgentSessionStop treats an empty AdapterName as "no
// adapter to rediscover a session file from," skipping straight to the
// "no session file, clear state" branch — so the stop completes
// deterministically without touching a real coding-agent session file on
// disk. Fixtures with AdapterName set (e.g. createActiveRecording's
// "claude-code") instead take the file-rediscovery path and leave the
// recording state preserved for recovery when no real file exists, which
// would make the "did stop succeed" assertion flaky/false here.
func createStoppableRecording(t *testing.T, projectRoot, repoID, agentID string) {
	t.Helper()
	sessionsBase := filepath.Join(session.GetContextPath(repoID), "sessions")
	sessionPath := filepath.Join(sessionsBase, "2026-01-01T00-00-user-"+agentID)

	state := &session.RecordingState{
		AgentID:     agentID,
		StartedAt:   time.Now().Add(-1 * time.Minute),
		SessionPath: sessionPath,
		ParentPID:   os.Getpid(),
	}
	require.NoError(t, session.SaveRecordingState(projectRoot, state))
}

// withStubbedGlobalConfig sets the package-level cfg (read by
// runAgentSessionStop for output-format selection) to a non-nil zero value
// and restores the previous value on cleanup. Without this, runAgentSessionStop
// dereferences a nil cfg and panics — see agent_session_manual_publish_test.go
// for the same requirement.
func withStubbedGlobalConfig(t *testing.T) {
	t.Helper()
	old := cfg
	cfg = &config.Config{}
	t.Cleanup(func() { cfg = old })
}

// --- A. Flag validation — these fail before touching project/recording
// state at all, so no project fixture is needed. ---

// TestSessionForceStop_CurrentAndAgentIDMutuallyExclusive verifies --current
// and --agent-id together are rejected rather than one silently winning.
// Failure prevented: an agent passing both flags (e.g. from a templated
// command) silently stops the wrong session instead of getting an error.
func TestSessionForceStop_CurrentAndAgentIDMutuallyExclusive(t *testing.T) {
	resetSessionForceStopFlags(t)
	cmd := sessionForceStopCmd
	require.NoError(t, cmd.Flags().Set("current", "true"))
	require.NoError(t, cmd.Flags().Set("agent-id", "OxSomeAgent"))

	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

// TestSessionForceStop_CurrentRequiresEnvVar verifies --current without
// SAGEOX_AGENT_ID set fails with the same error text session_status.go uses
// for its --current flag, rather than a bespoke message or a nil-agent-ID
// crash further down the call chain.
func TestSessionForceStop_CurrentRequiresEnvVar(t *testing.T) {
	resetSessionForceStopFlags(t)
	t.Setenv("SAGEOX_AGENT_ID", "")
	cmd := sessionForceStopCmd
	require.NoError(t, cmd.Flags().Set("current", "true"))

	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	assert.Equal(t, "--current requires SAGEOX_AGENT_ID environment variable (set by 'ox agent prime')", err.Error())
}

// --- B. Resolution + stop — exercise the full RunE against a real fixture
// on disk, proving --current reaches the same stop machinery --agent-id
// does (and, as a regression baseline, that --agent-id itself works at
// all — it had zero test coverage before this file). ---

// TestSessionForceStop_CurrentResolvesAndStops verifies --current resolves
// the agent ID from SAGEOX_AGENT_ID and stops that agent's recording.
// Failure prevented: --current parses but never actually reaches the stop
// path (e.g. a wiring mistake that resolves agentID but forgets to use it).
func TestSessionForceStop_CurrentResolvesAndStops(t *testing.T) {
	resetSessionForceStopFlags(t)
	withStubbedGlobalConfig(t)

	projectRoot, repoID := setupTestProject(t)
	t.Setenv(config.EnvProjectRoot, projectRoot)

	agentID := "OxCurStop1"
	createStoppableRecording(t, projectRoot, repoID, agentID)
	t.Setenv("SAGEOX_AGENT_ID", agentID)

	cmd := sessionForceStopCmd
	require.NoError(t, cmd.Flags().Set("current", "true"))

	err := cmd.RunE(cmd, nil)
	require.NoError(t, err)

	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	assert.Nil(t, state, "recording should be cleared after 'session stop --current'")
}

// TestSessionForceStop_AgentIDFlagStopsRecording is a regression baseline
// for the pre-existing --agent-id path, which had no test coverage at all
// before this file. Failure prevented: a future change to the shared
// resolve-then-stop logic silently breaking the original --agent-id
// behavior while --current tests stay green.
func TestSessionForceStop_AgentIDFlagStopsRecording(t *testing.T) {
	resetSessionForceStopFlags(t)
	withStubbedGlobalConfig(t)

	projectRoot, repoID := setupTestProject(t)
	t.Setenv(config.EnvProjectRoot, projectRoot)

	agentID := "OxAgentIDStop1"
	createStoppableRecording(t, projectRoot, repoID, agentID)

	cmd := sessionForceStopCmd
	require.NoError(t, cmd.Flags().Set("agent-id", agentID))

	err := cmd.RunE(cmd, nil)
	require.NoError(t, err)

	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	assert.Nil(t, state, "recording should be cleared after 'session stop --agent-id'")
}

// TestSessionForceStop_AgentIDFlagUnknownAgent verifies the pre-existing
// "no active recording found" error still fires for an agent ID that
// doesn't match any recording — a second regression baseline for the
// previously-untested --agent-id path.
func TestSessionForceStop_AgentIDFlagUnknownAgent(t *testing.T) {
	resetSessionForceStopFlags(t)

	projectRoot, _ := setupTestProject(t)
	t.Setenv(config.EnvProjectRoot, projectRoot)

	cmd := sessionForceStopCmd
	require.NoError(t, cmd.Flags().Set("agent-id", "OxNoSuchAgent"))

	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `no active recording found for agent "OxNoSuchAgent"`)
}
