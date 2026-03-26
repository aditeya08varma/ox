package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// IsDaemonDisabled
// =============================================================================

func TestIsDaemonDisabled(t *testing.T) {
	tests := []struct {
		name     string
		envVal   string
		expected bool
	}{
		{"disabled lowercase", "false", true},
		{"disabled uppercase", "FALSE", true},
		{"disabled mixed case", "False", true},
		{"enabled true", "true", false},
		{"enabled empty", "", false},
		{"enabled 0", "0", false},
		{"enabled arbitrary", "no", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SAGEOX_DAEMON", tt.envVal)
			assert.Equal(t, tt.expected, IsDaemonDisabled())
		})
	}
}

// =============================================================================
// ErrorStorePathForWorkspace
// =============================================================================

func TestErrorStorePathForWorkspace(t *testing.T) {
	path := ErrorStorePathForWorkspace("abc12345")
	assert.Contains(t, path, "errors-abc12345.json")
	assert.True(t, filepath.IsAbs(path))
}

func TestErrorStorePathForWorkspace_DifferentIDs(t *testing.T) {
	p1 := ErrorStorePathForWorkspace("aaaa1111")
	p2 := ErrorStorePathForWorkspace("bbbb2222")
	assert.NotEqual(t, p1, p2)
}

// =============================================================================
// ErrorStore.Save (explicit call)
// =============================================================================

func TestErrorStore_Save_ExplicitCall(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "errors.json")

	store := NewErrorStore(storePath)
	store.Add(StoredError{Code: "test", Message: "msg", Severity: "error"})

	// explicitly call Save
	err := store.Save()
	assert.NoError(t, err)

	// verify file exists and is valid JSON
	data, err := os.ReadFile(storePath)
	require.NoError(t, err)

	var errors []StoredError
	require.NoError(t, json.Unmarshal(data, &errors))
	assert.Len(t, errors, 1)
	assert.Equal(t, "test", errors[0].Code)
}

// =============================================================================
// Instance.computeStatus
// =============================================================================

func TestInstance_computeStatus(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name          string
		lastHeartbeat time.Time
		expected      string
	}{
		{"just now is active", now, StatusActive},
		{"5s ago is active", now.Add(-5 * time.Second), StatusActive},
		{"29s ago is active", now.Add(-29 * time.Second), StatusActive},
		{"31s ago is idle", now.Add(-31 * time.Second), StatusIdle},
		{"2m ago is idle", now.Add(-2 * time.Minute), StatusIdle},
		{"4m ago is idle", now.Add(-4 * time.Minute), StatusIdle},
		{"5m ago is stale", now.Add(-5 * time.Minute), StatusStale},
		{"10m ago is stale", now.Add(-10 * time.Minute), StatusStale},
		{"1h ago is stale", now.Add(-1 * time.Hour), StatusStale},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inst := &Instance{LastHeartbeat: tt.lastHeartbeat}
			assert.Equal(t, tt.expected, inst.computeStatus(now))
		})
	}
}

// =============================================================================
// Instance.IsProcessAlive
// =============================================================================

func TestInstance_IsProcessAlive_NoPID(t *testing.T) {
	inst := &Instance{ParentPID: 0}
	assert.False(t, inst.IsProcessAlive())
}

func TestInstance_IsProcessAlive_NegativePID(t *testing.T) {
	inst := &Instance{ParentPID: -1}
	assert.False(t, inst.IsProcessAlive())
}

func TestInstance_IsProcessAlive_CurrentProcess(t *testing.T) {
	// current process is always alive
	inst := &Instance{ParentPID: os.Getpid()}
	assert.True(t, inst.IsProcessAlive())
}

func TestInstance_IsProcessAlive_DeadProcess(t *testing.T) {
	// PID 99999999 is almost certainly not running
	inst := &Instance{ParentPID: 99999999}
	assert.False(t, inst.IsProcessAlive())
}

// =============================================================================
// InstanceStore
// =============================================================================

func TestInstanceStore_RegisterAndGet(t *testing.T) {
	store := NewInstanceStore(testLogger())

	store.Register(&Instance{
		ID:            "inst-1",
		AgentType:     "claude-code",
		WorkspacePath: "/tmp/project",
		ParentPID:     os.Getpid(),
	})

	got := store.Get("inst-1")
	require.NotNil(t, got)
	assert.Equal(t, "inst-1", got.ID)
	assert.Equal(t, "claude-code", got.AgentType)
	assert.Equal(t, "/tmp/project", got.WorkspacePath)
	assert.Equal(t, StatusActive, got.Status)
}

