package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseQueryArgs_PositionalQuery(t *testing.T) {
	t.Parallel()
	qa, err := parseQueryArgs([]string{"architecture decisions"})
	require.NoError(t, err)
	assert.Equal(t, "architecture decisions", qa.query)
	assert.Equal(t, "hybrid", qa.mode)
	assert.Equal(t, 5, qa.limit)
}

func TestParseQueryArgs_ModeSeparate(t *testing.T) {
	t.Parallel()
	qa, err := parseQueryArgs([]string{"--mode", "knn", "search"})
	require.NoError(t, err)
	assert.Equal(t, "knn", qa.mode)
	assert.Equal(t, "search", qa.query)
}

func TestParseQueryArgs_ModeEquals(t *testing.T) {
	t.Parallel()
	qa, err := parseQueryArgs([]string{"--mode=bm25", "search"})
	require.NoError(t, err)
	assert.Equal(t, "bm25", qa.mode)
}

func TestParseQueryArgs_LimitSeparate(t *testing.T) {
	t.Parallel()
	qa, err := parseQueryArgs([]string{"--limit", "3", "search"})
	require.NoError(t, err)
	assert.Equal(t, 3, qa.limit)
}

func TestParseQueryArgs_LimitEquals(t *testing.T) {
	t.Parallel()
	qa, err := parseQueryArgs([]string{"--limit=10", "search"})
	require.NoError(t, err)
	assert.Equal(t, 10, qa.limit)
}

func TestParseQueryArgs_TeamAndRepo(t *testing.T) {
	t.Parallel()
	qa, err := parseQueryArgs([]string{"--team", "t1", "--repo", "r1", "q"})
	require.NoError(t, err)
	assert.Equal(t, "t1", qa.teamID)
	assert.Equal(t, "r1", qa.repoID)
	assert.Equal(t, "q", qa.query)
}

func TestParseQueryArgs_TeamAndRepoEquals(t *testing.T) {
	t.Parallel()
	qa, err := parseQueryArgs([]string{"--team=t1", "--repo=r1", "q"})
	require.NoError(t, err)
	assert.Equal(t, "t1", qa.teamID)
	assert.Equal(t, "r1", qa.repoID)
}

func TestParseQueryArgs_AllFlags(t *testing.T) {
	t.Parallel()
	qa, err := parseQueryArgs([]string{"--mode", "bm25", "--limit", "20", "--team", "team-abc", "--repo", "repo-xyz", "how do we deploy"})
	require.NoError(t, err)
	assert.Equal(t, "how do we deploy", qa.query)
	assert.Equal(t, "bm25", qa.mode)
	assert.Equal(t, 20, qa.limit)
	assert.Equal(t, "team-abc", qa.teamID)
	assert.Equal(t, "repo-xyz", qa.repoID)
}

