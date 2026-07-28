package daemon

import (
	"testing"
	"time"
)

// TestIntervalForType pins the uniform cadence: under ADR-028 every kb
// type is a curated synthesis that changes only when a curator
// publishes, so ALL types ride SyncIntervalRead. The per-type seam
// survives (this function is still the single routing point), but no
// type may route to the faster team-context cadence anymore.
//
// Failure prevented: a type silently regaining the 15s tier and
// hammering the API for content that only changes on publish.
func TestIntervalForType(t *testing.T) {
	cfg := &Config{
		SyncIntervalRead:        60 * time.Second,
		TeamContextSyncInterval: 15 * time.Second, // must be ignored by kb routing
	}
	cases := []string{
		"personal", "profile", "team", "repo", "custom", "channel",
		"unknown", "", "future_type_we_dont_know_yet",
	}
	for _, kind := range cases {
		t.Run(kind, func(t *testing.T) {
			got := intervalForType(cfg, kind)
			if got != 60*time.Second {
				t.Errorf("intervalForType(%q) = %v, want uniform 60s read cadence", kind, got)
			}
		})
	}
}

// TestIntervalForType_Defaults verifies the helper's hard-coded fallback
// kicks in when the config interval is zero (or the config itself nil) —
// a misconfigured daemon must still pick a sane cadence rather than
// busy-loop.
func TestIntervalForType_Defaults(t *testing.T) {
	if got := intervalForType(&Config{}, "team"); got != 60*time.Second {
		t.Errorf("zero-config team interval = %v, want 60s fallback", got)
	}
	if got := intervalForType(nil, "team"); got != 60*time.Second {
		t.Errorf("nil-config team interval = %v, want 60s fallback", got)
	}
}
