//go:build !short

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionMarkerDir(t *testing.T) {
	dir := SessionMarkerDir()
	assert.Contains(t, dir, "sageox")
	assert.True(t, strings.HasSuffix(dir, "sessions"))
}

func TestMarkerPath(t *testing.T) {
	t.Run("simple session ID", func(t *testing.T) {
		path := markerPath("abc123")
		assert.Equal(t, filepath.Join(SessionMarkerDir(), "abc123.json"), path)
	})

	t.Run("sanitizes path traversal", func(t *testing.T) {
		path := markerPath("../../../etc/passwd")
		assert.True(t, strings.HasPrefix(path, SessionMarkerDir()))
		assert.NotContains(t, path, "..")
	})

	t.Run("sanitizes slashes", func(t *testing.T) {
		path := markerPath("path/to/session")
		assert.NotContains(t, path, "/to/")
	})
}

func TestSessionMarkerReadWrite(t *testing.T) {
	t.Run("write and read marker", func(t *testing.T) {
		sessionID := "test_" + time.Now().Format("20060102150405.000")
		marker := &SessionMarker{
			AgentID:        "OxTest",
			SessionID:      "oxsid_test123",
			AgentSessionID: sessionID,
			PrimedAt:       time.Now().Truncate(time.Second),
		}

		// write
		err := WriteSessionMarker(marker)
		require.NoError(t, err)
		t.Cleanup(func() {
			DeleteSessionMarker(sessionID)
		})

		// read back
		read, err := ReadSessionMarker(sessionID)
		require.NoError(t, err)
		require.NotNil(t, read)

		assert.Equal(t, marker.AgentID, read.AgentID)
		assert.Equal(t, marker.SessionID, read.SessionID)
		assert.Equal(t, marker.AgentSessionID, read.AgentSessionID)
		assert.Equal(t, marker.PrimedAt.Unix(), read.PrimedAt.Unix())
	})

	t.Run("read non-existent marker returns nil", func(t *testing.T) {
		read, err := ReadSessionMarker("nonexistent_session_id")
		assert.NoError(t, err)
		assert.Nil(t, read)
	})

	t.Run("read empty session ID returns nil", func(t *testing.T) {
		read, err := ReadSessionMarker("")
		assert.NoError(t, err)
		assert.Nil(t, read)
	})

	t.Run("delete marker", func(t *testing.T) {
		sessionID := "test_delete_" + time.Now().Format("20060102150405.000")
		marker := &SessionMarker{
			AgentID:        "OxDel",
			AgentSessionID: sessionID,
			PrimedAt:       time.Now(),
		}

		// write
		err := WriteSessionMarker(marker)
		require.NoError(t, err)

		// verify exists
		read, err := ReadSessionMarker(sessionID)
		require.NoError(t, err)
		require.NotNil(t, read)

		// delete
		err = DeleteSessionMarker(sessionID)
		require.NoError(t, err)

		// verify gone
		read, err = ReadSessionMarker(sessionID)
		require.NoError(t, err)
		assert.Nil(t, read)
	})

	t.Run("delete non-existent marker is no-op", func(t *testing.T) {
		err := DeleteSessionMarker("nonexistent_delete_test")
		assert.NoError(t, err)
	})

	t.Run("delete empty session ID is no-op", func(t *testing.T) {
		err := DeleteSessionMarker("")
		assert.NoError(t, err)
	})
}

func TestSessionMarkerJSONFormat(t *testing.T) {
	// verify the marker file is in JSON format
	sessionID := "test_format_" + time.Now().Format("20060102150405.000")
	marker := &SessionMarker{
		AgentID:        "OxFmt",
		SessionID:      "oxsid_format123",
		AgentSessionID: sessionID,
		PrimedAt:       time.Unix(1700000000, 0),
	}

	err := WriteSessionMarker(marker)
	require.NoError(t, err)
	t.Cleanup(func() {
		DeleteSessionMarker(sessionID)
	})

	// read raw file content
	path := markerPath(sessionID)
	content, err := os.ReadFile(path)
	require.NoError(t, err)

	// verify JSON format
	assert.Contains(t, string(content), `"agent_id": "OxFmt"`)
	assert.Contains(t, string(content), `"session_id": "oxsid_format123"`)
	assert.Contains(t, string(content), `"agent_session_id"`)
}

