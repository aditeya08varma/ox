package prime

// kb_snapshot_test.go — pins the JSON envelope shape of `ox agent prime`'s
// kb output against a committed testdata snapshot. The snapshot is the
// reviewable artifact: any change to KBInfo's serialized shape (renamed
// keys, dropped fields, new fields without omitempty) shows up as a diff
// in the testdata file in PR review, which is exactly how we detect silent
// envelope drift before it ships.
//
// Update protocol: when a deliberate envelope change is in flight, run
//
//	UPDATE_SNAPSHOTS=1 go test ./internal/prime -run TestPrime_KBEnvelopeSnapshot
//
// to refresh the testdata file. Reviewers see the new shape in the same
// PR as the code change.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/kb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// snapshotInputBubbles is the canonical mixed-type fixture: one bubble per
// primary kb_type, all sourced from the KB API — under ox ADR-028 the KB
// API is the only source of bubble rows (team contexts and ledgers are
// conversation stores, never bubbles).
func snapshotInputBubbles() []kb.Bubble {
	return []kb.Bubble{
		{
			KBID:       "kb_personal",
			Type:       api.KBTypePersonal,
			Slug:       "personal-abc",
			Name:       "Personal",
			ViewerRole: "owner",
			LocalPath:  "/data/sageox/sageox.ai/kb/kb_personal",
		},
		{
			KBID:        "kb_team",
			Type:        api.KBTypeTeam,
			Slug:        "platform",
			Name:        "Platform",
			Description: "Curated platform-team knowledge",
			Topics:      []string{"infra", "deploys"},
			ViewerRole:  "member",
			LocalPath:   "/data/sageox/sageox.ai/kb/kb_team",
		},
		{
			KBID:       "kb_repo",
			Type:       api.KBTypeRepo,
			Slug:       "my-app",
			Name:       "my-app",
			ViewerRole: "owner",
			LocalPath:  "/data/sageox/sageox.ai/kb/kb_repo",
		},
	}
}

// TestPrime_KBEnvelopeSnapshot snapshots the JSON output of BuildKBInfos
// against a committed testdata fixture so any change to the JSON shape
// is visible in PR diffs.
//
// Failure prevented: a silent rename of a KBInfo field, a dropped field,
// or a newly-added field without omitempty would otherwise ship without
// a test failure — even though every downstream consumer (Claude prompt
// templates, status renderers, scripted jq pipelines) keys off these
// names. The snapshot is the contract.
func TestPrime_KBEnvelopeSnapshot(t *testing.T) {
	t.Parallel()

	res := kb.ListResult{Bubbles: snapshotInputBubbles()}
	tokens := map[string]int64{
		"personal": 100,
		"team":     400,
		"repo":     50,
	}

	got := BuildKBInfos(res, tokens)
	require.Len(t, got, 3, "all three rows must survive the build")

	// stable indented JSON so diffs in testdata read like prose.
	body, err := json.MarshalIndent(got, "", "  ")
	require.NoError(t, err)
	body = append(body, '\n')

	snapshotPath := filepath.Join("testdata", "kb_envelope_snapshot.json")

	if os.Getenv("UPDATE_SNAPSHOTS") == "1" {
		require.NoError(t, os.WriteFile(snapshotPath, body, 0o644))
		t.Logf("snapshot updated: %s", snapshotPath)
		return
	}

	expected, err := os.ReadFile(snapshotPath)
	require.NoError(t, err, "snapshot must exist; run with UPDATE_SNAPSHOTS=1 to regenerate")

	if string(body) != string(expected) {
		t.Errorf("kb envelope JSON drifted from snapshot.\n\n--- got ---\n%s\n--- want ---\n%s\n\nIf this change is intentional, run:\n  UPDATE_SNAPSHOTS=1 go test ./internal/prime -run TestPrime_KBEnvelopeSnapshot",
			string(body), string(expected))
	}
}

// TestPrime_TokenTotalsConsistency verifies the contract that the sum of
// KBInfo.Tokens per type equals the per-type rollup the daemon's
// counters report — the sanity check "the per-bubble field sums to the
// per-type rollup."
//
// Failure prevented: a bug that splits tokens across the wrong number of
// bubbles would silently inflate or deflate dashboard numbers.
func TestPrime_TokenTotalsConsistency(t *testing.T) {
	t.Parallel()

	res := kb.ListResult{Bubbles: snapshotInputBubbles()}
	rollup := map[string]int64{
		"personal": 100,
		"team":     400,
		"repo":     50,
	}

	got := BuildKBInfos(res, rollup)

	sum := map[string]int{}
	for _, k := range got {
		sum[k.Type] += k.Tokens
	}
	assert.Equal(t, int(rollup["personal"]), sum["personal"], "personal tokens must equal the per-type rollup")
	assert.Equal(t, int(rollup["team"]), sum["team"], "team tokens must equal the per-type rollup")
	assert.Equal(t, int(rollup["repo"]), sum["repo"], "repo tokens must equal the per-type rollup")
}

// TestPrime_ForwardCompatUnknownType verifies a kb_type value the CLI
// doesn't recognize collapses to "unknown" in the JSON envelope rather
// than appearing as a blank string or causing a panic.
//
// Failure prevented: a future server rollout adds a sixth kb_type ahead
// of the CLI; without this test, the field would render as the empty
// string and consumers branching on `type==""` would mis-classify the
// row.
func TestPrime_ForwardCompatUnknownType(t *testing.T) {
	t.Parallel()

	res := kb.ListResult{Bubbles: []kb.Bubble{
		{KBID: "kb_y", Type: api.KBTypeUnknown, Slug: "future-x", Name: "Future"},
		{KBID: "kb_blank", Type: "", Slug: "blank", Name: "Blank"},
	}}

	got := BuildKBInfos(res, nil)

	require.Len(t, got, 2)
	for _, k := range got {
		assert.Equal(t, "unknown", k.Type, "unknown / empty types must render as the literal 'unknown' so consumers branching on type don't see a blank")
	}
}

// TestPrime_KBEnvelopeRoundTrip verifies the JSON marshaling preserves
// every field that the snapshot pins. Decoding the snapshot back into a
// []KBInfo and comparing to the original input is a lower-cost guard
// for callers who don't run the snapshot test directly.
//
// Failure prevented: a future change to KBInfo that adds a non-tagged
// field (or drops a json tag) would silently break round-trip even when
// the snapshot was regenerated to match the new shape.
func TestPrime_KBEnvelopeRoundTrip(t *testing.T) {
	t.Parallel()

	res := kb.ListResult{Bubbles: snapshotInputBubbles()}
	tokens := map[string]int64{"personal": 100, "team": 400, "repo": 50}

	got := BuildKBInfos(res, tokens)
	body, err := json.Marshal(got)
	require.NoError(t, err)

	var decoded []KBInfo
	require.NoError(t, json.Unmarshal(body, &decoded))

	require.Equal(t, len(got), len(decoded))
	for i := range got {
		assert.Equal(t, got[i], decoded[i], "round-trip must preserve KBInfo[%d]", i)
	}
}
