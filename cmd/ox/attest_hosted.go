package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/repotools"
	"github.com/sageox/ox/internal/useragent"
	"github.com/spf13/cobra"
)

const hostedAttestReadLimit = 8 << 20

var (
	hostedAttestRequest     = auth.AuthenticatedRequest
	hostedAttestOpenBrowser = cli.OpenInBrowser
)

type hostedAttestReadModel struct {
	State         string            `json:"state"`
	PublicationID string            `json:"publicationId"`
	PublishedAt   string            `json:"publishedAt,omitempty"`
	Report        json.RawMessage   `json:"report,omitempty"`
	Proofs        []json.RawMessage `json:"proofs,omitempty"`
	Runs          []json.RawMessage `json:"runs,omitempty"`
	Message       string            `json:"message,omitempty"`
}

type hostedAttestGrant struct {
	BaseURL   string `json:"base_url"`
	Bootstrap struct {
		URL    string `json:"url"`
		Method string `json:"method"`
		Token  string `json:"token"`
	} `json:"bootstrap"`
	Manifest struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	} `json:"manifest"`
}

type hostedAttestManifest struct {
	PublicationID string                     `json:"publicationId"`
	Files         []hostedAttestManifestFile `json:"files"`
}

type hostedAttestManifestFile struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type hostedAttestFailures struct {
	Version       int             `json:"version"`
	PublicationID string          `json:"publicationId"`
	Failures      json.RawMessage `json:"failures"`
	RunFailures   json.RawMessage `json:"runLevelFailures"`
}

var attestViewCmd = &cobra.Command{
	Use:   "view <attpub_id> | --latest",
	Short: "View a hosted Attest publication",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runAttestView,
}

var attestFailuresCmd = &cobra.Command{
	Use:   "failures <attpub_id> | --latest",
	Short: "Read the verified failure index from a hosted publication",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runAttestFailures,
}

func runAttestView(cmd *cobra.Command, args []string) error {
	root, publicationID, latest, err := hostedAttestSelector(cmd, args)
	if err != nil {
		return err
	}
	model, err := readHostedAttest(cmd.Context(), root, publicationID, latest)
	if err != nil {
		return err
	}
	if model.PublicationID == "" {
		return fmt.Errorf("hosted Attest publication is %s: %s", model.State, model.Message)
	}

	web, _ := cmd.Flags().GetBool("web")
	jsonOut, agentID, err := hostedAttestOutputMode(cmd, true)
	if err != nil {
		return err
	}
	if web {
		webURL := endpoint.GetForProject(root) + "/attest/" + url.PathEscape(model.PublicationID)
		if err := hostedAttestOpenBrowser(webURL); err != nil {
			if errors.Is(err, cli.ErrHeadless) {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "View Attest at: %s\n", webURL)
				return nil
			}
			return fmt.Errorf("open Attest in browser: %w", err)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Opening %s\n", webURL)
		return nil
	}

	human := fmt.Sprintf("\nAttest  %s\n  published  %s\n  proofs     %d\n  runs       %d\n\n",
		model.PublicationID, model.PublishedAt, len(model.Proofs), len(model.Runs))
	return emit(cmd, jsonOut, agentID, model, human, "attest view")
}

func runAttestFailures(cmd *cobra.Command, args []string) error {
	root, publicationID, latest, err := hostedAttestSelector(cmd, args)
	if err != nil {
		return err
	}
	model, err := readHostedAttest(cmd.Context(), root, publicationID, latest)
	if err != nil {
		return err
	}
	if model.PublicationID == "" {
		return fmt.Errorf("hosted Attest publication is %s: %s", model.State, model.Message)
	}

	failures, err := readHostedAttestFailures(cmd.Context(), root, model.PublicationID)
	if err != nil {
		return err
	}
	jsonOut, agentID, err := hostedAttestOutputMode(cmd, false)
	if err != nil {
		return err
	}
	human := fmt.Sprintf("\nAttest failures  %s\n  scenario failures  %d\n  run failures       %d\n\n",
		model.PublicationID, rawJSONArrayLength(failures.Failures), rawJSONArrayLength(failures.RunFailures))
	return emit(cmd, jsonOut, agentID, failures, human, "attest failures")
}

func hostedAttestSelector(cmd *cobra.Command, args []string) (root, publicationID string, latest bool, err error) {
	latest, _ = cmd.Flags().GetBool("latest")
	if (len(args) == 1) == latest {
		return "", "", false, errors.New("specify exactly one <attpub_id> or --latest")
	}
	root, err = repotools.FindRepoRoot(repotools.VCSGit)
	if err != nil {
		return "", "", false, errors.New("not in a git repository")
	}
	if len(args) == 1 {
		publicationID = args[0]
		if err := config.NewValidator().ValidateID(publicationID, "attpub"); err != nil {
			return "", "", false, fmt.Errorf("invalid Attest publication ID: %w", err)
		}
	}
	return root, publicationID, latest, nil
}

