package kb

// bubbles.go — the scoped KB-API bubble catalog fetch.
//
// Under ox ADR-028 the only source of bubble rows is the KB API, and the
// list endpoint requires an explicit scope per call (sageox-mono ADR-073:
// listing is per-context, members-only). This file owns:
//
//   - AmbientScopes: which scopes a project context implies (v1: the
//     project's team only; the caller's personal scope is deferred until
//     the ADR-086 personal-team backfill fix lands — bead ox-cag9.8),
//   - FetchBubbles: fan-out over those scopes, union + dedup, warning
//     collection. Shared by `ox kb list`, `ox status`, prime, and the
//     daemon so the surfaces can't disagree.
//
// Escape hatch: OX_KB_DISABLE=1 skips the fetch entirely. Mirrors the
// OX_XDG_DISABLE pattern; used for debugging rollout issues, not daily
// operation. The fetch is also skipped when the runtime has no persistent
// disk (nothing to reconcile against — see runtime.Caps; ephemeral
// runtimes are steered to the cloud MCP for knowledge-bubble queries).

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"os"
	"strings"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/runtime"
)

// envDisableKBSource is the escape hatch: when set to a truthy value the
// fetch returns an empty result. The daemon's sync loop honors the same
// variable independently (internal/daemon/sync_bubbles.go).
const envDisableKBSource = "OX_KB_DISABLE"

// Bubble is one knowledge-bubble row as the CLI presents it. Converted
// from api.KB; LocalPath is populated by the caller where known (the KB
// API does not return it).
type Bubble struct {
	// KBID is the immutable kb identifier (kb_xxx).
	KBID string

	// Type is the kb_type bucket. Unknown server values are already
	// collapsed to KBTypeUnknown by the kb client.
	Type api.KBType

	// Slug is the human-readable slug (kebab-case, bare — the leading `#`
	// is display-only). Unique within a scope, NOT globally (ADR-073).
	Slug string

	// Name is the display name.
	Name string

	// ScopeType/ScopeID locate the bubble: "user"|"team" + the owning id.
	ScopeType string
	ScopeID   string

	// Description is the bubble's free-text description.
	Description string

	// Topics is the declared topic list.
	Topics []string

	// ViewerRole is the caller's role ("admin", "member", "viewer").
	// The server omits it from list responses; populated on single reads.
	ViewerRole string

	// LifecycleState is "provisioning", "active", or "provision-failed".
	LifecycleState string

	// LocalPath is the on-disk checkout path when the bubble is mounted.
	LocalPath string

	// RepoURL is the git clone URL: the server-supplied URL when present,
	// otherwise derived from GitPath + the endpoint's git host. Empty when
	// the bubble's repo has not been provisioned yet.
	RepoURL string

	// LastActivityAt is the bubble repo's last activity (RFC3339, from
	// GitLab) when known.
	LastActivityAt string

	// Endpoint is the SageOx API endpoint this row belongs to.
	Endpoint string
}

// Warning is a non-fatal fetch problem the caller can render without losing
// the rows that did arrive.
type Warning struct {
	Err string
}

// ListResult is the outcome of FetchBubbles.
type ListResult struct {
	Bubbles  []Bubble
	Warnings []Warning
}

// KBSource is the contract for fetching kb rows for one scope. Defined as an
// interface so tests can supply fakes without an httptest server; production
// passes *api.KBClient.
type KBSource interface {
	ListBubbles(ctx context.Context, scope api.KBScope) ([]api.KB, error)
}

// AmbientScopes returns the scopes an `ox init`-ed project implies: the
// project's team. Returns nil when the project has no team binding — there
// is then no scope to list, and FetchBubbles returns empty.
//
// Deliberately NOT included yet: the caller's personal scope. Personal
// bubbles are scoped to per-user private teams (ADR-086), whose backfill
// has a known server-side issue; until that is fixed the CLI is
// project-team-only across the board (ox ADR-028 §4, bead ox-cag9.8).
// This helper is the single place the scope-pair list lives so enabling
// personal later is a one-function change.
func AmbientScopes(teamID string) []api.KBScope {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return nil
	}
	return []api.KBScope{{Type: api.KBScopeTypeTeam, ID: teamID}}
}

