package cli

import (
	"log/slog"
	"os"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
)

func TestContext_Accessors(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *config.Config
		method   string
		expected bool
	}{
		{"verbose true", &config.Config{Verbose: true}, "IsVerbose", true},
		{"verbose false", &config.Config{Verbose: false}, "IsVerbose", false},
		{"quiet true", &config.Config{Quiet: true}, "IsQuiet", true},
		{"quiet false", &config.Config{Quiet: false}, "IsQuiet", false},
		{"json true", &config.Config{JSON: true}, "IsJSON", true},
		{"json false", &config.Config{JSON: false}, "IsJSON", false},
		{"text true", &config.Config{Text: true}, "IsText", true},
		{"text false", &config.Config{Text: false}, "IsText", false},
		{"review true", &config.Config{Review: true}, "IsReview", true},
		{"review false", &config.Config{Review: false}, "IsReview", false},
		{"no-interactive true", &config.Config{NoInteractive: true}, "IsNoInteractive", true},
		{"no-interactive false", &config.Config{NoInteractive: false}, "IsNoInteractive", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &Context{Config: tt.cfg}
			var got bool
			switch tt.method {
			case "IsVerbose":
				got = ctx.IsVerbose()
			case "IsQuiet":
				got = ctx.IsQuiet()
			case "IsJSON":
				got = ctx.IsJSON()
			case "IsText":
				got = ctx.IsText()
			case "IsReview":
				got = ctx.IsReview()
			case "IsNoInteractive":
				got = ctx.IsNoInteractive()
			}
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestContext_LogMethods_NilLogger(t *testing.T) {
	// should not panic with nil logger
	ctx := &Context{Config: &config.Config{}, Logger: nil}

	assert.NotPanics(t, func() { ctx.LogInfo("test") })
	assert.NotPanics(t, func() { ctx.LogDebug("test") })
	assert.NotPanics(t, func() { ctx.LogWarn("test") })
	assert.NotPanics(t, func() { ctx.LogError("test") })
}

func TestContext_LogMethods_WithLogger(t *testing.T) {
	ctx := &Context{Config: &config.Config{}, Logger: slog.Default()}

	// should not panic with real logger
	assert.NotPanics(t, func() { ctx.LogInfo("info msg", "key", "val") })
	assert.NotPanics(t, func() { ctx.LogDebug("debug msg", "key", "val") })
	assert.NotPanics(t, func() { ctx.LogWarn("warn msg", "key", "val") })
	assert.NotPanics(t, func() { ctx.LogError("error msg", "key", "val") })
}

func TestContext_Shutdown_NilTelemetry(t *testing.T) {
	ctx := &Context{Config: &config.Config{}, TelemetryClient: nil}
	assert.NotPanics(t, func() { ctx.Shutdown() })
}

func TestContext_TrackCommandCompletion_NilTelemetry(t *testing.T) {
	ctx := &Context{Config: &config.Config{}, TelemetryClient: nil}
	assert.NotPanics(t, func() { ctx.TrackCommandCompletion(nil) })
}

func TestContext_TrackCommandError_NilTelemetry(t *testing.T) {
	ctx := &Context{Config: &config.Config{}, TelemetryClient: nil}
	assert.NotPanics(t, func() { ctx.TrackCommandError(nil, nil) })
}

func TestContext_PrintMethods_QuietMode(t *testing.T) {
	// quiet mode should suppress output without panicking
	ctx := &Context{Config: &config.Config{Quiet: true}}

	assert.NotPanics(t, func() { ctx.PrintSuccess("test") })
	assert.NotPanics(t, func() { ctx.PrintWarning("test") })
	assert.NotPanics(t, func() { ctx.PrintPreserved("test") })
	assert.NotPanics(t, func() { ctx.Printf("test %s", "val") })
	assert.NotPanics(t, func() { ctx.Println("test") })
}

func TestContext_PrintMethods_JSONMode(t *testing.T) {
	// json mode should suppress human output without panicking
	ctx := &Context{Config: &config.Config{JSON: true}}

	assert.NotPanics(t, func() { ctx.PrintSuccess("test") })
	assert.NotPanics(t, func() { ctx.PrintWarning("test") })
	assert.NotPanics(t, func() { ctx.PrintError("test") })
	assert.NotPanics(t, func() { ctx.PrintPreserved("test") })
	assert.NotPanics(t, func() { ctx.Printf("test %s", "val") })
	assert.NotPanics(t, func() { ctx.Println("test") })
	assert.NotPanics(t, func() { ctx.Fprintf(os.Stderr, "test %s", "val") })
}

func TestContext_PrintMethods_NormalMode(t *testing.T) {
	SetJSONMode(false)
	defer SetJSONMode(false)

	ctx := &Context{Config: &config.Config{}}

	// capture stdout to avoid noise
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	devNull, _ := os.Open(os.DevNull)
	os.Stdout = devNull
	os.Stderr = devNull
	defer func() {
		os.Stdout = oldStdout
		os.Stderr = oldStderr
	}()

	// these should all execute without panic and actually call the underlying Print* functions
	assert.NotPanics(t, func() { ctx.PrintSuccess("test") })
	assert.NotPanics(t, func() { ctx.PrintWarning("test") })
	assert.NotPanics(t, func() { ctx.PrintError("test") })
	assert.NotPanics(t, func() { ctx.PrintPreserved("test") })
	assert.NotPanics(t, func() { ctx.Printf("test %s\n", "val") })
	assert.NotPanics(t, func() { ctx.Println("test") })
	assert.NotPanics(t, func() { ctx.Fprintf(os.Stderr, "test %s", "val") })
}

func TestContext_TrackCommandError_NilCmd(t *testing.T) {
	ctx := &Context{Config: &config.Config{}, TelemetryClient: nil}
	// nil cmd should not panic
	assert.NotPanics(t, func() { ctx.TrackCommandError(nil, nil) })
}
