package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/paths"
)

// SessionMarkerDir returns the per-user directory for session markers.
//
// Markers are stored in /tmp/<user>/sageox/sessions/ intentionally — they are
// ephemeral and the OS cleans them on reboot. No explicit cleanup is needed.
// Stale markers from crashed or abandoned sessions are harmless.
//
// See paths.TempDir() for why /tmp/<user>/sageox/ instead of /tmp/sageox/.
func SessionMarkerDir() string {
	return filepath.Join(paths.TempDir(), "sessions")
}

// SessionMarker tracks a primed coding agent session.
//
// Created by `ox agent prime`, one marker per coding agent session (any agent,
// not just Claude Code). Keyed by the agent's native session identifier, which
// comes from hook stdin JSON (HookInput.SessionID) or an agent-specific env var
// (e.g., CLAUDE_CODE_SESSION_ID, CODEX_THREAD_ID, AMP_THREAD_URL).
//
// Purpose:
//   - Idempotency: re-priming the same session reuses the ox agent ID
//   - Hook context: agent_hook.go reads markers to pass agent state to handlers
type SessionMarker struct {
	AgentID        string    `json:"agent_id"`
	SessionID      string    `json:"session_id,omitempty"` // ox-generated server session ID
	AgentSessionID string    `json:"agent_session_id"`     // coding agent's native session identifier
	PrimedAt       time.Time `json:"primed_at"`            // when session was primed
	ParentPID      int       `json:"parent_pid,omitempty"` // parent agent process ID
}

// AgentHookInput is an alias for agentx.HookInput.
// All coding agents that support hooks pipe session context via stdin JSON.
type AgentHookInput = agentx.HookInput

// markerPath returns the path to the marker file for a given agent session ID.
func markerPath(agentSessionID string) string {
	// sanitize session ID to prevent path traversal
	sanitized := strings.ReplaceAll(agentSessionID, "/", "_")
	sanitized = strings.ReplaceAll(sanitized, "\\", "_")
	sanitized = strings.ReplaceAll(sanitized, "..", "_")
	return filepath.Join(SessionMarkerDir(), sanitized+".json")
}

// FindSessionMarkerByPID scans the session marker directory for a marker whose
// ParentPID matches the given agent-ancestor PID. Returns the first match, or
// nil if no marker references this process.
//
// This is the fallback for #527/#529 re-entry detection when agent_session_id
// is unavailable — e.g. a second prime invoked from a CLAUDE.md BLOCKING
// instruction has no hook stdin JSON, so the session-id-keyed lookup misses.
// Process identity is a reliable alternative key: the hook-driven prime wrote
// the marker with the agent's PID, and any later prime inside the same agent
// process can find it by walking up to that same PID.
func FindSessionMarkerByPID(agentPID int) *SessionMarker {
	if agentPID <= 0 {
		return nil
	}
	entries, err := os.ReadDir(SessionMarkerDir())
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		// WriteSessionMarker emits atomic temp files as "<sid>.json.tmp"
		// and renames to "<sid>.json" — the .json suffix check here also
		// excludes the .tmp variants, which end in .tmp not .json.
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(SessionMarkerDir(), entry.Name()))
		if err != nil {
			continue
		}
		var m SessionMarker
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		if m.ParentPID == agentPID {
			return &m
		}
	}
	return nil
}

// ReadSessionMarker reads a session marker from disk.
// Returns nil, nil if the marker doesn't exist.
func ReadSessionMarker(agentSessionID string) (*SessionMarker, error) {
	if agentSessionID == "" {
		return nil, nil
	}

	path := markerPath(agentSessionID)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read marker: %w", err)
	}

	var marker SessionMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return nil, fmt.Errorf("failed to parse marker: %w", err)
	}

	// ensure AgentSessionID is set (may not be in old marker files)
	if marker.AgentSessionID == "" {
		marker.AgentSessionID = agentSessionID
	}

	return &marker, nil
}

// WriteSessionMarker writes a session marker to disk.
// Creates the marker directory if it doesn't exist.
// Uses atomic write pattern (temp file + rename) for safety.
func WriteSessionMarker(marker *SessionMarker) error {
	if marker.AgentSessionID == "" {
		return fmt.Errorf("agent session ID is required")
	}

	// ensure directory exists
	if err := os.MkdirAll(SessionMarkerDir(), 0700); err != nil {
		return fmt.Errorf("failed to create marker directory: %w", err)
	}

	path := markerPath(marker.AgentSessionID)

	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal marker: %w", err)
	}

	// atomic write: temp file + rename
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write marker temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath) // clean up on failure
		return fmt.Errorf("failed to rename marker: %w", err)
	}

	return nil
}

