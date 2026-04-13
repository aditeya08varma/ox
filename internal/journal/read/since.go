package read

import "context"

// sinceEntries is the implementation behind the exported Since wrapper.
// It is pure composition: call listEntries with bodies off to enumerate
// the window, hand the resulting stem IDs to loadEntries with bodies on,
// and return parallel slices (entries, bodies) + the ListMeta from the
// bodyless scan so the envelope can surface the day-rounded window.
//
// Per Unit 5 of journal-read-plan.md §3, the composition must:
//
//   - Return empty parallel slices and no error when the window has no
//     matching files (not an error — spec §3.6 "empty result is
//     success:true with entries: [] and an empty bodies array").
//   - Preserve Unit 3's ordering contract: date ascending then
//     created_at ascending. listEntries already sorts its output; the
//     ID slice handed to loadEntries is already in that order, and
//     loadEntries processes IDs in the order given, so the parallel
//     slices come back ordered.
//   - Guarantee bodies[i] corresponds to entries[i] by position, same
//     length. For full-stem IDs (which is what we pass — we never hand
//     loadEntries a bare date), matchID exact-matches once, so
//     len(loaded) == len(ids) and the mapping is 1:1.
//
// Mid-flight materialization failures (the file disappears between the
// list scan and the load scan, or becomes unreadable) surface as rows
// with Status="read_error" and an empty body. The caller — typically
// the content-mode renderer in cmd/ox/journal_since.go — skips non-ok
// rows so the stream stays clean.
func sinceEntries(ctx context.Context, q ReadQuery) ([]Entry, []string, ListMeta, error) {
	listQ := q
	listQ.WantBody = false
	entries, meta, err := listEntries(ctx, listQ)
	if err != nil {
		return nil, nil, meta, err
	}
	if len(entries) == 0 {
		return []Entry{}, []string{}, meta, nil
	}

	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.ID)
	}

	loadQ := q
	loadQ.WantBody = true
	loadQ.Limit = 0
	loaded, err := loadEntries(ctx, loadQ, ids)
	if err != nil {
		return nil, nil, meta, err
	}

	bodies := make([]string, len(loaded))
	for i, e := range loaded {
		bodies[i] = e.BodyMD
	}
	return loaded, bodies, meta, nil
}
