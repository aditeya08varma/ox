package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestKBClient builds a KBClient bound to a test server. Kept tiny on
// purpose so each test reads top-to-bottom without indirection.
func newTestKBClient(baseURL string) *KBClient {
	return &KBClient{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 5 * time.Second},
		authToken:  "test-token",
		version:    "test-version",
	}
}

// testScope is the fixture scope every list/resolve call uses. The list
// endpoint requires an explicit scope per call (sageox-mono ADR-073).
func testScope() KBScope {
	return KBScope{Type: KBScopeTypeTeam, ID: "team_test"}
}

// TestListBubbles_RequestCarriesScopeParams verifies the request URL carries
// scope_type + scope_id query parameters derived from the caller's scope.
// Failure prevented: dropping the scope params would make the server 400
// every list call (the contextless union no longer exists server-side).
func TestListBubbles_RequestCarriesScopeParams(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/v1/kb", r.URL.Path)
		assert.Equal(t, "team", r.URL.Query().Get("scope_type"))
		assert.Equal(t, "team_test", r.URL.Query().Get("scope_id"))

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"kbs":[]}`))
	}))
	defer srv.Close()

	client := newTestKBClient(srv.URL)
	_, err := client.ListBubbles(context.Background(), testScope())
	require.NoError(t, err)
}

// TestListBubbles_EmptyScope_NoHTTPCall ensures a zero-value scope is
// rejected client-side before any request is issued.
// Failure prevented: a caller forgetting to resolve the ambient scope would
// otherwise burn a round-trip on a guaranteed server-side 400.
func TestListBubbles_EmptyScope_NoHTTPCall(t *testing.T) {
	t.Parallel()

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"kbs":[]}`))
	}))
	defer srv.Close()

	client := newTestKBClient(srv.URL)

	for _, scope := range []KBScope{
		{},
		{Type: KBScopeTypeTeam}, // missing ID
		{ID: "team_test"},       // missing Type
	} {
		bubbles, err := client.ListBubbles(context.Background(), scope)
		require.Error(t, err, "scope %+v must be rejected", scope)
		assert.Nil(t, bubbles)
		assert.Contains(t, err.Error(), "scope")
	}
	assert.Equal(t, 0, calls, "invalid scopes must never reach the server")
}

// TestListBubbles_KBsEnvelope_CurrentServerShape verifies the current server
// shape decodes: {"kbs": [...]} envelope with "id" keys plus the new fields
// (scope_type/scope_id, topics, git_path, description, manager, steering,
// default_branch, last_activity_at).
// Failure prevented: the CLI silently dropping every row because the decoder
// only recognized the retired {"bubbles": [...]} / "kb_id" shape.
func TestListBubbles_KBsEnvelope_CurrentServerShape(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"kbs": [
				{
					"id": "kb_01",
					"kb_type": "team",
					"slug": "platform",
					"name": "Platform",
					"scope_type": "team",
					"scope_id": "team_test",
					"description": "Platform team knowledge",
					"steering": "route infra questions here",
					"topics": ["infra", "deploys"],
					"git_path": "kb/kb_01",
					"default_branch": "main",
					"last_activity_at": "2026-07-27T10:00:00Z",
					"manager": "usr_mgr",
					"lifecycle_state": "active"
				}
			]
		}`))
	}))
	defer srv.Close()

	client := newTestKBClient(srv.URL)
	bubbles, err := client.ListBubbles(context.Background(), testScope())

	require.NoError(t, err)
	require.Len(t, bubbles, 1)

	b := bubbles[0]
	assert.Equal(t, "kb_01", b.KBID, `"id" key must decode into KBID`)
	assert.Equal(t, KBTypeTeam, b.KBType)
	assert.Equal(t, "platform", b.Slug)
	assert.Equal(t, "team", b.ScopeType)
	assert.Equal(t, "team_test", b.ScopeID)
	assert.Equal(t, "Platform team knowledge", b.Description)
	assert.Equal(t, "route infra questions here", b.Steering)
	assert.Equal(t, []string{"infra", "deploys"}, b.Topics)
	assert.Equal(t, "kb/kb_01", b.GitPath)
	assert.Equal(t, "main", b.DefaultBranch)
	assert.Equal(t, "2026-07-27T10:00:00Z", b.LastActivityAt)
	assert.Equal(t, "usr_mgr", b.Manager)
}

// TestListBubbles_LegacyBubblesEnvelope_KBIDAlias verifies the older
// {"bubbles": [...]} envelope with "kb_id" keys still decodes — the alias
// keeps transitional server responses and committed fixtures working.
// Failure prevented: a decoder cleanup dropping the alias would break
// against any endpoint still serving the transitional shape.
func TestListBubbles_LegacyBubblesEnvelope_KBIDAlias(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"bubbles": [
				{
					"kb_id": "kb_01",
					"kb_type": "personal",
					"slug": "personal-abc",
					"name": "Personal",
					"owner_user_id": "user_01",
					"lifecycle_state": "active",
					"viewer_role": "owner",
					"repo_url": "https://example.invalid/personal.git"
				},
				{
					"kb_id": "kb_02",
					"kb_type": "team",
					"slug": "platform",
					"name": "Platform",
					"viewer_role": "member"
				}
			]
		}`))
	}))
	defer srv.Close()

	client := newTestKBClient(srv.URL)
	bubbles, err := client.ListBubbles(context.Background(), testScope())

	require.NoError(t, err)
	require.Len(t, bubbles, 2)

	assert.Equal(t, "kb_01", bubbles[0].KBID, `legacy "kb_id" key must decode via the alias`)
	assert.Equal(t, KBTypePersonal, bubbles[0].KBType)
	assert.Equal(t, "personal-abc", bubbles[0].Slug)
	assert.Equal(t, "owner", bubbles[0].ViewerRole)
	assert.Equal(t, "https://example.invalid/personal.git", bubbles[0].RepoURL)

	assert.Equal(t, "kb_02", bubbles[1].KBID)
	assert.Equal(t, KBTypeTeam, bubbles[1].KBType)
}

