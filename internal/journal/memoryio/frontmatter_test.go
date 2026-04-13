package memoryio

import (
	"reflect"
	"testing"
)

// TestParseFrontmatterSources — regression coverage for the frontmatter
// source list extractor. Mirrors the canonical parseDailySources in
// cmd/ox/distill.go:1516. The two copies are held to the same behavior
// by TestJournalFrontmatterBridge_CanonicalParserRoundTrip in
// cmd/ox/journal_citations_bridge_test.go — any drift between them
// fails that bridge test.
func TestParseFrontmatterSources(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "three sources",
			in: "---\nsources:\n" +
				"  - memory/.discussion-facts/2026-03-10-abc.jsonl\n" +
				"  - memory/.github-facts/2026-03-10-def.jsonl\n" +
				"  - memory/.observations/2026-03-10/ghi.jsonl\n" +
				"---\n# Daily Memory — 2026-03-10\nBody.\n",
			want: []string{
				"memory/.discussion-facts/2026-03-10-abc.jsonl",
				"memory/.github-facts/2026-03-10-def.jsonl",
				"memory/.observations/2026-03-10/ghi.jsonl",
			},
		},
		{
			name: "empty sources list",
			in:   "---\nsources:\n---\n# Heading\n",
			want: nil,
		},
		{
			name: "no sources key",
			in:   "---\ntitle: test\n---\n# Heading\n",
			want: nil,
		},
		{
			name: "sources followed by non-indented key ends block",
			in: "---\nsources:\n" +
				"  - memory/.a.jsonl\n" +
				"  - memory/.b.jsonl\n" +
				"title: test\n" +
				"---\n# Heading\n",
			want: []string{"memory/.a.jsonl", "memory/.b.jsonl"},
		},
		{
			name: "no frontmatter returns nil",
			in:   "# Heading\nBody.\n",
			want: nil,
		},
		{
			name: "unterminated frontmatter returns nil",
			in:   "---\nsources:\n  - memory/.a.jsonl\n",
			want: nil,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ParseFrontmatterSources(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseFrontmatterSources = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestStripFrontmatter — new in Unit 2. Cases cover the four branches
// called out in team-memory-journal.md §3.5: no frontmatter, present
// frontmatter, present frontmatter with an internal "---" horizontal rule
// in the body (the rule must not trip the closer scanner), and an empty
// body after the closing delimiter.
func TestStripFrontmatter(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"no frontmatter", "# body\nmore\n", "# body\nmore\n"},
		{"empty input", "", ""},
		{"frontmatter then body", "---\nsources:\n  - a\n---\n# heading\ntext\n", "# heading\ntext\n"},
		{"frontmatter then empty body", "---\nkey: val\n---\n", ""},
		{"internal horizontal rule preserved", "---\nkey: val\n---\n# heading\n\n---\n\nmore\n", "# heading\n\n---\n\nmore\n"},
		{"leading delimiter with no closer passes through", "---\nfoo\n", "---\nfoo\n"},
		{"frontmatter closes without trailing newline", "---\nsources:\n  - a\n---\n# done", "# done"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := StripFrontmatter(tc.in)
			if got != tc.want {
				t.Fatalf("StripFrontmatter(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
