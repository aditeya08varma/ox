package memoryio

import (
	"regexp"
	"strconv"
	"strings"
)

// ParseSourcesSection extracts Citation entries from a "## Sources" block
// previously written by cmd/ox's renderSourcesSection. It mirrors
// parseSourcesSection at cmd/ox/distill_citations.go:575 and is pinned to
// the canonical copy by a round-trip test in
// cmd/ox/distill_history_citations_bridge_test.go. The reader duplicates the
// logic rather than calling the original so the reader stays standalone
// per the Unit 1 invariant.
//
// Returns nil when the input has no Sources section. The parser is
// tolerant: entries with reference-style links are parsed fully, bare-text
// entries (no URL) are returned with an empty URL, and malformed lines are
// skipped. A trailing "<!-- ref:<key> -->" HTML comment on any entry is
// honored as the stable Key — this lets two label-only sources that share
// a label stay distinct on cross-layer merges.
func ParseSourcesSection(md string) []Citation {
	headerEnd := findSourcesHeadingEnd(md)
	if headerEnd < 0 {
		return nil
	}
	rest := md[headerEnd:]
	if next := nextH2HeadingIndex(rest); next >= 0 {
		rest = rest[:next]
	}

	urls := make(map[int]string)
	for _, line := range strings.Split(rest, "\n") {
		line = strings.TrimSpace(line)
		if m := refLinkDefRe.FindStringSubmatch(line); m != nil {
			if num, err := strconv.Atoi(m[1]); err == nil {
				urls[num] = m[2]
			}
		}
	}

	var cites []Citation
	for _, line := range strings.Split(rest, "\n") {
		line = strings.TrimSpace(line)
		var refKey string
		if m := refCommentRe.FindStringSubmatchIndex(line); m != nil {
			refKey = strings.TrimSpace(line[m[2]:m[3]])
			line = strings.TrimSpace(line[:m[0]])
		}
		if m := numberedRefEntryRe.FindStringSubmatch(line); m != nil {
			num, _ := strconv.Atoi(m[1])
			label := unescapeMarkdownLinkText(m[2])
			refNum, _ := strconv.Atoi(m[3])
			url := urls[refNum]
			key := refKey
			if key == "" {
				key = url
			}
			if key == "" {
				key = "label:" + label
			}
			cites = append(cites, Citation{
				Num:   num,
				Label: label,
				URL:   url,
				Key:   key,
			})
			continue
		}
		if m := numberedPlainEntryRe.FindStringSubmatch(line); m != nil {
			num, _ := strconv.Atoi(m[1])
			label := strings.TrimSpace(m[2])
			key := refKey
			if key == "" {
				key = "label:" + label
			}
			cites = append(cites, Citation{
				Num:   num,
				Label: label,
				Key:   key,
			})
		}
	}
	return cites
}

// findSourcesHeadingEnd returns the byte offset just after the first
// "## Sources" heading that lies outside any fenced code block. A body
// that quotes the distilled format inside a markdown code sample would
// otherwise match the fence-internal heading and slice from the wrong
// region.
func findSourcesHeadingEnd(md string) int {
	cursor := 0
	insideCode := false
	for cursor < len(md) {
		nl := strings.IndexByte(md[cursor:], '\n')
		var line string
		var lineEnd int
		if nl < 0 {
			line = md[cursor:]
			lineEnd = len(md)
		} else {
			line = md[cursor : cursor+nl]
			lineEnd = cursor + nl + 1
		}
		if strings.HasPrefix(line, "```") {
			insideCode = !insideCode
		} else if !insideCode {
			if line == "## Sources" || (strings.HasPrefix(line, "## Sources") && strings.TrimSpace(line[len("## Sources"):]) == "") {
				if nl < 0 {
					return len(md)
				}
				return cursor + nl + 1
			}
		}
		cursor = lineEnd
	}
	return -1
}

// nextH2HeadingIndex returns the byte offset of the next "## " heading in
// s, or -1 if none. Used to bound the Sources section at the next H2 so
// an appendix following the citations does not leak into the parse.
//
// The offset-0 case matters: ParseSourcesSection passes in `rest`, which
// begins immediately after the "## Sources" line. An empty Sources
// section directly followed by "## Appendix" (no citations, no blank
// line) leaves `rest` starting with "## ", and without the offset-0
// check nextH2HeadingIndex would return -1 — causing the parser to
// swallow the appendix as citation content.
func nextH2HeadingIndex(s string) int {
	if strings.HasPrefix(s, "## ") {
		return 0
	}
	if i := strings.Index(s, "\n## "); i >= 0 {
		return i
	}
	return -1
}

// unescapeMarkdownLinkText reverses the bracket escaping applied by
// cmd/ox's escapeMarkdownLinkText so a label like `Foo \[bar\]` survives
// the round trip back to `Foo [bar]`.
func unescapeMarkdownLinkText(s string) string {
	return strings.NewReplacer(
		`\[`, `[`,
		`\]`, `]`,
	).Replace(s)
}

var (
	refLinkDefRe         = regexp.MustCompile(`^\[(\d+)\]:\s*(\S+)\s*$`)
	numberedRefEntryRe   = regexp.MustCompile(`^(\d+)\.\s+\[((?:\\\[|\\\]|[^\[\]])+)\]\[(\d+)\]\s*$`)
	numberedPlainEntryRe = regexp.MustCompile(`^(\d+)\.\s+(.+)$`)
	refCommentRe         = regexp.MustCompile(`\s*<!--\s*ref:([^>]*?)\s*-->\s*$`)
)
