package plan

import (
	"strings"
	"testing"
)

// artifactFixture builds a plan with a Mermaid block and a deterministic
// prior-art badge whose source resolves to an absolute SageOx URL — the two
// things artifact mode must handle: drop the Mermaid CDN, but keep the
// enrichment reference as a clickable absolute link.
func artifactFixture() (Input, Result, RenderOptions) {
	in := Input{
		Raw: "# Title\n\n## Architecture\nflow here.\n",
		Sections: []Section{
			{Heading: "", Body: "# Title\n\nPreamble."},
			{
				Heading: "Architecture",
				Body:    "Sequence of calls.\n\n```mermaid\nflowchart TB\n  A[\"a\"] --> B[\"b\"]\n```\n",
				Files:   []string{"internal/plan/render.go"},
			},
		},
	}
	res := Result{
		Annotations: []Annotation{{
			Section:   "Architecture",
			Kind:      BadgeDeterministic,
			Type:      BadgePriorArt,
			Why:       "prior session redesigned this area",
			SourceURL: "2026-05-12-plan",
			RefKind:   "session",
			Files:     []string{"internal/plan/render.go"},
		}},
	}
	opts := RenderOptions{
		Slug: "artifact-test",
		PriorArtURL: func(refKind, ref string) string {
			if refKind == "session" {
				return "https://app.sageox.example/repo/x/sessions/" + ref + "/view"
			}
			return ""
		},
	}
	return in, res, opts
}

const absLink = "https://app.sageox.example/repo/x/sessions/2026-05-12-plan/view"

func TestRenderArtifactIsCSPClean(t *testing.T) {
	in, res, opts := artifactFixture()
	opts.Artifact = true

	out, err := RenderHTMLOpts(in, res, opts)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	html := string(out)

	// No external resource loads — the artifact CSP would block them.
	for _, banned := range []string{"fonts.googleapis.com", "fonts.gstatic.com", "cdn.jsdelivr.net", "EventSource"} {
		if strings.Contains(html, banned) {
			t.Errorf("artifact render must not contain %q", banned)
		}
	}

	// The moat survives: the enrichment reference is a clickable absolute link.
	if !strings.Contains(html, `href="`+absLink+`"`) {
		t.Errorf("artifact render dropped the absolute enrichment link %q", absLink)
	}
	// And the page still credits SageOx (the lint contract).
	if !strings.Contains(html, "enriched by SageOx") {
		t.Error("artifact render lost the SageOx footer credit")
	}

	// LintArtifact agrees the page is publishable.
	if findings := LintArtifact(out); len(findings) != 0 {
		t.Errorf("LintArtifact found issues in a clean artifact render: %+v", findings)
	}
}

func TestRenderDefaultHasExternalsAndLintCatchesThem(t *testing.T) {
	in, res, opts := artifactFixture() // opts.Artifact == false

	out, err := RenderHTMLOpts(in, res, opts)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	html := string(out)

	// The normal render keeps the CDN Mermaid + web fonts + SSE review layer.
	for _, want := range []string{"cdn.jsdelivr.net", "fonts.googleapis.com", "EventSource"} {
		if !strings.Contains(html, want) {
			t.Errorf("default render expected to contain %q", want)
		}
	}

	// LintArtifact must flag the default render as NOT artifact-safe.
	findings := LintArtifact(out)
	if len(findings) == 0 {
		t.Fatal("LintArtifact found nothing in a render that loads external resources")
	}
	rules := map[string]bool{}
	for _, f := range findings {
		rules[f.Rule] = true
	}
	for _, want := range []string{"artifact.external-script", "artifact.external-stylesheet", "artifact.eventsource"} {
		if !rules[want] {
			t.Errorf("LintArtifact missing expected finding %q (got %v)", want, rules)
		}
	}
}

// An <a href="https://…"> navigation link must NOT be flagged — only resource
// loads are CSP-blocked, and the enrichment links rely on navigation.
func TestLintArtifactAllowsAnchorLinks(t *testing.T) {
	page := `<!doctype html><html><body>
	<a href="https://github.com/sageox/x/pull/42">collision PR</a>
	<a class="src-link" href="` + absLink + `">prior art</a>
	<style>body{font-family:Inter,system-ui,sans-serif}</style>
	</body></html>`
	if findings := LintArtifact([]byte(page)); len(findings) != 0 {
		t.Errorf("LintArtifact flagged a page whose only remote refs are <a href> navigation: %+v", findings)
	}
}
