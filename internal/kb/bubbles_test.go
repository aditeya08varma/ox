package kb

// bubbles_test.go — covers the scoped KB-API catalog fetch (FetchBubbles),
// ambient-scope resolution (AmbientScopes), and clone-URL derivation
// (GitCloneURL / bubbleFromRow). The OX_KB_DISABLE escape hatch and the
// no-persistent-disk skip are pinned in cmd/ox/kb_compat_test.go — not
// duplicated here.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/runtime"
)

// resetRuntimeBaseline clears venue / capability env vars so the runtime
// probe reports a "laptop default" baseline (PersistDisk=true) and the
// fetch is enabled. Without it, a sibling test that set OX_EPHEMERAL into
// the cached runtime.Caps() would silently skip every fetch here.
func resetRuntimeBaseline(t *testing.T) {
	t.Helper()
	for _, ev := range []string{"OX_EPHEMERAL", "CLAUDE_CODE_REMOTE", "DEVIN_TASK_ID", "OX_PERSIST_DISK", "OX_NO_DAEMON", "OX_KB_DISABLE"} {
		t.Setenv(ev, "")
	}
	runtime.Reset()
	t.Cleanup(runtime.Reset)
}

// scopedFakeSource returns per-scope rows/errors keyed by scope ID and
// records which scopes were requested.
type scopedFakeSource struct {
	rows      map[string][]api.KB
	errs      map[string]error
	requested []api.KBScope
}

func (f *scopedFakeSource) ListBubbles(_ context.Context, scope api.KBScope) ([]api.KB, error) {
	f.requested = append(f.requested, scope)
	if err, ok := f.errs[scope.ID]; ok {
		return nil, err
	}
	return f.rows[scope.ID], nil
}

func teamScope(id string) api.KBScope {
	return api.KBScope{Type: api.KBScopeTypeTeam, ID: id}
}

// --- A. FetchBubbles fan-out semantics ---

// TestFetchBubbles_MultiScopeUnionDedupedByKBID verifies rows from multiple
// scopes are unioned, with duplicates (same KBID appearing in more than one
// scope) collapsed to the first occurrence.
// Failure prevented: once the personal scope joins the ambient pair
// (ox-cag9.8), a bubble visible in both scopes would render twice in
// `ox kb list` and prime.
func TestFetchBubbles_MultiScopeUnionDedupedByKBID(t *testing.T) {
	resetRuntimeBaseline(t)

	source := &scopedFakeSource{rows: map[string][]api.KB{
		"team_a": {
			{KBID: "kb_shared", KBType: api.KBTypeTeam, Slug: "shared"},
			{KBID: "kb_a_only", KBType: api.KBTypeTeam, Slug: "a-only"},
		},
		"team_b": {
			{KBID: "kb_shared", KBType: api.KBTypeTeam, Slug: "shared"},
			{KBID: "kb_b_only", KBType: api.KBTypeTeam, Slug: "b-only"},
		},
	}}

	res := FetchBubbles(context.Background(), source, "https://sageox.ai",
		[]api.KBScope{teamScope("team_a"), teamScope("team_b")})

	if len(res.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %+v", res.Warnings)
	}
	if len(res.Bubbles) != 3 {
		t.Fatalf("expected 3 deduped bubbles, got %d: %+v", len(res.Bubbles), res.Bubbles)
	}
	seen := map[string]int{}
	for _, b := range res.Bubbles {
		seen[b.KBID]++
	}
	for _, id := range []string{"kb_shared", "kb_a_only", "kb_b_only"} {
		if seen[id] != 1 {
			t.Errorf("KBID %q appeared %d times, want exactly 1", id, seen[id])
		}
	}
	if len(source.requested) != 2 {
		t.Errorf("expected both scopes queried, got %+v", source.requested)
	}
}

