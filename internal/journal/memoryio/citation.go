package memoryio

// Citation mirrors one entry in a rendered "## Sources" section. The four
// fields track team-memory-journal.md §4.3: Num is the 1-based marker used
// in [N] references, Label is the human-scannable text, URL is the
// resolvable link (empty when the source has no clickable URL), and Key is
// the dedup identifier — URL if one is present, otherwise the stable
// source_ref or a "label:<text>" fallback.
//
// This shape is intentionally identical to cmd/ox's private `citation`
// struct so cmd/ox/journal_citations_bridge.go can convert between the two
// with no lossy mapping.
type Citation struct {
	Num   int
	Label string
	URL   string
	Key   string
}
