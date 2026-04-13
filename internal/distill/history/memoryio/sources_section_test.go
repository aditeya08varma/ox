package memoryio

import (
	"reflect"
	"testing"
)

// TestParseSourcesSection pins the behavior of the Sources-section parser
// so a future edit of either copy (this one or cmd/ox's canonical) cannot
// silently diverge. Each case exercises one shape the reader may see on
// disk in memory/daily/*.md.
func TestParseSourcesSection(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want []Citation
	}{
		{
			name: "numbered entries with ref links",
			in: "# Daily\n\nBody. [1] [2]\n\n## Sources\n\n" +
				"1. [Discussion: 2026-03-10 — Architecture sync][1]\n" +
				"2. [sageox/ox#152][2]\n\n" +
				"[1]: https://example.com/discussion\n" +
				"[2]: https://github.com/sageox/ox/pull/152\n",
			want: []Citation{
				{
					Num:   1,
					Label: "Discussion: 2026-03-10 — Architecture sync",
					URL:   "https://example.com/discussion",
					Key:   "https://example.com/discussion",
				},
				{
					Num:   2,
					Label: "sageox/ox#152",
					URL:   "https://github.com/sageox/ox/pull/152",
					Key:   "https://github.com/sageox/ox/pull/152",
				},
			},
		},
		{
			name: "plain entry with ref comment carries stable key",
			in: "# Daily\n\n## Sources\n\n" +
				"1. Session: 2026-03-11 — stateless refactor <!-- ref:sessions/2026-03-11T09-00-ryan-Oxabcd -->\n",
			want: []Citation{
				{
					Num:   1,
					Label: "Session: 2026-03-11 — stateless refactor",
					Key:   "sessions/2026-03-11T09-00-ryan-Oxabcd",
				},
			},
		},
		{
			name: "plain entry without ref comment synthesizes label key",
			in: "# Daily\n\n## Sources\n\n" +
				"1. Observation 2026-03-10\n",
			want: []Citation{
				{Num: 1, Label: "Observation 2026-03-10", Key: "label:Observation 2026-03-10"},
			},
		},
		{
			name: "escaped brackets in label survive round trip",
			in: "# Daily\n\n## Sources\n\n" +
				`1. [Foo \[bar\]][1]` + "\n\n" +
				"[1]: https://example.com/foo\n",
			want: []Citation{
				{Num: 1, Label: "Foo [bar]", URL: "https://example.com/foo", Key: "https://example.com/foo"},
			},
		},
		{
			name: "no sources section returns nil",
			in:   "# Daily\n\nJust prose, no Sources section.\n",
			want: nil,
		},
		{
			name: "stops at next h2 heading",
			in: "# Daily\n\n## Sources\n\n" +
				"1. [Real cite][1]\n\n" +
				"[1]: https://example.com/real\n\n" +
				"## Appendix\n\n" +
				"1. Not a citation\n\n" +
				"[2]: https://example.com/bogus\n",
			want: []Citation{
				{Num: 1, Label: "Real cite", URL: "https://example.com/real", Key: "https://example.com/real"},
			},
		},
		{
			name: "fenced code block with a fake heading is ignored",
			in: "# Daily\n\n" +
				"```markdown\n" +
				"## Sources\n\n" +
				"1. [Fake][99]\n" +
				"```\n\n" +
				"## Sources\n\n" +
				"1. [Real][1]\n\n" +
				"[1]: https://example.com/real\n",
			want: []Citation{
				{Num: 1, Label: "Real", URL: "https://example.com/real", Key: "https://example.com/real"},
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ParseSourcesSection(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseSourcesSection: got %#v, want %#v", got, tc.want)
			}
		})
	}
}
