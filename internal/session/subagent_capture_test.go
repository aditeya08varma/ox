package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectTaskToolCalls(t *testing.T) {
	t.Run("detects Task tool calls", func(t *testing.T) {
		ts := time.Now()
		entries := []map[string]any{
			{"type": "user", "content": "Hello"},
			{"type": "tool", "tool_name": "Task", "tool_input": "Do something", "ts": ts.Format(time.RFC3339Nano)},
			{"type": "assistant", "content": "Done"},
			{"type": "tool", "tool_name": "bash", "tool_input": "ls"},
			{"type": "tool", "tool_name": "task", "tool_input": "Another task", "timestamp": ts.Add(time.Minute).Format(time.RFC3339Nano)},
		}

		calls := DetectTaskToolCalls(entries)

		assert.Len(t, calls, 2)
		assert.Equal(t, 1, calls[0].EntryIndex)
		assert.Equal(t, "Do something", calls[0].ToolInput)
		assert.Equal(t, 4, calls[1].EntryIndex)
		assert.Equal(t, "Another task", calls[1].ToolInput)
	})

	t.Run("no Task tool calls", func(t *testing.T) {
		entries := []map[string]any{
			{"type": "user", "content": "Hello"},
			{"type": "tool", "tool_name": "bash", "tool_input": "ls"},
			{"type": "tool", "tool_name": "read", "tool_input": "file.go"},
		}

		calls := DetectTaskToolCalls(entries)
		assert.Empty(t, calls)
	})

	t.Run("parses JSON tool input", func(t *testing.T) {
		input := `{"description": "Test task", "agent_type": "backend-developer"}`
		entries := []map[string]any{
			{"type": "tool", "tool_name": "Task", "tool_input": input},
		}

		calls := DetectTaskToolCalls(entries)

		require.Len(t, calls, 1)
		require.NotNil(t, calls[0].ParsedInput)
		assert.Equal(t, "Test task", calls[0].ParsedInput.Description)
		assert.Equal(t, "backend-developer", calls[0].ParsedInput.AgentType)
	})
}

func TestMatchTasksToSubagents(t *testing.T) {
	baseTime := time.Date(2026, 1, 17, 10, 0, 0, 0, time.UTC)

	t.Run("matches single task to subagent", func(t *testing.T) {
		taskCalls := []TaskToolCallInfo{
			{EntryIndex: 1, Timestamp: baseTime},
		}
		subagents := []*SubagentSession{
			{SubagentID: "Oxabc", SessionName: "2026-01-17T10-01-user-Oxabc", EntryCount: 50, CompletedAt: baseTime.Add(5 * time.Minute)},
		}

		matches := MatchTasksToSubagents(taskCalls, subagents)

		assert.Len(t, matches, 1)
		assert.NotNil(t, matches[1])
		assert.Equal(t, "Oxabc", matches[1].SessionID)
		assert.Equal(t, 50, matches[1].EntryCount)
	})

	t.Run("matches multiple tasks to multiple subagents", func(t *testing.T) {
		taskCalls := []TaskToolCallInfo{
			{EntryIndex: 1, Timestamp: baseTime},
			{EntryIndex: 5, Timestamp: baseTime.Add(10 * time.Minute)},
		}
		subagents := []*SubagentSession{
			{SubagentID: "Ox111", SessionName: "sess1", CompletedAt: baseTime.Add(8 * time.Minute)},
			{SubagentID: "Ox222", SessionName: "sess2", CompletedAt: baseTime.Add(15 * time.Minute)},
		}

		matches := MatchTasksToSubagents(taskCalls, subagents)

		assert.Len(t, matches, 2)
		assert.Equal(t, "Ox111", matches[1].SessionID)
		assert.Equal(t, "Ox222", matches[5].SessionID)
	})

	t.Run("no matches when subagents complete before task", func(t *testing.T) {
		taskCalls := []TaskToolCallInfo{
			{EntryIndex: 1, Timestamp: baseTime},
		}
		subagents := []*SubagentSession{
			{SubagentID: "Oxold", CompletedAt: baseTime.Add(-10 * time.Minute)},
		}

		matches := MatchTasksToSubagents(taskCalls, subagents)
		assert.Empty(t, matches)
	})

	t.Run("empty inputs", func(t *testing.T) {
		assert.Empty(t, MatchTasksToSubagents(nil, nil))
		assert.Empty(t, MatchTasksToSubagents([]TaskToolCallInfo{}, []*SubagentSession{}))
	})
}

