package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const (
	perplexityEndpoint  = "https://api.perplexity.ai/chat/completions"
	perplexityAPIKeyEnv = "PERPLEXITY_API_KEY"

	scoutDefaultModel = "sonar-pro"

	scoutDefaultQuestion = "What companies, products, or open-source projects are working on the technical topics, decisions, and problems described in the context below?"

	// Deliberately avoid the words "meeting"/"transcript"/"discussion" in the
	// search-facing prompt — Sonar uses the user message to generate web search
	// queries, and any framing noun there will hijack the searches away from
	// the actual technical substance. Keep this prompt focused on the *kind*
	// of answer we want; the context itself supplies the search terms.
	scoutSystemPrompt = `You are a research assistant for a software team. Read the context the user provides, identify the specific technical topics, products, libraries, and decisions in it, and search the web for companies, open-source projects, and prior art working on those exact topics.

Focus on accelerators and opportunities — prior art the team could borrow from, patterns to learn from, market signals worth knowing. Cite specifics by name. Be concise: 3 to 5 findings max. Ground every claim in a citation.`
)

// scoutAllowedModels enumerates the Perplexity sonar models we accept.
// Keeping the set tight avoids silent typos that yield a 400 from the API
// with a less obvious error message than a local validation failure.
var scoutAllowedModels = map[string]bool{
	"sonar":               true,
	"sonar-pro":           true,
	"sonar-reasoning-pro": true,
	"sonar-deep-research": true,
}

// scoutDefaultSearchDomains constrains Perplexity's web search to surfaces
// where software-engineering prior art actually lives. Without this, the
// search retrieves keyword-matched marketing pages (e.g. searching for
// "dedicated" returned blockchain node providers) and the model fabricates
// citation numbers around those unrelated URLs.
//
// Tradeoff: vendor docs (temporal.io, aws.amazon.com) and engineering blogs
// are excluded, so some specific official references are missed. The win is
// that what *does* come back is verifiable prior art on GitHub or in the
// open developer commons. Override per-call with --domains.
var scoutDefaultSearchDomains = []string{
	"github.com",
	"news.ycombinator.com",
	"stackoverflow.com",
	"arxiv.org",
}

type scoutArgs struct {
	transcriptPath string
	question       string
	model          string
	domains        []string
	showCitations  bool
	jsonOnly       bool
}

var scoutCmd = &cobra.Command{
	Use:   "scout <transcript-path>",
	Short: "Search the web for prior art related to a meeting",
	Long: `Read a meeting transcript, then ask Perplexity for companies, products, and
projects in the wild that relate to what the team discussed.

Pass "-" as the path to read the transcript from stdin.

Requires the PERPLEXITY_API_KEY environment variable.

Examples:
  ox scout ./meeting.md
  cat transcript.txt | ox scout -
  ox scout ./meeting.md --question "What open-source libraries solve this?"
  ox scout ./meeting.md --model sonar-pro
  ox scout ./meeting.md --json`,
	Args: cobra.ExactArgs(1),
	RunE: runScout,
}

func init() {
	scoutCmd.Flags().String("question", "", "override the default research question")
	scoutCmd.Flags().String("model", scoutDefaultModel, "Perplexity model: sonar, sonar-pro, sonar-reasoning-pro, or sonar-deep-research")
	scoutCmd.Flags().StringSlice("domains", scoutDefaultSearchDomains, "comma-separated search domain whitelist (empty = no filter)")
	// Citations are hidden by default: Perplexity's web-search retrieval frequently
	// returns URLs unrelated to the response text (it keyword-matches generic terms
	// like "dedicated" or "workflow" and lands on marketing pages). The model's
	// inline references — by exact project/product name — are the real signal.
	// Enable explicitly with --citations when you trust the search results.
	scoutCmd.Flags().Bool("citations", false, "show the raw 'Sources' list from Perplexity (often unrelated)")
	scoutCmd.Flags().Bool("json", false, "emit the raw Perplexity API response as JSON")
}

