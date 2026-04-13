//go:build slow

// journal_read_e2e_show_test.go — JR-* skeletons for `ox journal show`.
// The 7 cases that exercise the show command. Harness, fixtures, and
// recipes live in journal_read_e2e_test.go; this file only adds the
// per-case test funcs.
//
// Fixture staging and subprocess invocation execute BEFORE t.Skip, so
// a regression in the harness still trips this file even while the
// assertion block is skip-gated. Once Unit 4 (LoadEntries + ox journal
// show) lands, replace each Skip with real assertions.
//
// See docs/ai/specs/journal-read-test-plan.md §2 rows JR-04..JR-08,
// JR-17, JR-18.

package main

import (
	"testing"
	"time"
)

// skipPendingUnit4 gates the assertion block of every show skeleton
// until Unit 4 of journal-read-plan.md lands.
const skipPendingUnit4 = "impl not landed yet: depends on Unit 4 (LoadEntries + ox journal show)"

// TestJournalRead_JR04_ShowBareDate_ReturnsAllSnapshots — bare-date
// match returns the full same-day union. Failure prevented: reader
// picks "newest snapshot" as the default or returns only one entry.
func TestJournalRead_JR04_ShowBareDate_ReturnsAllSnapshots(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	e := setupJournalE2E(t, now)
	fx := recipeMultiSnapshotSameDay(t, e.primaryTeam, now)

	// Use the captured `now` to form the date argument. Both staged
	// snapshots share this date.
	date := dayString(now)
	out, exit := e.Run(t, "journal", "show", date, "--format=json")
	t.Logf("JR-04 exit=%d out_bytes=%d dailies=%d date=%s",
		exit, len(out), len(fx.Dailies), date)

	t.Skip(skipPendingUnit4)
	// TODO Unit 4: exit 0; data.entries contains BOTH fx.Dailies, ordered by
	// created_at asc; no --latest shortcut honored.
}

// TestJournalRead_JR05_ShowLatestRejected — --latest must be rejected
// by cobra at parse time. Failure prevented: --latest was accidentally
// implemented, or is silently ignored (drops to JR-04 behavior).
func TestJournalRead_JR05_ShowLatestRejected(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	e := setupJournalE2E(t, now)
	fx := recipeMultiSnapshotSameDay(t, e.primaryTeam, now)

	date := dayString(now)
	out, exit := e.Run(t, "journal", "show", date, "--latest", "--format=json")
	t.Logf("JR-05 exit=%d out_bytes=%d dailies=%d date=%s",
		exit, len(out), len(fx.Dailies), date)

	t.Skip(skipPendingUnit4)
	// TODO Unit 4: exit 2; JSON error envelope; error.code="usage_error".
}

// TestJournalRead_JR06_ShowShortUUID7Prefix — 8-char UUID7 prefix
// matches exactly one file. Failure prevented: prefix matcher only
// accepts full IDs or only date-prefixed IDs (spec §4.4).
func TestJournalRead_JR06_ShowShortUUID7Prefix(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	e := setupJournalE2E(t, now)
	fx := recipeMultiSnapshotSameDay(t, e.primaryTeam, now)

	// Pull a deterministic short prefix off the first staged file's ID.
	// The recipe uses "019c8a3f" for the older snapshot, but drive the
	// test off fx rather than hard-coding — that way recipe edits
	// propagate automatically.
	if len(fx.Dailies) < 1 {
		t.Fatalf("JR-06: recipe staged %d dailies, want ≥1", len(fx.Dailies))
	}
	// The stem shape is "<date>-<uuid7>[-extra]"; slice off the date prefix
	// (11 chars incl. trailing '-') to reach the UUID7 segment.
	stem := fx.Dailies[0].ID
	const datePrefixLen = len("2006-01-02") + 1 // YYYY-MM-DD + '-'
	shortID := stem[datePrefixLen : datePrefixLen+8]

	out, exit := e.Run(t, "journal", "show", shortID, "--format=json")
	t.Logf("JR-06 exit=%d out_bytes=%d shortID=%s", exit, len(out), shortID)

	t.Skip(skipPendingUnit4)
	// TODO Unit 4: exit 0; data.entries has exactly one row whose id equals
	// fx.Dailies[0].ID.
}

