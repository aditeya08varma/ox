package plan

import (
	"regexp"
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

// semanticVisualClassTokens are authored containers whose CSS gives their
// content visual structure. Unlike generated renderer classes, an empty or
// hidden container is not evidence of an explanatory visual.
var semanticVisualClassTokens = map[string]struct{}{
	"hero-map": {}, "runtime-map": {}, "system-map": {}, "architecture-map": {},
	"sequence": {}, "timeline": {}, "states": {}, "access-map": {},
	"mock-shell": {}, "delete-map": {}, "flow-map": {}, "layer-stack": {},
}

var decorativeClassTokens = map[string]struct{}{
	"ox-marker": {}, "ox-ico": {}, "toc-brand": {}, "wm": {},
}

var stylesheetRuleRe = regexp.MustCompile(`(?s)([^{}]+)\{([^{}]*)\}`)

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
	hidingSelectors := stylesheetHidingSelectors(doc)
	var walk func(*xhtml.Node, bool, bool) bool
	walk = func(n *xhtml.Node, decorativeAncestor, hiddenAncestor bool) bool {
		classes := classTokens(n)
		decorative := decorativeAncestor || hasAnyClass(classes, decorativeClassTokens) || hasAttr(n, "data-ox-wordmark")
		hidden := hiddenAncestor || visuallyHidden(n) || stylesheetHidesNode(n, hidingSelectors)
		if !decorative && !hidden && hasAnyClass(classes, visualClassTokens) &&
			(!hasAnyClass(classes, semanticVisualClassTokens) || meaningfulSemanticVisual(n)) {
			return true
		}
		if !decorative && !hidden && n.Type == xhtml.ElementNode && n.Data == "svg" && visibleExplanatorySVG(n) {
			return true
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if walk(c, decorative, hidden) {
				return true
			}
		}
		return false
	}
	return walk(doc, false, false)
}

func meaningfulSemanticVisual(n *xhtml.Node) bool {
	if strings.TrimSpace(textContent(n)) != "" {
		return true
	}
	return attrVal(n, "role") == "img" && strings.TrimSpace(attrVal(n, "aria-label")) != ""
}

func visuallyHidden(n *xhtml.Node) bool {
	if n.Type != xhtml.ElementNode {
		return false
	}
	if hasAttr(n, "hidden") || hasAttr(n, "inert") || strings.EqualFold(attrVal(n, "aria-hidden"), "true") {
		return true
	}
	style := strings.NewReplacer(" ", "", "\t", "", "\n", "", "\r", "").Replace(strings.ToLower(attrVal(n, "style")))
	return strings.Contains(style, "display:none") || strings.Contains(style, "visibility:hidden") || strings.Contains(style, "content-visibility:hidden")
}

func stylesheetHidingSelectors(doc *xhtml.Node) []string {
	var selectors []string
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if n.Type == xhtml.ElementNode && n.Data == "style" {
			for _, match := range stylesheetRuleRe.FindAllStringSubmatch(textContent(n), -1) {
				if !hidesByDeclaration(match[2]) {
					continue
				}
				for _, selector := range strings.Split(match[1], ",") {
					if selector = strings.TrimSpace(selector); selector != "" {
						selectors = append(selectors, selector)
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return selectors
}

func hidesByDeclaration(declarations string) bool {
	compact := strings.NewReplacer(" ", "", "\t", "", "\n", "", "\r", "").Replace(strings.ToLower(declarations))
	return strings.Contains(compact, "display:none") || strings.Contains(compact, "visibility:hidden") || strings.Contains(compact, "content-visibility:hidden")
}

func stylesheetHidesNode(n *xhtml.Node, selectors []string) bool {
	for _, selector := range selectors {
		if simpleSelectorMatches(n, selector) {
			return true
		}
	}
	return false
}

// simpleSelectorMatches supports a selector's final compound, which is the
// element directly affected by its declaration. It intentionally ignores
// pseudo-classes and attribute selectors: neither proves a static page hides
// the hero, so the lint remains conservative outside its bounded CSS model.
func simpleSelectorMatches(n *xhtml.Node, selector string) bool {
	selector = strings.TrimSpace(selector)
	if selector == "" || strings.HasPrefix(selector, "@") {
		return false
	}
	if i := strings.LastIndexAny(selector, " \t\n\r>+~"); i >= 0 {
		selector = selector[i+1:]
	}
	if selector == "" || strings.ContainsAny(selector, ":[") {
		return false
	}
	classes := classTokens(n)
	for i := 0; i < len(selector); {
		switch selector[i] {
		case '.':
			j := i + 1
			for j < len(selector) && cssIdentifierByte(selector[j]) {
				j++
			}
			if j == i+1 {
				return false
			}
			if _, ok := classes[selector[i+1:j]]; !ok {
				return false
			}
			i = j
		case '#':
			j := i + 1
			for j < len(selector) && cssIdentifierByte(selector[j]) {
				j++
			}
			if j == i+1 || attrVal(n, "id") != selector[i+1:j] {
				return false
			}
			i = j
		case '*':
			i++
		default:
			j := i
			for j < len(selector) && cssIdentifierByte(selector[j]) {
				j++
			}
			if j == i || n.Data != selector[i:j] {
				return false
			}
			i = j
		}
	}
	return true
}

func cssIdentifierByte(b byte) bool {
	return b == '-' || b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
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