func TestIsAgentHookContext(t *testing.T) {
	// save original env
	origProjectDir := os.Getenv("CLAUDE_PROJECT_DIR")
	origClaudeCode := os.Getenv("CLAUDECODE")
	origEntrypoint := os.Getenv("CLAUDE_CODE_ENTRYPOINT")
	t.Cleanup(func() {
		os.Setenv("CLAUDE_PROJECT_DIR", origProjectDir)
		os.Setenv("CLAUDECODE", origClaudeCode)
		os.Setenv("CLAUDE_CODE_ENTRYPOINT", origEntrypoint)
	})

	t.Run("detects CLAUDE_PROJECT_DIR", func(t *testing.T) {
		os.Setenv("CLAUDE_PROJECT_DIR", "/some/project")
		os.Unsetenv("CLAUDECODE")
		os.Unsetenv("CLAUDE_CODE_ENTRYPOINT")
		assert.True(t, IsAgentHookContext())
	})

	t.Run("detects CLAUDECODE=1", func(t *testing.T) {
		os.Unsetenv("CLAUDE_PROJECT_DIR")
		os.Setenv("CLAUDECODE", "1")
		os.Unsetenv("CLAUDE_CODE_ENTRYPOINT")
		assert.True(t, IsAgentHookContext())
	})

	t.Run("detects CLAUDE_CODE_ENTRYPOINT", func(t *testing.T) {
		os.Unsetenv("CLAUDE_PROJECT_DIR")
		os.Unsetenv("CLAUDECODE")
		os.Setenv("CLAUDE_CODE_ENTRYPOINT", "cli")
		assert.True(t, IsAgentHookContext())
	})

	t.Run("returns false when no agent env vars", func(t *testing.T) {
		os.Unsetenv("CLAUDE_PROJECT_DIR")
		os.Unsetenv("CLAUDECODE")
		os.Unsetenv("CLAUDE_CODE_ENTRYPOINT")
		assert.False(t, IsAgentHookContext())
	})
}

func TestWriteToAgentEnvFile(t *testing.T) {
	t.Run("no-op when CLAUDE_ENV_FILE not set", func(t *testing.T) {
		origEnv := os.Getenv("CLAUDE_ENV_FILE")
		os.Unsetenv("CLAUDE_ENV_FILE")
		t.Cleanup(func() {
			if origEnv != "" {
				os.Setenv("CLAUDE_ENV_FILE", origEnv)
			}
		})

		err := WriteToAgentEnvFile(map[string]string{"KEY": "value"})
		assert.NoError(t, err)
	})

	t.Run("writes to env file when set", func(t *testing.T) {
		tmpDir := t.TempDir()
		envFile := filepath.Join(tmpDir, "env")

		origEnv := os.Getenv("CLAUDE_ENV_FILE")
		os.Setenv("CLAUDE_ENV_FILE", envFile)
		t.Cleanup(func() {
			if origEnv != "" {
				os.Setenv("CLAUDE_ENV_FILE", origEnv)
			} else {
				os.Unsetenv("CLAUDE_ENV_FILE")
			}
		})

		err := WriteToAgentEnvFile(map[string]string{
			"AGENT_ENV":       "claude-code",
			"SAGEOX_AGENT_ID": "OxEnv1",
		})
		require.NoError(t, err)

		// verify content
		content, err := os.ReadFile(envFile)
		require.NoError(t, err)
		assert.Contains(t, string(content), `export AGENT_ENV="claude-code"`)
		assert.Contains(t, string(content), `export SAGEOX_AGENT_ID="OxEnv1"`)
	})
}

