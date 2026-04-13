package main

import "github.com/sageox/ox/internal/distill/history/memoryio"

// toMemoryIOCitation converts one cmd/ox citation into the memoryio
// Citation shape the reader surface consumes. The two types carry the
// same four fields (Num, Label, URL, Key) — the conversion exists so
// cmd/ox code that already holds a []citation can hand it to the reader
// layer without the reader having to import cmd/ox.
//
// This is intentionally a pure copy with no field synthesis: any future
// drift between citation and memoryio.Citation must be surfaced by the
// compiler here, and by the round-trip test in
// distill_history_citations_bridge_test.go.
func toMemoryIOCitation(c citation) memoryio.Citation {
	return memoryio.Citation{
		Num:   c.Num,
		Label: c.Label,
		URL:   c.URL,
		Key:   c.Key,
	}
}

// toMemoryIOCitations is the slice form of toMemoryIOCitation. Returns a
// freshly allocated slice (never a re-slice of the input) so the caller
// can freely mutate either side.
func toMemoryIOCitations(cs []citation) []memoryio.Citation {
	if len(cs) == 0 {
		return nil
	}
	out := make([]memoryio.Citation, len(cs))
	for i, c := range cs {
		out[i] = toMemoryIOCitation(c)
	}
	return out
}
