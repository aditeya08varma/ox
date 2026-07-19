package logger

import (
	"log/slog"
	"regexp"
)

// redactSecrets scrubs credential material from a string before it reaches a
// log sink. It is the last-line backstop wired into every handler through
// ReplaceAttr (see Init), so no call site can leak a raw secret by logging an
// arbitrary string value — most importantly the full HTTP response bodies that
// carry the git PAT (ReposResponse.token / TeamInfo git_token) and repo_salt.
//
// It deliberately overlaps a little with internal/auth.redactedBody (auth JSON
// error-body allowlist) and internal/gitutil.SanitizeOutput (git subprocess
// output). Those redact at their own layers; this guards the slog sink itself.
// logger imports only the stdlib, so it can sit under every other package
// without an import cycle — which is exactly why the backstop lives here.
//
// Matched shapes:
//   - JSON string field with a sensitive key: "token":"…", "git_token":"…"
//   - Authorization: Bearer <token> and a bare "Bearer <token>"
//   - credentials in a URL userinfo: https://user:secret@host
//   - well-known PAT prefixes (SageOx oxp_, GitLab glpat-, GitHub gh*_)
func redactSecrets(s string) string {
	if s == "" {
		return s
	}
	s = jsonSecretRe.ReplaceAllString(s, `${1}[REDACTED]${2}`)
	s = bearerRe.ReplaceAllString(s, `${1}[REDACTED]`)
	s = urlCredRe.ReplaceAllString(s, `${1}:[REDACTED]@`)
	s = patRe.ReplaceAllString(s, `[REDACTED]`)
	return s
}

var (
	// "sensitive_key": "value" — case-insensitive key, string values only.
	// Longer keys precede their substrings so the alternation matches the
	// specific field (refresh_token) rather than the generic one (token).
	jsonSecretRe = regexp.MustCompile(`(?i)("(?:access_token|refresh_token|session_token|git_token|token|password|passwd|authorization|repo_salt|client_secret|secret|api[_-]?key)"\s*:\s*")[^"]*(")`)
	// Authorization: Bearer xxxxx  /  "Bearer xxxxx"
	bearerRe = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]+`)
	// scheme://user:secret@host — keep the username, redact the secret.
	urlCredRe = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^:/?#\s@]+):[^@/?#\s]+@`)
	// well-known personal-access-token prefixes (bare token strings).
	patRe = regexp.MustCompile(`(?:oxp_|glpat-|ghp_|gho_|ghu_|ghs_|github_pat_)[A-Za-z0-9_-]{8,}`)
)

// redactAttr is the slog ReplaceAttr hook: scrub every string-valued attribute
// (body, response, url, error, …) through redactSecrets. Non-string attrs are
// passed through untouched.
func redactAttr(_ []string, a slog.Attr) slog.Attr {
	if a.Value.Kind() == slog.KindString {
		a.Value = slog.StringValue(redactSecrets(a.Value.String()))
	}
	return a
}
