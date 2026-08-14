package plan

import "bytes"

var (
	markdownRendererMarker = []byte(`<meta name="generator" content="ox plan markdown renderer"`)
	legacyRendererBrand    = []byte(`<div class="brand">OX · PLAN</div>`)
	legacyRendererEyebrow  = []byte(`<div class="eyebrow">SageOx · enriched plan</div>`)
)

// ShouldUseStoredHTML decides whether plan.html is authored content rather
// than a disposable projection of plan.md. Old versions accepted --plan and
// --html together without recording PrimaryHTML; preserving a non-generated
// page here prevents review from silently replacing its visual argument with
// the generic markdown renderer. The paired legacy signatures are deliberately
// specific so arbitrary authored pages that mention SageOx are not demoted.
func ShouldUseStoredHTML(meta Meta, html []byte) bool {
	if len(html) == 0 {
		return false
	}
	if meta.Primary == PrimaryHTML {
		return true
	}
	if meta.Primary != "" {
		return false
	}
	return !IsGeneratedMarkdownRender(html)
}

// IsGeneratedMarkdownRender recognizes both newly stamped markdown renders
// and pages generated before the explicit marker existed.
func IsGeneratedMarkdownRender(html []byte) bool {
	return bytes.Contains(html, markdownRendererMarker) ||
		(bytes.Contains(html, legacyRendererBrand) && bytes.Contains(html, legacyRendererEyebrow))
}