func runScout(cmd *cobra.Command, args []string) error {
	question, _ := cmd.Flags().GetString("question")
	model, _ := cmd.Flags().GetString("model")
	domains, _ := cmd.Flags().GetStringSlice("domains")
	showCitations, _ := cmd.Flags().GetBool("citations")
	jsonOut, _ := cmd.Flags().GetBool("json")

	sa := &scoutArgs{
		transcriptPath: strings.TrimSpace(args[0]),
		question:       strings.TrimSpace(question),
		model:          strings.TrimSpace(model),
		domains:        domains,
		showCitations:  showCitations,
		jsonOnly:       jsonOut,
	}

	if sa.transcriptPath == "" {
		return fmt.Errorf("transcript path is required (use \"-\" for stdin)")
	}
	if !scoutAllowedModels[sa.model] {
		return fmt.Errorf("invalid --model %q: must be sonar, sonar-pro, sonar-reasoning-pro, or sonar-deep-research", sa.model)
	}
	if sa.question == "" {
		sa.question = scoutDefaultQuestion
	}

	return executeScout(sa, os.Stdin, os.Stdout)
}

// executeScout reads the transcript, calls Perplexity, and writes results to out.
// stdin is parameterized so tests can inject content for the "-" path.
func executeScout(sa *scoutArgs, stdin io.Reader, out io.Writer) error {
	apiKey := strings.TrimSpace(os.Getenv(perplexityAPIKeyEnv))
	if apiKey == "" {
		return fmt.Errorf("missing %s environment variable. Get a key at https://www.perplexity.ai/settings/api", perplexityAPIKeyEnv)
	}

	transcript, err := readTranscript(sa.transcriptPath, stdin)
	if err != nil {
		return err
	}
	if strings.TrimSpace(transcript) == "" {
		return fmt.Errorf("transcript is empty")
	}

	userContent := fmt.Sprintf("%s\n\n--- CONTEXT ---\n%s", sa.question, transcript)

	reqBody := perplexityRequest{
		Model: sa.model,
		Messages: []perplexityMessage{
			{Role: "system", Content: scoutSystemPrompt},
			{Role: "user", Content: userContent},
		},
		ReturnCitations:    true,
		SearchDomainFilter: sa.domains,
	}

	resp, raw, err := callPerplexity(apiKey, reqBody)
	if err != nil {
		return err
	}

	if sa.jsonOnly {
		_, err := out.Write(raw)
		return err
	}

	return renderScoutResponse(out, resp, sa.showCitations)
}

func readTranscript(path string, stdin io.Reader) (string, error) {
	if path == "-" {
		b, err := io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return string(b), nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read transcript %q: %w", path, err)
	}
	return string(b), nil
}

type perplexityRequest struct {
	Model              string              `json:"model"`
	Messages           []perplexityMessage `json:"messages"`
	ReturnCitations    bool                `json:"return_citations"`
	SearchDomainFilter []string            `json:"search_domain_filter,omitempty"`
}

type perplexityMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type perplexityResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message perplexityMessage `json:"message"`
	} `json:"choices"`
	Citations []string `json:"citations"`
}

func callPerplexity(apiKey string, body perplexityRequest) (*perplexityResponse, []byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, nil, fmt.Errorf("encode request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, perplexityEndpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	// 60s covers sonar-reasoning's longer search-and-reason cycles; sonar/sonar-pro
	// typically respond in under 15s.
	client := &http.Client{Timeout: 60 * time.Second}
	httpResp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("perplexity request failed: %w", err)
	}
	defer httpResp.Body.Close()

	raw, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("read perplexity response: %w", err)
	}

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		excerpt := string(raw)
		if len(excerpt) > 500 {
			excerpt = excerpt[:500] + "..."
		}
		return nil, raw, fmt.Errorf("perplexity returned %s: %s", httpResp.Status, excerpt)
	}

	var resp perplexityResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, raw, fmt.Errorf("decode perplexity response: %w", err)
	}
	return &resp, raw, nil
}

func renderScoutResponse(out io.Writer, resp *perplexityResponse, showCitations bool) error {
	if len(resp.Choices) == 0 {
		return fmt.Errorf("perplexity returned no choices")
	}

	content := strings.TrimSpace(resp.Choices[0].Message.Content)
	if _, err := fmt.Fprintln(out, content); err != nil {
		return err
	}

	if showCitations && len(resp.Citations) > 0 {
		if _, err := fmt.Fprintln(out, "\nSources:"); err != nil {
			return err
		}
		for i, url := range resp.Citations {
			if _, err := fmt.Fprintf(out, "  [%d] %s\n", i+1, url); err != nil {
				return err
			}
		}
	}
	return nil
}