// TestJournalRead_JR07_ShowAmbiguousPrefix_Errors — duplicate prefix
// forces id_ambiguous. Failure prevented: ambiguous-prefix logic
// returns the first match instead of failing, silently feeding agents
// the wrong content.
func TestJournalRead_JR07_ShowAmbiguousPrefix_Errors(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	e := setupJournalE2E(t, now)
	fx := recipeAmbiguousPrefix(t, e.primaryTeam, now)

	// The recipe stages two files whose short UUID7 prefix is "019c8a3f"
	// (one base snapshot and one "-dup" suffix). The short prefix alone
	// is the ambiguous query.
	out, exit := e.Run(t, "journal", "show", "019c8a3f", "--format=json")
	t.Logf("JR-07 exit=%d out_bytes=%d dailies=%d", exit, len(out), len(fx.Dailies))

	t.Skip(skipPendingUnit4)
	// TODO Unit 4: exit 1; success=false; error.code="id_ambiguous";
	// retryable=false; stderr lists the full set of matching stems.
}

// TestJournalRead_JR08_ShowContentFormatStripsEnvelope — --format=content
// emits only markdown on stdout. Failure prevented: frontmatter not
// stripped, JSON envelope leaks into stdout, or stderr content lands on
// stdout and corrupts the pipe to `claude`. factory/distill/summary.ts
// depends on this exact contract.
func TestJournalRead_JR08_ShowContentFormatStripsEnvelope(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	e := setupJournalE2E(t, now)
	fx := recipeMinimalDaily(t, e.primaryTeam, now)

	if len(fx.Dailies) < 1 {
		t.Fatalf("JR-08: recipe staged %d dailies, want 1", len(fx.Dailies))
	}
	fullID := fx.Dailies[0].ID

	out, exit := e.Run(t, "journal", "show", fullID, "--format=content")
	t.Logf("JR-08 exit=%d out_bytes=%d id=%s", exit, len(out), fullID)

	t.Skip(skipPendingUnit4)
	// TODO Unit 4: exit 0; stdout is ONLY the markdown body (no JSON envelope);
	// first non-empty line equals "# Daily Memory — "+fx.Dailies[0].Date;
	// frontmatter is NOT present (no leading `---`); stderr may carry warnings
	// but does not pollute stdout.
}

// TestJournalRead_JR17_ShowJsonFormat_FullBody — JSON show returns
// body_md, citations, source_files, elapsed_ms. Failure prevented:
// show returns only metadata or leaves frontmatter in body_md.
func TestJournalRead_JR17_ShowJsonFormat_FullBody(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	e := setupJournalE2E(t, now)
	fx := recipeMinimalDaily(t, e.primaryTeam, now)

	if len(fx.Dailies) < 1 {
		t.Fatalf("JR-17: recipe staged %d dailies, want 1", len(fx.Dailies))
	}
	fullID := fx.Dailies[0].ID

	out, exit := e.Run(t, "journal", "show", fullID, "--format=json")
	t.Logf("JR-17 exit=%d out_bytes=%d id=%s", exit, len(out), fullID)

	t.Skip(skipPendingUnit4)
	// TODO Unit 4: exit 0; success=true; response has body_md (frontmatter
	// stripped), citations array, source_files array, elapsed_ms present.
}

// TestJournalRead_JR18_ShowBadFrontmatterNeighbor_SurvivesGoodID —
// one malformed neighbor does not break show for an unrelated good ID.
// Failure prevented: one bad frontmatter file poisons the whole
// directory walk and hides the valid entry.
func TestJournalRead_JR18_ShowBadFrontmatterNeighbor_SurvivesGoodID(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	e := setupJournalE2E(t, now)
	fx := recipeBadFrontmatterNeighbor(t, e.primaryTeam, now)

	if len(fx.Dailies) < 1 {
		t.Fatalf("JR-18: recipe staged %d dailies, want 1", len(fx.Dailies))
	}
	goodID := fx.Dailies[0].ID

	out, exit := e.Run(t, "journal", "show", goodID, "--format=json")
	t.Logf("JR-18 exit=%d out_bytes=%d id=%s extras=%d",
		exit, len(out), goodID, len(fx.ExtraFiles))

	t.Skip(skipPendingUnit4)
	// TODO Unit 4: exit 0; data.entries has the good entry; stderr warns about
	// the bad neighbor; no error envelope.
}
