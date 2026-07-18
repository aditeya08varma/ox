package plan

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeDR(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDecisionRetriever(t *testing.T) {
	root := t.TempDir()
	writeDR(t, root, "docs/adr/ADR-021-plan-context.md",
		"# ADR-021: Plan Context Not Inference\n\n**Status**: Accepted\n**Date**: 2026-06-03\n\n## Context\n\nox provides context for plan enrichment, the client does inference.\n")

	in := Input{Sections: []Section{{Heading: "Plan context inference enrichment"}}}
	items, err := decisionRetriever{}.Retrieve(context.Background(), in, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items: %+v", items)
	}
	it := items[0]
	if it.Kind != "adr" || it.Ref != "docs/adr/ADR-021-plan-context.md" || it.Score < 0.55 {
		t.Errorf("item: %+v", it)
	}

	// empty gitRoot fails open
	if items, _ := (decisionRetriever{}).Retrieve(context.Background(), in, ""); items != nil {
		t.Errorf("empty root should be nil: %+v", items)
	}
}

// The inline-marker regex must recognize BOTH basename conventions: mono's
// numeric ("051-foo.md") and this repo's prefixed ("ADR-021-foo.md").
func TestContextMarkers_ADRPrefixedBasenames(t *testing.T) {
	items := []ContextItem{
		{Kind: "adr", Ref: "docs/adr/ADR-021-plan-context.md", Title: "ADR-021"},
		{Kind: "adr", Ref: "docs/adr/051-consent-tooling.md", Title: "ADR-051"},
		{Kind: "session", Ref: "not-an-adr"},
	}
	markers := contextMarkers(items)
	if len(markers) != 2 {
		t.Fatalf("markers: %d", len(markers))
	}
	if !markers[0].re.MatchString("per ADR-021 we decided") {
		t.Error("prefixed-basename marker does not match ADR-021 prose")
	}
	if !markers[1].re.MatchString("see ADR 051 for background") {
		t.Error("numeric-basename marker does not match its prose token")
	}
}
