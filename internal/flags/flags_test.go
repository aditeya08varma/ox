package flags_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/sageox/ox/internal/flags"
)

func TestDefaults(t *testing.T) {
	d := flags.Defaults()
	if !d.CodeDBEnabled {
		t.Error("CodeDBEnabled should default true")
	}
	if !d.WhisperEnabled {
		t.Error("WhisperEnabled should default true")
	}
	if !d.DistillEnabled {
		t.Error("DistillEnabled should default true")
	}
	if d.AutoDistill {
		t.Error("AutoDistill should default false")
	}
	if d.TUIEnabled {
		t.Error("TUIEnabled should default false")
	}
	if d.DisableFileDeleteTools {
		t.Error("DisableFileDeleteTools should default false")
	}
	if d.DisableShellExecTools {
		t.Error("DisableShellExecTools should default false")
	}
	if d.PrimeAppend != "" {
		t.Error("PrimeAppend should default empty")
	}
}

func TestResolveNoProviders(t *testing.T) {
	got := flags.Resolve(context.Background())
	want := flags.Defaults()
	if got != want {
		t.Errorf("Resolve() with no providers = %+v, want %+v", got, want)
	}
}

func TestEnvProviderUnset(t *testing.T) {
	os.Unsetenv("FEATURE_MEMORY")
	os.Unsetenv("FEATURE_TUI")

	f := flags.Resolve(context.Background(), flags.EnvProvider{})
	// unset env vars should not change defaults
	if !f.DistillEnabled {
		t.Error("DistillEnabled should remain true when FEATURE_MEMORY unset")
	}
	if f.TUIEnabled {
		t.Error("TUIEnabled should remain false when FEATURE_TUI unset")
	}
}

func TestEnvProviderDisablesFeature(t *testing.T) {
	t.Setenv("FEATURE_MEMORY", "false")

	f := flags.Resolve(context.Background(), flags.EnvProvider{})
	if f.DistillEnabled {
		t.Error("DistillEnabled should be false when FEATURE_MEMORY=false")
	}
}

func TestEnvProviderEnablesFeature(t *testing.T) {
	t.Setenv("FEATURE_TUI", "true")

	f := flags.Resolve(context.Background(), flags.EnvProvider{})
	if !f.TUIEnabled {
		t.Error("TUIEnabled should be true when FEATURE_TUI=true")
	}
}

func TestEnvProviderRecognisesVariants(t *testing.T) {
	for _, val := range []string{"1", "yes", "true"} {
		t.Setenv("FEATURE_MEMORY", val)
		f := flags.Resolve(context.Background(), flags.EnvProvider{})
		if !f.DistillEnabled {
			t.Errorf("DistillEnabled should be true for FEATURE_MEMORY=%q", val)
		}
	}
}

func TestDaemonProviderNilSettings(t *testing.T) {
	p := flags.DaemonProvider{CachedSettings: nil}
	f := flags.Resolve(context.Background(), p)
	if f != flags.Defaults() {
		t.Error("DaemonProvider with nil cache should not change defaults")
	}
}

func TestDaemonProviderStaleCache(t *testing.T) {
	stale := &flags.CLISettingsResponse{
		Features: flags.CLIFeatures{
			CodeDB:  false, // server wanted codedb off
			Whisper: true,
			Distill: true,
		},
		FetchedAt: time.Now().Add(-3 * time.Hour), // older than 2× max age
	}
	p := flags.DaemonProvider{CachedSettings: stale}
	f := flags.Resolve(context.Background(), p)
	// stale cache should be ignored — defaults apply
	if !f.CodeDBEnabled {
		t.Error("stale cache should be ignored; CodeDBEnabled should be default true")
	}
}

func TestDaemonProviderFreshCache(t *testing.T) {
	fresh := &flags.CLISettingsResponse{
		Features: flags.CLIFeatures{
			CodeDB:  false, // server disabled codedb
			Whisper: true,
			Distill: true,
		},
		Killswitches: flags.CLIKillswitches{
			DisableFileDeleteTools: true,
		},
		PrimeAppend: "Always use snake_case.",
		FetchedAt:   time.Now(),
	}
	p := flags.DaemonProvider{CachedSettings: fresh}
	f := flags.Resolve(context.Background(), p)
	if f.CodeDBEnabled {
		t.Error("CodeDBEnabled should be false per remote settings")
	}
	if !f.DisableFileDeleteTools {
		t.Error("DisableFileDeleteTools kill switch should be active")
	}
	if f.PrimeAppend != "Always use snake_case." {
		t.Errorf("PrimeAppend = %q, want %q", f.PrimeAppend, "Always use snake_case.")
	}
}

