package main

import (
	"reflect"
	"testing"

	"github.com/sageox/ox/internal/distill/history/memoryio"
)

// TestDistillHistoryCitationsBridge_NoFieldLoss — the memoryio.Citation struct
// and cmd/ox's private citation struct must stay shape-compatible. If a
// future edit adds a field to one side and forgets the other, the
// conversion silently drops data. This test round-trips a hand-built
// []citation through toMemoryIOCitations and asserts the result matches
// a hand-built []memoryio.Citation.
//
// Failure prevented: a read-surface consumer sees a stale Key/URL/Label
// because the bridge conversion dropped a field after a struct edit.
func TestDistillHistoryCitationsBridge_NoFieldLoss(t *testing.T) {
	t.Parallel()
	in := []citation{
		{Num: 1, Label: "Discussion: 2026-03-10", URL: "https://example.com/d", Key: "https://example.com/d"},
		{Num: 2, Label: "sageox/ox#152", URL: "https://github.com/sageox/ox/pull/152", Key: "https://github.com/sageox/ox/pull/152"},
		{Num: 3, Label: "Session: 2026-03-11 — refactor", Key: "sessions/2026-03-11T09-00-ryan-Oxabcd"},
	}
	want := []memoryio.Citation{
		{Num: 1, Label: "Discussion: 2026-03-10", URL: "https://example.com/d", Key: "https://example.com/d"},
		{Num: 2, Label: "sageox/ox#152", URL: "https://github.com/sageox/ox/pull/152", Key: "https://github.com/sageox/ox/pull/152"},
		{Num: 3, Label: "Session: 2026-03-11 — refactor", Key: "sessions/2026-03-11T09-00-ryan-Oxabcd"},
	}
	got := toMemoryIOCitations(in)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("toMemoryIOCitations dropped a field:\n got=%#v\nwant=%#v", got, want)
	}
}

// TestDistillHistoryCitationsBridge_CanonicalParserRoundTrip takes a daily file
// rendered by cmd/ox's own renderSourcesSection, parses it through both
// parsers (cmd/ox's canonical parseSourcesSection and the reader's
// memoryio.ParseSourcesSection via the bridge), and asserts they produce
// the same output. This pins the two copies together: any future drift
// between distill_citations.go and internal/distill/history/memoryio/
// sources_section.go fails the test.
//
// Failure prevented: the reader silently emits different Citations than
// the writer committed, because the duplicated parser in memoryio has
// drifted from the canonical one.
func TestDistillHistoryCitationsBridge_CanonicalParserRoundTrip(t *testing.T) {
	t.Parallel()
	cites := []citation{
		{Num: 1, Label: "Discussion: 2026-03-10", URL: "https://example.com/d", Key: "https://example.com/d"},
		{Num: 2, Label: "sageox/ox#152", URL: "https://github.com/sageox/ox/pull/152", Key: "https://github.com/sageox/ox/pull/152"},
		{Num: 3, Label: "Session: 2026-03-11 — refactor", Key: "sessions/2026-03-11T09-00-ryan-Oxabcd"},
	}
	rendered := renderSourcesSection(cites)
	doc := "# Daily — 2026-03-10\n\nBody. [1] [2]\n\n" + rendered

	canonicalParsed := toMemoryIOCitations(parseSourcesSection(doc))
	memoryioParsed := memoryio.ParseSourcesSection(doc)

	if !reflect.DeepEqual(canonicalParsed, memoryioParsed) {
		t.Fatalf("canonical and memoryio parsers diverged:\ncanonical=%#v\nmemoryio =%#v",
			canonicalParsed, memoryioParsed)
	}
}

// TestDistillHistoryRegexBridge_CanonicalPatternsMatch pins cmd/ox's canonical
// filename regexes (distill.go:40/43/46) to the shared memoryio
// constants that the standalone reader (internal/distill/history/read) compiles
// from. If a future edit changes distill.go's regex literal without
// updating internal/distill/history/memoryio/patterns.go, the reader and the
// distill pipeline will disagree on which filenames are valid and this
// test fails immediately.
//
// Failure prevented: the reader silently classifies a filename the
// distill pipeline accepts (or vice versa) because a regex was tweaked
// on one side only, so a listing under ox distill history list omits an entry
// that is present on disk.
func TestDistillHistoryRegexBridge_CanonicalPatternsMatch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		got    string
		want   string
		source string
	}{
		{"dailyDateRe", dailyDateRe.String(), memoryio.DailyDatePattern, "cmd/ox/distill.go:40"},
		{"weeklyRe", weeklyRe.String(), memoryio.WeeklyPattern, "cmd/ox/distill.go:43"},
		{"monthlyRe", monthlyRe.String(), memoryio.MonthlyPattern, "cmd/ox/distill.go:46"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s at %s drifted:\n  got : %q\n  want: %q (memoryio.%s)",
				tc.name, tc.source, tc.got, tc.want, tc.name)
		}
	}
}

// TestDistillHistoryFrontmatterBridge_CanonicalParserRoundTrip pins cmd/ox's
// canonical parseDailySources (distill.go:1516) against the reader's
// memoryio.ParseFrontmatterSources. Both copies must produce byte-equal
// output for every frontmatter shape the distill pipeline writes and
// every shape the reader might encounter on disk. Cases cover:
//
//   - the happy path (two sources)
//   - an empty sources list
//   - sources followed by a non-indented key (block terminator)
//   - unterminated frontmatter (returns nil from both)
//   - no leading frontmatter block
//
// Failure prevented: a future edit to parseDailySources or
// memoryio.ParseFrontmatterSources lands without touching the other
// copy, so the reader silently surfaces a different source list than
// the one the writer committed. The bridge test fires immediately on
// drift.
func TestDistillHistoryFrontmatterBridge_CanonicalParserRoundTrip(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
	}{
		{
			name: "happy path two sources",
			in: "---\nsources:\n" +
				"  - memory/.github-facts/2026-03-30-xxx.jsonl\n" +
				"  - memory/.session-facts/2026-03-30/yyy.jsonl\n" +
				"---\n# Daily Memory — 2026-03-30\nBody.\n",
		},
		{
			name: "empty sources list",
			in:   "---\nsources:\n---\n# Heading\n",
		},
		{
			name: "sources then non-indented key ends block",
			in: "---\nsources:\n" +
				"  - memory/.a.jsonl\n" +
				"  - memory/.b.jsonl\n" +
				"title: test\n" +
				"---\n# Heading\n",
		},
		{
			name: "unterminated frontmatter",
			in:   "---\nsources:\n  - memory/.a.jsonl\n",
		},
		{
			name: "no frontmatter",
			in:   "# Heading\nBody.\n",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			canonical := parseDailySources(tc.in)
			fromMemoryio := memoryio.ParseFrontmatterSources(tc.in)
			if !reflect.DeepEqual(canonical, fromMemoryio) {
				t.Fatalf("canonical and memoryio frontmatter parsers diverged:\ncanonical=%#v\nmemoryio =%#v",
					canonical, fromMemoryio)
			}
		})
	}
}