func TestInstanceStore_Get_NotFound(t *testing.T) {
	store := NewInstanceStore(testLogger())
	assert.Nil(t, store.Get("nonexistent"))
}

func TestInstanceStore_Get_EmptyID(t *testing.T) {
	store := NewInstanceStore(testLogger())
	assert.Nil(t, store.Get(""))
}

func TestInstanceStore_Register_NilAndEmpty(t *testing.T) {
	store := NewInstanceStore(testLogger())

	// nil instance is no-op
	store.Register(nil)
	assert.Equal(t, 0, store.Count())

	// empty ID is no-op
	store.Register(&Instance{ID: ""})
	assert.Equal(t, 0, store.Count())
}

func TestInstanceStore_Register_Update(t *testing.T) {
	store := NewInstanceStore(testLogger())

	store.Register(&Instance{
		ID:            "inst-1",
		AgentType:     "claude-code",
		WorkspacePath: "/tmp/old",
	})

	// update with new workspace path
	store.Register(&Instance{
		ID:            "inst-1",
		WorkspacePath: "/tmp/new",
	})

	got := store.Get("inst-1")
	require.NotNil(t, got)
	assert.Equal(t, "/tmp/new", got.WorkspacePath)
	// agent type preserved from original registration
	assert.Equal(t, "claude-code", got.AgentType)
	assert.Equal(t, 1, store.Count())
}

func TestInstanceStore_Heartbeat(t *testing.T) {
	store := NewInstanceStore(testLogger())

	store.Register(&Instance{ID: "inst-1"})

	// wait a tiny bit so heartbeat time advances
	time.Sleep(1 * time.Millisecond)

	store.Heartbeat("inst-1")
	got := store.Get("inst-1")
	require.NotNil(t, got)
	assert.Equal(t, StatusActive, got.Status)
}

func TestInstanceStore_Heartbeat_EmptyAndMissing(t *testing.T) {
	store := NewInstanceStore(testLogger())

	// no-op for empty ID and missing instances
	store.Heartbeat("")
	store.Heartbeat("nonexistent")
	assert.Equal(t, 0, store.Count())
}

func TestInstanceStore_Deregister(t *testing.T) {
	store := NewInstanceStore(testLogger())

	store.Register(&Instance{ID: "inst-1"})
	store.Register(&Instance{ID: "inst-2"})
	assert.Equal(t, 2, store.Count())

	store.Deregister("inst-1")
	assert.Equal(t, 1, store.Count())
	assert.Nil(t, store.Get("inst-1"))
	assert.NotNil(t, store.Get("inst-2"))
}

func TestInstanceStore_Deregister_EmptyAndMissing(t *testing.T) {
	store := NewInstanceStore(testLogger())
	store.Register(&Instance{ID: "inst-1"})

	// no-op for empty and missing
	store.Deregister("")
	store.Deregister("nonexistent")
	assert.Equal(t, 1, store.Count())
}

func TestInstanceStore_GetActive(t *testing.T) {
	store := NewInstanceStore(testLogger())

	now := time.Now()
	store.Register(&Instance{
		ID:            "active",
		LastHeartbeat: now,
	})
	store.Register(&Instance{
		ID:            "idle",
		LastHeartbeat: now.Add(-1 * time.Minute),
	})
	store.Register(&Instance{
		ID:            "stale",
		LastHeartbeat: now.Add(-10 * time.Minute),
	})

	active := store.GetActive()
	// active and idle are returned; stale is excluded
	assert.Len(t, active, 2)

	// most recent first
	assert.Equal(t, "active", active[0].ID)
	assert.Equal(t, "idle", active[1].ID)
}

func TestInstanceStore_GetStale(t *testing.T) {
	store := NewInstanceStore(testLogger())

	now := time.Now()
	store.Register(&Instance{
		ID:            "active",
		LastHeartbeat: now,
	})
	store.Register(&Instance{
		ID:            "stale1",
		LastHeartbeat: now.Add(-10 * time.Minute),
	})
	store.Register(&Instance{
		ID:            "stale2",
		LastHeartbeat: now.Add(-20 * time.Minute),
	})

	stale := store.GetStale(5 * time.Minute)
	assert.Len(t, stale, 2)

	// oldest first
	assert.Equal(t, "stale2", stale[0].ID)
	assert.Equal(t, "stale1", stale[1].ID)
}

