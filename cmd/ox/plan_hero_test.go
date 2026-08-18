package main

// plan_hero_test.go tests the hero.svg WIRING in savePlanArtifacts (plan.go):
// it fires only on an HTML-primary save, respects the plan.hero config
// opt-out, and never breaks the save when hero generation itself fails. The
// poster's own rendering/escaping correctness is covered exhaustively by
// internal/planhero's tests — these tests are about the composition, not the
// SVG content.

import (
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/plan"
)

// heroTestInput is a realistic HTML-primary save: a markdown body (derived
// from an authored page, as runPlanSaveFile would produce) carrying an
// explicit TL;DR, two H2 sections, and one mermaid fence — so the wiring
// test can assert the hero reflects real, non-zero structure counts.
const heroTestMarkdown = `# Plan Gallery Thumbnails

TL;DR: Show a designed poster of each plan on its gallery card.

## Design

` + "```mermaid\nflowchart TB\n  A --> B\n```" + `

## Rollout

Ship behind a config flag.
`

func heroTestInput() plan.Input {
	return plan.Parse(heroTestMarkdown)
}

// expectedPlanDir predicts the exact directory plan.Save will create for in,
// mirroring savePlanArtifacts' own derivation (planTopic → Slugify, dated
// with today's UTC date) — so a test can pre-stage a conflict at that exact
// path before calling savePlanArtifacts.
func expectedPlanDir(t *testing.T, root string, in plan.Input) string {
	t.Helper()
	ctx, err := config.LoadProjectContext(root)
	if err != nil || ctx == nil {
		t.Fatalf("LoadProjectContext: %v", err)
	}
	ledger := ctx.DefaultLedgerPath()
	if ledger == "" {
		t.Fatal("no ledger resolved — fixture is broken")
	}
	slug := plan.Slugify(plan.PlanTopic(in))
	dirName := time.Now().UTC().Format("2006-01-02") + "-" + slug
	return filepath.Join(ledger, "data", "plans", dirName)
}

func TestSavePlanArtifacts_WritesHeroSVGOnHTMLPrimarySave(t *testing.T) {
	root := newPlanCaptureTestRepo(t)
	t.Setenv("SAGEOX_AGENT_ID", "")

	in := heroTestInput()
	// Collisions=3 is deliberately distinct from the section count (2) so the
	// two chips aren't indistinguishable in the rendered SVG.
	result := plan.Result{Signals: plan.SignalSummary{Collisions: 3}}
	html := []byte("<html><body>authored plan page</body></html>")

	dir := savePlanArtifacts(root, in, result, html, plan.PrimaryHTML)
	if dir == "" {
		t.Fatal("savePlanArtifacts returned empty dir — the plan was not saved at all")
	}

	heroPath := filepath.Join(dir, "hero.svg")
	data, err := os.ReadFile(heroPath)
	if err != nil {
		t.Fatalf("hero.svg was not written for an HTML-primary save: %v", err)
	}

	var root2 struct {
		XMLName xml.Name
	}
	if err := xml.Unmarshal(data, &root2); err != nil {
		t.Fatalf("hero.svg is not well-formed XML: %v\n%s", err, data)
	}
	if root2.XMLName.Local != "svg" {
		t.Fatalf("hero.svg root element = %q, want svg", root2.XMLName.Local)
	}

	out := string(data)
	for _, want := range []string{
		"Plan Gallery Thumbnails",             // title, derived from the H1
		"Show a designed poster of each plan", // TL;DR body (word-wrapped, so check a prefix that survives wrapping)
		">2</text>", "sections",               // 2 H2 sections (Design, Rollout)
		">1</text>", "diagram", // 1 mermaid fence (singular label)
		">3</text>", "collisions", // res.Signals.Collisions — distinct from the section count
		"draft", // a freshly created plan's lifecycle status
	} {
		if !strings.Contains(out, want) {
			t.Errorf("hero.svg missing expected content %q:\n%s", want, out)
		}
	}
	// The "TL;DR:" marker itself must be stripped, not rendered onto the poster —
	// asserting only the body substring would pass even if marker-stripping broke.
	if strings.Contains(out, "TL;DR") {
		t.Errorf("hero.svg should not contain the raw TL;DR marker (it must be stripped):\n%s", out)
	}
}

func TestSavePlanArtifacts_SkipsHeroSVGOnMarkdownPrimarySave(t *testing.T) {
	root := newPlanCaptureTestRepo(t)
	t.Setenv("SAGEOX_AGENT_ID", "")

	in := heroTestInput()
	dir := savePlanArtifacts(root, in, plan.Result{}, nil, "") // primary == "" (markdown-primary)
	if dir == "" {
		t.Fatal("savePlanArtifacts returned empty dir")
	}

	if _, err := os.Stat(filepath.Join(dir, "hero.svg")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("hero.svg should not be written on a markdown-primary save (err=%v)", err)
	}
}

func TestSavePlanArtifacts_SkipsHeroSVGWhenConfigDisabled(t *testing.T) {
	root := newPlanCaptureTestRepo(t)
	t.Setenv("SAGEOX_AGENT_ID", "")

	// plan.hero: false at the project level (.sageox/config.json, the same
	// file newPlanCaptureTestRepo already seeded with repo_id — merge, don't
	// overwrite, or LoadProjectContext loses the repo_id and the ledger
	// stops resolving).
	cfgPath := filepath.Join(root, ".sageox", "config.json")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read seeded project config: %v", err)
	}
	merged := strings.TrimSuffix(strings.TrimSpace(string(raw)), "}") + `,"plan":{"hero":false}}`
	if err := os.WriteFile(cfgPath, []byte(merged), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	if config.PlanHero(root) {
		t.Fatalf("fixture broken: config.PlanHero(%q) = true, want false after setting plan.hero=false", root)
	}

	in := heroTestInput()
	dir := savePlanArtifacts(root, in, plan.Result{}, []byte("<html></html>"), plan.PrimaryHTML)
	if dir == "" {
		t.Fatal("savePlanArtifacts returned empty dir")
	}

	if _, err := os.Stat(filepath.Join(dir, "hero.svg")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("hero.svg should not be written when plan.hero=false (err=%v)", err)
	}
}

