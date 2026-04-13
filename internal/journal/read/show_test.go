package read

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestMatchID — table-driven coverage of the full spec §4.4 grammar.
// Every row pins one (id, index) input pair against the expected match
// set by ID. matchID is the single point where prefix/bare-date/uuid7
// grammars intersect; every regression in this grammar must show up
// here before it can surface at the CLI layer.
//
// Failure prevented: matchID silently picks the wrong branch for an
// input or returns an empty match for a valid ID, both of which would
// cause `ox journal show` to return not_found or the wrong row with
// exit code 0 + success:true.
func TestMatchID(t *testing.T) {
	t.Parallel()

	stem1 := "2026-04-12-019c8a3f-0001"
	stem2 := "2026-04-12-019c8a3f-0002"
	stem3 := "2026-04-12-019c8bbb-0001"
	stem4 := "2026-04-11-019c7777-0001"
	index := []Entry{
		{ID: stem1, Date: "2026-04-12", Layer: LayerDaily},
		{ID: stem2, Date: "2026-04-12", Layer: LayerDaily},
		{ID: stem3, Date: "2026-04-12", Layer: LayerDaily},
		{ID: stem4, Date: "2026-04-11", Layer: LayerDaily},
	}

	cases := []struct {
		name    string
		id      string
		wantIDs []string
	}{
		{
			name:    "full stem exact match",
			id:      stem1,
			wantIDs: []string{stem1},
		},
		{
			name:    "bare date — all same-day entries",
			id:      "2026-04-12",
			wantIDs: []string{stem1, stem2, stem3},
		},
		{
			name:    "bare date — zero matches",
			id:      "2026-04-10",
			wantIDs: nil,
		},
		{
			name:    "stem prefix — unambiguous",
			id:      "2026-04-12-019c8bbb",
			wantIDs: []string{stem3},
		},
		{
			name:    "stem prefix — ambiguous across same day",
			id:      "2026-04-12-019c8a3f",
			wantIDs: []string{stem1, stem2},
		},
		{
			name:    "uuid7 short form — 6 hex chars, unambiguous",
			id:      "019c8b",
			wantIDs: []string{stem3},
		},
		{
			name:    "uuid7 short form — 8 hex chars, ambiguous across day",
			id:      "019c8a3f",
			wantIDs: []string{stem1, stem2},
		},
		{
			name:    "uuid7 longer form with dash — unambiguous",
			id:      "019c8a3f-0002",
			wantIDs: []string{stem2},
		},
		{
			name:    "uuid7 short form — cross-day unambiguous",
			id:      "019c77",
			wantIDs: []string{stem4},
		},
		{
			name:    "unknown prefix — not found",
			id:      "deadbe",
			wantIDs: nil,
		},
		{
			name:    "empty id — not found",
			id:      "",
			wantIDs: nil,
		},
		{
			name:    "too-short non-date — not found",
			id:      "019",
			wantIDs: nil,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := matchID(tc.id, index)
			gotIDs := make([]string, 0, len(got))
			for _, e := range got {
				gotIDs = append(gotIDs, e.ID)
			}
			if !equalSortedStrings(gotIDs, tc.wantIDs) {
				t.Fatalf("matchID(%q) = %v, want %v", tc.id, gotIDs, tc.wantIDs)
			}
		})
	}
}

func equalSortedStrings(a, b []string) bool {
	aa := append([]string(nil), a...)
	bb := append([]string(nil), b...)
	sort.Strings(aa)
	sort.Strings(bb)
	if len(aa) != len(bb) {
		return false
	}
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}