func TestInstanceStore_GetAll(t *testing.T) {
	store := NewInstanceStore(testLogger())

	base := time.Now()
	store.Register(&Instance{
		ID:        "first",
		StartTime: base.Add(-2 * time.Hour),
	})
	store.Register(&Instance{
		ID:        "second",
		StartTime: base.Add(-1 * time.Hour),
	})
	store.Register(&Instance{
		ID:        "third",
		StartTime: base,
	})

	all := store.GetAll()
	assert.Len(t, all, 3)

	// oldest first by start time
	assert.Equal(t, "first", all[0].ID)
	assert.Equal(t, "second", all[1].ID)
	assert.Equal(t, "third", all[2].ID)
}

func TestInstanceStore_Count_and_ActiveCount(t *testing.T) {
	store := NewInstanceStore(testLogger())
	assert.Equal(t, 0, store.Count())
	assert.Equal(t, 0, store.ActiveCount())

	now := time.Now()
	store.Register(&Instance{ID: "active", LastHeartbeat: now})
	store.Register(&Instance{ID: "stale", LastHeartbeat: now.Add(-10 * time.Minute)})

	assert.Equal(t, 2, store.Count())
	assert.Equal(t, 1, store.ActiveCount())
}

func TestInstanceStore_EvictsOnCapacity(t *testing.T) {
	store := NewInstanceStore(testLogger())

	// fill to max
	base := time.Now()
	for i := range MaxInstances {
		store.Register(&Instance{
			ID:            "inst-" + time.Now().Format("150405.000000") + "-" + string(rune('a'+i%26)),
			LastHeartbeat: base.Add(-time.Duration(MaxInstances-i) * time.Second),
		})
	}
	assert.Equal(t, MaxInstances, store.Count())

	// register one more; should evict oldest
	store.Register(&Instance{
		ID:            "new-inst",
		LastHeartbeat: base,
	})
	assert.LessOrEqual(t, store.Count(), MaxInstances)
	assert.NotNil(t, store.Get("new-inst"))
}

func TestInstanceStore_Cleanup_RemovesExpired(t *testing.T) {
	store := NewInstanceStore(testLogger())

	// manually add an instance that is > MaxAge old
	store.Register(&Instance{
		ID:        "ancient",
		StartTime: time.Now().Add(-25 * time.Hour),
	})

	store.cleanup()
	assert.Equal(t, 0, store.Count())
}

func TestInstanceStore_Cleanup_RemovesStale(t *testing.T) {
	store := NewInstanceStore(testLogger())

	store.Register(&Instance{
		ID:            "stale",
		LastHeartbeat: time.Now().Add(-10 * time.Minute),
		StartTime:     time.Now(), // not expired by age
	})

	store.cleanup()
	assert.Equal(t, 0, store.Count())
}

func TestInstanceStore_StartCleanup_StopCleanup(t *testing.T) {
	store := NewInstanceStore(testLogger())
	store.StartCleanup()

	// should be safe to stop
	store.Stop()
}

// =============================================================================
// Heartbeat File I/O
// =============================================================================

func TestWriteAndReadHeartbeatFromPath(t *testing.T) {
	tmpDir := t.TempDir()
	hbPath := filepath.Join(tmpDir, "heartbeats", "test.jsonl")

	entry := HeartbeatEntry{
		Timestamp:     time.Now().Truncate(time.Millisecond),
		DaemonPID:     12345,
		DaemonVersion: "0.5.0",
		Workspace:     "/tmp/project",
		Status:        "healthy",
	}

	err := WriteHeartbeatToPath(hbPath, entry)
	require.NoError(t, err)

	last, err := ReadLastHeartbeatFromPath(hbPath)
	require.NoError(t, err)
	require.NotNil(t, last)

	assert.Equal(t, entry.DaemonPID, last.DaemonPID)
	assert.Equal(t, entry.DaemonVersion, last.DaemonVersion)
	assert.Equal(t, entry.Workspace, last.Workspace)
	assert.Equal(t, entry.Status, last.Status)
}