func TestEnrichEntriesWithSubagents(t *testing.T) {
	t.Run("adds subagent field to matched entries", func(t *testing.T) {
		entries := []map[string]any{
			{"type": "user", "content": "Hello"},
			{"type": "tool", "tool_name": "Task", "tool_input": "Do task"},
			{"type": "assistant", "content": "Done"},
		}

		matches := map[int]*SubagentReference{
			1: {
				SessionID:   "Ox123",
				SessionPath: "sessions/2026-01-17T10-00-user-Ox123/",
				EntryCount:  47,
				Model:       "claude-sonnet-4",
			},
		}

		EnrichEntriesWithSubagents(entries, matches)

		// check that subagent field was added
		subagent, ok := entries[1]["subagent"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "Ox123", subagent["session_id"])
		assert.Equal(t, "sessions/2026-01-17T10-00-user-Ox123/", subagent["session_path"])
		assert.Equal(t, 47, subagent["entry_count"])
		assert.Equal(t, "claude-sonnet-4", subagent["model"])

		// check that other entries were not modified
		_, hasSubagent := entries[0]["subagent"]
		assert.False(t, hasSubagent)
		_, hasSubagent = entries[2]["subagent"]
		assert.False(t, hasSubagent)
	})

	t.Run("handles out of bounds gracefully", func(t *testing.T) {
		entries := []map[string]any{
			{"type": "user", "content": "Hello"},
		}

		matches := map[int]*SubagentReference{
			10: {SessionID: "Ox999"},
		}

		// should not panic
		EnrichEntriesWithSubagents(entries, matches)
	})
}

func TestCaptureSubagentSessions(t *testing.T) {
	t.Run("full capture flow with registry", func(t *testing.T) {
		// create temp directory structure
		tmpDir := t.TempDir()
		sessionsDir := filepath.Join(tmpDir, "sessions")
		require.NoError(t, os.MkdirAll(sessionsDir, 0755))

		baseTime := time.Now()

		// create parent session
		parentSession := filepath.Join(sessionsDir, "2026-01-17T10-00-user-OxParent")
		require.NoError(t, os.MkdirAll(parentSession, 0755))

		// register a subagent in the parent session's registry
		registry, err := NewSubagentRegistry(parentSession)
		require.NoError(t, err)
		err = registry.Register(&SubagentSession{
			SubagentID:  "OxChild",
			SessionName: "2026-01-17T10-05-user-OxChild",
			SessionPath: filepath.Join(sessionsDir, "2026-01-17T10-05-user-OxChild", "raw.jsonl"),
			CompletedAt: baseTime.Add(5 * time.Minute),
			EntryCount:  42,
			Model:       "claude-sonnet-4",
			AgentType:   "backend-developer",
		})
		require.NoError(t, err)

		// create entries with a Task tool call
		entries := []map[string]any{
			{"type": "user", "content": "Start task"},
			{"type": "tool", "tool_name": "Task", "tool_input": "Do something", "ts": baseTime.Add(-time.Minute).Format(time.RFC3339Nano)},
			{"type": "assistant", "content": "Task dispatched"},
		}

		opts := CaptureSubagentOptions{
			SessionPath:        parentSession,
			SessionsBasePath:   sessionsDir,
			RecordingStartTime: baseTime.Add(-10 * time.Minute),
			RecordingEndTime:   baseTime.Add(10 * time.Minute),
			TimeWindowBuffer:   10 * time.Minute,
		}

		result, err := CaptureSubagentSessions(entries, opts)
		require.NoError(t, err)

		assert.Equal(t, 1, result.TaskCallsFound)
		assert.Equal(t, 1, result.SubagentsMatched)
		assert.Contains(t, result.EnrichedEntries, 1)

		// verify entry was enriched
		subagent, ok := entries[1]["subagent"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "OxChild", subagent["session_id"])
		assert.Equal(t, 42, subagent["entry_count"])
		assert.Equal(t, "claude-sonnet-4", subagent["model"])
	})

	t.Run("capture with session scanning", func(t *testing.T) {
		// create temp directory structure
		tmpDir := t.TempDir()
		sessionsDir := filepath.Join(tmpDir, "sessions")
		require.NoError(t, os.MkdirAll(sessionsDir, 0755))

		baseTime := time.Now().UTC()

		// create parent session with current timestamp
		parentSessionName := baseTime.Format("2006-01-02T15-04") + "-user-OxParent"
		parentSession := filepath.Join(sessionsDir, parentSessionName)
		require.NoError(t, os.MkdirAll(parentSession, 0755))

		// create subagent session
		subagentName := baseTime.Format("2006-01-02T15-04") + "-user-OxChild"
		subagentSession := filepath.Join(sessionsDir, subagentName)
		require.NoError(t, os.MkdirAll(subagentSession, 0755))

		// write subagent raw.jsonl
		rawPath := filepath.Join(subagentSession, "raw.jsonl")
		rawContent := `{"type":"header","metadata":{"version":"1.0","agent_id":"OxChild","model":"claude-sonnet-4"}}
{"type":"user","content":"Subagent task","seq":1}
{"type":"assistant","content":"Done","seq":2}
{"type":"footer","entry_count":2}
`
		require.NoError(t, os.WriteFile(rawPath, []byte(rawContent), 0644))

		// create entries with a Task tool call
		entries := []map[string]any{
			{"type": "user", "content": "Start task"},
			{"type": "tool", "tool_name": "Task", "tool_input": "Do something", "ts": baseTime.Add(-time.Minute).Format(time.RFC3339Nano)},
			{"type": "assistant", "content": "Task dispatched"},
		}

		opts := CaptureSubagentOptions{
			SessionPath:        parentSession,
			SessionsBasePath:   sessionsDir,
			RecordingStartTime: baseTime.Add(-10 * time.Minute),
			RecordingEndTime:   baseTime.Add(10 * time.Minute),
			TimeWindowBuffer:   10 * time.Minute,
		}

		result, err := CaptureSubagentSessions(entries, opts)
		require.NoError(t, err)

		assert.Equal(t, 1, result.TaskCallsFound)
		assert.Equal(t, 1, result.SubagentsMatched)
		assert.Contains(t, result.EnrichedEntries, 1)

		// verify entry was enriched
		subagent, ok := entries[1]["subagent"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "OxChild", subagent["session_id"])
		assert.Equal(t, 2, subagent["entry_count"])
	})

	t.Run("no task calls returns early", func(t *testing.T) {
		entries := []map[string]any{
			{"type": "user", "content": "Hello"},
			{"type": "assistant", "content": "Hi"},
		}

		result, err := CaptureSubagentSessions(entries, CaptureSubagentOptions{})
		require.NoError(t, err)
		assert.Equal(t, 0, result.TaskCallsFound)
	})

	t.Run("no subagents found marks tasks as unmatched", func(t *testing.T) {
		tmpDir := t.TempDir()
		sessionsDir := filepath.Join(tmpDir, "sessions")
		require.NoError(t, os.MkdirAll(sessionsDir, 0755))

		parentSession := filepath.Join(sessionsDir, "2026-01-17T10-00-user-OxParent")
		require.NoError(t, os.MkdirAll(parentSession, 0755))

		entries := []map[string]any{
			{"type": "user", "content": "Start task"},
			{"type": "tool", "tool_name": "Task", "tool_input": "Do something"},
			{"type": "assistant", "content": "Task dispatched"},
		}

		opts := CaptureSubagentOptions{
			SessionPath:      parentSession,
			SessionsBasePath: sessionsDir,
		}

		result, err := CaptureSubagentSessions(entries, opts)
		require.NoError(t, err)

		assert.Equal(t, 1, result.TaskCallsFound)
		assert.Equal(t, 0, result.SubagentsMatched)
		assert.Contains(t, result.UnmatchedTasks, 1)
	})
}

