package decision

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
)

// monoADR is a sageox-mono-template fixture: header metadata lines, numbered
// Decision subheads, an explicit D-anchor, a dated amendment, outbound refs.
const monoADR = `# ADR-047: Customer-Facing Env Var Namespace

**Status**: Accepted — merged via PR #1200
**Date**: 2026-03-14
**Decision Makers**: Person A, Person B

## Context

The namespace for customer-facing environment variables drifted across
subsystems, so operators could not predict which prefix a knob would use.

## Decision

### 1. SAGEOX_ prefix is canonical

All customer-facing vars use it.

### D4: OX_ is internal-only

Per ADR-046 D9 and adr 12, internal tooling keeps OX_.

**Amendment (2026-05-01):** additive clarification, decision unchanged.

## Consequences

Superseded by ADR-030 for the daemon subset.
`

func writeCorpus(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func defaultCorpusFiles() map[string]string {
	return map[string]string{
		"docs/adr/ADR-047-env-var-namespace.md": monoADR,
		"docs/adr/ADR-021-plan-context.md":      "# ADR-021: Plan Context Not Inference\n\n**Status**: Accepted\n**Date**: 2026-06-03\n\n## Context\n\nox provides context for plan enrichment, the client does inference.\n\n## Decision\n\n### 1. No LLM calls\n\nDeterministic only.\n",
		"docs/adr/002-daemon-architecture.md":   "# Daemon Architecture\n\n**Status**: Accepted\n**Date**: 2025-11-01\n\n## Context\n\nDaemon owns pulls.\n",
		"docs/adr/ADR-002-unix-socket.md":       "# ADR-002: Unix Socket IPC\n\n**Status**: Accepted\n**Date**: 2025-12-01\n\n## Context\n\nIPC over unix sockets.\n",
		"docs/adr/README.md":                    "# Index\n\n| ADR | Title |\n",
		"docs/adr/notes.md":                     "just some markdown with no decision shape\n",
	}
}

func TestParseContent_MonoTemplate(t *testing.T) {
	rec := ParseContent("docs/adr/ADR-047-env-var-namespace.md", monoADR)

	if rec.ID != "ADR-047" || rec.Prefix != "ADR" || rec.Number != 47 {
		t.Fatalf("id parse: got %q %q %d", rec.ID, rec.Prefix, rec.Number)
	}
	if rec.Title != "Customer-Facing Env Var Namespace" {
		t.Errorf("title: %q", rec.Title)
	}
	if !strings.HasPrefix(rec.Status, "Accepted") {
		t.Errorf("status: %q", rec.Status)
	}
	if rec.Date != "2026-03-14" {
		t.Errorf("date: %q", rec.Date)
	}
	if len(rec.Deciders) != 2 || rec.Deciders[0] != "Person A" {
		t.Errorf("deciders: %v", rec.Deciders)
	}
	anchorIDs := map[string]bool{}
	for _, d := range rec.DSections {
		anchorIDs[d.ID] = true
	}
	if !anchorIDs["D1"] || !anchorIDs["D4"] {
		t.Errorf("anchors: %v", rec.DSections)
	}
	if len(rec.Amendments) != 1 || rec.Amendments[0].Date != "2026-05-01" {
		t.Errorf("amendments: %v", rec.Amendments)
	}
	wantRefs := map[string]bool{"ADR-046": true, "ADR-012": true, "ADR-030": true}
	for _, r := range rec.Refs {
		if !wantRefs[r] {
			t.Errorf("unexpected ref %q", r)
		}
		delete(wantRefs, r)
	}
	for missing := range wantRefs {
		t.Errorf("missing ref %q", missing)
	}
	if rec.SupersededBy != "ADR-030" {
		t.Errorf("superseded_by: %q", rec.SupersededBy)
	}
	if rec.DRType != "adr" {
		t.Errorf("dr_type: %q", rec.DRType)
	}
	if !strings.Contains(rec.Excerpt, "namespace") {
		t.Errorf("excerpt: %q", rec.Excerpt)
	}
	if !rec.IsRecord() {
		t.Error("IsRecord = false")
	}
}

func TestParseContent_Variants(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		body     string
		wantID   string
		wantType string
		isRecord bool
	}{
		{"numeric-only filename", "002-daemon.md", "# Daemon\n\n**Status**: Accepted\n", "ADR-002", "adr", true},
		{"ddr prefix", "DDR-003-color-tokens.md", "# DDR-003: Color Tokens\n\n**Status**: Proposed\n", "DDR-003", "ddr", true},
		{"unnumbered with status", "adr-ephemeral-mode.md", "# Ephemeral Mode\n\n**Status**: Accepted\n", "", "adr", true},
		{"plain markdown", "notes.md", "# Notes\n\nnothing decision-shaped here\n", "", "other", false},
		{"stdin draft", "", "# ADR-099: Draft Thing\n\n**Status**: Proposed\n", "ADR-099", "adr", true},
		{"frontmatter title wins", "ADR-005-x.md", "---\ntitle: Frontmatter Title\nstatus: Accepted\n---\n# ADR-005: H1 Title\n", "ADR-005", "adr", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := ParseContent(tt.path, tt.body)
			if rec.ID != tt.wantID {
				t.Errorf("id: got %q want %q", rec.ID, tt.wantID)
			}
			if rec.DRType != tt.wantType {
				t.Errorf("dr_type: got %q want %q", rec.DRType, tt.wantType)
			}
			if rec.IsRecord() != tt.isRecord {
				t.Errorf("IsRecord: got %v want %v", rec.IsRecord(), tt.isRecord)
			}
			if tt.name == "frontmatter title wins" && rec.Title != "Frontmatter Title" {
				t.Errorf("title precedence: %q", rec.Title)
			}
		})
	}
}

