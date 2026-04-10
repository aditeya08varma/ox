package adapters

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockAdapter implements Adapter for testing
type mockAdapter struct {
	name      string
	detect    bool
	findErr   error
	readErr   error
	watchErr  error
	entries   []RawEntry
	sessionID string
}

func (m *mockAdapter) Name() string {
	return m.name
}

func (m *mockAdapter) Detect() bool {
	return m.detect
}

func (m *mockAdapter) FindSessionFile(lookup SessionLookup) (string, error) {
	if m.findErr != nil {
		return "", m.findErr
	}
	return "/path/to/session/" + m.sessionID, nil
}

func (m *mockAdapter) Read(sessionPath string) ([]RawEntry, error) {
	if m.readErr != nil {
		return nil, m.readErr
	}
	return m.entries, nil
}

func (m *mockAdapter) ReadMetadata(sessionPath string) (*SessionMetadata, error) {
	// return empty metadata for mock adapter
	return nil, nil
}

func (m *mockAdapter) Watch(ctx context.Context, sessionPath string) (<-chan RawEntry, error) {
	if m.watchErr != nil {
		return nil, m.watchErr
	}
	ch := make(chan RawEntry)
	go func() {
		defer close(ch)
		for _, entry := range m.entries {
			select {
			case <-ctx.Done():
				return
			case ch <- entry:
			}
		}
	}()
	return ch, nil
}

func TestRegister_AddsAdapter(t *testing.T) {
	ResetRegistry()

	adapter := &mockAdapter{name: "test-adapter"}
	Register(adapter)

	got, err := GetAdapter("test-adapter")
	require.NoError(t, err)
	assert.Equal(t, "test-adapter", got.Name())
}

func TestRegister_PanicsOnDuplicate(t *testing.T) {
	ResetRegistry()

	adapter1 := &mockAdapter{name: "duplicate"}
	adapter2 := &mockAdapter{name: "duplicate"}

	Register(adapter1)

	assert.Panics(t, func() {
		Register(adapter2)
	}, "Register() should panic on duplicate adapter")
}

func TestGetAdapter_ReturnsErrNotFound(t *testing.T) {
	ResetRegistry()

	_, err := GetAdapter("nonexistent")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAdapterNotFound)
}

func TestDetectAdapter_ReturnsFirstMatch(t *testing.T) {
	ResetRegistry()

	// register adapters in order
	adapter1 := &mockAdapter{name: "adapter-1", detect: false}
	adapter2 := &mockAdapter{name: "adapter-2", detect: true}
	adapter3 := &mockAdapter{name: "adapter-3", detect: true}

	Register(adapter1)
	Register(adapter2)
	Register(adapter3)

	got, err := DetectAdapter()
	require.NoError(t, err)

	// should return one of the detecting adapters (map iteration order is non-deterministic)
	assert.True(t, got.Detect(), "DetectAdapter() should return adapter that detects")
}

func TestDetectAdapter_ReturnsErrNoAdapter(t *testing.T) {
	ResetRegistry()

	adapter1 := &mockAdapter{name: "adapter-1", detect: false}
	adapter2 := &mockAdapter{name: "adapter-2", detect: false}

	Register(adapter1)
	Register(adapter2)

	_, err := DetectAdapter()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNoAdapterDetected)
}

func TestDetectAdapter_EmptyRegistry(t *testing.T) {
	ResetRegistry()

	_, err := DetectAdapter()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNoAdapterDetected)
}

func TestListAdapters_ReturnsAllNames(t *testing.T) {
	ResetRegistry()

	adapter1 := &mockAdapter{name: "adapter-a"}
	adapter2 := &mockAdapter{name: "adapter-b"}
	adapter3 := &mockAdapter{name: "adapter-c"}

	Register(adapter1)
	Register(adapter2)
	Register(adapter3)

	names := ListAdapters()
	require.Len(t, names, 3)

	// check all names are present (order is non-deterministic)
	nameSet := make(map[string]bool)
	for _, name := range names {
		nameSet[name] = true
	}

	for _, expected := range []string{"adapter-a", "adapter-b", "adapter-c"} {
		assert.True(t, nameSet[expected], "ListAdapters() missing %q", expected)
	}
}

func TestGetAdapter_Aliases(t *testing.T) {
	ResetRegistry()

	adapter := &mockAdapter{name: "claude-code", detect: true}
	Register(adapter)

	tests := []struct {
		name    string
		lookup  string
		wantErr bool
	}{
		{"exact match", "claude-code", false},
		{"display name", "Claude Code", false},
		{"shorthand", "claude", false},
		{"uppercase shorthand", "CLAUDE", false},
		{"unknown", "gemini", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GetAdapter(tt.lookup)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, "claude-code", got.Name())
			}
		})
	}
}

func TestListAdapters_EmptyRegistry(t *testing.T) {
	ResetRegistry()

	names := ListAdapters()
	assert.Len(t, names, 0)
}

func TestResetRegistry_ClearsAllAdapters(t *testing.T) {
	ResetRegistry()

	adapter := &mockAdapter{name: "test"}
	Register(adapter)

	// verify registered
	names := ListAdapters()
	require.Len(t, names, 1, "expected 1 adapter after Register")

	ResetRegistry()

	// verify cleared
	names = ListAdapters()
	assert.Len(t, names, 0, "expected 0 adapters after ResetRegistry")
}

