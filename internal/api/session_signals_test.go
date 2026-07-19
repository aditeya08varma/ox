package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Session lifecycle signals (started / uploaded / aborted) ---

// TestNotifySessionUploaded_ParsesPRLinkMisses verifies the uploaded notify
// returns the server's repair tasks so the stop path can hand them to the
// agent.
// Failure prevented: server detects a PR missing its SageOx-Session trailer
// but the repair task is silently dropped client-side.
func TestNotifySessionUploaded_ParsesPRLinkMisses(t *testing.T) {
	var gotBody SessionUploadedNotification
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/sessions/ses_x/uploaded", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pr_link_misses":[{"pr_url":"https://github.com/o/r/pull/7","expected_line":"SageOx-Session: https://sageox.ai/c/ses_x"}]}`))
	}))
	defer srv.Close()

	client := NewRepoClientWithEndpoint(srv.URL)
	misses, err := client.NotifySessionUploaded(SessionUploadedNotification{
		SessionID:   "ses_x",
		RepoID:      "repo_01abc",
		SessionName: "2026-01-01T00-00-user-OxA1b2",
	})
	require.NoError(t, err)
	require.Len(t, misses, 1)
	assert.Equal(t, "https://github.com/o/r/pull/7", misses[0].PRURL)
	assert.Contains(t, misses[0].ExpectedLine, "SageOx-Session:")
	// server binds name↔id from the payload without waiting for ledger ingest
	assert.Equal(t, "2026-01-01T00-00-user-OxA1b2", gotBody.SessionName)
}

// TestNotifySessionUploaded_OldServerGracefulDegradation verifies 404
// (endpoint not deployed) and an empty/non-JSON body are treated as accepted
// with zero misses.
// Failure prevented: retry thrash against old servers, or a nil-parse crash
// on an empty 200 body.
func TestNotifySessionUploaded_OldServerGracefulDegradation(t *testing.T) {
	t.Run("404 endpoint not deployed", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}))
		defer srv.Close()
		misses, err := NewRepoClientWithEndpoint(srv.URL).NotifySessionUploaded(SessionUploadedNotification{SessionID: "ses_x"})
		assert.NoError(t, err)
		assert.Empty(t, misses)
	})
	t.Run("empty 200 body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()
		misses, err := NewRepoClientWithEndpoint(srv.URL).NotifySessionUploaded(SessionUploadedNotification{SessionID: "ses_x"})
		assert.NoError(t, err)
		assert.Empty(t, misses)
	})
	t.Run("5xx is a retryable error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		_, err := NewRepoClientWithEndpoint(srv.URL).NotifySessionUploaded(SessionUploadedNotification{SessionID: "ses_x"})
		assert.Error(t, err, "5xx must surface so doctor can retry (notify_failed)")
	})
}

// TestNotifySessionStartedAndAborted verifies the register-at-start and
// aborted signals hit their routes and degrade gracefully on old servers.
// Failure prevented: /c/ pending pages never registering, or an aborted
// session stuck showing "in progress".
func TestNotifySessionStartedAndAborted(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewRepoClientWithEndpoint(srv.URL)
	require.NoError(t, client.NotifySessionStarted(SessionStartedNotification{SessionID: "ses_x", RepoID: "repo_01abc", Branch: "main"}))
	require.NoError(t, client.NotifySessionAborted(SessionAbortedNotification{SessionID: "ses_x"}))
	assert.Equal(t, []string{"/api/v1/sessions/ses_x/started", "/api/v1/sessions/ses_x/aborted"}, paths)

	notFound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer notFound.Close()
	old := NewRepoClientWithEndpoint(notFound.URL)
	assert.NoError(t, old.NotifySessionStarted(SessionStartedNotification{SessionID: "ses_x"}), "old server 404 = accepted")
	assert.NoError(t, old.NotifySessionAborted(SessionAbortedNotification{SessionID: "ses_x"}), "old server 404 = accepted")
}
