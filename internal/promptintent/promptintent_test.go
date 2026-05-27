package promptintent

import (
	"strings"
	"testing"
)

func TestLooksLikeRecall_LengthGate(t *testing.T) {
	cases := []struct {
		name   string
		prompt string
		want   bool
	}{
		{"empty", "", false},
		{"too short", "find foo bar", false},
		{"just under threshold", strings.Repeat("a", MinPromptChars-1), false},
		{"long but no intent", strings.Repeat("alpha beta ", 30), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := LooksLikeRecall(tc.prompt); got != tc.want {
				t.Fatalf("LooksLikeRecall(%q)=%v want %v", tc.prompt, got, tc.want)
			}
		})
	}
}

func TestLooksLikeRecall_RecallIntents(t *testing.T) {
	const filler = " — providing extra context here so the prompt clears the length gate without changing leading intent shape"
	cases := []string{
		"find the spot where we decided to use rate-limited queues for the daemon path" + filler,
		"how did we end up handling LFS pointer hydration without the git-lfs binary?" + filler,
		"has anyone explored using a content-addressed cache for redaction rules across sessions?" + filler,
		"what is the canonical place that owns the ledger path resolution helper in the codebase?" + filler,
		"explain the trade-off we picked between inline summarization and delegated summarization workflows" + filler,
		"where is the rate limiter used for the murmur delivery channel implemented in this repo?" + filler,
		"why does the prime command emit a banner even when stdout is not a tty in our hook flow?" + filler,
		"search for the redaction rule file we added in the gitleaks-extras integration last week" + filler,
	}
	for _, c := range cases {
		t.Run(c[:30], func(t *testing.T) {
			if !LooksLikeRecall(c) {
				t.Fatalf("expected recall intent for %q", c)
			}
		})
	}
}

func TestLooksLikeRecall_ActionVerbSkipped(t *testing.T) {
	cases := []string{
		"fix the bug where ox doctor reports a stale recording even when the daemon was bounced cleanly",
		"add a new redaction rule for AWS access key pairs that mirrors the GitHub PAT rule we shipped last sprint",
		"write a regression test that proves the hook does not block past the 100ms budget when the daemon is slow",
		"refactor the agent_hook.go phase dispatcher so each phase handler lives in its own file under cmd/ox",
		"delete the dead code in internal/lfs/legacy.go that nothing has imported since the rewrite landed",
		"implement the new whisper cap rule so per-author murmurs are bounded before token budgeting runs",
	}
	for _, c := range cases {
		t.Run(c[:20], func(t *testing.T) {
			if LooksLikeRecall(c) {
				t.Fatalf("expected action verb skip for %q", c)
			}
		})
	}
}

func TestLooksLikeRecall_ActionVerbDoesNotMatchMidPrompt(t *testing.T) {
	// "explain why fix X failed" should still trigger recall — the word
	// "fix" appears later, not as the leading verb.
	p := "explain why the fix we attempted in the prime command failed when run under the gemini cli adapter, including the relevant context around the hook firing order details"
	if !LooksLikeRecall(p) {
		t.Fatalf("expected mid-prompt action verb to NOT veto recall: %q", p)
	}
}

func TestStartsWithActionVerb_TrailingPunct(t *testing.T) {
	if !startsWithActionVerb("fix, the thing") {
		t.Fatalf("expected punctuation-stripped action verb to match")
	}
	if !startsWithActionVerb("FIX the thing") {
		t.Fatalf("expected case-insensitive action verb match")
	}
	if startsWithActionVerb("explain the thing") {
		t.Fatalf("non-action verb should not match")
	}
}
