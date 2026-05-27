package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/useragent"
	"github.com/spf13/cobra"
)

// recsAnalyzePath is the API route that triggers a fresh recommendations analysis
// for the user's primary kb. Mirrors the route registered by api-go.
const recsAnalyzePath = "/api/v1/recs/analyze"

// recsWebPath is the inbox page (Track E) opened in the browser.
const recsWebPath = "/recs"

var recommendAnalyze bool

var recommendCmd = &cobra.Command{
	Use:   "recommend",
	Short: "Open SageOx skill recommendations in your browser",
	Long: `Open SageOx skill recommendations in your browser.

By default, opens the recommendations inbox at /recs in the user's
default browser. Pass --analyze to first trigger a fresh analysis run
for your primary team's knowledge base; the dashboard then renders
results as they complete (~30s).

Recommendations are gated by a server-side rollout flag — if your
account doesn't have access yet, the CLI prints an informational
message and exits 0.`,
	RunE: runRecommend,
}

func init() {
	recommendCmd.Flags().BoolVar(&recommendAnalyze, "analyze", false, "trigger fresh analysis if the last run is >24h old")
	rootCmd.AddCommand(recommendCmd)
}

func runRecommend(cmd *cobra.Command, _ []string) error {
	// Resolve endpoint via existing project-aware helper. findProjectRoot returns
	// "" for non-project cwds; GetForProject handles that and falls back to env / default.
	projectRoot, _ := findProjectRoot()
	ep := endpoint.GetForProject(projectRoot)

	if recommendAnalyze {
		// best-effort trigger — failures (403/404, no auth, no team) must not block the
		// browser open. The dashboard itself surfaces analysis state.
		triggerAnalyze(cmd.Context(), cmd, projectRoot, ep)
	}

	url := strings.TrimSuffix(ep, "/") + recsWebPath
	if err := cli.OpenInBrowser(url); err != nil {
		// headless / browser launch failure: print URL so the user can open manually
		fmt.Fprintf(cmd.ErrOrStderr(), "Could not open browser: %v\nOpen this URL manually: %s\n", err, url)
		return err
	}
	return nil
}

// triggerAnalyze posts to /api/v1/recs/analyze for the user's primary team kb.
// Returns no error: any failure is reported to stderr and we continue to open
// the dashboard (which renders an empty/loading state if needed).
//
// Status mapping:
//   - 2xx    : print short progress hint
//   - 403/404: rollout flag off → informational message, exit 0
//   - other  : short stderr error (still proceeds, exit 0 — the dashboard is the source of truth)
//
// Note: server is the source of truth for the >24h freshness check (it can dedupe
// across CLI invocations and clients). The CLI unconditionally requests; the API
// no-ops or returns the existing run_id when a recent analysis exists.
func triggerAnalyze(ctx context.Context, cmd *cobra.Command, projectRoot, ep string) {
	stderr := cmd.ErrOrStderr()

	token, err := auth.GetTokenForEndpoint(ep)
	if err != nil || token == nil || token.AccessToken == "" {
		fmt.Fprintln(stderr, "Not signed in — skipping analysis. Run 'ox login' to authenticate, then retry.")
		return
	}

	// Resolve primary team — the rec API treats team_id as the kb_id (one kb per team today).
	// Fall back to the first available team if no primary is marked.
	var kbID string
	if projectRoot != "" {
		for _, t := range discoverAllTeams(projectRoot) {
			if t.Primary {
				kbID = t.TeamID
				break
			}
		}
	}
	if kbID == "" {
		// no project context, or project has no primary — pick the first global team
		teams := discoverTeamsGlobal()
		if len(teams) > 0 {
			kbID = teams[0].TeamID
		}
	}
	if kbID == "" {
		fmt.Fprintln(stderr, "No team found — skipping analysis. Run 'ox login' and 'ox init' first.")
		return
	}

	body, _ := json.Marshal(map[string]string{"kb_id": kbID})
	reqURL := strings.TrimSuffix(ep, "/") + recsAnalyzePath

	// 15s is enough for the API to enqueue the workflow (it returns immediately with
	// workflow_run_id); the actual analysis runs async server-side.
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := useragent.NewRequest(reqCtx, http.MethodPost, reqURL, bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(stderr, "Skipping analysis: %v\n", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)

	httpClient := &http.Client{Timeout: 15 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "Could not reach SageOx to start analysis (%v). Opening dashboard anyway.\n", err)
		return
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusForbidden, resp.StatusCode == http.StatusNotFound:
		// rollout flag off for this user; informational, not an error
		fmt.Fprintln(stderr, "Recommendations aren't enabled for your account yet. Reach out to your SageOx admin if you'd like early access.")
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		fmt.Fprintln(stderr, "Analyzing your recent sessions… this takes ~30s. Open the dashboard while you wait.")
	default:
		fmt.Fprintf(stderr, "Analysis request failed (HTTP %d). Opening dashboard anyway.\n", resp.StatusCode)
	}
}
