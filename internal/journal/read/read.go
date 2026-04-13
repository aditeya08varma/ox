package read

import (
	"context"
	"errors"
)

// errNotImplemented is returned by reader entry points that Unit 1 stubs
// out. Later units replace it with real implementations. Callers should
// use errors.Is to check for this sentinel.
var errNotImplemented = errors.New("journal read: not yet implemented")

// ListEntries enumerates journal entries matching q without materializing
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
// is the data path behind `ox journal show`. Per-ID errors are attached
// to the returned Entry's Status / Error fields; whole-call failures
// (empty team set, index scan I/O error, invalid window) are returned
// as a Go error so the command layer maps them to a single envelope
// error. See show.go for the ID grammar and partial-success contract.
func LoadEntries(ctx context.Context, q ReadQuery, ids []string) ([]Entry, error) {
	return loadEntries(ctx, q, ids)
}

// Since is the list+load composite used by ox journal since. It is
// stubbed in Unit 1 and lands in Unit 5.
func Since(ctx context.Context, q ReadQuery) ([]Entry, []string, ListMeta, error) {
	_ = ctx
	_ = q
	return nil, nil, ListMeta{}, errNotImplemented
}
