package main

import (
	"log/slog"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/session"
)

// sessionSignalWait bounds how long the CLI waits for a fire-and-forget
// session lifecycle signal before moving on. The goroutine would not
// survive process exit, so a short bounded wait is what makes "fire and
// forget" actually deliver in a short-lived CLI — while capping the
// latency added to prime/start/abort. The uploaded notification at stop
// remains the authoritative signal; losing a started/aborted signal only
// degrades the /c/ pending-page UX.
const sessionSignalWait = 750 * time.Millisecond

// notifySessionStartedAsync registers a just-started recording with the
// server so its /c/<session_id> conversation link resolves to an
// "in progress" page from t=0. Fire-and-forget per the IPC architecture:
// never blocks the start path beyond sessionSignalWait, all failures are
// silent (debug log only), and it no-ops for recordings without a
// start-minted ID or without the session-attribution toggle enabled.
func notifySessionStartedAsync(projectRoot string, state *session.RecordingState) {
	if state == nil || state.SessionID == "" {
		return
	}
	attr := loadResolvedAttribution()
	if attr.Session == "" {
		return
	}
	runSessionSignal("started", func(client *api.RepoClient, repoID string) error {
		return client.NotifySessionStarted(api.SessionStartedNotification{
			SessionID:   state.SessionID,
			RepoID:      repoID,
			SessionName: session.GetSessionName(state.SessionPath),
			AgentID:     state.AgentID,
			Branch:      state.Branch,
			StartedAt:   state.StartedAt.Format(time.RFC3339),
		})
	}, projectRoot)
}

// notifySessionAbortedAsync flips a registered recording to "discarded" so
// its /c/ page stops claiming "in progress" and the server drops pending
// PR-link repair tasks. Same fire-and-forget contract as started.
func notifySessionAbortedAsync(projectRoot, sessionID string) {
	if sessionID == "" {
		return
	}
	runSessionSignal("aborted", func(client *api.RepoClient, repoID string) error {
		return client.NotifySessionAborted(api.SessionAbortedNotification{
			SessionID: sessionID,
			RepoID:    repoID,
		})
	}, projectRoot)
}

// runSessionSignal resolves config/auth and runs send in a goroutine,
// waiting at most sessionSignalWait. Missing config or credentials are a
// silent no-op — these signals are never load-bearing.
func runSessionSignal(label string, send func(client *api.RepoClient, repoID string) error, projectRoot string) {
	cfg, err := config.LoadProjectConfig(projectRoot)
	if err != nil || cfg == nil || cfg.RepoID == "" {
		return
	}
	ep := endpoint.GetForProject(projectRoot)
	token, err := auth.GetTokenForEndpoint(ep)
	if err != nil || token == nil || token.AccessToken == "" {
		return
	}
	client := api.NewRepoClientWithEndpoint(ep).WithAuthToken(token.AccessToken)

	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := send(client, cfg.RepoID); err != nil {
			slog.Debug("session signal failed", "signal", label, "error", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(sessionSignalWait):
		slog.Debug("session signal timed out", "signal", label, "wait_ms", sessionSignalWait.Milliseconds())
	}
}
