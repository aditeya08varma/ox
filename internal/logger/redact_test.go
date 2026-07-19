package logger

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestRedactSecrets_Shapes verifies every credential shape the sink must scrub.
// Failure prevented: a raw token/PAT/URL-credential reaching a log line.
func TestRedactSecrets_Shapes(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		secret  string // must NOT appear in output
		mustCon string // must still appear (non-secret context preserved)
	}{
		{
			name:    "repos response git PAT (the HIGH finding)",
			in:      `{"token":"glpat-AABBCCDDEEFFGGHHIIJJ","server_url":"https://git.sageox.ai","username":"oauth2"}`,
			secret:  "glpat-AABBCCDDEEFFGGHHIIJJ",
			mustCon: "server_url",
		},
		{
			name:    "team info git_token",
			in:      `{"git_token":"oxp_1234567890abcdefghij","name":"team-x"}`,
			secret:  "oxp_1234567890abcdefghij",
			mustCon: "team-x",
		},
		{
			name:    "oauth access + refresh token",
			in:      `{"access_token":"eyJhbGciOi.secretpart.sig","refresh_token":"rt_supersecretvalue"}`,
			secret:  "rt_supersecretvalue",
			mustCon: "refresh_token",
		},
		{
			name:    "repo_salt auth material",
			in:      `{"repo_salt":"c2FsdHNhbHRzYWx0","repo":"acme"}`,
			secret:  "c2FsdHNhbHRzYWx0",
			mustCon: "acme",
		},
		{
			name:    "authorization bearer header",
			in:      "Authorization: Bearer eyJ0eXAiOiJKV1QifQ.payload.signature",
			secret:  "eyJ0eXAiOiJKV1QifQ.payload.signature",
			mustCon: "Authorization",
		},
		{
			name:    "credentials embedded in url",
			in:      "cloning https://oauth2:glpat-ZZZZZZZZZZ@git.sageox.ai/team/ledger.git",
			secret:  "glpat-ZZZZZZZZZZ",
			mustCon: "git.sageox.ai",
		},
		{
			name:    "bare sageox PAT prefix",
			in:      "probe used token oxp_deadbeefdeadbeefdead for ls-remote",
			secret:  "oxp_deadbeefdeadbeefdead",
			mustCon: "ls-remote",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactSecrets(tc.in)
			if strings.Contains(got, tc.secret) {
				t.Fatalf("secret leaked through redaction:\n in:  %s\n out: %s", tc.in, got)
			}
			if !strings.Contains(got, "[REDACTED]") {
				t.Fatalf("expected [REDACTED] marker in output, got: %s", got)
			}
			if tc.mustCon != "" && !strings.Contains(got, tc.mustCon) {
				t.Fatalf("non-secret context %q was destroyed: %s", tc.mustCon, got)
			}
		})
	}
}

// TestRedactSecrets_NoFalsePositives keeps the scrubber from mangling ordinary
// log lines — over-redaction hides real debugging signal.
func TestRedactSecrets_NoFalsePositives(t *testing.T) {
	clean := []string{
		`{"status":"ok","count":3,"endpoint":"sageox.ai"}`,
		"http response body (empty body)",
		"POST https://api.sageox.ai/api/v1/repo/init 200 in 42ms",
	}
	for _, s := range clean {
		if got := redactSecrets(s); got != s {
			t.Errorf("clean string altered:\n in:  %s\n out: %s", s, got)
		}
	}
}

// TestReplaceAttr_Backstop proves the sink itself scrubs — the durable fix.
// Failure prevented: any slog call site logging a token-bearing string leaks it.
func TestReplaceAttr_Backstop(t *testing.T) {
	var buf bytes.Buffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{
		Level:       slog.LevelDebug,
		ReplaceAttr: redactAttr,
	})
	l := slog.New(h)

	// Simulate LogHTTPResponseBody on the GetRepos success path.
	l.Debug("http response body", "body", `{"token":"glpat-LIVEPATVALUE123456","username":"oauth2"}`)

	out := buf.String()
	if strings.Contains(out, "glpat-LIVEPATVALUE123456") {
		t.Fatalf("PAT reached the log sink despite ReplaceAttr backstop:\n%s", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("expected redaction marker in sink output:\n%s", out)
	}
}
