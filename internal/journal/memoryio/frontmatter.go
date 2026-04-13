package memoryio

import "strings"

// ParseFrontmatterSources extracts the `sources:` list from YAML
// frontmatter at the top of a memory entry. It mirrors parseDailySources
// at cmd/ox/distill.go:1516 so the reader can consume it without
// importing cmd/ox; the canonical copy stays in cmd/ox so the existing
// distill pipeline and its regression tests are undisturbed. The two
// copies are pinned to the same behavior by
// TestJournalFrontmatterBridge_CanonicalParserRoundTrip in
// cmd/ox/journal_citations_bridge_test.go.
//
// Expected shape:
//
//	---
//	sources:
//	  - memory/.github-facts/2026-03-30-xxx.jsonl
//	  - memory/.session-facts/2026-03-30/yyy.jsonl
//	---
//
// Returns nil for content that has no leading frontmatter block.
func ParseFrontmatterSources(content string) []string {
	if !strings.HasPrefix(content, "---\n") {
		return nil
	}
	end := strings.Index(content[4:], "\n---")
	if end < 0 {
		return nil
	}
	frontmatter := content[4 : 4+end]

	var sources []string
	inSources := false
	for _, line := range strings.Split(frontmatter, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "sources:" {
			inSources = true
			continue
		}
		if inSources && strings.HasPrefix(line, "  - ") {
			sources = append(sources, strings.TrimPrefix(trimmed, "- "))
		} else if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			inSources = false
		}
	}
	return sources
}

// StripFrontmatter returns content with any leading YAML frontmatter block
// removed. The block is the region between a leading "---\n" and the
// matching "\n---" closer, and a single following newline (if present) is
// consumed as well so the returned body starts at the first real line.
//
// Content that does not start with a frontmatter block is returned
// unchanged. An internal `---` horizontal rule inside the body is
// preserved — only the leading delimiter pair is treated as a fence.
//
// Specified in team-memory-journal.md §3.5.
func StripFrontmatter(content string) string {
	if !strings.HasPrefix(content, "---\n") {
		return content
	}
	end := strings.Index(content[4:], "\n---")
	if end < 0 {
		return content
	}
	rest := content[4+end+len("\n---"):]
	rest = strings.TrimPrefix(rest, "\n")
	return rest
}
