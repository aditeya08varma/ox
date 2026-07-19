package main

import (
	"fmt"
	"net/url"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/sessionid"
)

// buildSessionURL constructs the canonical web URL for viewing a session.
// Returns empty string if required config (repo_id, endpoint) is missing.
// Used for best-effort commit trailers — the URL is only available while a
// session is actively recording.
func buildSessionURL(cfg *config.ProjectConfig, sessionName string) string {
	if cfg == nil || cfg.RepoID == "" || sessionName == "" {
		return ""
	}
	ep := endpoint.NormalizeEndpoint(cfg.GetEndpoint())
	if ep == "" {
		return ""
	}
	return fmt.Sprintf("%s/repo/%s/sessions/%s/view",
		ep,
		url.PathEscape(cfg.RepoID),
		url.PathEscape(sessionName),
	)
}

// buildConversationURL constructs the universal conversation link for a
// session: <endpoint>/c/<ses_id>. This is the durable canonical URL for
// artifacts that outlive the recording (commit trailers, PR bodies, plan
// footers) — unlike buildSessionURL it does not depend on repo_id or the
// mutable session directory name. Returns "" when the endpoint is missing
// or sessionID is not a valid ses_ identifier (e.g. recordings started
// under an older binary), so callers can fall back to the name-based URL.
func buildConversationURL(cfg *config.ProjectConfig, sessionID string) string {
	if cfg == nil || !sessionid.IsValidSessionID(sessionID) {
		return ""
	}
	ep := endpoint.NormalizeEndpoint(cfg.GetEndpoint())
	if ep == "" {
		return ""
	}
	return fmt.Sprintf("%s/c/%s", ep, url.PathEscape(sessionID))
}
