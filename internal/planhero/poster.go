// Package planhero renders a saved plan's hero poster: a designed 1280x720
// SVG card (status-colored spine, title, TL;DR excerpt, structure-derived
// stat chips, author/date footer) generated directly from the plan's
// structured data. This is deliberately NOT a browser screenshot of
// plan.html — ox ships as a single static binary and a headless-browser/
// Chromium dependency would bloat every install to save a render nobody but
// a gallery thumbnail consumes. Pure Go templating only.
//
// RenderPosterSVG is a pure function: no I/O, no globals mutated, safe to
// call from any caller (cmd/ox wires it into `ox plan save`). Every field is
// optional and degrades independently — see the doc comment on PosterInput.
package planhero

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
)

// PosterInput is the structured data a hero poster is rendered from. Every
// string field is UNTRUSTED plan/user content (a plan's title or TL;DR can
// contain anything the author typed, including markup) and is XML-escaped
// before it reaches the SVG — see escapeXML. Every field independently
// degrades when absent: an empty TLDR drops that block, zero-value Sections/
// Diagrams/Collisions drop their chip, an empty/unrecognized Status renders a
// neutral spine instead of failing.
type PosterInput struct {
	Title  string
	Status string // a plan.PlanStatus value (e.g. "approved"); "" = unknown/draft
	TLDR   string // a short plain-text lede; markdown formatting should already be stripped by the caller

	Sections   int
	Diagrams   int
	Collisions int

	Author string
	Date   string // pre-formatted (e.g. "2026-08-18"); this package does no date parsing
}

// Poster canvas size — matches the reference design
// (.context/hero-poster-mockup.svg) and the aspect ratio gallery thumbnail
// grids expect.
const (
	posterWidth  = 1280
	posterHeight = 720
	marginX      = 72 // left/right content margin, matches the reference mockup
)

// pillDotOffset/pillTextOffset are the status pill's internal layout: the
// dot sits 28px in from the pill's left edge, the label text starts at 44px
// (dot + its own padding) — both lifted directly from the reference mockup's
// fixed-width pill so a variable-width pill keeps the same internal rhythm.
const (
	pillDotOffset  = 28
	pillTextOffset = 44
)

// statusStyle is the spine-gradient + pill color set for one recognized plan
// status. Hues follow the same semantic map internal/plan/planfacts.go uses
// for hero stat chips (sage=good/shipped, amber=hold/caution, red=risk),
// extended here to a lifecycle spine: green=approved, blue=realized work,
// red=abandoned, amber=superseded, neutral gray=everything else (including
// draft and any status this package doesn't recognize).
type statusStyle struct {
	Spine1, Spine2                        string
	PillBG, PillBorder, PillDot, PillText string
}

var neutralStatusStyle = statusStyle{
	Spine1: "#6e7681", Spine2: "#484f58",
	PillBG: "#21262d", PillBorder: "#6e7681", PillDot: "#8b949e", PillText: "#adbac7",
}

var statusStyles = map[string]statusStyle{
	"approved": {
		Spine1: "#3fb950", Spine2: "#2ea043",
		PillBG: "#132a17", PillBorder: "#2ea043", PillDot: "#3fb950", PillText: "#3fb950",
	},
	"implemented": {
		Spine1: "#58a6ff", Spine2: "#388bfd",
		PillBG: "#0d2847", PillBorder: "#388bfd", PillDot: "#58a6ff", PillText: "#58a6ff",
	},
	"realized": {
		Spine1: "#58a6ff", Spine2: "#388bfd",
		PillBG: "#0d2847", PillBorder: "#388bfd", PillDot: "#58a6ff", PillText: "#58a6ff",
	},
	"abandoned": {
		Spine1: "#f85149", Spine2: "#da3633",
		PillBG: "#3c1618", PillBorder: "#da3633", PillDot: "#f85149", PillText: "#f85149",
	},
	"superseded": {
		Spine1: "#d29922", Spine2: "#9e6a03",
		PillBG: "#2b2111", PillBorder: "#9e6a03", PillDot: "#d29922", PillText: "#d29922",
	},
	// "draft" is intentionally absent: it shares neutralStatusStyle's colors,
	// so an unset/draft/unrecognized status all fall through to the same
	// lookup miss below rather than needing a duplicate map entry.
}

// xmlEscaper covers every XML predefined entity. Order matters: '&' must be
// replaced first, or a literal "<" would become "&amp;lt;" instead of
// "&lt;". strings.Replacer applies its pairs in a single left-to-right scan
// (not sequential passes), so this is safe as written — but the ampersand
// pair is still listed first for a human reader's sake.
var xmlEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&quot;",
	"'", "&apos;",
)