func TestGetSubagentCaptureOptions(t *testing.T) {
	state := &RecordingState{
		SessionPath: "/path/to/sessions/2026-01-17T10-00-user-Ox123",
		StartedAt:   time.Date(2026, 1, 17, 10, 0, 0, 0, time.UTC),
	}

	opts := GetSubagentCaptureOptions(state)

	assert.Equal(t, state.SessionPath, opts.SessionPath)
	assert.Equal(t, "/path/to/sessions", opts.SessionsBasePath)
	assert.Equal(t, state.StartedAt, opts.RecordingStartTime)
	assert.Equal(t, 5*time.Minute, opts.TimeWindowBuffer)
}

func TestSubagentReferenceJSON(t *testing.T) {
	ref := SubagentReference{
		SessionID:   "Ox123",
		SessionPath: "sessions/2026-01-17T10-00-user-Ox123/",
		EntryCount:  47,
		DurationMs:  120000,
		Model:       "claude-sonnet-4",
	}

	data, err := json.Marshal(ref)
	require.NoError(t, err)

	// verify JSON structure matches expected output format
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(data, &parsed))

	assert.Equal(t, "Ox123", parsed["session_id"])
	assert.Equal(t, "sessions/2026-01-17T10-00-user-Ox123/", parsed["session_path"])
	assert.Equal(t, float64(47), parsed["entry_count"])
	assert.Equal(t, float64(120000), parsed["duration_ms"])
	assert.Equal(t, "claude-sonnet-4", parsed["model"])
}

func TestFindSubagentSessions(t *testing.T) {
	t.Run("finds registered subagents", func(t *testing.T) {
		tmpDir := t.TempDir()
		parentSession := filepath.Join(tmpDir, "parent")
		require.NoError(t, os.MkdirAll(parentSession, 0755))

		// register a subagent
		registry, err := NewSubagentRegistry(parentSession)
		require.NoError(t, err)
		err = registry.Register(&SubagentSession{
			SubagentID:  "Ox123",
			SessionName: "session-123",
			EntryCount:  10,
		})
		require.NoError(t, err)

		opts := CaptureSubagentOptions{
			SessionPath: parentSession,
		}

		found, err := FindSubagentSessions(opts)
		require.NoError(t, err)
		assert.Len(t, found, 1)
		assert.Equal(t, "Ox123", found[0].SubagentID)
	})

	t.Run("returns empty for no subagents", func(t *testing.T) {
		tmpDir := t.TempDir()
		parentSession := filepath.Join(tmpDir, "parent")
		require.NoError(t, os.MkdirAll(parentSession, 0755))

		opts := CaptureSubagentOptions{
			SessionPath: parentSession,
		}

		found, err := FindSubagentSessions(opts)
		require.NoError(t, err)
		assert.Empty(t, found)
	})
}
