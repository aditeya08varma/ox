package read

import "context"

// ListEntries enumerates distill-history entries matching q without materializing
// their bodies. The returned entries are ordered by Date ascending, then
// CreatedAt ascending. The ListMeta carries the day-rounded effective
// window (the envelope's source of truth for window.since / window.until)
// and any per-file warnings.
//
// Unit 1 implements the daily layer only; weekly/monthly queries return
// a nil slice with a "layer_not_implemented" warning on ListMeta.
func ListEntries(ctx context.Context, q ReadQuery) ([]Entry, ListMeta, error) {
	return listEntries(ctx, q)
}

// LoadEntries materializes entry bodies for a specific set of IDs. It
// is the data path behind `ox distill history show`. Per-ID errors are attached
// to the returned Entry's Status / Error fields; whole-call failures
// (empty team set, index scan I/O error, invalid window) are returned
// as a Go error so the command layer maps them to a single envelope
// error. See show.go for the ID grammar and partial-success contract.
func LoadEntries(ctx context.Context, q ReadQuery, ids []string) ([]Entry, error) {
	return loadEntries(ctx, q, ids)
}

// Since is the list+load composite used by ox distill history since. It
// enumerates entries in q's window via listEntries (bodies off),
// materializes their bodies via loadEntries (bodies on), and returns
// parallel slices where bodies[i] corresponds to entries[i]. The
// ListMeta carries the day-rounded effective window the envelope
// needs; warnings from the list scan pass through unchanged.
//
// Empty window is not an error: callers get ([]Entry{}, []string{},
// meta, nil) so the envelope emits `entries: []` + empty bodies
// array per spec §3.6.
func Since(ctx context.Context, q ReadQuery) ([]Entry, []string, ListMeta, error) {
	return sinceEntries(ctx, q)
}