func TestWriteHeartbeatToPath_RollingHistory(t *testing.T) {
	tmpDir := t.TempDir()
	hbPath := filepath.Join(tmpDir, "test.jsonl")

	// write more than maxHeartbeatHistory entries
	for i := range maxHeartbeatHistory + 3 {
		err := WriteHeartbeatToPath(hbPath, HeartbeatEntry{
			DaemonPID: i,
			Status:    "healthy",
			Timestamp: time.Now(),
		})
		require.NoError(t, err)
	}

	// should only keep the last maxHeartbeatHistory entries
	entries, err := readHeartbeatsFromPath(hbPath)
	require.NoError(t, err)
	assert.Len(t, entries, maxHeartbeatHistory)

	// last entry should be the most recent
	last := entries[len(entries)-1]
	assert.Equal(t, maxHeartbeatHistory+2, last.DaemonPID)
}

func TestReadLastHeartbeatFromPath_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	hbPath := filepath.Join(tmpDir, "empty.jsonl")

	err := os.WriteFile(hbPath, []byte{}, 0600)
	require.NoError(t, err)

	last, err := ReadLastHeartbeatFromPath(hbPath)
	require.NoError(t, err)
	assert.Nil(t, last)
}

func TestReadLastHeartbeatFromPath_Nonexistent(t *testing.T) {
	_, err := ReadLastHeartbeatFromPath("/nonexistent/path/heartbeat.jsonl")
	assert.Error(t, err)
}

func TestReadHeartbeatsFromPath_MalformedLines(t *testing.T) {
	tmpDir := t.TempDir()
	hbPath := filepath.Join(tmpDir, "malformed.jsonl")

	content := `{"pid":1,"status":"healthy","ts":"2026-01-01T00:00:00Z"}
not valid json
{"pid":2,"status":"healthy","ts":"2026-01-02T00:00:00Z"}
`
	err := os.WriteFile(hbPath, []byte(content), 0600)
	require.NoError(t, err)

	entries, err := readHeartbeatsFromPath(hbPath)
	require.NoError(t, err)
	// malformed line is skipped
	assert.Len(t, entries, 2)
	assert.Equal(t, 1, entries[0].DaemonPID)
	assert.Equal(t, 2, entries[1].DaemonPID)
}

func TestUserHeartbeatPath(t *testing.T) {
	p := UserHeartbeatPath("sageox.ai", "repo_abc", "ws123")
	assert.Contains(t, p, "repo_abc_ws123.jsonl")
	assert.Contains(t, p, "heartbeats")
}

func TestUserLedgerHeartbeatPath(t *testing.T) {
	p := UserLedgerHeartbeatPath("sageox.ai", "repo_abc", "ws123")
	assert.Contains(t, p, "repo_abc_ws123_ledger.jsonl")
	assert.Contains(t, p, "heartbeats")
}

func TestUserTeamHeartbeatPath(t *testing.T) {
	p := UserTeamHeartbeatPath("sageox.ai", "team_abc123")
	assert.Contains(t, p, "team_abc123.jsonl")
	assert.Contains(t, p, "heartbeats")
}

// =============================================================================
// status_display.go pure functions
// =============================================================================

func TestFormatDurationCompact(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		expected string
	}{
		{"sub-second", 100 * time.Millisecond, "<1s"},
		{"zero", 0, "<1s"},
		{"1 second", time.Second, "1s"},
		{"30 seconds", 30 * time.Second, "30s"},
		{"59 seconds", 59 * time.Second, "59s"},
		{"1 minute exact", time.Minute, "1m"},
		{"1m 30s", 90 * time.Second, "1m 30s"},
		{"5 minutes", 5 * time.Minute, "5m"},
		{"1 hour exact", time.Hour, "1h"},
		{"1h 30m", 90 * time.Minute, "1h 30m"},
		{"2 hours", 2 * time.Hour, "2h"},
		{"2h 15m", 2*time.Hour + 15*time.Minute, "2h 15m"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, formatDurationCompact(tt.duration))
		})
	}
}