// TestWriteToAgentEnvFile_WritesAgentIDWithoutSessionID is the regression test for
// #258: env file must be written even when SAGEOX_SESSION_ID is empty (no hook stdin,
// no session ID). The agent ID must always reach the env file so /clear in Claude
// Code inherits the correct agent ID.
func TestWriteToAgentEnvFile_WritesAgentIDWithoutSessionID(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, "env")

	origEnv := os.Getenv("CLAUDE_ENV_FILE")
	os.Setenv("CLAUDE_ENV_FILE", envFile)
	t.Cleanup(func() {
		if origEnv != "" {
			os.Setenv("CLAUDE_ENV_FILE", origEnv)
		} else {
			os.Unsetenv("CLAUDE_ENV_FILE")
		}
	})

	// simulate what the fixed prime code does: write both vars unconditionally,
	// even when session ID is empty (no hook context)
	err := WriteToAgentEnvFile(map[string]string{
		"SAGEOX_AGENT_ID":   "OxTest",
		"SAGEOX_SESSION_ID": "", // empty — no session from hook stdin
	})
	require.NoError(t, err)

	content, err := os.ReadFile(envFile)
	require.NoError(t, err)
	// agent ID must always be written regardless of empty session ID
	assert.Contains(t, string(content), `export SAGEOX_AGENT_ID="OxTest"`,
		"SAGEOX_AGENT_ID must be written to env file even when session ID is empty")
}

// TestWriteToAgentEnvFile_Idempotent verifies upsert semantics: a second
// write of the same key replaces the first, leaving exactly one definition
// in the file rather than stacking duplicates.
//
// Failure prevented: #527/#529 cross-session AGENT_ENV poisoning. Without
// upsert, a second prime that mis-claims AGENT_ENV=pi after an earlier
// AGENT_ENV=claude-code would leave both lines in the file and any later
// subprocess (re-sourcing the file) could inherit the wrong value
// depending on ordering and shell semantics.
func TestWriteToAgentEnvFile_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, "env")

	origEnv := os.Getenv("CLAUDE_ENV_FILE")
	os.Setenv("CLAUDE_ENV_FILE", envFile)
	t.Cleanup(func() {
		if origEnv != "" {
			os.Setenv("CLAUDE_ENV_FILE", origEnv)
		} else {
			os.Unsetenv("CLAUDE_ENV_FILE")
		}
	})

	// first prime call
	require.NoError(t, WriteToAgentEnvFile(map[string]string{
		"SAGEOX_AGENT_ID": "OxFirst",
	}))

	// second prime call (after /clear) with a different agent ID
	require.NoError(t, WriteToAgentEnvFile(map[string]string{
		"SAGEOX_AGENT_ID": "OxSecond",
	}))

	content, err := os.ReadFile(envFile)
	require.NoError(t, err)
	// second write wins; first is not retained
	assert.Contains(t, string(content), `export SAGEOX_AGENT_ID="OxSecond"`)
	assert.NotContains(t, string(content), `export SAGEOX_AGENT_ID="OxFirst"`,
		"first write must be replaced, not stacked — append-and-stack is the #527/#529 bug class")
	// exactly one export line for this key
	assert.Equal(t, 1, strings.Count(string(content), `export SAGEOX_AGENT_ID=`),
		"only one export line per key should remain after upsert")
}

// TestWriteToAgentEnvFile_AgentEnvUpsertAfterAdapterMismatch is a direct
// reproducer for #527: the first prime (from the correct SessionStart hook)
// sets AGENT_ENV=claude-code; a second prime driven by a tainted CLAUDE.md
// block would have injected AGENT_ENV=pi. After upsert, only the latest
// write should remain — and this test pins that contract.
func TestWriteToAgentEnvFile_AgentEnvUpsertAfterAdapterMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, "env")

	origEnv := os.Getenv("CLAUDE_ENV_FILE")
	os.Setenv("CLAUDE_ENV_FILE", envFile)
	t.Cleanup(func() {
		if origEnv != "" {
			os.Setenv("CLAUDE_ENV_FILE", origEnv)
		} else {
			os.Unsetenv("CLAUDE_ENV_FILE")
		}
	})

	// hook-driven prime: correct agent type
	require.NoError(t, WriteToAgentEnvFile(map[string]string{
		"AGENT_ENV": "claude-code",
	}))

	// CLAUDE.md-driven re-prime that wrongly claims pi (pre-fix #527 shape)
	require.NoError(t, WriteToAgentEnvFile(map[string]string{
		"AGENT_ENV": "pi",
	}))

	content, err := os.ReadFile(envFile)
	require.NoError(t, err)
	// only the last write should remain in the file
	assert.Equal(t, 1, strings.Count(string(content), `export AGENT_ENV=`),
		"AGENT_ENV must not stack across primes")
}