// TestListBubbles_KBsEnvelopePreferredOverBubbles verifies the envelope
// preference order: when both keys are present, "kbs" wins.
// Failure prevented: a server sending both keys during a migration window
// causing the CLI to read the stale array.
func TestListBubbles_KBsEnvelopePreferredOverBubbles(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"kbs":     [{"id": "kb_current", "kb_type": "team", "slug": "current"}],
			"bubbles": [{"kb_id": "kb_stale", "kb_type": "team", "slug": "stale"}]
		}`))
	}))
	defer srv.Close()

	client := newTestKBClient(srv.URL)
	bubbles, err := client.ListBubbles(context.Background(), testScope())

	require.NoError(t, err)
	require.Len(t, bubbles, 1)
	assert.Equal(t, "kb_current", bubbles[0].KBID)
}

// TestListBubbles_Success_BareArray verifies the client also accepts a bare
// JSON array as the response body.
// Failure prevented: a server simplification (returning [] instead of
// an envelope) breaking the CLI silently.
func TestListBubbles_Success_BareArray(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[
			{"kb_id":"kb_x","kb_type":"team","slug":"team-x","name":"Team X"}
		]`))
	}))
	defer srv.Close()

	client := newTestKBClient(srv.URL)
	bubbles, err := client.ListBubbles(context.Background(), testScope())

	require.NoError(t, err)
	require.Len(t, bubbles, 1)
	assert.Equal(t, "kb_x", bubbles[0].KBID)
	assert.Equal(t, KBTypeTeam, bubbles[0].KBType)
}

// TestListBubbles_UnknownKBType_NormalizedToUnknown verifies a future server
// kb_type value gets bucketed into KBTypeUnknown rather than crashing or
// being dropped — required for forward compatibility.
// Failure prevented: a server-side rollout of a new kb_type breaking
// every CLI client until they upgrade.
func TestListBubbles_UnknownKBType_NormalizedToUnknown(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"kbs":[
			{"id":"kb_y","kb_type":"future-shape","slug":"future","name":"Future"}
		]}`))
	}))
	defer srv.Close()

	client := newTestKBClient(srv.URL)
	bubbles, err := client.ListBubbles(context.Background(), testScope())

	require.NoError(t, err)
	require.Len(t, bubbles, 1)
	assert.Equal(t, KBTypeUnknown, bubbles[0].KBType, "unknown kb_type must collapse to KBTypeUnknown")
	assert.Equal(t, "kb_y", bubbles[0].KBID, "row must still surface even with unknown type")
}

// TestListBubbles_ChannelType_IsTypedConst verifies kb_type=channel decodes
// as the promoted KBTypeChannel const, not the unknown bucket.
// Failure prevented: channel bubbles regressing to "unknown" and losing
// their type-specific hint/sort slot after KBTypeChannel was promoted.
func TestListBubbles_ChannelType_IsTypedConst(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"kbs":[
			{"id":"kb_ch","kb_type":"channel","slug":"wip"}
		]}`))
	}))
	defer srv.Close()

	client := newTestKBClient(srv.URL)
	bubbles, err := client.ListBubbles(context.Background(), testScope())

	require.NoError(t, err)
	require.Len(t, bubbles, 1)
	assert.Equal(t, KBTypeChannel, bubbles[0].KBType)
}

// --- ResolveSlug ---