func readHostedAttest(ctx context.Context, root, publicationID string, latest bool) (*hostedAttestReadModel, error) {
	apiURL := endpoint.GetForProject(root) + "/api/v1/attest/" + url.PathEscape(publicationID)
	if latest {
		repoID := config.GetRepoID(root)
		if repoID == "" {
			return nil, errors.New("project is missing its SageOx repo ID; run 'ox doctor'")
		}
		apiURL = endpoint.GetForProject(root) + "/api/v1/repos/" + url.PathEscape(repoID) + "/attest"
	}
	response, err := hostedAttestRequest(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("read hosted Attest publication: %w", err)
	}
	if !response.Ok() {
		return nil, fmt.Errorf("read hosted Attest publication: API returned %d", response.StatusCode)
	}
	var model hostedAttestReadModel
	if err := decodeAPIData(response.Data, &model); err != nil {
		return nil, fmt.Errorf("decode hosted Attest publication: %w", err)
	}
	return &model, nil
}

func readHostedAttestFailures(ctx context.Context, root, publicationID string) (*hostedAttestFailures, error) {
	grantURL := endpoint.GetForProject(root) + "/api/v1/attest/" + url.PathEscape(publicationID) + "/grants"
	response, err := hostedAttestRequest(ctx, http.MethodPost, grantURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create Attest evidence grant: %w", err)
	}
	if !response.Ok() {
		return nil, fmt.Errorf("create Attest evidence grant: API returned %d", response.StatusCode)
	}
	var grant hostedAttestGrant
	if err := decodeAPIData(response.Data, &grant); err != nil {
		return nil, fmt.Errorf("decode Attest evidence grant: %w", err)
	}
	failures, err := fetchHostedAttestFailures(ctx, &grant)
	if err != nil {
		return nil, err
	}
	if failures.PublicationID != publicationID {
		return nil, errors.New("verified Attest failure index names a different publication")
	}
	return failures, nil
}

func fetchHostedAttestFailures(ctx context.Context, grant *hostedAttestGrant) (*hostedAttestFailures, error) {
	base, bootstrap, err := validateHostedAttestGrant(grant)
	if err != nil {
		return nil, err
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("create ephemeral Attest cookie jar: %w", err)
	}
	client := &http.Client{
		Jar:     jar,
		Timeout: 30 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("attest data-plane redirects are not allowed")
		},
	}
	form := url.Values{"token": {grant.Bootstrap.Token}}
	req, err := useragent.NewRequest(ctx, http.MethodPost, bootstrap.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create Attest grant exchange: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	exchange, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("exchange Attest evidence grant: %w", err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(exchange.Body, 64<<10))
	_ = exchange.Body.Close()
	if exchange.StatusCode < 200 || exchange.StatusCode >= 300 {
		return nil, fmt.Errorf("exchange Attest evidence grant: data plane returned %d", exchange.StatusCode)
	}

	manifestRaw, err := fetchHostedAttestObject(ctx, client, base, grant.Manifest.Path)
	if err != nil {
		return nil, err
	}
	if !digestMatches(manifestRaw, grant.Manifest.SHA256) {
		return nil, errors.New("attest manifest digest does not match the authenticated grant")
	}
	var manifest hostedAttestManifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		return nil, fmt.Errorf("decode verified Attest manifest: %w", err)
	}
	var failureFile *hostedAttestManifestFile
	for i := range manifest.Files {
		if manifest.Files[i].Path == "failures/index.json" {
			failureFile = &manifest.Files[i]
			break
		}
	}
	if failureFile == nil {
		return nil, errors.New("verified Attest manifest has no failures/index.json")
	}
	failureRaw, err := fetchHostedAttestObject(ctx, client, base, failureFile.Path)
	if err != nil {
		return nil, err
	}
	if failureFile.Bytes > 0 && int64(len(failureRaw)) != failureFile.Bytes {
		return nil, errors.New("attest failure index size does not match the verified manifest")
	}
	if !digestMatches(failureRaw, failureFile.SHA256) {
		return nil, errors.New("attest failure index digest does not match the verified manifest")
	}
	var failures hostedAttestFailures
	if err := json.Unmarshal(failureRaw, &failures); err != nil {
		return nil, fmt.Errorf("decode verified Attest failure index: %w", err)
	}
	return &failures, nil
}