func TestFormatRelativeTime(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		expected string
	}{
		{"5 seconds", 5 * time.Second, "5s ago"},
		{"30 seconds", 30 * time.Second, "30s ago"},
		{"5 minutes", 5 * time.Minute, "5m ago"},
		{"1 hour", time.Hour, "1h ago"},
		{"3 hours", 3 * time.Hour, "3h ago"},
		{"1 day", 24 * time.Hour, "1d ago"},
		{"3 days", 72 * time.Hour, "3d ago"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, formatRelativeTime(tt.duration))
		})
	}
}

func TestSemverOnly(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"1.2.3", "1.2.3"},
		{"1.2.3+build123", "1.2.3"},
		{"0.5.0+abc.def", "0.5.0"},
		{"", ""},
		{"no-plus-sign", "no-plus-sign"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, semverOnly(tt.input))
		})
	}
}

func TestStripANSI(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"no ANSI", "hello world", "hello world"},
		{"bold", "\033[1mhello\033[0m", "hello"},
		{"color", "\033[32mgreen\033[0m text", "green text"},
		{"empty", "", ""},
		{"multiple escapes", "\033[1m\033[31mred bold\033[0m", "red bold"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, stripANSI(tt.input))
		})
	}
}

func TestShortenPath(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"home prefix", filepath.Join(home, "project"), "~/project"},
		{"no home prefix", "/tmp/project", "/tmp/project"},
		{"empty", "", ""},
		{"home itself", home, "~"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, shortenPath(tt.input))
		})
	}
}

func TestDetermineHealth(t *testing.T) {
	tests := []struct {
		name     string
		status   *StatusData
		expected HealthStatus
	}{
		{
			name:     "healthy: no errors, recent sync",
			status:   &StatusData{RecentErrorCount: 0},
			expected: HealthHealthy,
		},
		{
			name:     "warning: some errors",
			status:   &StatusData{RecentErrorCount: 2},
			expected: HealthWarning,
		},
		{
			name:     "critical: 5+ errors",
			status:   &StatusData{RecentErrorCount: 5},
			expected: HealthCritical,
		},
		{
			name:     "critical: many errors",
			status:   &StatusData{RecentErrorCount: 10},
			expected: HealthCritical,
		},
		{
			name: "warning: stale sync",
			status: &StatusData{
				LastSync:         time.Now().Add(-10 * time.Minute),
				SyncIntervalRead: time.Minute, // 4x = 4min, 10min > 4min
			},
			expected: HealthWarning,
		},
		{
			name: "healthy: recent sync within threshold",
			status: &StatusData{
				LastSync:         time.Now().Add(-30 * time.Second),
				SyncIntervalRead: time.Minute,
			},
			expected: HealthHealthy,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, determineHealth(tt.status))
		})
	}
}

func TestCountWorkspaces(t *testing.T) {
	tests := []struct {
		name     string
		status   *StatusData
		expected int
	}{
		{
			name:     "nil workspaces",
			status:   &StatusData{},
			expected: 0,
		},
		{
			name: "single ledger",
			status: &StatusData{
				Workspaces: map[string][]WorkspaceSyncStatus{
					"ledger": {{ID: "l1"}},
				},
			},
			expected: 1,
		},
		{
			name: "ledger plus team contexts",
			status: &StatusData{
				Workspaces: map[string][]WorkspaceSyncStatus{
					"ledger":       {{ID: "l1"}},
					"team-context": {{ID: "t1"}, {ID: "t2"}},
				},
			},
			expected: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, countWorkspaces(tt.status))
		})
	}
}

func TestFormatVersionMatch(t *testing.T) {
	tests := []struct {
		name    string
		daemon  string
		cli     string
		wantOK  bool // true = contains "matching", false = contains "mismatch"
	}{
		{"matching", "0.5.0", "0.5.0", true},
		{"matching with build", "0.5.0+abc", "0.5.0+def", true},
		{"mismatched", "0.5.0", "0.6.0", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatVersionMatch(tt.daemon, tt.cli)
			stripped := stripANSI(result)
			if tt.wantOK {
				assert.Contains(t, stripped, "matching")
			} else {
				assert.Contains(t, stripped, "mismatch")
			}
		})
	}
}