// TestResolveSlug_HappyPath verifies the resolve endpoint call: URL path,
// scope + slug query params, and {"kb_id": ...} decode.
// Failure prevented: a wiring break in the scoped slug → kb_id path that
// `ox kb describe <#slug>` depends on.
func TestResolveSlug_HappyPath(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/v1/kb/resolve", r.URL.Path)
		assert.Equal(t, "team", r.URL.Query().Get("scope_type"))
		assert.Equal(t, "team_test", r.URL.Query().Get("scope_id"))
		assert.Equal(t, "platform", r.URL.Query().Get("slug"))

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"kb_id":"kb_resolved"}`))
	}))
	defer srv.Close()

	client := newTestKBClient(srv.URL)
	kbID, err := client.ResolveSlug(context.Background(), testScope(), "platform")

	require.NoError(t, err)
	assert.Equal(t, "kb_resolved", kbID)
}

// TestResolveSlug_404_IsKBAPIUnavailable verifies not-found (which the
// server deliberately makes indistinguishable from no-access so slugs
// cannot be enumerated) surfaces as the non-fatal sentinel.
// Failure prevented: a mistyped slug producing a raw HTTP dump instead of
// the dedicated "no knowledge bubble matching ..." copy downstream.
func TestResolveSlug_404_IsKBAPIUnavailable(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := newTestKBClient(srv.URL)
	kbID, err := client.ResolveSlug(context.Background(), testScope(), "missing")

	require.Error(t, err)
	assert.Empty(t, kbID)
	assert.True(t, errors.Is(err, ErrKBAPIUnavailable),
		"404 must wrap ErrKBAPIUnavailable so callers can errors.Is it; got %v", err)
}

// TestResolveSlug_MissingInputs_NoHTTPCall verifies the client rejects
// empty scope/slug combinations before issuing a request.
// Failure prevented: a bare resolve request that the server would 400.
func TestResolveSlug_MissingInputs_NoHTTPCall(t *testing.T) {
	t.Parallel()

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"kb_id":"kb_x"}`))
	}))
	defer srv.Close()

	client := newTestKBClient(srv.URL)

	cases := []struct {
		name  string
		scope KBScope
		slug  string
	}{
		{"empty scope", KBScope{}, "platform"},
		{"missing scope id", KBScope{Type: KBScopeTypeTeam}, "platform"},
		{"empty slug", testScope(), ""},
		{"whitespace slug", testScope(), "   "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kbID, err := client.ResolveSlug(context.Background(), tc.scope, tc.slug)
			require.Error(t, err)
			assert.Empty(t, kbID)
		})
	}
	assert.Equal(t, 0, calls, "invalid resolve inputs must never reach the server")
}

// TestResolveSlug_EmptyKBIDInResponse_Rejected verifies a 200 body with an
// empty kb_id is treated as an error rather than a silent empty success.
// Failure prevented: "" propagating into GET /api/v1/kb/ (the list URL)
// and returning a confusing envelope instead of a bubble.
func TestResolveSlug_EmptyKBIDInResponse_Rejected(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"kb_id":""}`))
	}))
	defer srv.Close()

	client := newTestKBClient(srv.URL)
	_, err := client.ResolveSlug(context.Background(), testScope(), "platform")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty kb_id")
}

// --- GetBubble ---

// TestGetBubble_Success verifies single-bubble fetch decodes correctly and
// the URL path is correctly templated with the kb_id, including the new
// detail fields.
// Failure prevented: a path-templating regression sending the request to
// the list endpoint and silently returning the first bubble.
func TestGetBubble_Success(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/kb/kb_42", r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id":"kb_42",
			"kb_type":"profile",
			"slug":"profile-42",
			"name":"Profile",
			"viewer_role":"owner",
			"scope_type":"user",
			"scope_id":"usr_01",
			"topics":["bio"],
			"git_path":"kb/kb_42"
		}`))
	}))
	defer srv.Close()

	client := newTestKBClient(srv.URL)
	bubble, err := client.GetBubble(context.Background(), "kb_42")

	require.NoError(t, err)
	require.NotNil(t, bubble)
	assert.Equal(t, "kb_42", bubble.KBID)
	assert.Equal(t, KBTypeProfile, bubble.KBType)
	assert.Equal(t, "user", bubble.ScopeType)
	assert.Equal(t, "usr_01", bubble.ScopeID)
	assert.Equal(t, []string{"bio"}, bubble.Topics)
	assert.Equal(t, "kb/kb_42", bubble.GitPath)
}

// TestGetBubble_EmptyID_Rejected ensures the client doesn't issue a request
// when the caller passes an empty id, which would resolve to the list URL
// on the server.
// Failure prevented: GetBubble("") accidentally returning the list response
// or a 404 with confusing context.
func TestGetBubble_EmptyID_Rejected(t *testing.T) {
	t.Parallel()

	client := newTestKBClient("http://unused.invalid")
	bubble, err := client.GetBubble(context.Background(), "")

	require.Error(t, err)
	assert.Nil(t, bubble)
	assert.Contains(t, err.Error(), "kb id is required")
}

