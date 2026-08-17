package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/sageox/ox/internal/auth"
)

// TestLoginShadowedByEnvToken covers the "green login, then nothing works" case.
//
// Failure prevented: a user with a truncated SAGEOX_TOKEN runs `ox login`, the
// device flow completes and stores a good credential, and then every subsequent
// command fails — because the env token takes precedence and is itself refused.
// Without this predicate, `ox login` either reports a bare
// "authentication succeeded but failed to retrieve token" (false: the login
// worked and was stored) or, before the fail-closed change, reported plain
// success and left the user with no way to connect the two events.
//
// These use t.Setenv, which is incompatible with t.Parallel — env-token
// resolution is process-global, matching the serial pattern internal/auth's own
// env-token tests use.
func TestLoginShadowedByEnvToken(t *testing.T) {
	const ep = "https://sageox.ai"

	// a CRC-failing value: the pinned personal vector with a corrupted final
	// character, i.e. exactly the shape of a truncated or mistyped paste.
	const malformed = "oxp_test_4bDZfX"
	// the same vector, intact.
	const valid = "oxp_test_4bDZfN"

	tests := []struct {
		name     string
		envToken string
		// endpoint SAGEOX_ENDPOINT is pinned to; "" means leave it unset so the
		// env token binds to the production default.
		envEndpoint string
		tokenErr    error
		want        bool
	}{
		{
			name:     "malformed env token shadows the stored login",
			envToken: malformed,
			want:     true,
		},
		{
			name:     "sentinel from the post-login token lookup is sufficient on its own",
			envToken: "",
			tokenErr: fmt.Errorf("wrapped: %w", auth.ErrEnvTokenMalformed),
			want:     true,
		},
		{
			name:     "a valid env token is not a shadow — it is a working credential",
			envToken: valid,
			want:     false,
		},
		{
			name:     "no env token at all",
			envToken: "",
			want:     false,
		},
		{
			name: "an unrelated token-lookup failure must not be reported as a shadow",
			// this is the guard against over-reach: a genuine failure to read
			// auth.json must still surface as an error, not be swallowed into a
			// cheerful "your login was stored" message.
			envToken: "",
			tokenErr: errors.New("failed to parse auth file"),
			want:     false,
		},
		{
			name: "malformed env token bound to a DIFFERENT endpoint does not shadow this one",
			// the env token was never meant for this endpoint, so a login here
			// is genuinely usable and must not be reported as shadowed.
			envToken:    malformed,
			envEndpoint: "https://staging.sageox.ai",
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(auth.EnvVarToken, tt.envToken)
			if tt.envEndpoint != "" {
				t.Setenv("SAGEOX_ENDPOINT", tt.envEndpoint)
			}

			got := loginShadowedByEnvToken(ep, tt.tokenErr)
			if got != tt.want {
				t.Errorf("loginShadowedByEnvToken(%q, %v) = %v, want %v", ep, tt.tokenErr, got, tt.want)
			}
		})
	}
}