func TestParseQueryArgs_InvalidLimitSeparate(t *testing.T) {
	t.Parallel()
	_, err := parseQueryArgs([]string{"--limit", "abc", "search"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --limit")
	assert.Contains(t, err.Error(), "abc")
}

func TestParseQueryArgs_InvalidLimitEquals(t *testing.T) {
	t.Parallel()
	_, err := parseQueryArgs([]string{"--limit=xyz", "search"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --limit")
	assert.Contains(t, err.Error(), "xyz")
}

func TestParseQueryArgs_InvalidMode(t *testing.T) {
	t.Parallel()
	_, err := parseQueryArgs([]string{"--mode", "semantic", "search"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid mode")
	assert.Contains(t, err.Error(), "semantic")
}

func TestParseQueryArgs_MissingQuery(t *testing.T) {
	t.Parallel()
	_, err := parseQueryArgs([]string{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query text is required")
}

func TestParseQueryArgs_OnlyFlags(t *testing.T) {
	t.Parallel()
	_, err := parseQueryArgs([]string{"--mode", "knn", "--limit", "3"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query text is required")
}

func TestParseQueryArgs_FlagsAfterQuery(t *testing.T) {
	t.Parallel()
	qa, err := parseQueryArgs([]string{"search text", "--limit", "7", "--mode=knn"})
	require.NoError(t, err)
	assert.Equal(t, "search text", qa.query)
	assert.Equal(t, 7, qa.limit)
	assert.Equal(t, "knn", qa.mode)
}

// --k is a hidden friction alias for --limit
func TestParseQueryArgs_KAliasSeparate(t *testing.T) {
	t.Parallel()
	qa, err := parseQueryArgs([]string{"--k", "3", "search"})
	require.NoError(t, err)
	assert.Equal(t, 3, qa.limit)
}

func TestParseQueryArgs_KAliasEquals(t *testing.T) {
	t.Parallel()
	qa, err := parseQueryArgs([]string{"--k=10", "search"})
	require.NoError(t, err)
	assert.Equal(t, 10, qa.limit)
}

func TestParseQueryArgs_KAliasInvalidValue(t *testing.T) {
	t.Parallel()
	_, err := parseQueryArgs([]string{"--k", "abc", "search"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --limit")
}

func TestParseQueryArgs_Source(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		args       []string
		wantSource string
		wantErr    string // substring match; empty means no error
	}{
		{
			name:       "default source is team",
			args:       []string{"search query"},
			wantSource: "team",
		},
		{
			name:       "source team canonical",
			args:       []string{"--source", "team", "search query"},
			wantSource: "team",
		},
		{
			name:       "source team equals syntax",
			args:       []string{"--source=team", "search query"},
			wantSource: "team",
		},
		{
			name:       "source code",
			args:       []string{"--source", "code", "search query"},
			wantSource: "code",
		},
		{
			name:       "source all",
			args:       []string{"--source=all", "search query"},
			wantSource: "all",
		},
		{
			name:       "teamctx alias normalizes to team",
			args:       []string{"--source", "teamctx", "search query"},
			wantSource: "team",
		},
		{
			name:       "teamctx alias equals syntax",
			args:       []string{"--source=teamctx", "search query"},
			wantSource: "team",
		},
		{
			name:    "invalid source errors",
			args:    []string{"--source", "invalid", "search query"},
			wantErr: "invalid source",
		},
		{
			name:       "source team with mode knn",
			args:       []string{"--source=team", "--mode=knn", "search query"},
			wantSource: "team",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			qa, err := parseQueryArgs(tt.args)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantSource, qa.source)
		})
	}
}

func TestParseQueryArgs_SourceWithModeKnn(t *testing.T) {
	t.Parallel()
	qa, err := parseQueryArgs([]string{"--source=team", "--mode=knn", "search query"})
	require.NoError(t, err)
	assert.Equal(t, "team", qa.source)
	assert.Equal(t, "knn", qa.mode)
	assert.Equal(t, "search query", qa.query)
}

func TestParseQueryArgs_LocalFlag(t *testing.T) {
	t.Parallel()
	qa, err := parseQueryArgs([]string{"--local", "cache"})
	require.NoError(t, err)
	assert.Equal(t, "local", qa.source)
	assert.Equal(t, "cache", qa.query)
}

func TestParseQueryArgs_SourceLocal(t *testing.T) {
	t.Parallel()
	qa, err := parseQueryArgs([]string{"--source=local", "cache"})
	require.NoError(t, err)
	assert.Equal(t, "local", qa.source)
}

func TestParseQueryArgs_SourceLocalLedgerAlias(t *testing.T) {
	t.Parallel()
	qa, err := parseQueryArgs([]string{"--source=local-ledger", "cache"})
	require.NoError(t, err)
	assert.Equal(t, "local", qa.source)
}

func TestParseQueryArgs_JSONFlag(t *testing.T) {
	t.Parallel()
	qa, err := parseQueryArgs([]string{"--local", "--json", "cache"})
	require.NoError(t, err)
	assert.True(t, qa.jsonOnly)
	assert.Equal(t, "local", qa.source)
}

// --- queryTeamContext auth: mid-session token refresh (ox-x5h5.4) ---
// Failure prevented: queryTeamContext used to call the raw, non-refreshing
// auth.GetTokenForEndpoint and gave up immediately on a 401 — so `ox query`
// failed with "not authenticated" mid-session even though `ox login`
// succeeded and the access token had simply aged past its ~1h TTL.

// newQueryTestProject sets up an isolated project + auth store pointed at
// apiServer, with the given token saved for that endpoint. Mirrors the
// httptest.NewServer + SAGEOX_ENDPOINT/XDG_CONFIG_HOME harness proven in
// agent_session_manual_publish_test.go.
func newQueryTestProject(t *testing.T, apiServer *httptest.Server, token *auth.StoredToken) (projectRoot string, qa *queryArgs) {
	t.Helper()

	t.Setenv("SAGEOX_ENDPOINT", apiServer.URL)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	projectRoot = createInitializedProjectWithConfig(t, &config.ProjectConfig{
		RepoID:   "test-repo",
		Endpoint: apiServer.URL,
		TeamID:   "test-team",
	})

	require.NoError(t, auth.SaveTokenForEndpoint(apiServer.URL, token))

	qa = &queryArgs{query: "test query", mode: "hybrid", limit: 5, source: "team"}
	return projectRoot, qa
}

// TestQueryTeamContext_NearExpiryTokenRefreshedBeforeQuery proves a token
// expiring within the 300s buffer is proactively refreshed before the query
// fires, so the API request carries a live token instead of a stale one.
func TestQueryTeamContext_NearExpiryTokenRefreshedBeforeQuery(t *testing.T) {
	var tokenEndpointHit, jwtExchangeHit bool
	var queryAuthHeader string

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case auth.TokenEndpoint:
			tokenEndpointHit = true
			require.NoError(t, r.ParseForm())
			assert.Equal(t, "refresh_token", r.Form.Get("grant_type"))
			json.NewEncoder(w).Encode(map[string]interface{}{
				"access_token":  "refreshed-opaque",
				"refresh_token": "refreshed-refresh",
				"token_type":    "Bearer",
				"expires_in":    3600,
			})
		case "/api/v1/cli/auth/token":
			jwtExchangeHit = true
			json.NewEncoder(w).Encode(map[string]interface{}{
				"access_token": "refreshed-jwt",
				"token_type":   "Bearer",
				"expires_in":   900,
			})
		case "/api/v1/query":
			queryAuthHeader = r.Header.Get("Authorization")
			json.NewEncoder(w).Encode(api.QueryResponse{Results: []api.QueryResult{{Text: "ok"}}})
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	// expires in 4 minutes — inside EnsureValidTokenForEndpoint's 300s buffer.
	projectRoot, qa := newQueryTestProject(t, apiServer, &auth.StoredToken{
		AccessToken:  "stale-access",
		RefreshToken: "test-refresh-token",
		ExpiresAt:    time.Now().Add(4 * time.Minute),
		TokenType:    "Bearer",
	})

	resp, err := queryTeamContext(qa, projectRoot, "agent-1", "claude-code")
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.True(t, tokenEndpointHit, "expected proactive refresh to hit the token endpoint")
	assert.True(t, jwtExchangeHit, "expected proactive refresh to complete JWT exchange")
	assert.Equal(t, "Bearer refreshed-jwt", queryAuthHeader, "query must carry the refreshed token, not the stale one")
}

// TestQueryTeamContext_RetriesOnceOn401ThenSucceeds proves a token that looks
// valid locally but is rejected server-side (e.g. revoked) triggers exactly
// one reactive refresh-and-retry, and the retried query succeeds.
func TestQueryTeamContext_RetriesOnceOn401ThenSucceeds(t *testing.T) {
	var queryCallCount int
	var authHeaders []string

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case auth.TokenEndpoint:
			json.NewEncoder(w).Encode(map[string]interface{}{
				"access_token":  "refreshed-opaque",
				"refresh_token": "refreshed-refresh",
				"token_type":    "Bearer",
				"expires_in":    3600,
			})
		case "/api/v1/cli/auth/token":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"access_token": "refreshed-jwt",
				"token_type":   "Bearer",
				"expires_in":   900,
			})
		case "/api/v1/query":
			queryCallCount++
			authHeaders = append(authHeaders, r.Header.Get("Authorization"))
			if queryCallCount == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode(api.QueryResponse{Results: []api.QueryResult{{Text: "ok"}}})
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	// far from expiry — proactive refresh must NOT fire; only the reactive
	// 401 retry path should trigger a refresh.
	projectRoot, qa := newQueryTestProject(t, apiServer, &auth.StoredToken{
		AccessToken:  "revoked-access",
		RefreshToken: "test-refresh-token",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
		TokenType:    "Bearer",
	})

	resp, err := queryTeamContext(qa, projectRoot, "agent-1", "claude-code")
	require.NoError(t, err)
	require.NotNil(t, resp)

	require.Equal(t, 2, queryCallCount, "expected exactly one retry after 401")
	assert.Equal(t, "Bearer revoked-access", authHeaders[0], "first attempt uses the originally stored token")
	assert.Equal(t, "Bearer refreshed-jwt", authHeaders[1], "retry uses the freshly refreshed token")
}

// TestQueryTeamContext_GivesUpIfRetryAlso401s proves a genuinely logged-out
// user (refresh succeeds but the server still rejects the new token, or the
// account is truly revoked) gets the unchanged "not authenticated" error
// after exactly one retry — never a loop.
func TestQueryTeamContext_GivesUpIfRetryAlso401s(t *testing.T) {
	var queryCallCount int

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case auth.TokenEndpoint:
			json.NewEncoder(w).Encode(map[string]interface{}{
				"access_token":  "refreshed-opaque",
				"refresh_token": "refreshed-refresh",
				"token_type":    "Bearer",
				"expires_in":    3600,
			})
		case "/api/v1/cli/auth/token":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"access_token": "refreshed-jwt",
				"token_type":   "Bearer",
				"expires_in":   900,
			})
		case "/api/v1/query":
			queryCallCount++
			w.WriteHeader(http.StatusUnauthorized)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	projectRoot, qa := newQueryTestProject(t, apiServer, &auth.StoredToken{
		AccessToken:  "revoked-access",
		RefreshToken: "test-refresh-token",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
		TokenType:    "Bearer",
	})

	resp, err := queryTeamContext(qa, projectRoot, "agent-1", "claude-code")
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "not authenticated")

	assert.Equal(t, 2, queryCallCount, "expected exactly one retry, not a loop")
}