func TestFormatWSStatus(t *testing.T) {
	tests := []struct {
		name string
		ws   WorkspaceSyncStatus
		want string // check stripped ANSI content
	}{
		{"not cloned", WorkspaceSyncStatus{Exists: false}, "not cloned"},
		{"error", WorkspaceSyncStatus{Exists: true, LastErr: "auth failed"}, "auth failed"},
		{"syncing", WorkspaceSyncStatus{Exists: true, Syncing: true}, "syncing..."},
		{"not synced", WorkspaceSyncStatus{Exists: true}, "not synced"},
		{"healthy", WorkspaceSyncStatus{Exists: true, LastSync: time.Now().Add(-30 * time.Second)}, "ago"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := stripANSI(formatWSStatus(tt.ws))
			assert.Contains(t, result, tt.want)
		})
	}
}

func TestFormatGCStatus(t *testing.T) {
	t.Run("never run", func(t *testing.T) {
		ws := WorkspaceSyncStatus{}
		result := stripANSI(formatGCStatus(ws, false))
		assert.Contains(t, result, "gc due")
	})

	t.Run("overdue", func(t *testing.T) {
		ws := WorkspaceSyncStatus{
			LastGCTime:     time.Now().Add(-10 * 24 * time.Hour),
			GCIntervalDays: 7,
		}
		result := stripANSI(formatGCStatus(ws, false))
		assert.Contains(t, result, "gc due")
	})

	t.Run("upcoming", func(t *testing.T) {
		ws := WorkspaceSyncStatus{
			LastGCTime:     time.Now().Add(-1 * 24 * time.Hour),
			GCIntervalDays: 7,
		}
		result := stripANSI(formatGCStatus(ws, false))
		assert.Contains(t, result, "gc in")
	})

	t.Run("verbose shows last gc", func(t *testing.T) {
		ws := WorkspaceSyncStatus{
			LastGCTime:     time.Now().Add(-1 * 24 * time.Hour),
			GCIntervalDays: 7,
		}
		result := stripANSI(formatGCStatus(ws, true))
		assert.Contains(t, result, "last")
		assert.Contains(t, result, "ago")
	})
}

func TestFormatNotRunning(t *testing.T) {
	t.Run("in project", func(t *testing.T) {
		result := stripANSI(FormatNotRunning(true))
		assert.Contains(t, result, "Not running")
		assert.Contains(t, result, "ox daemon start")
	})

	t.Run("not in project", func(t *testing.T) {
		result := stripANSI(FormatNotRunning(false))
		assert.Contains(t, result, "Not running")
		assert.Contains(t, result, "ox init")
	})
}

func TestFormatStarting(t *testing.T) {
	result := stripANSI(FormatStarting())
	assert.Contains(t, result, "Starting")
	assert.Contains(t, result, "not yet accepting connections")
}

func TestFormatDaemonList(t *testing.T) {
	t.Run("empty list", func(t *testing.T) {
		result := stripANSI(FormatDaemonList(nil))
		assert.Contains(t, result, "No ox daemons running")
	})

	t.Run("with daemons", func(t *testing.T) {
		daemons := []DaemonInfo{
			{
				WorkspacePath: "/tmp/project",
				PID:           12345,
				Version:       "0.5.0",
				StartedAt:     time.Now().Add(-1 * time.Hour),
			},
		}
		result := stripANSI(FormatDaemonList(daemons))
		assert.Contains(t, result, "Running Daemons (1)")
		assert.Contains(t, result, "12345")
		assert.Contains(t, result, "0.5.0")
	})
}

func TestFormatIssues(t *testing.T) {
	t.Run("no issues", func(t *testing.T) {
		result := formatIssues(false, nil)
		assert.Empty(t, result)
	})

	t.Run("with issues", func(t *testing.T) {
		issues := []DaemonIssue{
			{Severity: "error", Repo: "ledger", Summary: "merge conflict"},
			{Severity: "warning", Summary: "auth expiring"},
		}
		result := stripANSI(formatIssues(true, issues))
		assert.Contains(t, result, "Issues")
		assert.Contains(t, result, "merge conflict")
		assert.Contains(t, result, "auth expiring")
	})
}

func TestFormatActivity(t *testing.T) {
	t.Run("nil activity", func(t *testing.T) {
		assert.Empty(t, formatActivity(nil))
	})

	t.Run("empty activity", func(t *testing.T) {
		assert.Empty(t, formatActivity(&ActivitySummary{}))
	})

	t.Run("with repos", func(t *testing.T) {
		activity := &ActivitySummary{
			Repos: []ActivityEntry{
				{Key: "repo1", Last: time.Now()},
			},
		}
		result := stripANSI(formatActivity(activity))
		assert.Contains(t, result, "Activity")
		assert.Contains(t, result, "Repos")
	})
}

