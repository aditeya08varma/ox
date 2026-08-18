package planhero

import (
	"bytes"
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

// assertWellFormedSVG proves the output is well-formed XML with exactly one
// root <svg> element and no stray/injected element anywhere in the
// document — the structural half of the escaping guarantee (the textual
// half — that injected markup shows up as escaped text, not as tags — is
// checked separately by each test that needs it).
func assertWellFormedSVG(t *testing.T, doc []byte) {
	t.Helper()
	dec := xml.NewDecoder(bytes.NewReader(doc))
	depth := 0
	sawRoot := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("svg is not well-formed XML: %v\n--- output ---\n%s", err, doc)
		}
		switch se := tok.(type) {
		case xml.StartElement:
			if depth == 0 {
				if sawRoot {
					t.Fatalf("more than one root element: found a second root <%s>", se.Name.Local)
				}
				if se.Name.Local != "svg" {
					t.Fatalf("root element is <%s>, want <svg>", se.Name.Local)
				}
				sawRoot = true
			}
			if se.Name.Local == "script" {
				t.Fatalf("found an injected <script> element — untrusted content broke the SVG structure")
			}
			depth++
		case xml.EndElement:
			depth--
		}
	}
	if !sawRoot {
		t.Fatal("no root <svg> element found")
	}
	if depth != 0 {
		t.Fatalf("unbalanced tags, final depth=%d", depth)
	}
}

func realisticInput() PosterInput {
	return PosterInput{
		Title:      "Plan gallery thumbnails",
		Status:     "approved",
		TLDR:       "Show a designed poster of each plan on its gallery card, recognizable at a glance.",
		Sections:   7,
		Diagrams:   3,
		Collisions: 2,
		Author:     "Ryan",
		Date:       "2026-08-18",
	}
}

func TestRenderPosterSVG_ValidWellFormedSVG(t *testing.T) {
	svg, err := RenderPosterSVG(realisticInput())
	if err != nil {
		t.Fatalf("RenderPosterSVG: %v", err)
	}
	assertWellFormedSVG(t, svg)
}

func TestRenderPosterSVG_ContainsTitleAndTLDR(t *testing.T) {
	svg, err := RenderPosterSVG(realisticInput())
	if err != nil {
		t.Fatalf("RenderPosterSVG: %v", err)
	}
	out := string(svg)
	if !strings.Contains(out, "Plan gallery thumbnails") {
		t.Errorf("output missing title text:\n%s", out)
	}
	if !strings.Contains(out, "Show a designed poster of each plan on its") {
		t.Errorf("output missing TL;DR first line:\n%s", out)
	}
	// value and label render as separate <text> elements (see template.go), so
	// check each chip's two halves independently rather than one joined string.
	for _, want := range []string{">7</text>", "sections", ">3</text>", "diagrams", ">2</text>", "collisions"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing expected stat chip fragment %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "approved") {
		t.Errorf("output missing status pill label:\n%s", out)
	}
	if !strings.Contains(out, "Ryan") || !strings.Contains(out, "2026-08-18") {
		t.Errorf("output missing author/date footer:\n%s", out)
	}
}

// TestRenderPosterSVG_DegradesGracefully is table-driven over every field
// this package documents as independently optional (PosterInput's doc
// comment): each case removes exactly one input and asserts the
// corresponding block vanishes from the output, without RenderPosterSVG
// erroring or producing malformed SVG.
func TestRenderPosterSVG_DegradesGracefully(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(in *PosterInput)
		wantNot []string // substrings that must NOT appear
		wantHas []string // substrings that must still appear (the rest of the poster survives)
	}{
		{
			name:    "no TL;DR drops the excerpt block",
			mutate:  func(in *PosterInput) { in.TLDR = "" },
			wantNot: []string{"Show a designed poster"},
			wantHas: []string{"Plan gallery thumbnails", ">7</text>", "sections"},
		},
		{
			name: "no stats drops the whole chip row",
			mutate: func(in *PosterInput) {
				in.Sections, in.Diagrams, in.Collisions = 0, 0, 0
			},
			wantNot: []string{"sections", "diagrams", "collisions"},
			wantHas: []string{"Plan gallery thumbnails"},
		},
		{
			name:    "no author/date drops the footer's left text but keeps the wordmark",
			mutate:  func(in *PosterInput) { in.Author, in.Date = "", "" },
			wantNot: []string{"Ryan", "2026-08-18"},
			wantHas: []string{"SageOx"},
		},
		{
			name:    "empty title falls back to a placeholder rather than an empty <text>",
			mutate:  func(in *PosterInput) { in.Title = "" },
			wantHas: []string{"Untitled Plan"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := realisticInput()
			tt.mutate(&in)
			svg, err := RenderPosterSVG(in)
			if err != nil {
				t.Fatalf("RenderPosterSVG: %v", err)
			}
			assertWellFormedSVG(t, svg)
			out := string(svg)
			for _, s := range tt.wantNot {
				if strings.Contains(out, s) {
					t.Errorf("output should not contain %q:\n%s", s, out)
				}
			}
			for _, s := range tt.wantHas {
				if !strings.Contains(out, s) {
					t.Errorf("output missing %q:\n%s", s, out)
				}
			}
		})
	}
}