// escapeXML makes untrusted plan content safe to place inside SVG text
// content: a title of `</svg><script>alert(1)</script>` becomes inert text,
// never a parsed element, because every '<' and '&' is escaped before the
// template ever sees it (this package uses text/template, which does zero
// escaping on its own — see RenderPosterSVG).
func escapeXML(s string) string {
	return xmlEscaper.Replace(s)
}

// posterView is the template's data: every string field is ALREADY escaped
// (via escapeXML) or is first-party literal text (e.g. "SageOx"), and every
// numeric field is a precomputed layout coordinate — the template itself
// does pure substitution, no logic beyond {{if}}/{{range}} on booleans and
// slices already decided in Go.
type posterView struct {
	Width, Height int

	SpineStop1, SpineStop2 string

	PillX, PillWidth, PillDotX, PillTextX int
	PillBG, PillBorder, PillDot           string
	PillTextColor, StatusLabel            string

	Title         string
	TitleFontSize int

	TLDRPresent          bool
	TLDRLine1, TLDRLine2 string

	ChipsPresent                   bool
	Chips                          []chipView
	ChipsY, ChipValueY, ChipLabelY int

	FooterLeftPresent bool
	FooterLeft        string
	WordmarkX         int
}

type chipView struct {
	Value, Label    string
	X, TextX, Width int
}

// RenderPosterSVG renders a plan's hero poster from its structured data. Pure
// function: deterministic output for a given input, no I/O. Every field of
// in degrades independently rather than erroring — the only error path is an
// internal template failure, which would indicate a bug in this package, not
// bad input.
func RenderPosterSVG(in PosterInput) ([]byte, error) {
	tmpl, err := template.New("poster").Parse(posterTemplateSrc)
	if err != nil {
		return nil, fmt.Errorf("parse poster template: %w", err)
	}

	statusKey := strings.ToLower(strings.TrimSpace(in.Status))
	style, known := statusStyles[statusKey]
	if !known {
		style = neutralStatusStyle
	}
	statusLabel := statusKey
	if statusLabel == "" {
		statusLabel = "draft" // matches internal/plan/store.go: "Missing == draft for legacy plans"
	}
	pillWidth := pillWidthFor(statusLabel)
	pillX := posterWidth - marginX - pillWidth

	title, titleFontSize := fitTitle(strings.TrimSpace(in.Title))

	tldrLine1, tldrLine2 := wrapTLDR(strings.TrimSpace(in.TLDR))
	tldrPresent := tldrLine1 != ""

	// Tuned: 90px is the vertical space the TL;DR block occupies (two 56px
	// text lines plus lead-in) — dropping it entirely when there's no TL;DR
	// would leave a visually empty gap above the chips, so they shift up to
	// fill it instead of the layout just having a hole in it.
	chipsY := 470
	if !tldrPresent {
		chipsY = 380
	}

	chips := layoutChips(statChips(in))

	var footerParts []string
	if a := strings.TrimSpace(in.Author); a != "" {
		footerParts = append(footerParts, escapeXML(a))
	}
	if d := strings.TrimSpace(in.Date); d != "" {
		footerParts = append(footerParts, escapeXML(d))
	}

	view := posterView{
		Width:  posterWidth,
		Height: posterHeight,

		SpineStop1: style.Spine1,
		SpineStop2: style.Spine2,

		PillX:         pillX,
		PillWidth:     pillWidth,
		PillDotX:      pillX + pillDotOffset,
		PillTextX:     pillX + pillTextOffset,
		PillBG:        style.PillBG,
		PillBorder:    style.PillBorder,
		PillDot:       style.PillDot,
		PillTextColor: style.PillText,
		StatusLabel:   escapeXML(statusLabel),

		Title:         escapeXML(title),
		TitleFontSize: titleFontSize,

		TLDRPresent: tldrPresent,
		TLDRLine1:   escapeXML(tldrLine1),
		TLDRLine2:   escapeXML(tldrLine2),

		ChipsPresent: len(chips) > 0,
		Chips:        chips,
		ChipsY:       chipsY,
		ChipValueY:   chipsY + 48,
		ChipLabelY:   chipsY + 78,

		FooterLeftPresent: len(footerParts) > 0,
		FooterLeft:        strings.Join(footerParts, " · "), // middle dot, matches the reference mockup's "Author · Date"
		WordmarkX:         posterWidth - marginX,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, view); err != nil {
		return nil, fmt.Errorf("execute poster template: %w", err)
	}
	return buf.Bytes(), nil
}

// pillWidthFor sizes the status pill to its label. Tuned: ~10.5px/rune is
// the observed advance width of the pill's 22px semibold sans-serif label;
// 64px covers the fixed chrome (44px text offset + ~20px right padding). A
// 120px floor keeps a one-word label ("draft") from producing a cramped pill.
func pillWidthFor(label string) int {
	w := pillTextOffset + 20 + int(float64(len([]rune(label)))*10.5)
	if w < 120 {
		w = 120
	}
	return w
}