// FetchBubbles lists the caller's bubbles for the given scopes and converts
// them to presentation rows (union across scopes, deduped by kb_id).
//
// Failure semantics, per scope:
//   - api.ErrKBAPIUnavailable (403/404 — feature flag off or non-member)
//     → zero rows for that scope, NO warning: absence is not an error.
//   - any other error → one Warning so the caller can surface degraded
//     state without failing the whole command.
//
// A nil source, no scopes, OX_KB_DISABLE, or no persistent disk → empty.
func FetchBubbles(ctx context.Context, source KBSource, endpointURL string, scopes []api.KBScope) ListResult {
	if source == nil || len(scopes) == 0 || kbDisabledByEnv() {
		return ListResult{}
	}

	var out ListResult
	seen := make(map[string]bool)
	for _, scope := range scopes {
		rows, err := source.ListBubbles(ctx, scope)
		if err != nil {
			if errors.Is(err, api.ErrKBAPIUnavailable) {
				slog.Debug("kb fetch: scope unavailable, treating as 0 rows",
					"scope_type", scope.Type, "scope_id", scope.ID, "err", err)
				continue
			}
			slog.Warn("kb fetch: list failed",
				"scope_type", scope.Type, "scope_id", scope.ID, "err", err)
			out.Warnings = append(out.Warnings, Warning{Err: err.Error()})
			continue
		}
		for _, r := range rows {
			if r.KBID != "" && seen[r.KBID] {
				continue
			}
			if r.KBID != "" {
				seen[r.KBID] = true
			}
			out.Bubbles = append(out.Bubbles, bubbleFromRow(r, endpointURL))
		}
	}
	return out
}

// bubbleFromRow converts one API row, deriving the clone URL when the
// server supplied a git path but no full URL.
func bubbleFromRow(r api.KB, endpointURL string) Bubble {
	repoURL := r.RepoURL
	if repoURL == "" && r.GitPath != "" {
		repoURL = GitCloneURL(endpointURL, r.GitPath)
	}
	return Bubble{
		KBID:           r.KBID,
		Type:           r.KBType,
		Slug:           r.Slug,
		Name:           r.Name,
		ScopeType:      r.ScopeType,
		ScopeID:        r.ScopeID,
		Description:    r.Description,
		Topics:         r.Topics,
		ViewerRole:     r.ViewerRole,
		LifecycleState: r.LifecycleState,
		RepoURL:        repoURL,
		LastActivityAt: r.LastActivityAt,
		Endpoint:       endpointURL,
	}
}

// GitCloneURL derives a bubble's clone URL from the SageOx endpoint and the
// server-reported git project path, following the platform's git-subdomain
// convention (git.<endpoint-host>). The endpoint's scheme and port are
// preserved so custom/dev endpoints (http, nondefault ports) keep working;
// scheme defaults to https when the endpoint carries none. Returns "" when
// either input is empty.
func GitCloneURL(endpointURL, gitPath string) string {
	gitPath = strings.Trim(strings.TrimSpace(gitPath), "/")
	normalized := endpoint.NormalizeEndpoint(endpointURL)
	if normalized == "" || gitPath == "" {
		return ""
	}
	u, err := url.Parse(normalized)
	if err != nil || u.Host == "" {
		return ""
	}
	scheme := u.Scheme
	if scheme == "" {
		scheme = "https"
	}
	host := u.Host // includes the port when present
	if !strings.HasPrefix(host, "git.") {
		host = "git." + host
	}
	return scheme + "://" + host + "/" + gitPath + ".git"
}

// kbDisabledByEnv returns true when OX_KB_DISABLE is set to a value commonly
// understood as "on", OR when the runtime has no persistent disk for the
// fetch's consumers to reconcile against. Empty / "0" / "false" / "no" /
// "off" leave the fetch enabled when persistence is available.
func kbDisabledByEnv() bool {
	if !runtime.Caps().PersistDisk {
		return true
	}
	v := strings.TrimSpace(os.Getenv(envDisableKBSource))
	if v == "" {
		return false
	}
	switch strings.ToLower(v) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}
