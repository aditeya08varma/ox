// Package read is the read-only journal reader for memory/daily,
// memory/weekly, and memory/monthly under a team-context root. It is the
// sole entry point the three ox journal commands (list, show, since) use
// to enumerate and materialize entries.
//
// The reader is standalone: it does not import from cmd/ox, never consults
// a time.Location, and never mutates the filesystem. All window math runs
// in UTC. Callers pre-resolve user input (relative durations, --tz) into
// UTC RFC3339 instants before constructing a ReadQuery.
package read

import (
	"time"

	"github.com/sageox/ox/internal/journal/memoryio"
)

// Layer identifies which directory under memory/ a query or entry belongs to.
type Layer string

const (
	LayerDaily   Layer = "daily"
	LayerWeekly  Layer = "weekly"
	LayerMonthly Layer = "monthly"
	LayerAuto    Layer = "auto"
)

// ReadQuery is the input to every reader entry point. Since/Until are UTC
// instants already resolved from user input by the CLI layer. The reader
// never interprets a naked string and never consults a time.Location.
type ReadQuery struct {
	Since    time.Time
	Until    time.Time
	Layer    Layer
	Teams    []TeamRef
	Limit    int
	WantBody bool
}

// TeamRef points the reader at one team-context root. Slug is the
// user-facing identifier surfaced on Entry.Team; Path is the absolute
// directory that contains memory/daily, memory/weekly, memory/monthly.
type TeamRef struct {
	Slug string
	Path string
}

// Entry is one journal entry (one .md file on disk). The shape matches
// the spec's §4.3 Entry object; the command layer maps it into the JSON
// envelope without extra transformation.
type Entry struct {
	ID            string
	Layer         Layer
	Date          string
	Team          string
	RelPath       string
	FactCount     int
	CitationCount int
	SourceFiles   []string
	Citations     []Citation
	BodyMD        string
	CreatedAt     time.Time
	Status        string
	Error         *EntryError
}

// Citation mirrors the on-disk Sources-section citation shape. It is a
// type alias for memoryio.Citation so the reader and the bridge share one
// definition — a future edit that adds a field to memoryio.Citation is
// surfaced here with no ceremony.
type Citation = memoryio.Citation

// ListMeta carries whole-call metadata: which layer was actually read
// (after auto-resolution), the day-rounded effective window, whether the
// result was limit-truncated, and any per-file warnings the reader
// accumulated without aborting.
type ListMeta struct {
	LayerResolved  Layer
	EffectiveSince time.Time
	EffectiveUntil time.Time
	Truncated      bool
	Warnings       []Warning
}

// Warning is one non-fatal per-file problem. The command layer decides
// whether to promote these to stderr or agent_hint text; the reader
// surfaces them but never aborts a call on them.
type Warning struct {
	Path    string
	Code    string
	Message string
}

// EntryError is attached to an Entry when a per-ID resolution or read
// fails (used by LoadEntries in Unit 4). Whole-call failures are returned
// as a Go error instead.
type EntryError struct {
	Code    string
	Message string
}
