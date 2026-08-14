package plan

import (
	"strings"

	xhtml "golang.org/x/net/html"
)

var visualClassTokens = map[string]struct{}{
	"mermaid": {}, "swim": {}, "barc": {}, "linec": {}, "riskm": {},
	"statrow": {}, "ftree": {}, "heat": {}, "multiples": {}, "spark": {},
	"ba": {}, "pbar-fig": {}, "pmapv": {}, "donut": {}, "radar": {},
	"quad": {}, "tmap": {}, "sankey": {}, "chord": {}, "device": {},
	// Authored architecture pages use semantic containers rather than the
	// generated renderer's chart vocabulary. These names describe information
	// structure, not decoration, and cover the system-map patterns ox teaches.
	"hero-map": {}, "runtime-map": {}, "system-map": {}, "architecture-map": {},
	"sequence": {}, "timeline": {}, "states": {}, "access-map": {},
	"mock-shell": {}, "delete-map": {}, "flow-map": {}, "layer-stack": {},
}

var decorativeClassTokens = map[string]struct{}{
	"ox-marker": {}, "ox-ico": {}, "toc-brand": {}, "wm": {},
}

// meaningfulVisualPresent recognizes an explanatory visual while ignoring the
// hidden OX symbol sprite, wordmarks, and marker icons injected as chrome. A raw
// <svg> is intentionally insufficient: authored diagrams need an accessible
// role/label (or data-ox-viz), while generated charts are recognized by their
// semantic container class.
func meaningfulVisualPresent(src string) bool {
	doc, err := xhtml.Parse(strings.NewReader(src))
	if err != nil {
		return false
	}
	var walk func(*xhtml.Node, bool) bool
	walk = func(n *xhtml.Node, decorativeAncestor bool) bool {
		classes := classTokens(n)
		decorative := decorativeAncestor || hasAnyClass(classes, decorativeClassTokens) || hasAttr(n, "data-ox-wordmark")
		if !decorative && hasAnyClass(classes, visualClassTokens) {
			return true
		}
		if !decorative && n.Type == xhtml.ElementNode && n.Data == "svg" && visibleExplanatorySVG(n) {
			return true
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if walk(c, decorative) {
				return true
			}
		}
		return false
	}
	return walk(doc, false)
}

func visibleExplanatorySVG(n *xhtml.Node) bool {
	if strings.EqualFold(attrVal(n, "aria-hidden"), "true") || attrVal(n, "width") == "0" || attrVal(n, "height") == "0" {
		return false
	}
	return attrVal(n, "role") == "img" || hasAttr(n, "data-ox-viz")
}

func classTokens(n *xhtml.Node) map[string]struct{} {
	out := map[string]struct{}{}
	for _, token := range strings.Fields(attrVal(n, "class")) {
		out[token] = struct{}{}
	}
	return out
}

func hasAnyClass(got, wanted map[string]struct{}) bool {
	for token := range got {
		if _, ok := wanted[token]; ok {
			return true
		}
	}
	return false
}

// hasImplementationDisclosure requires material plans to preserve implementer
// depth without charging it to the approver's first scan. Native <details> is
// the portable disclosure primitive; leaving it closed is part of the contract.
func hasImplementationDisclosure(src string) bool {
	doc, err := xhtml.Parse(strings.NewReader(src))
	if err != nil {
		return false
	}
	var walk func(*xhtml.Node) bool
	walk = func(n *xhtml.Node) bool {
		if n.Type == xhtml.ElementNode && n.Data == "details" && !hasAttr(n, "open") {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type != xhtml.ElementNode || c.Data != "summary" {
					continue
				}
				summary := strings.ToLower(strings.TrimSpace(textContent(c)))
				if strings.Contains(summary, "implementation") || strings.Contains(summary, "technical detail") || strings.Contains(summary, "file-by-file") {
					return true
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if walk(c) {
				return true
			}
		}
		return false
	}
	return walk(doc)
}
