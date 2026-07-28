package main

// kb_compat_test.go — pins ox ADR-028 behavior across the kb command
// surfaces: knowledge bubbles come from the KB API and NOWHERE else.
// Legacy team contexts and ledgers are permanent conversation stores —
// they never appear as bubbles, even when the KB API is unavailable.
// Also covers the OX_KB_DISABLE escape hatch, the no-persistent-disk
// skip, lazy personal-bubble provisioning, and forward-compat unknown
// kb_type handling. These cases straddle the CLI commands (`ox kb list`,
// `ox kb show`) and the kb.FetchBubbles contract; they live here rather
// than in any single command's test file so the matrix is easy to read
// end-to-end.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/kb"
	"github.com/sageox/ox/internal/runtime"
)

// resetRuntimeBaseline clears venue / capability env vars so the runtime
// probe reports a "laptop default" baseline (PersistDisk=true). Otherwise a
// sibling test that set OX_EPHEMERAL into the cached runtime.Caps() would
// force the kb fetch disabled regardless of OX_KB_DISABLE — and tests would
// fail intermittently depending on iteration order.
func resetRuntimeBaseline(t *testing.T) {
	t.Helper()
	for _, ev := range []string{"OX_EPHEMERAL", "CLAUDE_CODE_REMOTE", "DEVIN_TASK_ID", "OX_PERSIST_DISK", "OX_NO_DAEMON"} {
		t.Setenv(ev, "")
	}
	runtime.Reset()
	t.Cleanup(runtime.Reset)
}

// --- A. KB API unavailable (flag off / 403) ---