func TestLoadCorpus(t *testing.T) {
	root := t.TempDir()
	writeCorpus(t, root, defaultCorpusFiles())

	corpus := LoadCorpus(root, nil)
	if len(corpus) != 4 {
		t.Fatalf("corpus size: got %d want 4 (README + non-DR excluded): %+v", len(corpus), corpus)
	}
	// sorted by number: 2, 2, 21, 47
	if corpus[0].Number != 2 || corpus[3].Number != 47 {
		t.Errorf("sort order: %d..%d", corpus[0].Number, corpus[3].Number)
	}
	for _, r := range corpus {
		if r.Corpus != "repo" || r.RelPath == "" {
			t.Errorf("record metadata: %+v", r)
		}
	}
}

func TestLoadCorpus_ConfiguredGlob(t *testing.T) {
	root := t.TempDir()
	writeCorpus(t, root, map[string]string{
		"eng/decisions/ADR-001-a.md": "# ADR-001: A\n\n**Status**: Accepted\n",
		"eng/other/skip.md":          "# ADR-002: B\n\n**Status**: Accepted\n",
	})
	cfg := &config.DecisionConfig{Paths: []string{"eng/decisions/**/*.md"}}
	corpus := LoadCorpus(root, cfg)
	if len(corpus) != 1 || corpus[0].ID != "ADR-001" {
		t.Fatalf("glob corpus: %+v", corpus)
	}
}

func TestCorpusDetected(t *testing.T) {
	empty := t.TempDir()
	if CorpusDetected(empty) {
		t.Error("detected corpus in empty repo")
	}
	withDir := t.TempDir()
	writeCorpus(t, withDir, map[string]string{"docs/adr/ADR-001-x.md": "# ADR-001: X\n"})
	if !CorpusDetected(withDir) {
		t.Error("default-dir corpus not detected")
	}
	if CorpusDetected("") {
		t.Error("detected corpus for empty root")
	}
}

// PathMatcher powers the `ox code search --decisions` filter and the
// doc_type:"decision" result tag — a wrong match either hides DRs from the
// filter or mislabels code as a decision.
func TestPathMatcher(t *testing.T) {
	root := t.TempDir()
	writeCorpus(t, root, map[string]string{
		"docs/adr/ADR-001-x.md": "# ADR-001: X\n**Status**: Accepted\n",
	})
	m := PathMatcher(root)

	tests := []struct {
		rel  string
		want bool
	}{
		{"docs/adr/ADR-001-x.md", true},
		{"docs/adr/nested/deep.md", true},  // dir match is prefix-recursive
		{"docs/adr/notes.txt", false},      // not markdown
		{"docs/other/ADR-002-y.md", false}, // outside the corpus
		{"internal/decision/parse.go", false},
	}
	for _, tt := range tests {
		if got := m(tt.rel); got != tt.want {
			t.Errorf("match(%q) = %v want %v", tt.rel, got, tt.want)
		}
	}

	// no corpus → always-false predicate, never nil
	if PathMatcher(t.TempDir())("docs/adr/x.md") {
		t.Error("empty corpus should match nothing")
	}
	if PathMatcher("")("docs/adr/x.md") {
		t.Error("empty root should match nothing")
	}
}

