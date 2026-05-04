package sessionsummary

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
)

// SessionHash returns a short stable correlation key for telemetry events
// that touch the same session at different points in the pipeline.
//
// Used by:
//   - cmd/ox/session_optimize_for_summary.go (EventTokenoptRun)
//   - internal/daemon/agentwork/session_finalize.go (EventSummarization)
//
// Both events emit this hash so operators can answer questions like
// "for sessions with >70% token reduction, what's the LLM input_tokens
// distribution?" — joining the compression-metrics event with the
// LLM-cost-shape event server-side.
//
// Privacy: hashes the session NAME, not the path. The session name is a
// per-session token (e.g. 2026-05-04T15-00-ryan-OxXyz1) that's safe to
// hash; the full path may contain a username and shouldn't be exposed
// in telemetry, even hashed (small hash space + known username pattern
// = recoverable). 8 bytes (64 bits) is collision-safe at per-user
// session volumes — a user generating 1000 sessions/day for 100 years
// has a collision probability under 1 in a billion.
func SessionHash(sessionName string) string {
	if sessionName == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(sessionName))
	return hex.EncodeToString(sum[:8])
}

// SessionHashFromRawPath extracts the session name from rawPath
// (.../<sessionName>/raw.jsonl) and returns its hash. Convenience for
// callers that have the path on hand. Returns "" when the path doesn't
// resolve to a recognizable session name.
func SessionHashFromRawPath(rawPath string) string {
	if rawPath == "" {
		return ""
	}
	dir := filepath.Dir(rawPath)
	name := filepath.Base(dir)
	if name == "" || name == "." {
		return ""
	}
	return SessionHash(name)
}