// TestLoadEntries_PartialSuccess — end-to-end partial-success matrix
// through LoadEntries against a real on-disk fixture. Proves the
// contract the command layer depends on: per-ID failure does NOT abort
// the call, success rows get body_md + citations materialized, and the
// bare-date special case returns every same-day file without promoting
// the multi-match to ambiguous.
//
// Failure prevented: one bad ID poisoning a multi-ID show, or
// bare-date returning only one entry, or the read path silently losing
// BodyMD when the index scan says the file exists.
func TestLoadEntries_PartialSuccess(t *testing.T) {
	t.Parallel()
	team := stageShowFixture(t)

	q := ReadQuery{
		Since:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Until:    time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
		Layer:    LayerDaily,
		Teams:    []TeamRef{team},
		WantBody: true,
	}

	ids := []string{
		"2026-04-12-019c8a3f-0001", // exact stem → ok
		"2026-04-12",               // bare date → 2 rows (union)
		"deadbeef",                 // unknown → not_found
		"019c8a3f",                 // ambiguous prefix across 2 stems
	}
	entries, err := LoadEntries(context.Background(), q, ids)
	if err != nil {
		t.Fatalf("LoadEntries: %v", err)
	}

	// Expected layout (order follows ids):
	//   [0]     ok  — exact stem match for ...8a3f-0001
	//   [1..3]  ok  — bare date 2026-04-12 expands to the 3 same-day files
	//                 (8a3f-0001, 8a3f-0002, 8bbb-0001)
	//   [4]     not_found — deadbeef has no matches
	//   [5]     ambiguous — 019c8a3f matches two stems (0001 and 0002)
	if len(entries) != 6 {
		t.Fatalf("len(entries) = %d, want 6: %+v", len(entries), entries)
	}

	if entries[0].Status != "ok" {
		t.Fatalf("entries[0].Status = %q, want ok: %+v", entries[0].Status, entries[0])
	}
	if entries[0].BodyMD == "" {
		t.Fatalf("entries[0].BodyMD empty — body not materialized")
	}
	if strings.HasPrefix(entries[0].BodyMD, "---") {
		t.Fatalf("entries[0].BodyMD still has frontmatter fence: %q", entries[0].BodyMD[:20])
	}

	for i := 1; i <= 3; i++ {
		if entries[i].Status != "ok" {
			t.Fatalf("bare-date row %d Status = %q, want ok", i, entries[i].Status)
		}
		if entries[i].Date != "2026-04-12" {
			t.Fatalf("bare-date row %d Date = %q, want 2026-04-12", i, entries[i].Date)
		}
		if entries[i].BodyMD == "" {
			t.Fatalf("bare-date row %d BodyMD empty, id=%s", i, entries[i].ID)
		}
	}

	if entries[4].Status != "not_found" {
		t.Fatalf("entries[4].Status = %q, want not_found", entries[4].Status)
	}
	if entries[4].Error == nil || entries[4].Error.Code != "id_not_found" {
		t.Fatalf("entries[4].Error = %+v, want code=id_not_found", entries[4].Error)
	}

	if entries[5].Status != "ambiguous" {
		t.Fatalf("entries[5].Status = %q, want ambiguous", entries[5].Status)
	}
	if entries[5].Error == nil || entries[5].Error.Code != "id_ambiguous" {
		t.Fatalf("entries[5].Error = %+v, want code=id_ambiguous", entries[5].Error)
	}
	// The candidates list must name both colliding stems.
	msg := entries[5].Error.Message
	if !strings.Contains(msg, "0001") || !strings.Contains(msg, "0002") {
		t.Fatalf("ambiguous message missing candidates: %q", msg)
	}
}