func TestScoreCorpus(t *testing.T) {
	corpus := []Record{
		{ID: "ADR-001", Number: 1, Title: "Unix socket IPC transport", Date: "2026-01-01", Excerpt: "sockets everywhere"},
		{ID: "ADR-002", Number: 2, Title: "Unrelated thing", Date: "2026-01-02", Excerpt: "socket mentioned in excerpt only, transport too"},
	}

	t.Run("exact id short-circuits", func(t *testing.T) {
		for _, q := range []string{"ADR-001", "adr 1", "001"} {
			got := scoreCorpus(corpus, q)
			if len(got) == 0 || got[0].rec.ID != "ADR-001" || got[0].score != 1.0 {
				t.Errorf("query %q: %+v", q, got)
			}
		}
	})

	t.Run("title beats excerpt", func(t *testing.T) {
		got := scoreCorpus(corpus, "socket transport")
		if len(got) < 2 {
			t.Fatalf("want both records matched: %+v", got)
		}
		if got[0].rec.ID != "ADR-001" {
			t.Errorf("title match should rank first: %+v", got)
		}
	})

	t.Run("low coverage excluded", func(t *testing.T) {
		got := scoreCorpus(corpus, "socket quantum blockchain kubernetes")
		for _, s := range got {
			if s.score > 0 && s.rec.ID == "ADR-002" {
				t.Errorf("1/4 coverage should be excluded: %+v", s)
			}
		}
	})
}

func TestResolveInput(t *testing.T) {
	t.Run("topic wins", func(t *testing.T) {
		in, err := ResolveInput("my topic", "/nonexistent", strings.NewReader("ignored"))
		if err != nil || in.Topic != "my topic" || in.Raw != "" {
			t.Fatalf("in=%+v err=%v", in, err)
		}
		if in.Terms() != "my topic" {
			t.Errorf("terms: %q", in.Terms())
		}
	})
	t.Run("file mode", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "ADR-009-x.md")
		if err := os.WriteFile(p, []byte("# ADR-009: File Mode\n\n**Status**: Accepted\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		in, err := ResolveInput("", p, nil)
		if err != nil || in.Path != p || in.Record.ID != "ADR-009" {
			t.Fatalf("in=%+v err=%v", in, err)
		}
	})
	t.Run("stdin draft", func(t *testing.T) {
		in, err := ResolveInput("", "", strings.NewReader("# ADR-011: Stdin Draft\n"))
		if err != nil || in.Record.ID != "ADR-011" {
			t.Fatalf("in=%+v err=%v", in, err)
		}
	})
	t.Run("empty stdin", func(t *testing.T) {
		in, err := ResolveInput("", "", strings.NewReader("  \n"))
		if err != nil || in.Raw != "" || in.Topic != "" {
			t.Fatalf("in=%+v err=%v", in, err)
		}
	})
}

func TestSourceRefsAndCredits(t *testing.T) {
	body := `# ADR-050: X

Per Person A's discussion, surfaced by SageOx.
<!-- SOURCE: sageox discussion:2026-05-28-1423-a#ch-2 -->
<!-- SOURCE: sageox adr:docs/adr/ADR-001-x.md#D5 -->
Guided by SageOx.
<!-- this comment says surfaced by SageOx but is invisible -->
`
	in := Input{Raw: body}
	refs := in.SourceRefs()
	if len(refs) != 2 || refs[0] != "discussion:2026-05-28-1423-a#ch-2" || refs[1] != "adr:docs/adr/ADR-001-x.md#D5" {
		t.Errorf("refs: %v", refs)
	}
	// two visible credits; the one inside an HTML comment must not count
	if n := in.VisibleSageoxCredits(); n != 2 {
		t.Errorf("visible credits: got %d want 2", n)
	}
}

// panicDetector / stubDetector exercise the fail-open orchestrator. The
// registry is global and additive, so these tests tolerate the built-in
// detectors running alongside.
type panicDetector struct{}

func (panicDetector) Name() string { return "test-panic" }
func (panicDetector) Detect(context.Context, *Env, Input) ([]Annotation, error) {
	panic("boom")
}

type stubDetector struct{}

func (stubDetector) Name() string { return "test-stub" }
func (stubDetector) Detect(context.Context, *Env, Input) ([]Annotation, error) {
	return []Annotation{{Kind: BadgeDeterministic, Type: BadgeDiagnostic, Rule: "test-stub", Why: "stub fired"}}, nil
}