// DeleteSessionMarker removes a session marker from disk.
// Used for test cleanup only — production markers are ephemeral in /tmp
// and cleaned by the OS on reboot.
func DeleteSessionMarker(agentSessionID string) error {
	if agentSessionID == "" {
		return nil
	}
	path := markerPath(agentSessionID)
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// ReadAgentHookInput reads hook input from stdin.
// Delegates to agentx.ReadHookInputFromStdin and validates session_id is present.
// Works for any coding agent that pipes hook context via stdin JSON.
func ReadAgentHookInput() *AgentHookInput {
	input := agentx.ReadHookInputFromStdin()
	if input == nil {
		return nil
	}

	// validate we got a session_id (required for marker keying)
	if input.SessionID == "" {
		return nil
	}

	return input
}

// WriteToAgentEnvFile writes environment variables to the agent's env file if available.
// Currently supports CLAUDE_ENV_FILE (Claude Code). Other agents may use different
// mechanisms for injecting env vars into subsequent tool calls.
//
// Semantics: upsert. The file is read, existing `export KEY="VALUE"` lines are
// parsed into a map, incoming vars are merged (incoming wins), and the result
// is rewritten atomically via temp-file + rename. Duplicate keys from earlier
// writes are collapsed. Non-export lines (comments, blanks, unrecognized
// syntax) are preserved in their original order, appended after the exports.
//
// Why upsert instead of O_APPEND: a second prime that claims a different
// agent_type would otherwise stack `export AGENT_ENV="pi"` after an earlier
// `export AGENT_ENV="claude-code"`, poisoning every subsequent subprocess
// until the next explicit prime. See #527.
func WriteToAgentEnvFile(vars map[string]string) error {
	envFilePath := os.Getenv("CLAUDE_ENV_FILE")
	if envFilePath == "" {
		return nil // not in an agent context with env file support
	}

	// Concurrent primes (e.g. SessionStart hook racing against the
	// CLAUDE.md BLOCKING re-prime — exactly the #527/#529 scenario) both
	// read-modify-write this file. Without a lock, last-renamer-wins and
	// the other prime's keys silently disappear. Serialize via flock on
	// a sibling .lock file. 2s budget matches agentinstance.Store.
	lock := flock.New(envFilePath + ".lock")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil {
		return fmt.Errorf("failed to acquire agent env file lock: %w", err)
	}
	if !locked {
		return fmt.Errorf("could not acquire agent env file lock within timeout")
	}
	defer func() { _ = lock.Unlock() }()

	// read existing content (may not exist yet)
	existing, err := os.ReadFile(envFilePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read agent env file: %w", err)
	}

	exports, preserved := parseEnvFile(string(existing))

	// merge: incoming vars override existing values for same key
	for k, v := range vars {
		exports[k] = v
	}

	// emit: sorted keys for deterministic output, then preserved non-export lines
	keys := make([]string, 0, len(exports))
	for k := range exports {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var buf strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&buf, "export %s=%q\n", k, exports[k])
	}
	for _, line := range preserved {
		buf.WriteString(line)
		buf.WriteByte('\n')
	}

	// atomic write: temp file + rename
	tmpPath := envFilePath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(buf.String()), 0644); err != nil {
		return fmt.Errorf("failed to write agent env temp: %w", err)
	}
	if err := os.Rename(tmpPath, envFilePath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to rename agent env file: %w", err)
	}
	return nil
}

// parseEnvFile splits a CLAUDE_ENV_FILE into a map of export KEY=VALUE pairs
// plus a slice of preserved non-export lines (comments, blanks, unknown syntax).
// Duplicate keys in the input are resolved by last-wins — matching how bash
// source() evaluates the file.
func parseEnvFile(content string) (map[string]string, []string) {
	exports := make(map[string]string)
	var preserved []string
	for _, line := range strings.Split(content, "\n") {
		if line == "" {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "export ") {
			preserved = append(preserved, line)
			continue
		}
		rest := strings.TrimPrefix(trimmed, "export ")
		eq := strings.IndexByte(rest, '=')
		if eq < 0 {
			preserved = append(preserved, line)
			continue
		}
		key := rest[:eq]
		val := rest[eq+1:]
		// strip surrounding double quotes if present (matches fmt.Fprintf %q shape)
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			unquoted, err := strconv.Unquote(val)
			if err == nil {
				val = unquoted
			} else {
				val = val[1 : len(val)-1]
			}
		}
		exports[key] = val
	}
	return exports, preserved
}

// IsAgentHookContext detects if we're running in a coding agent's hook context.
// Currently checks Claude Code env vars; extend for other agents as needed.
func IsAgentHookContext() bool {
	// check CLAUDE_PROJECT_DIR (set by Claude Code)
	if os.Getenv("CLAUDE_PROJECT_DIR") != "" {
		return true
	}

	// check CLAUDECODE env var
	if os.Getenv("CLAUDECODE") == "1" {
		return true
	}

	// check CLAUDE_CODE_ENTRYPOINT
	if os.Getenv("CLAUDE_CODE_ENTRYPOINT") != "" {
		return true
	}

	return false
}