// TestFindSessionMarkerByPID_MatchesParentPID is the #527/#529 regression
// guard for PID-based marker fallback: a second prime call that lacks a
// native agent_session_id (e.g. invoked from a CLAUDE.md BLOCKING
// instruction with no hook stdin) must still locate the marker the
// hook-driven prime wrote earlier in the same agent process. Without this,
// the second prime falls through to fresh-prime, appending a duplicate row
// to agent_instances.jsonl.
//
// Uses the current test process PID so the proc.IsAlive liveness gate
// passes — a recycled/dead PID is specifically what the gate filters out.
func TestFindSessionMarkerByPID_MatchesParentPID(t *testing.T) {
	agentPID := os.Getpid()
	sessionID := "findbyPIDtest_" + time.Now().Format("20060102150405.000")

	marker := &SessionMarker{
		AgentID:        "OxFindByPID",
		AgentSessionID: sessionID,
		PrimedAt:       time.Now().Truncate(time.Second),
		ParentPID:      agentPID,
	}
	require.NoError(t, WriteSessionMarker(marker))
	t.Cleanup(func() { DeleteSessionMarker(sessionID) })

	found := FindSessionMarkerByPID(agentPID)
	require.NotNil(t, found, "marker with matching ParentPID must be found")
	assert.Equal(t, "OxFindByPID", found.AgentID)
	assert.Equal(t, sessionID, found.AgentSessionID)
}

// TestFindSessionMarkerByPID_IgnoresDeadParentPID is the liveness regression
// guard: markers whose ParentPID no longer corresponds to a running process
// must not be returned, even if their PID field matches the query. Stale
// markers from crashed sessions are a primary source of cross-session
// identity bleed — see code-reviewer round 2 SUGGESTION on #527 PID fallback.
//
// Spawns a short-lived child process and records its PID, then Wait()s to
// reap it so we have a deterministically-dead PID to assert against —
// strictly safer than a "probably unused" integer which could flake on
// machines with long-running processes holding that PID.
func TestFindSessionMarkerByPID_IgnoresDeadParentPID(t *testing.T) {
	cmd := exec.Command("true")
	require.NoError(t, cmd.Start())
	deadPID := cmd.Process.Pid
	require.NoError(t, cmd.Wait()) // reap — PID is now guaranteed dead

	sessionID := "findbyPIDdead_" + time.Now().Format("20060102150405.000")
	marker := &SessionMarker{
		AgentID:        "OxDead",
		AgentSessionID: sessionID,
		PrimedAt:       time.Now().Truncate(time.Second),
		ParentPID:      deadPID,
	}
	require.NoError(t, WriteSessionMarker(marker))
	t.Cleanup(func() { DeleteSessionMarker(sessionID) })

	got := FindSessionMarkerByPID(deadPID)
	assert.Nil(t, got, "marker whose ParentPID is not alive must be rejected")
}

// TestFindSessionMarkerByPID_IgnoresNonMatchingPID confirms the scan is
// strict: unrelated markers on the system must not be returned for a PID
// they don't reference.
func TestFindSessionMarkerByPID_IgnoresNonMatchingPID(t *testing.T) {
	sessionID := "findbyPIDnomatch_" + time.Now().Format("20060102150405.000")
	marker := &SessionMarker{
		AgentID:        "OxOther",
		AgentSessionID: sessionID,
		PrimedAt:       time.Now().Truncate(time.Second),
		ParentPID:      111111,
	}
	require.NoError(t, WriteSessionMarker(marker))
	t.Cleanup(func() { DeleteSessionMarker(sessionID) })

	// query for a PID that no marker references
	got := FindSessionMarkerByPID(222222)
	assert.Nil(t, got, "must not return a marker whose ParentPID differs")
}

// TestFindSessionMarkerByPID_RejectsInvalidPID guards against matching
// placeholder / unset PID values (0, negative) against a stored marker
// that happens to record PID 0 from an earlier buggy write.
func TestFindSessionMarkerByPID_RejectsInvalidPID(t *testing.T) {
	assert.Nil(t, FindSessionMarkerByPID(0))
	assert.Nil(t, FindSessionMarkerByPID(-1))
}