// fitTitle picks a font size that keeps the title from overflowing the
// poster's 1136px title width (1280 canvas - 72px margins on each side) and
// falls back to truncation past a length no reasonable size fits. Tuned
// against Georgia bold's approximate glyph-advance width at each size; not
// exact glyph metrics (this package has no font-shaping dependency), so the
// thresholds are deliberately conservative.
func fitTitle(title string) (text string, fontSize int) {
	if title == "" {
		return "Untitled Plan", 88
	}
	n := len([]rune(title))
	switch {
	case n <= 24:
		return title, 88
	case n <= 34:
		return title, 68
	case n <= 46:
		return title, 54
	case n <= 70:
		return title, 44
	default:
		r := []rune(title)
		return string(r[:69]) + "…", 44
	}
}

// tldrMaxLineChars is the word-wrap width for the TL;DR excerpt: the poster
// renders it at 38px sans-serif across the 1136px content width, and 54
// characters is a conservative fit at that size/width for a proportional
// font (better to wrap one word early than to overflow the card).
const tldrMaxLineChars = 54

// wrapTLDR word-wraps text into at most two lines for the poster's TL;DR
// block, truncating with an ellipsis if more remains after two lines. Empty
// input returns ("", "") — the caller uses line1 == "" to decide whether the
// TL;DR block renders at all.
func wrapTLDR(text string) (line1, line2 string) {
	words := strings.Fields(text)
	if len(words) == 0 {
		return "", ""
	}

	var lines []string
	cur := ""
	i := 0
	for i < len(words) && len(lines) < 2 {
		w := words[i]
		// A single word wider than the line budget can never fit by wrapping
		// (the width check below is skipped when cur is empty) — hard-cut it so
		// a long URL or identifier can't overflow the card.
		if r := []rune(w); len(r) > tldrMaxLineChars {
			w = string(r[:tldrMaxLineChars-1]) + "…"
		}
		cand := w
		if cur != "" {
			cand = cur + " " + w
		}
		if cur != "" && len([]rune(cand)) > tldrMaxLineChars {
			lines = append(lines, cur)
			cur = ""
			continue // retry w as the start of the next line
		}
		cur = cand
		i++
	}
	if cur != "" && len(lines) < 2 {
		lines = append(lines, cur)
	}

	truncated := i < len(words)
	if truncated && len(lines) > 0 {
		last := strings.TrimRight(lines[len(lines)-1], ".,;: ")
		lines[len(lines)-1] = last + "…"
	}

	switch len(lines) {
	case 0:
		return "", ""
	case 1:
		return lines[0], ""
	default:
		return lines[0], lines[1]
	}
}

// chipRaw is one hero stat before layout: a value + a label already resolved
// to singular/plural.
type chipRaw struct {
	Value, Label string
}

// statChips builds the ordered chip list from in's counts, dropping any
// zero-value stat entirely — matches internal/plan/planfacts.go's
// structureStats: a stat that didn't fire doesn't get a chip.
func statChips(in PosterInput) []chipRaw {
	var out []chipRaw
	if in.Sections > 0 {
		out = append(out, chipRaw{fmt.Sprintf("%d", in.Sections), pluralLabel(in.Sections, "section", "sections")})
	}
	if in.Diagrams > 0 {
		out = append(out, chipRaw{fmt.Sprintf("%d", in.Diagrams), pluralLabel(in.Diagrams, "diagram", "diagrams")})
	}
	if in.Collisions > 0 {
		out = append(out, chipRaw{fmt.Sprintf("%d", in.Collisions), pluralLabel(in.Collisions, "collision", "collisions")})
	}
	return out
}

func pluralLabel(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

// layoutChips lays out chips left-to-right from the content margin, each
// sized to its label so "collisions" doesn't get truncated in a
// "sections"-sized box.
func layoutChips(raw []chipRaw) []chipView {
	const chipGap = 20
	const chipTextPad = 20
	x := marginX
	out := make([]chipView, 0, len(raw))
	for _, c := range raw {
		w := chipWidthFor(c.Label)
		out = append(out, chipView{
			Value: escapeXML(c.Value),
			Label: escapeXML(c.Label),
			X:     x,
			TextX: x + chipTextPad,
			Width: w,
		})
		x += w + chipGap
	}
	return out
}

// chipWidthFor sizes a chip to its label. Tuned: 11px/rune is a conservative
// advance width for the chip's 22px label text; 40px covers the chip's fixed
// chrome (20px padding on each side). A 120px floor matches the reference
// mockup's smallest chip ("sections", 150px at its font) so a short label
// never looks cramped.
func chipWidthFor(label string) int {
	w := 40 + len([]rune(label))*11
	if w < 120 {
		w = 120
	}
	return w
}