// TestFetchBubbles_UnavailableScope_SilentSkip verifies a scope that
// returns api.ErrKBAPIUnavailable (flag off / non-member 403/404)
// contributes zero rows and NO warning — absence is not an error — while
// other scopes' rows survive.
// Failure prevented: a flag-off scope spamming `ox kb list` with a warning
// banner on every invocation.
func TestFetchBubbles_UnavailableScope_SilentSkip(t *testing.T) {
	resetRuntimeBaseline(t)

	source := &scopedFakeSource{
		rows: map[string][]api.KB{
			"team_ok": {{KBID: "kb_ok", KBType: api.KBTypeTeam, Slug: "ok"}},
		},
		errs: map[string]error{
			"team_off": fmt.Errorf("kb list: %w", api.ErrKBAPIUnavailable),
		},
	}

	res := FetchBubbles(context.Background(), source, "https://sageox.ai",
		[]api.KBScope{teamScope("team_off"), teamScope("team_ok")})

	if len(res.Warnings) != 0 {
		t.Errorf("ErrKBAPIUnavailable must not surface as a warning, got %+v", res.Warnings)
	}
	if len(res.Bubbles) != 1 || res.Bubbles[0].KBID != "kb_ok" {
		t.Errorf("expected only the reachable scope's row, got %+v", res.Bubbles)
	}
}

// TestFetchBubbles_OtherError_BecomesWarning verifies a non-sentinel error
// (real outage, 5xx, network) produces one Warning while other scopes'
// rows still arrive — degraded, not failed.
// Failure prevented: one erroring scope wiping out the entire catalog, or
// a real outage being silently swallowed like a flag-off.
func TestFetchBubbles_OtherError_BecomesWarning(t *testing.T) {
	resetRuntimeBaseline(t)

	source := &scopedFakeSource{
		rows: map[string][]api.KB{
			"team_ok": {{KBID: "kb_ok", KBType: api.KBTypeTeam, Slug: "ok"}},
		},
		errs: map[string]error{
			"team_broken": errors.New("HTTP 500 from https://api.example/api/v1/kb"),
		},
	}

	res := FetchBubbles(context.Background(), source, "https://sageox.ai",
		[]api.KBScope{teamScope("team_broken"), teamScope("team_ok")})

	if len(res.Warnings) != 1 {
		t.Fatalf("expected exactly 1 warning, got %+v", res.Warnings)
	}
	if !strings.Contains(res.Warnings[0].Err, "HTTP 500") {
		t.Errorf("warning must carry the underlying error, got %q", res.Warnings[0].Err)
	}
	if len(res.Bubbles) != 1 || res.Bubbles[0].KBID != "kb_ok" {
		t.Errorf("healthy scope's rows must survive a sibling scope's failure, got %+v", res.Bubbles)
	}
}

// TestFetchBubbles_EmptyScopes_SourceNotCalled verifies no scopes → empty
// result with zero API calls (there is no contextless union to ask for).
// Failure prevented: a caller outside any project accidentally issuing a
// bare list request the server would 400.
func TestFetchBubbles_EmptyScopes_SourceNotCalled(t *testing.T) {
	resetRuntimeBaseline(t)

	source := &scopedFakeSource{rows: map[string][]api.KB{
		"team_a": {{KBID: "kb_a", Slug: "a"}},
	}}

	for name, scopes := range map[string][]api.KBScope{"nil": nil, "empty": {}} {
		res := FetchBubbles(context.Background(), source, "https://sageox.ai", scopes)
		if len(res.Bubbles) != 0 || len(res.Warnings) != 0 {
			t.Errorf("%s scopes: expected empty result, got %+v", name, res)
		}
	}
	if len(source.requested) != 0 {
		t.Errorf("source must not be called without scopes, got %+v", source.requested)
	}
}

// TestFetchBubbles_NilSource_Empty verifies a nil source short-circuits to
// an empty result rather than panicking.
// Failure prevented: a caller that failed to build the KB client (no
// endpoint, no auth) crashing prime/status instead of degrading.
func TestFetchBubbles_NilSource_Empty(t *testing.T) {
	resetRuntimeBaseline(t)

	res := FetchBubbles(context.Background(), nil, "https://sageox.ai",
		[]api.KBScope{teamScope("team_a")})
	if len(res.Bubbles) != 0 || len(res.Warnings) != 0 {
		t.Errorf("expected empty result for nil source, got %+v", res)
	}
}