func validateHostedAttestGrant(grant *hostedAttestGrant) (*url.URL, *url.URL, error) {
	if grant == nil || grant.BaseURL == "" || grant.Bootstrap.URL == "" || grant.Bootstrap.Token == "" {
		return nil, nil, errors.New("attest evidence grant is incomplete")
	}
	if grant.Bootstrap.Method != http.MethodPost || grant.Manifest.Path != "manifest.json" {
		return nil, nil, errors.New("attest evidence grant uses an unsupported protocol")
	}
	base, err := url.Parse(grant.BaseURL)
	if err != nil || base.Host == "" || base.User != nil || !strings.HasSuffix(base.Path, "/") || base.RawQuery != "" || base.Fragment != "" {
		return nil, nil, errors.New("attest evidence grant has an invalid base URL")
	}
	bootstrap, err := url.Parse(grant.Bootstrap.URL)
	if err != nil || bootstrap.Host == "" || bootstrap.User != nil || bootstrap.Path != "/__grant" || bootstrap.RawQuery != "" || bootstrap.Fragment != "" {
		return nil, nil, errors.New("attest evidence grant has an invalid exchange URL")
	}
	if !safeAttestScheme(base) || base.Scheme != bootstrap.Scheme || base.Host != bootstrap.Host {
		return nil, nil, errors.New("attest evidence grant crosses an untrusted origin")
	}
	return base, bootstrap, nil
}

func safeAttestScheme(target *url.URL) bool {
	if target.Scheme == "https" {
		return true
	}
	host := target.Hostname()
	return target.Scheme == "http" && (host == "localhost" || host == "127.0.0.1" || host == "::1")
}

func fetchHostedAttestObject(ctx context.Context, client *http.Client, base *url.URL, relativePath string) ([]byte, error) {
	if relativePath == "" || strings.HasPrefix(relativePath, "/") || strings.Contains(relativePath, "..") {
		return nil, errors.New("attest manifest contains an unsafe object path")
	}
	target := base.ResolveReference(&url.URL{Path: relativePath})
	if target.Scheme != base.Scheme || target.Host != base.Host || !strings.HasPrefix(target.Path, base.Path) {
		return nil, errors.New("attest object escaped its publication boundary")
	}
	req, err := useragent.NewRequest(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create Attest evidence request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("read Attest evidence: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("read Attest evidence: data plane returned %d", resp.StatusCode)
	}
	// Discovery files are intentionally compact. Bounding a single read to 8 MiB
	// prevents a corrupt origin from turning progressive disclosure into a dump.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, hostedAttestReadLimit+1))
	if err != nil {
		return nil, fmt.Errorf("read Attest evidence body: %w", err)
	}
	if len(raw) > hostedAttestReadLimit {
		return nil, errors.New("attest discovery object exceeds 8 MiB")
	}
	return raw, nil
}

func digestMatches(raw []byte, expected string) bool {
	expected = strings.TrimPrefix(strings.ToLower(expected), "sha256:")
	sum := sha256.Sum256(raw)
	return expected != "" && hex.EncodeToString(sum[:]) == expected
}

func decodeAPIData(data any, target any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

func rawJSONArrayLength(raw json.RawMessage) int {
	var values []json.RawMessage
	_ = json.Unmarshal(raw, &values)
	return len(values)
}

func boolCount(values ...bool) int {
	count := 0
	for _, value := range values {
		if value {
			count++
		}
	}
	return count
}

func hostedAttestOutputMode(cmd *cobra.Command, allowWeb bool) (bool, string, error) {
	jsonFlag, _ := cmd.Flags().GetBool("json")
	textFlag, _ := cmd.Flags().GetBool("text")
	webFlag := false
	if allowWeb {
		webFlag, _ = cmd.Flags().GetBool("web")
	}
	if boolCount(jsonFlag, textFlag, webFlag) > 1 {
		return false, "", errors.New("specify only one of --web, --json, or --text")
	}
	jsonOut, agentID := wantJSON(cmd)
	// Explicit human modes override AI-coworker detection; otherwise a detected
	// caller could not ask for terminal text without also appearing to ask for JSON.
	if textFlag || webFlag {
		jsonOut = false
	}
	return jsonOut, agentID, nil
}

func init() {
	for _, command := range []*cobra.Command{attestViewCmd, attestFailuresCmd} {
		command.Flags().Bool("latest", false, "select the newest complete hosted publication")
		command.Flags().Bool("json", false, "emit structured JSON")
		command.Flags().Bool("text", false, "render a compact terminal summary")
	}
	attestViewCmd.Flags().Bool("web", false, "open the stable SageOx Attest page")
	attestCmd.AddCommand(attestViewCmd, attestFailuresCmd)
}