func TestEnrich_FailOpenAndSchema(t *testing.T) {
	RegisterDetector(panicDetector{})
	RegisterDetector(stubDetector{})

	res := Enrich(context.Background(), Input{Topic: "anything at all"}, "")
	if res.SchemaVersion != SchemaVersion {
		t.Errorf("schema: %q", res.SchemaVersion)
	}
	found := false
	for _, a := range res.Annotations {
		if a.Rule == "test-stub" {
			found = true
		}
	}
	if !found {
		t.Error("stub detector output lost — panic in sibling detector aborted the run")
	}
}

func TestEnrich_OnRealTempCorpus(t *testing.T) {
	root := t.TempDir()
	writeCorpus(t, root, defaultCorpusFiles())

	res := Enrich(context.Background(), Input{Topic: "plan context inference"}, root)

	if res.Decision.SuggestedID != "ADR-048" {
		t.Errorf("suggested id: %q (next after 47)", res.Decision.SuggestedID)
	}
	if res.Conventions.NextNumber != 48 {
		t.Errorf("next number: %d", res.Conventions.NextNumber)
	}
	if len(res.Conventions.NumberCollisions) != 1 || res.Conventions.NumberCollisions[0] != "002" {
		t.Errorf("collisions: %v", res.Conventions.NumberCollisions)
	}
	var related *Annotation
	for i, a := range res.Annotations {
		if a.Type == BadgeRelatedDecision && a.Ref == "ADR-021" {
			related = &res.Annotations[i]
		}
	}
	if related == nil {
		t.Fatalf("ADR-021 not surfaced as related: %+v", res.Annotations)
	}
	if related.Relation != RelationCandidate && related.Relation != VariantSupersedeCandidate {
		t.Errorf("relation: %q", related.Relation)
	}
	if !res.Signals.Material {
		t.Error("material should be true with a related decision")
	}
	if res.Guidance == "" || !strings.Contains(res.Guidance, "ox code search") {
		t.Errorf("guidance: %q", res.Guidance)
	}
	// context items for decisions must carry paste-ready cites
	for _, c := range res.Context {
		if c.Kind == "decision" && (c.Cite == nil || !strings.Contains(c.Cite.Comment, "SOURCE: sageox adr:")) {
			t.Errorf("decision item without cite: %+v", c)
		}
		if c.Kind == "murmur" && c.Cite != nil {
			t.Errorf("murmur must not carry a cite: %+v", c)
		}
	}
}

func TestRefsDetector(t *testing.T) {
	root := t.TempDir()
	writeCorpus(t, root, defaultCorpusFiles())
	env := &Env{GitRoot: root, Corpus: LoadCorpus(root, nil)}

	body := `# ADR-090: Draft

Per ADR-021 this is fine. But ADR-999 is phantom, and ADR-047 D9 names a
missing anchor (ADR-047 defines D1 and D4).
<!-- SOURCE: sageox adr:docs/adr/ADR-021-plan-context.md -->
<!-- SOURCE: sageox adr:docs/adr/nope.md -->
surfaced by SageOx · surfaced by SageOx · surfaced by SageOx
`
	in := Input{Raw: body, Record: ParseContent("draft.md", body)}
	anns, err := refsDetector{}.Detect(context.Background(), env, in)
	if err != nil {
		t.Fatal(err)
	}

	rules := map[string]int{}
	var whys []string
	for _, a := range anns {
		rules[a.Rule]++
		whys = append(whys, a.Ref+": "+a.Why)
	}
	if rules[RuleDanglingRef] != 3 {
		t.Errorf("want 3 dangling (ADR-999, D9 anchor, bad SOURCE path), got %d: %v", rules[RuleDanglingRef], whys)
	}
	if rules[RuleSageoxCreditOverflow] != 1 {
		t.Errorf("credit overflow not flagged: %v", whys)
	}
	joined := strings.Join(whys, "\n")
	if !strings.Contains(joined, "D9") || !strings.Contains(joined, "D1") {
		t.Errorf("anchor diagnostics should name missing + available anchors: %s", joined)
	}
	// valid refs (ADR-021 token + its SOURCE line) must produce no annotations
	if strings.Contains(joined, "ADR-021:") {
		t.Errorf("valid ref flagged: %s", joined)
	}
}

func TestConventionsDetector_TakenNumber(t *testing.T) {
	root := t.TempDir()
	writeCorpus(t, root, defaultCorpusFiles())
	env := &Env{GitRoot: root, Corpus: LoadCorpus(root, nil)}

	body := "# ADR-021: Colliding Draft\n\n## Context\n\nx\n"
	in := Input{Raw: body, Record: ParseContent("draft.md", body)}
	anns, err := conventionsDetector{}.Detect(context.Background(), env, in)
	if err != nil {
		t.Fatal(err)
	}
	taken := false
	for _, a := range anns {
		if a.Type == BadgeNumbering && a.Rule == RuleDuplicateNumber && strings.Contains(a.Why, "ADR-021-plan-context.md") {
			taken = true
		}
	}
	if !taken {
		t.Errorf("taken-number not flagged with holder path: %+v", anns)
	}
}