// TestFetchBubbles_RowFieldsFlowThrough verifies the api.KB → Bubble
// conversion carries the ADR-028 fields end-to-end through FetchBubbles:
// scope, description, topics, lifecycle, last activity, endpoint.
// Failure prevented: a field silently dropped in bubbleFromRow would blank
// the corresponding column in `ox kb list --json` and prime's KB envelope.
func TestFetchBubbles_RowFieldsFlowThrough(t *testing.T) {
	resetRuntimeBaseline(t)

	source := &scopedFakeSource{rows: map[string][]api.KB{
		"team_a": {{
			KBID:           "kb_full",
			KBType:         api.KBTypeTeam,
			Slug:           "platform",
			Name:           "Platform",
			ScopeType:      "team",
			ScopeID:        "team_a",
			Description:    "Platform knowledge",
			Topics:         []string{"infra", "deploys"},
			ViewerRole:     "member",
			LifecycleState: "active",
			LastActivityAt: "2026-07-27T10:00:00Z",
			GitPath:        "kb/kb_full",
		}},
	}}

	res := FetchBubbles(context.Background(), source, "https://sageox.ai",
		[]api.KBScope{teamScope("team_a")})
	if len(res.Bubbles) != 1 {
		t.Fatalf("expected 1 bubble, got %+v", res.Bubbles)
	}

	b := res.Bubbles[0]
	checks := []struct{ name, got, want string }{
		{"KBID", b.KBID, "kb_full"},
		{"Slug", b.Slug, "platform"},
		{"Name", b.Name, "Platform"},
		{"ScopeType", b.ScopeType, "team"},
		{"ScopeID", b.ScopeID, "team_a"},
		{"Description", b.Description, "Platform knowledge"},
		{"ViewerRole", b.ViewerRole, "member"},
		{"LifecycleState", b.LifecycleState, "active"},
		{"LastActivityAt", b.LastActivityAt, "2026-07-27T10:00:00Z"},
		{"Endpoint", b.Endpoint, "https://sageox.ai"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}
	if len(b.Topics) != 2 || b.Topics[0] != "infra" || b.Topics[1] != "deploys" {
		t.Errorf("Topics: got %+v, want [infra deploys]", b.Topics)
	}
}

// --- B. clone-URL derivation ---

// TestFetchBubbles_RepoURLPrecedence verifies (through the real FetchBubbles
// → bubbleFromRow path) that a server-supplied repo_url wins over GitPath
// derivation, GitPath derives when repo_url is absent, and neither leaves
// RepoURL empty-safe.
// Failure prevented: re-deriving a URL the server already supplied (older
// response shapes) would clobber a correct clone URL with a guessed one.
func TestFetchBubbles_RepoURLPrecedence(t *testing.T) {
	resetRuntimeBaseline(t)

	source := &scopedFakeSource{rows: map[string][]api.KB{
		"team_a": {
			{KBID: "kb_explicit", Slug: "explicit", RepoURL: "https://example.invalid/explicit.git", GitPath: "kb/kb_explicit"},
			{KBID: "kb_derived", Slug: "derived", GitPath: "kb/kb_derived"},
			{KBID: "kb_unprovisioned", Slug: "unprovisioned"},
		},
	}}

	res := FetchBubbles(context.Background(), source, "https://sageox.ai",
		[]api.KBScope{teamScope("team_a")})
	if len(res.Bubbles) != 3 {
		t.Fatalf("expected 3 bubbles, got %+v", res.Bubbles)
	}

	byID := map[string]Bubble{}
	for _, b := range res.Bubbles {
		byID[b.KBID] = b
	}
	if got := byID["kb_explicit"].RepoURL; got != "https://example.invalid/explicit.git" {
		t.Errorf("server-supplied repo_url must win over GitPath derivation, got %q", got)
	}
	if got := byID["kb_derived"].RepoURL; got != "https://git.sageox.ai/kb/kb_derived.git" {
		t.Errorf("GitPath-derived clone URL: got %q, want https://git.sageox.ai/kb/kb_derived.git", got)
	}
	if got := byID["kb_unprovisioned"].RepoURL; got != "" {
		t.Errorf("un-provisioned bubble (no repo_url, no git_path) must have empty RepoURL, got %q", got)
	}
}

// TestGitCloneURL_Derivation pins the git-subdomain convention:
// https://git.<endpoint-host>/<path>.git, with subdomain-prefix
// normalization on the endpoint and slash-trimming on the path.
// Failure prevented: a URL-building drift (double slash, missing .git,
// unnormalized api. prefix) breaking every daemon mount clone.
func TestGitCloneURL_Derivation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		endpoint string
		gitPath  string
		want     string
	}{
		{"canonical", "https://sageox.ai", "kb/kb_01", "https://git.sageox.ai/kb/kb_01.git"},
		{"api subdomain normalized", "https://api.sageox.ai", "kb/kb_01", "https://git.sageox.ai/kb/kb_01.git"},
		{"path slashes trimmed", "https://sageox.ai", "/kb/kb_01/", "https://git.sageox.ai/kb/kb_01.git"},
		{"path whitespace trimmed", "https://sageox.ai", "  kb/kb_01  ", "https://git.sageox.ai/kb/kb_01.git"},
		{"empty endpoint", "", "kb/kb_01", ""},
		{"empty path", "https://sageox.ai", "", ""},
		{"whitespace-only path", "https://sageox.ai", "   ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := GitCloneURL(tc.endpoint, tc.gitPath); got != tc.want {
				t.Errorf("GitCloneURL(%q, %q) = %q, want %q", tc.endpoint, tc.gitPath, got, tc.want)
			}
		})
	}
}

