package observability

import (
	"context"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var traceparentRe = regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-0[01]$`)

// --- A. TraceParent without initialization ---

// TestTraceParent_NoInit ensures TraceParent returns empty before Init.
// Failure prevented: nil deref or garbage traceparent before OTel setup.
func TestTraceParent_NoInit(t *testing.T) {
	resetGlobals()
	assert.Empty(t, TraceParent())
}

// --- B. StartCommand without Init ---

// TestStartCommand_NoInit ensures StartCommand is safe before Init.
// Failure prevented: panic when tracing is disabled.
func TestStartCommand_NoInit(t *testing.T) {
	resetGlobals()
	ctx, span := StartCommand(context.Background(), "ox test")
	assert.NotNil(t, ctx)
	assert.NotNil(t, span)
	assert.Empty(t, TraceParent(), "should still be empty without Init")
}

// --- C. Enabled state ---

// TestEnabled_Default verifies Enabled returns false before Init.
func TestEnabled_Default(t *testing.T) {
	resetGlobals()
	assert.False(t, Enabled())
}

// --- D. Shutdown safety ---

// TestShutdown_NoInit ensures Shutdown is safe before Init.
// Failure prevented: nil deref on shutdown without init.
func TestShutdown_NoInit(t *testing.T) {
	resetGlobals()
	assert.NotPanics(t, func() {
		Shutdown(context.Background())
	})
}

// --- E. Init with bad endpoint ---

// TestInit_EmptyEndpoint disables tracing gracefully.
func TestInit_EmptyEndpoint(t *testing.T) {
	resetGlobals()
	err := Init(context.Background(), "test", "")
	require.NoError(t, err)
	assert.False(t, Enabled())
}

// TestInit_InvalidEndpoint disables tracing gracefully.
func TestInit_InvalidEndpoint(t *testing.T) {
	resetGlobals()
	err := Init(context.Background(), "test", "://bad")
	require.NoError(t, err)
	assert.False(t, Enabled())
}

// --- F. CommandName formatting ---

func TestCommandName(t *testing.T) {
	assert.Equal(t, "ox login", CommandName("login"))
	assert.Equal(t, "ox agent prime", CommandName("agent", "prime"))
	assert.Equal(t, "ox session stop", CommandName("session", "stop"))
}

// --- G. Init with valid endpoint (uses test exporter) ---

// TestInit_ValidEndpoint verifies full lifecycle: init → start → traceparent → shutdown.
// Uses a non-routable endpoint; export will fail silently (which is fine).
func TestInit_ValidEndpoint(t *testing.T) {
	resetGlobals()

	// Use a non-routable IP so export silently fails (no real Collector needed)
	err := Init(context.Background(), "ox-cli-test", "http://192.0.2.1:4318")
	require.NoError(t, err)
	assert.True(t, Enabled())

	// start a command span
	ctx, span := StartCommand(context.Background(), "ox test-cmd")
	assert.NotNil(t, ctx)
	assert.NotNil(t, span)

	// traceparent should be valid W3C format
	tp := TraceParent()
	assert.Regexp(t, traceparentRe, tp, "should produce valid W3C traceparent")

	// all calls should share the same trace ID
	tp2 := TraceParent()
	assert.Equal(t, tp, tp2, "same root span = same traceparent")

	// shutdown should not panic
	Shutdown(context.Background())
	assert.Empty(t, TraceParent(), "traceparent should be empty after shutdown")
}

// --- H. SetCommandStatus safety ---

func TestSetCommandStatus_NoSpan(t *testing.T) {
	resetGlobals()
	assert.NotPanics(t, func() {
		SetCommandStatus(nil)
		SetCommandStatus(assert.AnError)
	})
}

func resetGlobals() {
	mu.Lock()
	defer mu.Unlock()
	rootCtx = nil
	rootSpan = nil
	tracer = nil
	shutFn = nil
}