// --- error mapping ---

// TestListBubbles_404_IsKBAPIUnavailable ensures a 404 is wrapped in the
// non-fatal sentinel so FetchBubbles can treat the scope as empty.
// Failure prevented: a missing kb endpoint failing the whole `ox kb list`
// instead of degrading to an empty scope.
func TestListBubbles_404_IsKBAPIUnavailable(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := newTestKBClient(srv.URL)
	bubbles, err := client.ListBubbles(context.Background(), testScope())

	require.Error(t, err)
	assert.Nil(t, bubbles)
	assert.True(t, errors.Is(err, ErrKBAPIUnavailable),
		"404 must wrap ErrKBAPIUnavailable so callers can errors.Is it; got %v", err)
}

// TestListBubbles_403_IsKBAPIUnavailable mirrors the 404 case for the
// flag-off / non-member-scope path.
// Failure prevented: callers without the knowledge-bubbles flag seeing a
// hard error instead of a graceful empty scope.
func TestListBubbles_403_IsKBAPIUnavailable(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	client := newTestKBClient(srv.URL)
	bubbles, err := client.ListBubbles(context.Background(), testScope())

	require.Error(t, err)
	assert.Nil(t, bubbles)
	assert.True(t, errors.Is(err, ErrKBAPIUnavailable),
		"403 must wrap ErrKBAPIUnavailable; got %v", err)
}

// TestGetBubble_404_IsKBAPIUnavailable verifies the sentinel fires for the
// detail endpoint too — list, resolve, and detail share the same
// flag-gating semantics.
// Failure prevented: divergent error handling between list and detail paths.
func TestGetBubble_404_IsKBAPIUnavailable(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := newTestKBClient(srv.URL)
	bubble, err := client.GetBubble(context.Background(), "kb_missing")

	require.Error(t, err)
	assert.Nil(t, bubble)
	assert.True(t, errors.Is(err, ErrKBAPIUnavailable), "got %v", err)
}

// TestListBubbles_500_IsRegularError verifies that genuine server errors do
// NOT collapse into the non-fatal sentinel — FetchBubbles needs to surface
// them as warnings, not silently drop them.
// Failure prevented: a real outage being silently swallowed because every
// error looks like "flag off, no rows".
func TestListBubbles_500_IsRegularError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()

	client := newTestKBClient(srv.URL)
	bubbles, err := client.ListBubbles(context.Background(), testScope())

	require.Error(t, err)
	assert.Nil(t, bubbles)
	assert.False(t, errors.Is(err, ErrKBAPIUnavailable),
		"5xx must NOT be the non-fatal sentinel — caller needs to see real failures")
	assert.Contains(t, err.Error(), "500")
}

// TestListBubbles_Unauthorized_MapsToErrUnauthorized verifies 401 is reported
// via the package-shared ErrUnauthorized, matching the rest of internal/api.
// Failure prevented: callers having to special-case kb auth differently
// from every other API.
func TestListBubbles_Unauthorized_MapsToErrUnauthorized(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	client := newTestKBClient(srv.URL)
	bubbles, err := client.ListBubbles(context.Background(), testScope())

	require.Error(t, err)
	assert.Nil(t, bubbles)
	assert.True(t, errors.Is(err, ErrUnauthorized), "got %v", err)
}

// TestListBubbles_MalformedJSON_WrappedError verifies a garbage body produces
// a wrapped decode error rather than panicking.
// Failure prevented: a misconfigured proxy returning HTML 200 crashing the
// CLI instead of producing a debuggable error.
func TestListBubbles_MalformedJSON_WrappedError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<html>not json</html>`))
	}))
	defer srv.Close()

	client := newTestKBClient(srv.URL)
	bubbles, err := client.ListBubbles(context.Background(), testScope())

	require.Error(t, err)
	assert.Nil(t, bubbles)
	assert.Contains(t, strings.ToLower(err.Error()), "decode")
}

// TestListBubbles_ContextCancellation_Respected verifies the request honors
// context cancellation rather than blocking on a slow server.
// Failure prevented: a hung kb endpoint stalling `ox kb list` past any
// reasonable timeout because the client ignored the caller's context.
func TestListBubbles_ContextCancellation_Respected(t *testing.T) {
	t.Parallel()

	// server that never responds within the cancellation window
	blocked := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blocked
	}))
	defer srv.Close()
	defer close(blocked)

	client := newTestKBClient(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	bubbles, err := client.ListBubbles(ctx, testScope())

	require.Error(t, err)
	assert.Nil(t, bubbles)
	// the underlying error must be context-derived; check both common forms
	assert.True(t,
		errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "context"),
		"expected context cancellation in error, got %v", err)
}
