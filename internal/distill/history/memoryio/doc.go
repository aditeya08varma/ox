// Package memoryio is the shared on-disk format helper for memory/daily,
// memory/weekly, and memory/monthly entries. It contains the pure string
// parsers the reader (internal/distill/history/read) needs without importing the
// cmd/ox main package.
//
// This package factors three pieces out of cmd/ox/distill.go and
// cmd/ox/distill_citations.go:
//
//   - ParseFrontmatterSources — matches parseDailySources in
//     cmd/ox/distill.go:1516. The canonical copy stays in cmd/ox so the
//     existing distill pipeline and its regression tests are untouched;
//     this copy feeds the reader and is covered by its own table test.
//   - StripFrontmatter — specified in team-memory-journal.md §3.5. The
//     reader calls this when wantBody is true.
//   - ParseSourcesSection — matches parseSourcesSection in
//     cmd/ox/distill_citations.go:575. Duplicated (not delegated) so the
//     reader stays standalone: cmd/ox must not be importable from the
//     reader package.
//
// Both duplicated parsers are pinned to their canonical cmd/ox copies by
// round-trip tests in cmd/ox/distill_history_citations_bridge_test.go
// (TestDistillHistoryFrontmatterBridge_CanonicalParserRoundTrip and
// TestDistillHistoryCitationsBridge_CanonicalParserRoundTrip). Any drift
// between a canonical parser and its memoryio counterpart fails those
// tests.
//
// The Citation struct exposes only the four fields the reader surfaces
// (§4.3 in the spec). When future work needs to share more on-disk
// metadata between the reader and the distill pipeline, it lives here.
package memoryio