// TestSavePlanArtifacts_HeroFailureNeverBreaksSave is the best-effort proof:
// hero.svg's target path is pre-occupied by a DIRECTORY (so the real
// os.WriteFile the wiring performs cannot possibly succeed there — a
// realistic disk-level failure, not a mocked one), and the save must still
// return the plan dir with every other artifact intact.
func TestSavePlanArtifacts_HeroFailureNeverBreaksSave(t *testing.T) {
	root := newPlanCaptureTestRepo(t)
	t.Setenv("SAGEOX_AGENT_ID", "")

	in := heroTestInput()
	dir := expectedPlanDir(t, root, in)
	if err := os.MkdirAll(filepath.Join(dir, "hero.svg"), 0o755); err != nil {
		t.Fatalf("pre-stage hero.svg as a directory: %v", err)
	}

	got := savePlanArtifacts(root, in, plan.Result{}, []byte("<html></html>"), plan.PrimaryHTML)
	if got != dir {
		t.Fatalf("savePlanArtifacts returned dir=%q, want the pre-staged %q — fixture's path prediction is wrong", got, dir)
	}

	// The save itself must be unaffected: plan.md/meta.json still land.
	if _, err := os.Stat(filepath.Join(dir, "plan.md")); err != nil {
		t.Errorf("plan.md missing after a hero failure — the save was not best-effort: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "meta.json")); err != nil {
		t.Errorf("meta.json missing after a hero failure — the save was not best-effort: %v", err)
	}
	// hero.svg must remain the pre-staged directory (untouched), proving the
	// write was skipped rather than partially succeeding or panicking.
	info, err := os.Stat(filepath.Join(dir, "hero.svg"))
	if err != nil || !info.IsDir() {
		t.Errorf("hero.svg path should remain the pre-staged directory after a failed write, got info=%v err=%v", info, err)
	}
}

// TestWritePlanHero_ReturnsErrorOnWriteFailure unit-tests writePlanHero's own
// error contract in isolation: a destination whose parent directory doesn't
// exist must return a non-nil error (never panic), which is exactly what
// lets the savePlanArtifacts call site's `if err != nil { slog.Warn(...) }`
// treat it as best-effort.
func TestWritePlanHero_ReturnsErrorOnWriteFailure(t *testing.T) {
	badDir := filepath.Join(t.TempDir(), "does-not-exist", "plan-dir")
	err := writePlanHero(badDir, heroTestInput(), plan.Result{}, plan.Meta{})
	if err == nil {
		t.Fatal("writePlanHero should return an error when its target directory doesn't exist")
	}
}

// TestPlanHeroTLDR covers the extraction branches the single HTML-primary wiring
// test never reaches: the first-prose fallback (the COMMON case — most plans
// carry no explicit TL;DR marker) and the table/list/blockquote/heading-only
// rejections. Failure prevented: a plan with no marker showing a blank or wrong
// lede on its gallery thumbnail.
func TestPlanHeroTLDR(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"explicit marker, stripped", "TL;DR: The short lede.", "The short lede."},
		{"marker after a heading", "# Title\n\nTL;DR: The lede.", "The lede."},
		{"marker variant (tldr, emphasis, no colon)", "**tldr** the lede", "the lede"},
		{"first-prose fallback when no marker", "# Title\n\nJust the opening prose.", "Just the opening prose."},
		{"bold-opening prose is a lede, not a list", "# Title\n\n**Important:** ship it.", "Important: ship it."},
		{"skips heading-only preamble to first prose", "# A\n\n## B\n\nProse here.", "Prose here."},
		{"table opener is not a lede", "# T\n\n| a | b |\n| - | - |", ""},
		{"blockquote opener is not a lede", "# T\n\n> a quote", ""},
		{"bullet-list opener is not a lede", "# T\n\n- item one", ""},
		{"asterisk-list opener is not a lede", "# T\n\n* item one", ""},
		{"heading-only document has no lede", "# Only\n\n## Headings", ""},
		{"empty document", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := planHeroTLDR(tt.raw); got != tt.want {
				t.Errorf("planHeroTLDR(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// TestPlanHeroPlainText gives the markdown-stripping helper the assertions its
// 100%-line-coverage lacks: the only lede ever fed by other tests has no markup,
// so every transform runs as a no-op. Failure prevented: a lede with a link,
// emphasis, or inline code rendering raw markdown syntax onto the poster.
func TestPlanHeroPlainText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"link keeps label, drops target", "see [the docs](http://example.com)", "see the docs"},
		{"bold and inline code stripped", "make it **bold** and `code`", "make it bold and code"},
		{"underscore emphasis stripped", "very __strong__ point", "very strong point"},
		{"whitespace and newlines collapse", "a  b\n\tc   d", "a b c d"},
		{"plain text unchanged", "nothing to strip here", "nothing to strip here"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := planHeroPlainText(tt.in); got != tt.want {
				t.Errorf("planHeroPlainText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