// TestLoadEntries_BareDateZeroMatches — bare date with no entries in
// window returns a single not_found row, NOT an ambiguous row and NOT
// an empty slice. Failure prevented: a silent-miss where a typo'd date
// comes back with exit 0 and empty entries, leaving the agent
// thinking the day is genuinely empty.
func TestLoadEntries_BareDateZeroMatches(t *testing.T) {
	t.Parallel()
	team := stageShowFixture(t)
	q := ReadQuery{
		Since:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Until:    time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
		Layer:    LayerDaily,
		Teams:    []TeamRef{team},
		WantBody: true,
	}
	entries, err := LoadEntries(context.Background(), q, []string{"2026-04-10"})
	if err != nil {
		t.Fatalf("LoadEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	if entries[0].Status != "not_found" {
		t.Fatalf("Status = %q, want not_found", entries[0].Status)
	}
}

// TestLoadEntries_EmptyIDs — calling LoadEntries with no IDs is a
// whole-call failure, not a silent empty result. Failure prevented: a
// refactor accidentally turns this into a success path, silently
// returning nothing to a caller that almost certainly has a bug.
func TestLoadEntries_EmptyIDs(t *testing.T) {
	t.Parallel()
	team := stageShowFixture(t)
	q := ReadQuery{
		Since: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
		Layer: LayerDaily,
		Teams: []TeamRef{team},
	}
	if _, err := LoadEntries(context.Background(), q, nil); err == nil {
		t.Fatalf("want error for empty ids, got nil")
	}
}

// TestLoadEntries_NoTeams — a caller that forgets to set q.Teams must
// get a whole-call failure rather than silent empty results. Same
// rationale as TestLoadEntries_EmptyIDs.
func TestLoadEntries_NoTeams(t *testing.T) {
	t.Parallel()
	q := ReadQuery{
		Since: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
		Layer: LayerDaily,
	}
	if _, err := LoadEntries(context.Background(), q, []string{"2026-04-12"}); err == nil {
		t.Fatalf("want error for empty teams, got nil")
	}
}

// stageShowFixture writes a small set of daily entries into a fresh
// temp directory and returns a TeamRef pointing at the team-context
// root. The fixture is tuned to exercise every matchID branch:
//
//   - two same-day files sharing prefix 019c8a3f → ambiguous + bare-date
//   - one same-day file with unique uuid7 prefix 019c8bbb
//   - one prior-day file (2026-04-11) that must not leak into 2026-04-12
//
// Each file has real frontmatter + a body paragraph so WantBody=true
// materialization is exercised.
func stageShowFixture(t *testing.T) TeamRef {
	t.Helper()
	root := t.TempDir()
	dailyDir := filepath.Join(root, "memory", "daily")
	if err := os.MkdirAll(dailyDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	type fix struct {
		name string
		body string
		when time.Time
	}
	apr11 := time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC)
	apr12a := time.Date(2026, 4, 12, 9, 0, 0, 0, time.UTC)
	apr12b := time.Date(2026, 4, 12, 18, 0, 0, 0, time.UTC)
	apr12c := time.Date(2026, 4, 12, 22, 0, 0, 0, time.UTC)

	fixtures := []fix{
		{
			name: "2026-04-11-019c7777-0001.md",
			body: sampleMarkdown("2026-04-11", "First run."),
			when: apr11,
		},
		{
			name: "2026-04-12-019c8a3f-0001.md",
			body: sampleMarkdown("2026-04-12", "Morning rollup."),
			when: apr12a,
		},
		{
			name: "2026-04-12-019c8a3f-0002.md",
			body: sampleMarkdown("2026-04-12", "Afternoon rollup."),
			when: apr12b,
		},
		{
			name: "2026-04-12-019c8bbb-0001.md",
			body: sampleMarkdown("2026-04-12", "Evening rollup."),
			when: apr12c,
		},
	}
	for _, f := range fixtures {
		p := filepath.Join(dailyDir, f.name)
		if err := os.WriteFile(p, []byte(f.body), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
		if err := os.Chtimes(p, f.when, f.when); err != nil {
			t.Fatalf("chtimes %s: %v", p, err)
		}
	}
	return TeamRef{Slug: "show-fixture", Path: root}
}

func sampleMarkdown(date, note string) string {
	return "---\nsources:\n  - memory/.github-facts/" + date + "-source.jsonl\n---\n# Daily Memory — " + date + "\n\n" + note + "\n"
}