func TestNormalizeStatus(t *testing.T) {
	tests := map[string]string{
		"Accepted — merged via PR #621":  "Accepted",
		"Draft (Proposed) — awaiting":    "Draft",
		"Proposed":                       "Proposed",
		"  Accepted —  ":                 "Accepted",
		"Not adopted — deferred pending": "Not adopted",
	}
	for in, want := range tests {
		if got := normalizeStatus(in); got != want {
			t.Errorf("normalizeStatus(%q) = %q want %q", in, got, want)
		}
	}
}

func TestDriftDetector(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root                 // NEVER touch global git config
		cmd.Env = append(os.Environ(), // safe: git subprocess in a t.TempDir repo, identity via env only
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "commit.gpgsign", "false") // repo-local; host config may require a passphrase-protected key
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "main.go")
	run("commit", "-q", "-m", "one")
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main // changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "main.go")
	run("commit", "-q", "-m", "two")

	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	drBody := "# ADR-001: Uses Main\n\n**Status**: Accepted\n**Date**: " + yesterday + "\n\n## Context\n\nSee `main.go` for details.\n"
	drPath := filepath.Join(root, "docs", "adr", "ADR-001-x.md")
	if err := os.MkdirAll(filepath.Dir(drPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(drPath, []byte(drBody), 0o644); err != nil {
		t.Fatal(err)
	}

	in := Input{Path: drPath, Raw: drBody, Record: ParseContent(drPath, drBody)}
	anns, err := driftDetector{}.Detect(context.Background(), &Env{GitRoot: root}, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(anns) != 1 || anns[0].Type != BadgeDrift {
		t.Fatalf("drift annotations: %+v", anns)
	}
	if !strings.HasPrefix(anns[0].SourceURL, "commit:") || len(anns[0].Files) != 1 {
		t.Errorf("drift citation: %+v", anns[0])
	}

	// topic mode / missing date → fail-open nil
	if anns, _ := (driftDetector{}).Detect(context.Background(), &Env{GitRoot: root}, Input{Topic: "x"}); anns != nil {
		t.Errorf("topic mode should be nil: %+v", anns)
	}
}

func TestGuidanceBranches(t *testing.T) {
	conv := Conventions{NextNumber: 48, AmendmentMarker: "**Amendment (YYYY-MM-DD):**"}

	t.Run("zero context", func(t *testing.T) {
		g := buildGuidance(Input{Topic: "x"}, SignalSummary{}, conv, nil, nil)
		if !strings.Contains(g, "gap admitted beats a citation invented") {
			t.Errorf("zero-context lead missing: %q", g)
		}
	})
	t.Run("unresolved refs lead", func(t *testing.T) {
		g := buildGuidance(Input{Path: "a.md"}, SignalSummary{UnresolvedRefs: 2}, conv, nil, nil)
		if !strings.Contains(g, "do not resolve") {
			t.Errorf("unresolved lead missing: %q", g)
		}
	})
	t.Run("drift lead", func(t *testing.T) {
		anns := []Annotation{{Type: BadgeDrift}}
		g := buildGuidance(Input{Path: "a.md"}, SignalSummary{}, conv, anns, nil)
		if !strings.Contains(g, "drifted") {
			t.Errorf("drift lead missing: %q", g)
		}
	})
	t.Run("rich context + accepted amendment rule", func(t *testing.T) {
		body := "# ADR-001: X\n\n**Status**: Accepted\n"
		in := Input{Path: "a.md", Raw: body, Record: ParseContent("a.md", body)}
		items := []ContextItem{{Kind: "decision", Cite: &Cite{Comment: "<!-- SOURCE: sageox adr:x -->"}}}
		g := buildGuidance(in, SignalSummary{Related: 1}, conv, nil, items)
		if !strings.Contains(g, "team history") || !strings.Contains(g, "Amendment") || !strings.Contains(g, "VERBATIM") {
			t.Errorf("rich/update guidance incomplete: %q", g)
		}
	})
	// the credit cap is a standing rule in every branch
	g := buildGuidance(Input{Topic: "x"}, SignalSummary{}, conv, nil, nil)
	if !strings.Contains(g, "max 2 per DR") {
		t.Errorf("credit cap missing: %q", g)
	}
}
