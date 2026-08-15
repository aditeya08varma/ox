package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/spf13/cobra"
)

func TestVizCommandCanonicalAndCompatibilitySurfaces(t *testing.T) {
	if vizCmd.Hidden {
		t.Error("top-level ox viz must be visible")
	}
	if !planVizCmd.Hidden {
		t.Error("ox plan viz compatibility command must stay hidden")
	}
	for _, cmd := range []*cobra.Command{vizCmd, planVizCmd} {
		for _, name := range []string{"suggest", "render", "lint", "pr"} {
			if child, _, err := cmd.Find([]string{name}); err != nil || child.Name() != name {
				t.Errorf("%s is missing %s: child=%v err=%v", cmd.CommandPath(), name, child, err)
			}
		}
	}
}

func TestVizPRGuidanceMakesRichPRsDiscoverable(t *testing.T) {
	t.Setenv("OX_USER_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	cmd, out := vizTestCommand()
	if err := runVizPR(cmd); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ox viz suggest",
		"GitHub-safe Mermaid",
		"Diagram Design",
		".context/pr-visuals/",
		"GitHub's PR editor",
		"github-user-attachment-url",
		"Keep technical PR visuals unbranded",
		"no SageOx wordmark, footer credit, or logo",
		"connectors never pass under text",
		"Conceptual clarity comes before design richness",
		"smallest truthful baseline",
		"If Mermaid is clearer, use Mermaid",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("PR visual workflow missing %q: %s", want, out.String())
		}
	}
}

func TestVizPRHonorsPersonalRichVisualOptOut(t *testing.T) {
	t.Setenv("OX_USER_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	if err := SetConfigValue("pr_visuals.rich", "off", ConfigLevelUser, ""); err != nil {
		t.Fatal(err)
	}
	cmd, out := vizTestCommand()
	if err := runVizPR(cmd); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "automated rich PNG/SVG PR-image workflow is disabled") {
		t.Fatalf("expected disabled guidance, got: %s", got)
	}
	if strings.Contains(out.String(), "Diagram Design") {
		t.Fatalf("rich authoring instructions must be absent when disabled: %s", out.String())
	}
	if !strings.Contains(out.String(), "ox viz catalog") || !strings.Contains(out.String(), "ox viz suggest") {
		t.Fatalf("disabled workflow must keep catalog access explicit: %s", out.String())
	}
	if config.PRVisualsRich("") {
		t.Fatal("personal opt-out did not resolve")
	}
}

func TestVizPRIntentRanksGitHubSafeMermaidFirst(t *testing.T) {
	cmd, out := vizTestCommand()
	if err := runVizPRIntent(cmd, "where does this shared destination land and how does interrupted apply recover?", true); err != nil {
		t.Fatal(err)
	}
	var got prVisualSuggestions
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("parse JSON: %v\n%s", err, out.String())
	}
	if !got.MermaidFirst || len(got.Suggestions) == 0 {
		t.Fatalf("PR suggestions missing Mermaid preference: %+v", got)
	}
	if got.Suggestions[0].Authoring != "mermaid" {
		t.Fatalf("first PR suggestion = %+v, want GitHub-safe Mermaid", got.Suggestions[0])
	}
	if !strings.Contains(got.Guidance, "Conceptual clarity is the gate") || !strings.Contains(got.Guidance, "smallest truthful baseline") {
		t.Fatalf("PR JSON omitted the conceptual-clarity gate: %+v", got)
	}
}

func TestVizListOneSuggestAndJSON(t *testing.T) {
	cmd, out := vizTestCommand()
	if err := runVizList(cmd, false); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "architecture") || !strings.Contains(got, "ox viz suggest") {
		t.Fatalf("catalog list is not actionable: %s", got)
	}

	cmd, out = vizTestCommand()
	if err := runVizOne(cmd, "architecture", true); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"id": "architecture"`, `"use":`, `"why":`, `"body":`,
		`"category": "diagram"`, `"authoring": "inline-svg"`,
		`"origin": "cathrynlavery/diagram-design@f3622cf"`,
		`"visual_contract":`, `"reviewer_question":`, `"conceptual_clarity":`, `"evidence_slots":`,
		`"composition":`, `"typography":`, `"variants":`,
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("pattern JSON missing %s: %s", want, out.String())
		}
	}

	cmd, out = vizTestCommand()
	if err := runVizOne(cmd, "architecture", false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Visual contract:", "Reviewer question:", "Conceptual clarity — must pass before styling:", "Canvas: 1600x1000", "Evidence slots:", "Composition:", "Connector grammar:", "Finishing pass:"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("plain pattern output omitted construction guidance %q: %s", want, out.String())
		}
	}

	cmd, out = vizTestCommand()
	if err := runVizSuggest(cmd, "branching validation gates fallback", 1, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "flowchart") || !strings.Contains(out.String(), "ox viz flowchart") {
		t.Fatalf("suggestion is not actionable: %s", out.String())
	}
}

func TestVizLintAdvisoryAndStrictModes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "diagram.svg")
	fragment := `<svg data-ox-viz="example" role="img" aria-labelledby="t d"><title id="t">Example</title><desc id="d">Example diagram</desc></svg>`
	if err := os.WriteFile(path, []byte(fragment), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd, _ := vizTestCommand()
	if err := runVizLint(cmd, path, false, false); err != nil {
		t.Fatalf("warnings must be advisory by default: %v", err)
	}
	cmd, _ = vizTestCommand()
	if err := runVizLint(cmd, path, true, false); err == nil {
		t.Fatal("strict mode must fail on editorial warnings")
	}
}

func TestVizLintPNGUsesImageChecksWithoutParsingBinaryAsHTML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "diagram.png")
	img := image.NewPaletted(image.Rect(0, 0, 32, 18), color.Palette{color.White, color.Black})
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	cmd, out := vizTestCommand()
	if err := runVizLint(cmd, path, true, false); err != nil {
		t.Fatalf("clean PNG must pass strict image lint: %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "viz.missing") {
		t.Fatalf("PNG bytes were parsed as HTML: %s", out.String())
	}
}

func vizTestCommand() (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	return cmd, out
}
