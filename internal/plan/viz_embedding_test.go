package plan

import (
	"strings"
	"testing"
)

func TestEditorialSVGPatternsEmbedInBothPlanThemes(t *testing.T) {
	ids := []string{
		"architecture", "flowchart", "data-flow", "layer-stack",
		"sequence-diagram", "state-machine", "timeline", "loop",
	}
	for _, id := range ids {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			p, ok := VizPatternByID(id)
			if !ok {
				t.Fatalf("missing %s", id)
			}
			fragment := planHTMLFence(p.Body)
			if fragment == "" {
				t.Fatal("missing HTML recipe")
			}
			raw := "# Diagram\n\n## Explanation\n\n```html-interactive\n" + fragment + "\n```\n"
			out, err := RenderHTML(Parse(raw), Result{})
			if err != nil {
				t.Fatal(err)
			}
			html := string(out)
			// These literals are the scaffold's own surface tokens — the point is
			// that BOTH theme blocks reach the embedded recipe, not the specific
			// hexes. When assets/scaffold.css moves a token, move it here too;
			// light --panel is dust-50 (the cream panel under the white sheet),
			// not the pure white it was before the design-token pass.
			for _, want := range []string{
				`data-ox-viz="` + id + `"`,
				`:root{`,
				`--panel:#111411`,
				`html[data-theme="light"]`,
				`--panel:#f7f5f2`,
				`var(--panel,`,
			} {
				if !strings.Contains(html, want) {
					t.Errorf("embedded render missing theme/diagram contract %q", want)
				}
			}
			if findings := LintRender(out, Result{}); len(findings) != 0 {
				t.Errorf("embedded catalog recipe should lint cleanly: %+v", findings)
			}
		})
	}
}

func planHTMLFence(body string) string {
	const open = "```html\n"
	start := strings.Index(body, open)
	if start < 0 {
		return ""
	}
	rest := body[start+len(open):]
	end := strings.Index(rest, "\n```")
	if end < 0 {
		return ""
	}
	return rest[:end]
}