func TestFormatStatus_SmokeTest(t *testing.T) {
	// verify FormatStatus doesn't panic with minimal data
	status := &StatusData{
		Running: true,
		Version: "0.5.0",
		Uptime:  5 * time.Minute,
	}
	result := FormatStatus(status, "0.5.0")
	stripped := stripANSI(result)
	assert.Contains(t, stripped, "Healthy")
	assert.Contains(t, stripped, "0.5.0")
}

func TestFormatStatusWithSparkline_SmokeTest(t *testing.T) {
	status := &StatusData{
		Running: true,
		Version: "0.5.0",
		Uptime:  5 * time.Minute,
	}
	// no history: sparkline section is empty
	result := FormatStatusWithSparkline(status, nil, "0.5.0")
	assert.NotEmpty(t, result)
}

func TestFormatStatusVerbose_SmokeTest(t *testing.T) {
	status := &StatusData{
		Running:       true,
		Version:       "0.5.0",
		Uptime:        5 * time.Minute,
		Pid:           12345,
		WorkspacePath: "/tmp/project",
		LedgerPath:    "/tmp/ledger",
	}
	result := FormatStatusVerbose(status, nil, "0.5.0")
	stripped := stripANSI(result)
	assert.Contains(t, stripped, "Internals")
	assert.Contains(t, stripped, "12345")
}

func TestFormatSyncHistory(t *testing.T) {
	events := []SyncEvent{
		{Time: time.Now(), Type: "pull", Duration: 500 * time.Millisecond, FilesChanged: 3},
		{Time: time.Now(), Type: "push", Duration: 200 * time.Millisecond, FilesChanged: 1},
	}
	result := stripANSI(formatSyncHistory(events))
	assert.Contains(t, result, "Recent Syncs")
	assert.Contains(t, result, "pull")
	assert.Contains(t, result, "push")
}

// =============================================================================
// GetExtendedStatus
// =============================================================================

func TestGetExtendedStatus(t *testing.T) {
	t.Run("nil status", func(t *testing.T) {
		_, ok := GetExtendedStatus(nil)
		assert.False(t, ok)
	})

	t.Run("no errors", func(t *testing.T) {
		_, ok := GetExtendedStatus(&StatusData{})
		assert.False(t, ok)
	})

	t.Run("with errors", func(t *testing.T) {
		ext, ok := GetExtendedStatus(&StatusData{
			RecentErrorCount: 3,
			LastError:        "sync failed",
			LastErrorTime:    "2026-01-01T00:00:00Z",
		})
		assert.True(t, ok)
		assert.Equal(t, 3, ext.RecentErrorCount)
		assert.Equal(t, "sync failed", ext.LastError)
	})
}

// =============================================================================
// StatusData.LastSyncForPath
// =============================================================================

func TestStatusData_LastSyncForPath(t *testing.T) {
	t.Run("nil receiver", func(t *testing.T) {
		var s *StatusData
		_, ok := s.LastSyncForPath("/any")
		assert.False(t, ok)
	})

	t.Run("not found", func(t *testing.T) {
		s := &StatusData{
			Workspaces: map[string][]WorkspaceSyncStatus{
				"ledger": {{Path: "/other", LastSync: time.Now()}},
			},
		}
		_, ok := s.LastSyncForPath("/missing")
		assert.False(t, ok)
	})

	t.Run("found", func(t *testing.T) {
		syncTime := time.Now().Add(-5 * time.Minute)
		s := &StatusData{
			Workspaces: map[string][]WorkspaceSyncStatus{
				"team-context": {{Path: "/teams/frontend", LastSync: syncTime}},
			},
		}
		got, ok := s.LastSyncForPath("/teams/frontend")
		assert.True(t, ok)
		assert.Equal(t, syncTime, got)
	})

	t.Run("zero sync time returns false", func(t *testing.T) {
		s := &StatusData{
			Workspaces: map[string][]WorkspaceSyncStatus{
				"ledger": {{Path: "/ledger"}}, // LastSync is zero
			},
		}
		_, ok := s.LastSyncForPath("/ledger")
		assert.False(t, ok)
	})
}