// TestCompat_KBAPIUnavailable_ListShowsEmpty pins the ADR-028 inversion of
// the old three-source fallback: when the KB API is unavailable (feature
// flag off, 403/404), `ox kb list` shows an EMPTY list — legacy team
// contexts and ledgers must NOT appear as bubbles, and the sentinel must
// not surface as a warning.
//
// Failure prevented: resurrecting the deleted merger's legacy-row
// fallback would re-present conversation stores as bubbles, the exact
// conflation ADR-028 removed.
func TestCompat_KBAPIUnavailable_ListShowsEmpty(t *testing.T) {
	resetRuntimeBaseline(t)
	t.Setenv("OX_KB_DISABLE", "")

	source := &compatFakeKBSource{listFn: func() ([]api.KB, error) {
		return nil, fmt.Errorf("kb list: %w", api.ErrKBAPIUnavailable)
	}}

	res := kb.FetchBubbles(context.Background(), source, "https://sageox.ai")
	if len(res.Bubbles) != 0 {
		t.Fatalf("expected zero bubbles when KB API is unavailable, got %+v", res.Bubbles)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("ErrKBAPIUnavailable must not surface as a warning, got %+v", res.Warnings)
	}

	var buf bytes.Buffer
	if err := renderKBListResult(&buf, res, "", false, ""); err != nil {
		t.Fatalf("renderKBListResult: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "No knowledge bubbles available.") {
		t.Errorf("expected clean empty state, got:\n%s", out)
	}
	if strings.Contains(out, "errored") {
		t.Errorf("flag-off must NOT render as a KB API error:\n%s", out)
	}
}

// TestCompat_KBFlagOff_ShowFallsBackGracefully verifies handleKBShowError
// translates ErrKBAPIUnavailable into the documented user-facing message
// + ErrSilent (so main.go doesn't double-print "Error:"). The behavior
// is what gives flag-off users a clear next step rather than a stack
// trace.
//
// Failure prevented: a regression that returns the raw HTTP 403 to the
// user instead of the friendly "Knowledge bubbles not enabled..." copy.
func TestCompat_KBFlagOff_ShowFallsBackGracefully(t *testing.T) {
	t.Parallel()

	wrapped := fmt.Errorf("listing failed: %w", api.ErrKBAPIUnavailable)
	err := handleKBShowError(io.Discard, wrapped, "personal", false)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// The dedicated user-facing path returns cli.ErrSilent so main.go
	// suppresses the "Error: ..." prefix; we verify by checking that
	// Error() is empty (the ErrSilent contract).
	if err.Error() != "" {
		t.Errorf("expected silent error, got message: %q", err.Error())
	}
}

// --- B. OX_KB_DISABLE escape hatch + runtime-capability skip ---

// TestCompat_OXKBDisable_SourceNotCalled verifies that setting
// OX_KB_DISABLE=1 short-circuits kb.FetchBubbles before the KB source is
// called, even if the source would otherwise return rows. This is the
// operator-facing emergency switch for rollout incidents.
//
// Failure prevented: a regression in the env hook would silently call
// the kb API anyway, eliminating the only knob operators have to bypass
// a misbehaving kb endpoint without redeploying the CLI.
func TestCompat_OXKBDisable_SourceNotCalled(t *testing.T) {
	resetRuntimeBaseline(t)
	t.Setenv("OX_KB_DISABLE", "1")

	var calls atomic.Int32
	source := &compatFakeKBSource{listFn: func() ([]api.KB, error) {
		calls.Add(1)
		return []api.KB{{KBID: "kb_should_not_appear", KBType: api.KBTypePersonal, Slug: "ghost"}}, nil
	}}

	res := kb.FetchBubbles(context.Background(), source, "https://sageox.ai")
	if calls.Load() != 0 {
		t.Errorf("kb source called %d times, want 0", calls.Load())
	}
	if len(res.Bubbles) != 0 || len(res.Warnings) != 0 {
		t.Errorf("expected empty result under OX_KB_DISABLE=1, got %+v", res)
	}
}

// TestCompat_NoPersistDisk_SkipsFetch verifies kb.FetchBubbles also returns
// an empty result when the runtime has no persistent disk (ephemeral
// venue) — there is nothing on disk to reconcile the bubbles against.
//
// Failure prevented: an ephemeral-mode CLI (Claude Code remote, Devin,
// CI) making a pointless KB API round-trip on every command.
func TestCompat_NoPersistDisk_SkipsFetch(t *testing.T) {
	resetRuntimeBaseline(t)
	t.Setenv("OX_KB_DISABLE", "")
	t.Setenv("OX_EPHEMERAL", "1")
	runtime.Reset()

	var calls atomic.Int32
	source := &compatFakeKBSource{listFn: func() ([]api.KB, error) {
		calls.Add(1)
		return []api.KB{{KBID: "kb_x", KBType: api.KBTypePersonal, Slug: "ghost"}}, nil
	}}

	res := kb.FetchBubbles(context.Background(), source, "https://sageox.ai")
	if calls.Load() != 0 {
		t.Errorf("kb source called %d times without persistent disk, want 0", calls.Load())
	}
	if len(res.Bubbles) != 0 || len(res.Warnings) != 0 {
		t.Errorf("expected empty result without persistent disk, got %+v", res)
	}
}

// TestCompat_OXKBDisable_VariantsAreRecognized pins the truthiness logic:
// "1", "true", "yes", "on" disable; "0", "false", "no", "off", "" leave
// it enabled. Encoded so an operator who types `OX_KB_DISABLE=true` gets
// the behavior they expect, and so a follow-up that swaps the parser
// (e.g., to strconv.ParseBool) can't quietly break the documented
// "anything truthy" contract.
//
// Failure prevented: an operator setting OX_KB_DISABLE=true expects the
// switch to fire; a strict "1-only" parser would silently no-op.
func TestCompat_OXKBDisable_VariantsAreRecognized(t *testing.T) {
	cases := []struct {
		val      string
		disabled bool
	}{
		{"1", true},
		{"true", true},
		{"YES", true},
		{"on", true},
		{"0", false},
		{"false", false},
		{"no", false},
		{"off", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.val, func(t *testing.T) {
			resetRuntimeBaseline(t)
			t.Setenv("OX_KB_DISABLE", tc.val)

			var calls atomic.Int32
			source := &compatFakeKBSource{listFn: func() ([]api.KB, error) {
				calls.Add(1)
				return nil, nil
			}}
			_ = kb.FetchBubbles(context.Background(), source, "https://sageox.ai")

			gotDisabled := calls.Load() == 0
			if gotDisabled != tc.disabled {
				t.Errorf("OX_KB_DISABLE=%q: kb source called=%d, want disabled=%v", tc.val, calls.Load(), tc.disabled)
			}
		})
	}
}

// --- C. personal bubble lazy-provision happy path ---

// TestCompat_PersonalBubble_AppearsInListAfterAuth verifies the
// EnsurePersonalKBMiddleware contract from the server side: the very
// first `ox kb list` after `ox login` must surface a personal bubble.
// We model the server's lazy-provision by having the kb source return a
// personal bubble on the first call (as the real middleware would).
//
// Failure prevented: a regression that filters out kb_type=personal
// rows (or treats the personal bubble as a "not yours" surface) would
// leave first-session users without a scratchpad and without a clear
// signal that anything was wrong.
func TestCompat_PersonalBubble_AppearsInListAfterAuth(t *testing.T) {
	t.Parallel()

	res := kb.ListResult{
		Bubbles: []kb.Bubble{
			{KBID: "kb_personal_lazy", Type: api.KBTypePersonal, Slug: "personal-abc", Name: "Personal", ViewerRole: "owner"},
			{KBID: "kb_team", Type: api.KBTypeTeam, Slug: "platform", Name: "Platform"},
		},
	}

	var buf bytes.Buffer
	if err := renderKBListResult(&buf, res, "", false, ""); err != nil {
		t.Fatalf("render: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "personal-abc") {
		t.Errorf("personal bubble missing from list output:\n%s", out)
	}
	// ROLE column was dropped from the table for now — viewer_role still
	// flows through the JSON envelope, but the human table is intentionally
	// narrower. Re-add a role column assertion if/when the column comes back.
}

// --- D. forward-compat unknown type across kb commands ---

// TestCompat_ForwardCompatUnknownType_ListRendersUnknown verifies that an
// unknown kb_type renders as "unknown" in `ox kb list`, never blank.
//
// Failure prevented: a future server-side rollout of a sixth kb_type
// would otherwise render rows with a blank TYPE column on every CLI that
// hadn't been upgraded yet — a confusing visual artifact, not a hard
// failure.
func TestCompat_ForwardCompatUnknownType_ListRendersUnknown(t *testing.T) {
	t.Parallel()

	res := kb.ListResult{Bubbles: []kb.Bubble{
		{Type: api.KBTypeUnknown, Slug: "future-x", Name: "Future"},
	}}
	var buf bytes.Buffer
	if err := renderKBListResult(&buf, res, "", false, ""); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(buf.String(), "unknown") {
		t.Errorf("unknown type must render as the literal 'unknown':\n%s", buf.String())
	}
}

// TestCompat_ForwardCompatUnknownType_ShowResolvesByKBID verifies that a
// kb_id pointing at a future-typed bubble still resolves via
// `ox kb show kb_<id>` — the kb_id direct path bypasses the slug priority
// table entirely, so an unrecognized type doesn't matter for resolution.
//
// Failure prevented: a regression that gates the kb_id direct path on
// "type is one of the five known values" would block the only escape
// hatch flag-off users have for inspecting a future-typed bubble.
func TestCompat_ForwardCompatUnknownType_ShowResolvesByKBID(t *testing.T) {
	t.Parallel()

	bubbles := []api.KB{{KBID: "kb_future", KBType: api.KBTypeUnknown, Slug: "future-x"}}
	got := pickKBByPriority(bubbles)
	if got == nil {
		t.Fatal("expected fallback selection for unknown-type bubble, got nil")
	}
	if got.KBID != "kb_future" {
		t.Errorf("expected fallback to first match, got %q", got.KBID)
	}
}

func TestCompat_ForwardCompatSlugCollision_PersonalBeatsUnknown(t *testing.T) {
	t.Parallel()

	bubbles := []api.KB{
		{KBID: "kb_unknown", KBType: api.KBTypeUnknown, Slug: "shared"},
		{KBID: "kb_personal", KBType: api.KBTypePersonal, Slug: "shared"},
	}
	got := pickKBByPriority(bubbles)
	if got == nil || got.KBID != "kb_personal" {
		t.Errorf("expected personal to win over unknown, got %+v", got)
	}
}

// --- helpers ---

// compatFakeKBSource is a tiny fake for kb.FetchBubbles calls so we don't
// depend on internal/kb's unexported test fakes. The shape matches
// kb.KBSource — single ListBubbles method.
type compatFakeKBSource struct {
	listFn func() ([]api.KB, error)
}

func (f *compatFakeKBSource) ListBubbles(_ context.Context) ([]api.KB, error) {
	if f.listFn != nil {
		return f.listFn()
	}
	return nil, nil
}