func TestRemoteSettingsToPatchNil(t *testing.T) {
	if flags.RemoteSettingsToPatch(nil) != nil {
		t.Error("RemoteSettingsToPatch(nil) should return nil")
	}
}

func TestGlobalInitAndGet(t *testing.T) {
	t.Setenv("FEATURE_TUI", "true")
	flags.Init(context.Background(), flags.EnvProvider{})
	if !flags.Get().TUIEnabled {
		t.Error("Get() should reflect Init() result")
	}
	// reset to defaults for other tests
	flags.Init(context.Background())
}

func TestSaveThenLoadCachedSettings(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	ep := "https://sageox.ai"
	want := &flags.CLISettingsResponse{
		Features: flags.CLIFeatures{
			CodeDB:  false,
			Whisper: true,
			Distill: true,
			TUI:     true,
		},
		Killswitches: flags.CLIKillswitches{
			DisableFileDeleteTools: true,
		},
		PrimeAppend: "Always prefer snake_case.",
	}

	if err := flags.SaveCachedSettings(ep, want); err != nil {
		t.Fatalf("SaveCachedSettings: %v", err)
	}

	got, err := flags.LoadCachedSettings(ep)
	if err != nil {
		t.Fatalf("LoadCachedSettings: %v", err)
	}
	if got == nil {
		t.Fatal("LoadCachedSettings returned nil, want non-nil")
	}
	if got.Features != want.Features {
		t.Errorf("Features = %+v, want %+v", got.Features, want.Features)
	}
	if got.Killswitches != want.Killswitches {
		t.Errorf("Killswitches = %+v, want %+v", got.Killswitches, want.Killswitches)
	}
	if got.PrimeAppend != want.PrimeAppend {
		t.Errorf("PrimeAppend = %q, want %q", got.PrimeAppend, want.PrimeAppend)
	}
}

func TestLoadCachedSettingsMissingFile(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	got, err := flags.LoadCachedSettings("https://sageox.ai")
	if err != nil {
		t.Fatalf("expected no error for missing file, got: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing file, got: %+v", got)
	}
}

func TestLoadCachedSettingsWrongEndpoint(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	epA := "https://sageox.ai"
	epB := "https://staging.sageox.ai"

	r := &flags.CLISettingsResponse{
		Features: flags.CLIFeatures{CodeDB: false},
	}
	if err := flags.SaveCachedSettings(epA, r); err != nil {
		t.Fatalf("SaveCachedSettings: %v", err)
	}

	got, err := flags.LoadCachedSettings(epB)
	if err != nil {
		t.Fatalf("LoadCachedSettings: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for different endpoint, got: %+v", got)
	}
}

func TestSaveCachedSettingsIdempotent(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	epA := "https://sageox.ai"
	epB := "https://staging.sageox.ai"

	rA1 := &flags.CLISettingsResponse{
		Features:    flags.CLIFeatures{CodeDB: true},
		PrimeAppend: "first write",
	}
	rA2 := &flags.CLISettingsResponse{
		Features:    flags.CLIFeatures{CodeDB: false},
		PrimeAppend: "second write wins",
	}
	rB := &flags.CLISettingsResponse{
		Features: flags.CLIFeatures{Whisper: false},
	}

	// save both endpoints so we can confirm epB survives epA's second write
	if err := flags.SaveCachedSettings(epA, rA1); err != nil {
		t.Fatalf("SaveCachedSettings epA first: %v", err)
	}
	if err := flags.SaveCachedSettings(epB, rB); err != nil {
		t.Fatalf("SaveCachedSettings epB: %v", err)
	}
	if err := flags.SaveCachedSettings(epA, rA2); err != nil {
		t.Fatalf("SaveCachedSettings epA second: %v", err)
	}

	gotA, err := flags.LoadCachedSettings(epA)
	if err != nil || gotA == nil {
		t.Fatalf("LoadCachedSettings epA: err=%v, got=%v", err, gotA)
	}
	if gotA.PrimeAppend != "second write wins" {
		t.Errorf("epA PrimeAppend = %q, want %q", gotA.PrimeAppend, "second write wins")
	}
	if gotA.Features.CodeDB {
		t.Error("epA CodeDB should be false after second write")
	}

	// epB must be unaffected by epA's second write
	gotB, err := flags.LoadCachedSettings(epB)
	if err != nil || gotB == nil {
		t.Fatalf("LoadCachedSettings epB: err=%v, got=%v", err, gotB)
	}
	if gotB.Features.Whisper {
		t.Error("epB Whisper should be false as originally saved")
	}
}
