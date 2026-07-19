package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/sessionid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. Identity at birth ---

// TestStartRecording_MintsSessionIDAtBirth verifies the durable ses_<UUIDv7>
// exists from t=0 and survives the disk round-trip.
// Failure prevented: conversation URLs (/c/<ses_id>) circulated during a live
// session (commit trailers, PR bodies) pointing at an ID that never existed
// because it was only minted at stop.
func TestStartRecording_MintsSessionIDAtBirth(t *testing.T) {
	cacheDir := t.TempDir()
	projectRoot := setupRecordingTest(t, cacheDir)

	sessionFile := filepath.Join(t.TempDir(), "session.jsonl")
	require.NoError(t, os.WriteFile(sessionFile, []byte("{}\n"), 0644))

	state, err := StartRecording(projectRoot, StartRecordingOptions{
		AgentID:     "OxSeS1",
		AdapterName: "claude-code",
		SessionFile: sessionFile,
		Username:    "testuser",
	})
	require.NoError(t, err)
	require.NotNil(t, state)

	assert.True(t, sessionid.IsValidSessionID(state.SessionID),
		"StartRecording must mint a valid ses_ ID at birth, got %q", state.SessionID)

	// disk round-trip: the ID every later reader (hook, prime, stop) sees is
	// the exact minted value, never regenerated
	reloaded, err := LoadRecordingStateForAgent(projectRoot, "OxSeS1")
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	assert.Equal(t, state.SessionID, reloaded.SessionID)
}

// TestRecordingState_LegacyJSONWithoutSessionID verifies recordings started
// under an older binary (no session_id field) load with an empty ID so every
// emitter falls back to the name-based URL instead of crashing or inventing
// an identity.
// Failure prevented: mid-upgrade recordings break the commit hook or get a
// fabricated /c/ link that resolves to nothing.
func TestRecordingState_LegacyJSONWithoutSessionID(t *testing.T) {
	projectRoot := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	sessionsDir := filepath.Join(projectRoot, "sessions", "2026-01-01T00-00-user-OxLEG1")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))
	legacy := `{"agent_id":"OxLEG1","started_at":"2026-01-01T00:00:00Z","session_path":"` + sessionsDir + `","workspace_path":"` + projectRoot + `"}`
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, ".recording.json"), []byte(legacy), 0o644))

	state, err := LoadRecordingStateForAgent(projectRoot, "OxLEG1")
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Empty(t, state.SessionID, "legacy recording must load with empty SessionID (fallback to name URL)")
}

// --- B. Raw-header crash-safe carrier ---

// TestParseStoreMeta_SessionIDKeyDisambiguation verifies the overloaded
// "session_id" header key parses correctly in BOTH directions: ox headers
// carry a ses_-prefixed recording ID; the alternative format (e.g. manual
// session capture, third-party raws) uses the same key as an AGENT
// identifier.
// Failure prevented: a daemon orphan-finalize misreading an agent id as the
// recording identity (dangling /c/ links), or an alternative-format agent id
// vanishing because it was mistaken for a recording ID.
func TestParseStoreMeta_SessionIDKeyDisambiguation(t *testing.T) {
	sesID := "ses_01890a5d-ac96-774b-bcce-b302099a8057"

	tests := []struct {
		name          string
		header        map[string]any
		wantAgentID   string
		wantSessionID string
	}{
		{
			name:          "ox header carries both agent_id and ses_ recording id",
			header:        map[string]any{"version": "1.0", "agent_id": "OxA1b2", "session_id": sesID},
			wantAgentID:   "OxA1b2",
			wantSessionID: sesID,
		},
		{
			name:          "alternative format session_id is an agent identifier",
			header:        map[string]any{"schema_version": "1", "session_id": "manual"},
			wantAgentID:   "manual",
			wantSessionID: "",
		},
		{
			name:          "ses_-valued session_id without agent_id is a recording id, not an agent",
			header:        map[string]any{"version": "1.0", "session_id": sesID},
			wantAgentID:   "",
			wantSessionID: sesID,
		},
		{
			name:          "legacy ox header without session_id",
			header:        map[string]any{"version": "1.0", "agent_id": "OxA1b2"},
			wantAgentID:   "OxA1b2",
			wantSessionID: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta := ParseStoreMeta(tt.header)
			require.NotNil(t, meta)
			assert.Equal(t, tt.wantAgentID, meta.AgentID)
			assert.Equal(t, tt.wantSessionID, meta.SessionID)
		})
	}
}
