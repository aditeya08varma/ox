package flags

import (
	"context"
	"time"
)

// CLISettingsResponse is the wire type for GET /api/v1/cli/settings.
//
// sage-mono evaluates PostHog feature flags server-side (using device_id +
// user_id as distinct IDs) and returns pre-evaluated booleans. The ox CLI
// receives evaluated values — no PostHog SDK is needed in the CLI binary.
//
// If the endpoint is unreachable, [Defaults] are used. The daemon polls this
// endpoint on a background interval (see ox-ha7r) and caches the result to
// disk so CLI startup never blocks on a network call.
type CLISettingsResponse struct {
	Features     CLIFeatures     `json:"features"`
	Killswitches CLIKillswitches `json:"killswitches"`
	// PrimeAppend is appended to ox agent prime output after team context.
	// Allows the cloud to push org-wide prime additions without a CLI release.
	PrimeAppend string    `json:"prime_append,omitempty"`
	FetchedAt   time.Time `json:"fetched_at"`
}

// CLIFeatures contains server-evaluated feature gate values.
// All fields default to true (features on) when the endpoint is unavailable.
type CLIFeatures struct {
	CodeDB      bool `json:"codedb"`
	Whisper     bool `json:"whisper"`
	Distill     bool `json:"distill"`
	AutoDistill bool `json:"auto_distill"`
	TUI         bool `json:"tui"`
}

// CLIKillswitches contains server-evaluated kill switch values.
// All fields default to false (kill switches off) when the endpoint is unavailable.
type CLIKillswitches struct {
	DisableFileDeleteTools bool `json:"disable_file_delete_tools"`
	DisableShellExecTools  bool `json:"disable_shell_exec_tools"`
}

// CLISettingsMaxAge is the maximum age of a cached CLISettingsResponse before
// it is considered stale. Stale cache is still used — the daemon refreshes in
// the background. This matches the 1-hour polling interval.
const CLISettingsMaxAge = time.Hour

// CLISettingsFetchTimeout is the maximum time to wait for /api/v1/cli/settings.
// Fail fast: if the server is slow, use cached/default values immediately.
const CLISettingsFetchTimeout = 3 * time.Second

// RemoteSettingsToPatch converts a CLISettingsResponse into a Patch.
// Called by the daemon after a successful fetch before caching.
func RemoteSettingsToPatch(r *CLISettingsResponse) *Patch {
	if r == nil {
		return nil
	}
	return &Patch{
		CodeDBEnabled:          boolPtr(r.Features.CodeDB),
		WhisperEnabled:         boolPtr(r.Features.Whisper),
		DistillEnabled:         boolPtr(r.Features.Distill),
		AutoDistill:            boolPtr(r.Features.AutoDistill),
		TUIEnabled:             boolPtr(r.Features.TUI),
		DisableFileDeleteTools: boolPtr(r.Killswitches.DisableFileDeleteTools),
		DisableShellExecTools:  boolPtr(r.Killswitches.DisableShellExecTools),
		PrimeAppend:            strPtr(r.PrimeAppend),
	}
}

// DaemonProvider reads a pre-fetched CLISettingsResponse from the daemon IPC
// cache. It is a stub until ox-ha7r wires in the daemon polling loop and IPC
// handler. Until then, Patch always returns nil (no opinion).
//
// Once ox-ha7r lands, this provider is initialized with the daemon-cached
// response and passes it to RemoteSettingsToPatch.
type DaemonProvider struct {
	// CachedSettings is populated by the daemon IPC settings_get handler.
	// Nil means the daemon has not yet fetched remote settings.
	CachedSettings *CLISettingsResponse
}

func (p DaemonProvider) Patch(_ context.Context) (*Patch, Source, error) {
	if p.CachedSettings == nil {
		return nil, SourceDaemon, nil
	}
	// treat stale cache as no opinion — daemon will refresh in background
	if time.Since(p.CachedSettings.FetchedAt) > CLISettingsMaxAge*2 {
		return nil, SourceDaemon, nil
	}
	return RemoteSettingsToPatch(p.CachedSettings), SourceDaemon, nil
}
