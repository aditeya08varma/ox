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

// LoadEntries materializes entry bodies for a specific set of IDs. It is
// stubbed in Unit 1 and lands in Unit 4 (ox journal show).
func LoadEntries(ctx context.Context, q ReadQuery, ids []string) ([]Entry, error) {
	_ = ctx
	_ = q
	_ = ids
	return nil, errNotImplemented
}

// Since is the list+load composite used by ox journal since. It is
// stubbed in Unit 1 and lands in Unit 5.
func Since(ctx context.Context, q ReadQuery) ([]Entry, []string, ListMeta, error) {
	_ = ctx
	_ = q
	return nil, nil, ListMeta{}, errNotImplemented
}