func TestMockAdapter_FindSessionFile(t *testing.T) {
	adapter := &mockAdapter{
		name:      "test",
		sessionID: "session-123",
	}

	path, err := adapter.FindSessionFile(SessionLookup{RepoRoot: "/tmp", AgentID: "agent-id", Since: time.Now()})
	require.NoError(t, err)
	assert.Equal(t, "/path/to/session/session-123", path)
}

func TestMockAdapter_FindSessionFile_Error(t *testing.T) {
	expectedErr := errors.New("session not found")
	adapter := &mockAdapter{
		name:    "test",
		findErr: expectedErr,
	}

	_, err := adapter.FindSessionFile(SessionLookup{RepoRoot: "/tmp", AgentID: "agent-id", Since: time.Now()})
	require.Error(t, err)
	assert.Equal(t, expectedErr, err)
}

func TestMockAdapter_Read(t *testing.T) {
	entries := []RawEntry{
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there"},
	}
	adapter := &mockAdapter{
		name:    "test",
		entries: entries,
	}

	got, err := adapter.Read("/path/to/session")
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "user", got[0].Role)
	assert.Equal(t, "assistant", got[1].Role)
}

func TestMockAdapter_Read_Error(t *testing.T) {
	expectedErr := errors.New("read failed")
	adapter := &mockAdapter{
		name:    "test",
		readErr: expectedErr,
	}

	_, err := adapter.Read("/path/to/session")
	require.Error(t, err)
	assert.Equal(t, expectedErr, err)
}

func TestMockAdapter_Watch(t *testing.T) {
	entries := []RawEntry{
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there"},
	}
	adapter := &mockAdapter{
		name:    "test",
		entries: entries,
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	ch, err := adapter.Watch(ctx, "/path/to/session")
	require.NoError(t, err)

	var received []RawEntry
	for entry := range ch {
		received = append(received, entry)
	}

	assert.Len(t, received, 2)
}

func TestMockAdapter_Watch_Error(t *testing.T) {
	expectedErr := errors.New("watch failed")
	adapter := &mockAdapter{
		name:     "test",
		watchErr: expectedErr,
	}

	ctx := context.Background()
	_, err := adapter.Watch(ctx, "/path/to/session")
	require.Error(t, err)
	assert.Equal(t, expectedErr, err)
}

func TestMockAdapter_Watch_CancelContext(t *testing.T) {
	// create adapter with many entries
	entries := make([]RawEntry, 100)
	for i := range entries {
		entries[i] = RawEntry{Role: "user", Content: "message"}
	}
	adapter := &mockAdapter{
		name:    "test",
		entries: entries,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := adapter.Watch(ctx, "/path/to/session")
	require.NoError(t, err)

	// receive a few entries then cancel
	received := 0
	for range ch {
		received++
		if received >= 3 {
			cancel()
			break
		}
	}

	// drain remaining entries (should stop quickly after cancel)
	for range ch {
		received++
	}

	// should have stopped early due to cancellation
	assert.Less(t, received, 100, "Watch() should respect context cancellation")
}

func TestRawEntry_Fields(t *testing.T) {
	now := time.Now()
	entry := RawEntry{
		Timestamp: now,
		Role:      "tool",
		Content:   "result output",
		ToolName:  "read_file",
		ToolInput: "/path/to/file",
		Raw:       []byte(`{"original":"data"}`),
	}

	assert.Equal(t, now, entry.Timestamp)
	assert.Equal(t, "tool", entry.Role)
	assert.Equal(t, "result output", entry.Content)
	assert.Equal(t, "read_file", entry.ToolName)
	assert.Equal(t, "/path/to/file", entry.ToolInput)
	assert.Equal(t, `{"original":"data"}`, string(entry.Raw))
}

func TestErrorSentinels(t *testing.T) {
	// verify error sentinel values are usable
	tests := []struct {
		name string
		err  error
	}{
		{"ErrNoAdapterDetected", ErrNoAdapterDetected},
		{"ErrAdapterNotFound", ErrAdapterNotFound},
		{"ErrSessionNotFound", ErrSessionNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NotNil(t, tt.err, "error sentinel should not be nil")
			assert.NotEmpty(t, tt.err.Error(), "error sentinel should have message")
		})
	}
}

func TestConcurrentRegistryAccess(t *testing.T) {
	ResetRegistry()

	// test concurrent reads and writes
	done := make(chan bool)

	// writer goroutine
	go func() {
		for i := 0; i < 100; i++ {
			adapter := &mockAdapter{name: "concurrent-" + string(rune('a'+i%26)) + string(rune('0'+i%10))}
			func() {
				defer func() { recover() }() // ignore duplicate panics
				Register(adapter)
			}()
		}
		done <- true
	}()

	// reader goroutines
	for i := 0; i < 3; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				ListAdapters()
				GetAdapter("nonexistent")
				DetectAdapter()
			}
			done <- true
		}()
	}

	// wait for all goroutines
	for i := 0; i < 4; i++ {
		<-done
	}
}
