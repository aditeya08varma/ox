package memoryio

// Canonical filename regex patterns for memory/daily, memory/weekly, and
// memory/monthly entries. These constants mirror the corresponding vars
// in cmd/ox/distill.go (dailyDateRe, weeklyRe, monthlyRe). Exporting them
// here gives the reader package (internal/journal/read) one shared source
// of truth it can compile into its own regex vars, and gives a bridge
// test in cmd/ox a fixed target to pin distill.go's copies against.
//
// Drift detection:
//
//   - internal/journal/read/list.go compiles these constants directly, so
//     it cannot drift from memoryio by construction.
//   - cmd/ox/journal_citations_bridge_test.go pins cmd/ox's dailyDateRe /
//     weeklyRe / monthlyRe .String() values against these constants. A
//     future edit to distill.go's regexes that forgets to update memoryio
//     fails that test immediately.
//
// The patterns are duplicated (not delegated) because cmd/ox is package
// main and cannot be imported. Keeping the canonical strings here, where
// both sides can reach them, is the cheapest way to keep drift surfaced.
const (
	// DailyDatePattern matches the YYYY-MM-DD prefix of daily memory
	// filenames. Handles both old naming (2026-03-10.md) and new UUID7
	// naming (2026-03-10-{uuid7}.md).
	DailyDatePattern = `^(\d{4}-\d{2}-\d{2})`

	// WeeklyPattern matches YYYY-WXX weekly filenames with an optional
	// UUID7 suffix.
	WeeklyPattern = `^(\d{4})-W(\d{2})`

	// MonthlyPattern matches YYYY-MM monthly filenames with an optional
	// UUID7 suffix, anchored on the .md extension.
	MonthlyPattern = `^(\d{4}-\d{2})(?:-[0-9a-f-]+)?\.md$`
)
