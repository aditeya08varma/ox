package daemon

import "time"

// intervalForType returns the daemon sync cadence for a given kb type.
// Centralized here so all sync paths converge on one selection point —
// future migrations (ox-yvc1.3) replace per-callsite switches with calls
// to this function.
//
// Cadence rationale (ADR-028): every bubble is now a curated synthesis
// that changes only when a curator publishes — there is no high-churn
// personal/profile/team tier anymore, so ALL types share the slower
// read cadence (SyncIntervalRead, 60s default). The per-type seam is
// deliberately kept even though the switch is gone: if a future kb kind
// reintroduces a faster tier, this function is the single place to
// route it.
func intervalForType(cfg *Config, kbType string) time.Duration {
	_ = kbType // uniform cadence across all kb types — see rationale above
	if cfg != nil && cfg.SyncIntervalRead > 0 {
		return cfg.SyncIntervalRead
	}
	return 60 * time.Second
}