func TestRenderPosterSVG_UnknownStatusUsesNeutralSpine(t *testing.T) {
	tests := []string{"", "draft", "some-future-status-this-package-has-never-heard-of"}
	for _, status := range tests {
		t.Run("status="+status, func(t *testing.T) {
			in := realisticInput()
			in.Status = status
			svg, err := RenderPosterSVG(in)
			if err != nil {
				t.Fatalf("RenderPosterSVG: %v", err)
			}
			assertWellFormedSVG(t, svg)
			out := string(svg)
			if !strings.Contains(out, neutralStatusStyle.Spine1) {
				t.Errorf("unrecognized status %q should render the neutral spine color %q:\n%s", status, neutralStatusStyle.Spine1, out)
			}
			for key, style := range statusStyles {
				if strings.Contains(out, style.Spine1) {
					t.Errorf("unrecognized status %q should not render the %q spine color:\n%s", status, key, out)
				}
			}
		})
	}
}

func TestRenderPosterSVG_KnownStatusUsesItsOwnSpine(t *testing.T) {
	for status, style := range statusStyles {
		t.Run(status, func(t *testing.T) {
			in := realisticInput()
			in.Status = status
			svg, err := RenderPosterSVG(in)
			if err != nil {
				t.Fatalf("RenderPosterSVG: %v", err)
			}
			assertWellFormedSVG(t, svg)
			if out := string(svg); !strings.Contains(out, style.Spine1) {
				t.Errorf("status %q should render its own spine color %q:\n%s", status, style.Spine1, out)
			}
		})
	}
}

// TestRenderPosterSVG_UntrustedContentIsEscaped is the security assertion:
// plan content is author-controlled and untrusted. A title or author string
// containing raw SVG/XML syntax must never be interpreted as markup — it must
// come out as inert, escaped text, and the document must still parse as one
// well-formed <svg> with no injected element.
func TestRenderPosterSVG_UntrustedContentIsEscaped(t *testing.T) {
	tests := []struct {
		name string
		in   PosterInput
	}{
		{
			name: "closing tag + script injection in the title",
			in: PosterInput{
				Title: `</svg><script>alert(1)</script>`,
			},
		},
		{
			name: "raw XML special characters in the title",
			in: PosterInput{
				Title: `&"<>'`,
			},
		},
		{
			name: "injection attempt in TL;DR",
			in: PosterInput{
				Title: "Normal Title",
				TLDR:  `</text><rect x="0" y="0" width="9999" height="9999" fill="red"/><text>`,
			},
		},
		{
			name: "injection attempt in author/date footer",
			in: PosterInput{
				Title:  "Normal Title",
				Author: `"><image href="x" onerror="alert(1)"/>`,
				Date:   `&<>`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svg, err := RenderPosterSVG(tt.in)
			if err != nil {
				t.Fatalf("RenderPosterSVG: %v", err)
			}
			// Structural proof: exactly one root <svg>, no injected element
			// (in particular no <script>), tags balanced — encoding/xml would
			// fail to decode a document where the injected markup actually
			// broke out into real elements.
			assertWellFormedSVG(t, svg)

			out := string(svg)
			if strings.Contains(out, "<script>") {
				t.Errorf("raw <script> tag survived unescaped:\n%s", out)
			}
			if strings.Contains(out, `<rect x="0" y="0" width="9999"`) {
				t.Errorf("raw injected <rect> tag survived unescaped:\n%s", out)
			}
			if strings.Contains(out, "<image ") {
				t.Errorf("raw injected <image> tag survived unescaped:\n%s", out)
			}
			// Textual proof: the dangerous characters were actually escaped,
			// not merely absent from the raw form by coincidence.
			if strings.Contains(tt.in.Title, "<") && !strings.Contains(out, "&lt;") {
				t.Errorf("expected an escaped '<' (&lt;) in output for title %q:\n%s", tt.in.Title, out)
			}
			if strings.Contains(tt.in.Title, "&") && !strings.Contains(out, "&amp;") {
				t.Errorf("expected an escaped '&' (&amp;) in output for title %q:\n%s", tt.in.Title, out)
			}
		})
	}
}

func TestWrapTLDR(t *testing.T) {
	tests := []struct {
		name         string
		text         string
		exact        bool // when true, l1/l2 must equal wantLine1/wantLine2 exactly
		wantLine1    string
		wantLine2    string
		wantLine2end string // when set, l2 must end with this (used for the truncation case, where the exact wrap point isn't the point of the test)
	}{
		{name: "empty", text: "", exact: true, wantLine1: "", wantLine2: ""},
		{name: "short fits one line", text: "A short lede.", exact: true, wantLine1: "A short lede.", wantLine2: ""},
		{
			name:      "wraps at word boundary onto a second line",
			text:      "Show a designed poster of each plan on its gallery card, recognizable at a glance.",
			exact:     true,
			wantLine1: "Show a designed poster of each plan on its gallery",
			wantLine2: "card, recognizable at a glance.",
		},
		{
			name:         "truncates with an ellipsis past two lines",
			text:         strings.Repeat("word ", 40),
			wantLine2end: "…",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l1, l2 := wrapTLDR(tt.text)
			if tt.exact {
				if l1 != tt.wantLine1 {
					t.Errorf("line1 = %q, want %q", l1, tt.wantLine1)
				}
				if l2 != tt.wantLine2 {
					t.Errorf("line2 = %q, want %q", l2, tt.wantLine2)
				}
			}
			if tt.wantLine2end != "" && !strings.HasSuffix(l2, tt.wantLine2end) {
				t.Errorf("line2 = %q, want suffix %q", l2, tt.wantLine2end)
			}
			for _, l := range []string{l1, l2} {
				if n := len([]rune(l)); n > tldrMaxLineChars+1 { // +1: the ellipsis itself
					t.Errorf("line %q is %d runes, want <= %d", l, n, tldrMaxLineChars+1)
				}
			}
		})
	}
}
