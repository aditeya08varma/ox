package kb

// bubbles.go — the KB-API-only bubble catalog fetch.
//
// Replaces the deleted three-source merger (merge.go) under ox ADR-028 /
// epic ox-gmkd: team contexts and ledgers are permanent conversation stores,
// never presented as bubbles, so the only source of bubble rows is the KB
// API. This is a thin fetch-and-convert helper shared by `ox kb list`,
// `ox status`, and prime so the three surfaces can't disagree.
//
// Escape hatch: OX_KB_DISABLE=1 skips the fetch entirely. Mirrors the
// OX_XDG_DISABLE pattern; used for debugging rollout issues, not daily
// operation. The fetch is also skipped when the runtime has no persistent
// disk (nothing to reconcile against — see runtime.Caps).

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/runtime"
)

// envDisableKBSource is the escape hatch: when set to a truthy value the
// fetch returns an empty result. The daemon's sync loop honors the same
// variable independently (internal/daemon/sync_bubbles.go).
const envDisableKBSource = "OX_KB_DISABLE"

// Bubble is one knowledge-bubble row as the CLI presents it. Converted
// from api.KB; LocalPath and Endpoint are populated by the caller from
// project context where known (the KB API does not return them today).
type Bubble struct {
	// KBID is the immutable kb identifier (kb_xxx).
	KBID string

	// Type is the kb_type bucket. Unknown server values are already
	// collapsed to KBTypeUnknown by the kb client.
	Type api.KBType

	// Slug is the human-readable slug (kebab-case, bare — the leading `#`
	// is display-only).
	Slug string

	// Name is the display name.
	Name string

	// ViewerRole is the caller's role on this bubble ("admin", "member",
	// "viewer").
	ViewerRole string

	// LocalPath is the on-disk checkout path when the bubble is mounted.
	LocalPath string

	// RepoURL is the git clone URL when known.
	RepoURL string

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

// KBSource is the contract for fetching kb rows. Defined as an interface so
// tests can supply fakes without an httptest server; production passes
// *api.KBClient.
type KBSource interface {
	ListBubbles(ctx context.Context) ([]api.KB, error)
}

// FetchBubbles lists the caller's bubbles from the KB API and converts them
// to presentation rows.
//
// Failure semantics:
//   - nil source, OX_KB_DISABLE, or no persistent disk → empty result.
//   - api.ErrKBAPIUnavailable (403/404 — feature flag off for this caller)
//     → empty result, NO warning: absence of the feature is not an error.
//   - any other error → empty rows plus one Warning so the caller can
//     surface degraded state without failing the whole command.
func FetchBubbles(ctx context.Context, source KBSource, endpoint string) ListResult {
	if source == nil || kbDisabledByEnv() {
		return ListResult{}
	}

	rows, err := source.ListBubbles(ctx)
	if err != nil {
		if errors.Is(err, api.ErrKBAPIUnavailable) {
			slog.Debug("kb fetch: kb API unavailable, treating as 0 rows", "err", err)
			return ListResult{}
		}
		slog.Warn("kb fetch: list failed", "err", err)
		return ListResult{Warnings: []Warning{{Err: err.Error()}}}
	}

	bubbles := make([]Bubble, 0, len(rows))
	for _, r := range rows {
		bubbles = append(bubbles, Bubble{
			KBID:       r.KBID,
			Type:       r.KBType,
			Slug:       r.Slug,
			Name:       r.Name,
			ViewerRole: r.ViewerRole,
			RepoURL:    r.RepoURL,
			Endpoint:   endpoint,
		})
	}
	return ListResult{Bubbles: bubbles}
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