// --- C. ambient scopes ---

// TestAmbientScopes verifies the v1 contract: a team-bound project implies
// exactly the team scope; no team binding (empty / whitespace) implies no
// scopes at all. The personal scope is deliberately absent until the
// ADR-086 backfill fix lands (bead ox-cag9.8).
// Failure prevented: an empty team ID leaking through as a scope would
// send a guaranteed-400 list request on every command outside a project;
// a premature personal scope would resurrect the deferred half of the pair.
func TestAmbientScopes(t *testing.T) {
	t.Parallel()

	t.Run("empty team ID → nil", func(t *testing.T) {
		if got := AmbientScopes(""); got != nil {
			t.Errorf("expected nil scopes for empty team ID, got %+v", got)
		}
	})

	t.Run("whitespace team ID → nil", func(t *testing.T) {
		if got := AmbientScopes("   "); got != nil {
			t.Errorf("expected nil scopes for whitespace team ID, got %+v", got)
		}
	})

	t.Run("team ID → single team scope", func(t *testing.T) {
		got := AmbientScopes("team_abc")
		if len(got) != 1 {
			t.Fatalf("expected exactly one scope (personal is deferred, ox-cag9.8), got %+v", got)
		}
		if got[0].Type != api.KBScopeTypeTeam || got[0].ID != "team_abc" {
			t.Errorf("expected {team team_abc}, got %+v", got[0])
		}
	})
}

// TestGitCloneURL_PreservesSchemeAndPort pins clone-URL derivation for
// custom/dev endpoints: scheme (http) and nondefault ports must survive,
// and an endpoint already on the git. subdomain must not double-prefix.
// Failure prevented: every current-shape bubble clone failing on dev
// endpoints because derivation forced https://git.<bare-host>.
func TestGitCloneURL_PreservesSchemeAndPort(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, endpoint, gitPath, want string
	}{
		{"https default", "https://test.sageox.ai", "kb/kb_1", "https://git.test.sageox.ai/kb/kb_1.git"},
		{"http localhost with port", "http://localhost:8080", "kb/kb_1", "http://git.localhost:8080/kb/kb_1.git"},
		{"bare host defaults https", "test.sageox.ai", "kb/kb_1", "https://git.test.sageox.ai/kb/kb_1.git"},
		{"git subdomain not doubled", "https://git.test.sageox.ai", "kb/kb_1", "https://git.test.sageox.ai/kb/kb_1.git"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GitCloneURL(tt.endpoint, tt.gitPath); got != tt.want {
				t.Errorf("GitCloneURL(%q, %q) = %q, want %q", tt.endpoint, tt.gitPath, got, tt.want)
			}
		})
	}
}
