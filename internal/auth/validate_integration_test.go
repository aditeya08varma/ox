package auth_test

import (
	"testing"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/twinapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Server-side token validation against the API twin ---
//
// auth.ValidateTokenServerSide routes every credential family through one
// family-agnostic introspection endpoint (auth.IntrospectEndpoint). The twin
// serves that route against a real HTTP listener, so these tests drive the
// whole path end to end: URL construction, bearer header, status handling,
// and — the behavior that motivated this file — surfacing the server's own
// error_description instead of a generic "authentication required".
//
// validate_test.go covers the same function against hand-authored httptest
// responses, which pins the parser. This file pins the interaction: a server
// that actually validates the credential decides the outcome, so each
// assertion below depends on WHY the credential was refused, not merely that
// something non-2xx came back.

// TestValidateTokenServerSide_ValidJWT verifies that a proper JWT passes validation.
// Failure prevented: false negatives in server-side validation.
func TestValidateTokenServerSide_ValidJWT(t *testing.T) {
	tw := twinapi.Start(t)
	fix := tw.WithAuthenticatedUser("valid@example.com", "Valid User")

	err := auth.ValidateTokenServerSide(tw.URL(), fix.JWT)
	assert.NoError(t, err)

	// the credential must have been validated at the introspection endpoint,
	// not at a retired per-family route that happens to also answer 200
	tw.AssertCalled(t, "GET", auth.IntrospectEndpoint)
}

// TestIntrospect_ReturnsUserPrincipal verifies the parsed result names the
// principal, not just that the credential was accepted.
// Failure prevented: a wire shape that unmarshals cleanly but leaves User nil,
// which would make every identity-rendering caller fall through to the generic
// "identity resolved server-side" copy for a token that named a real person.
func TestIntrospect_ReturnsUserPrincipal(t *testing.T) {
	tw := twinapi.Start(t)
	fix := tw.WithAuthenticatedUser("principal@example.com", "Principal Person")

	result, err := auth.Introspect(tw.URL(), fix.JWT)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.True(t, result.Active)
	assert.Equal(t, "user", result.PrincipalKind)
	require.NotNil(t, result.User, "a user principal must carry the user branch")
	assert.Equal(t, fix.UserID, result.User.ID)
	assert.Equal(t, "principal@example.com", result.User.Email)
	assert.Equal(t, "Principal Person", result.User.Name)
	assert.Nil(t, result.Team, "a user principal must not also carry a team branch")
}

// TestValidateTokenServerSide_OpaqueToken verifies that an opaque session token
// is rejected by server-side validation.
// Failure prevented: THE BUG — CLI stored opaque token as access_token, local
// check said "valid", but server rejected it. This test proves the server-side
// validation catches it.
func TestValidateTokenServerSide_OpaqueToken(t *testing.T) {
	tw := twinapi.Start(t)
	fix := tw.WithAuthenticatedUser("opaque@example.com", "Opaque User")

	err := auth.ValidateTokenServerSide(tw.URL(), fix.SessionToken)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "server rejected token")
	// the reason must reach the caller: rejected because it is not a JWT, not
	// because of a missing header or an unreachable server
	assert.Contains(t, err.Error(), "malformed JWT")
}

// TestValidateTokenServerSide_ExpiredJWT verifies that an expired JWT is
// rejected by the server even though it was once valid.
// Failure prevented: local check passes (token exists in auth.json) but
// server correctly identifies expiry.
func TestValidateTokenServerSide_ExpiredJWT(t *testing.T) {
	tw := twinapi.Start(t)
	fix := tw.WithAuthenticatedUser("expired@example.com", "Expired User")

	// the same credential is accepted before the clock moves, so the rejection
	// below can only be attributed to expiry
	require.NoError(t, auth.ValidateTokenServerSide(tw.URL(), fix.JWT))

	// advance clock past 24h JWT expiry
	tw.AdvanceTime(25 * time.Hour)

	err := auth.ValidateTokenServerSide(tw.URL(), fix.JWT)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "server rejected token")
	assert.Contains(t, err.Error(), "expired")
}

// TestValidateTokenServerSide_ServerDown verifies graceful handling when the
// server is unreachable. This path never reaches the twin.
// Failure prevented: init/doctor/status crashing instead of showing a useful
// error when the server is down.
func TestValidateTokenServerSide_ServerDown(t *testing.T) {
	// use a URL that will immediately refuse connections
	err := auth.ValidateTokenServerSide("http://127.0.0.1:1", "some-jwt-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not reach")
}

// TestValidateTokenServerSide_NonJWTTokenRejected verifies that an orphaned
// session token — opaque, and pointing at a user that never existed — is
// rejected by introspection, and rejected for the right reason.
// Failure prevented: an opaque credential passing because introspection only
// checked that SOMETHING was in the Authorization header.
func TestValidateTokenServerSide_NonJWTTokenRejected(t *testing.T) {
	tw := twinapi.Start(t)

	sess := tw.CreateOrphanedSession("usr_ghost")

	err := auth.ValidateTokenServerSide(tw.URL(), sess.Token)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "server rejected token")
	// an opaque session token is not a JWT, so it never reaches the principal
	// lookup — it fails the structural check first
	assert.Contains(t, err.Error(), "malformed JWT")
}

// TestValidateTokenServerSide_UserNotFoundError verifies that a descriptive
// server-side rejection reaches the caller verbatim.
// Failure prevented: the reported production bug — the server explained that
// the account behind an otherwise valid credential no longer existed, and the
// CLI replaced that with a generic "authentication required", sending the
// operator to rotate a credential that was fine.
func TestValidateTokenServerSide_UserNotFoundError(t *testing.T) {
	tw := twinapi.Start(t)
	fix := tw.WithAuthenticatedUser("vanish@example.com", "Vanisher")

	// the credential is accepted while the account exists, so the rejection
	// below is attributable to the vanished principal and nothing else
	require.NoError(t, auth.ValidateTokenServerSide(tw.URL(), fix.JWT))

	tw.DeleteUser(fix.UserID)

	err := auth.ValidateTokenServerSide(tw.URL(), fix.JWT)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "server rejected token")
	// the server's own description — NOT a generic status-code fallback
	assert.Contains(t, err.Error(), "user account not found")
	assert.NotContains(t, err.Error(), "HTTP 401",
		"a bare status code means the error_description was dropped")
}

// TestValidateTokenServerSide_FaultInjection verifies that a server-side
// failure on the introspection endpoint is detected, reported with its status,
// and recovered from once the failure clears.
// Failure prevented: a transient 5xx being cached or latched into a permanent
// "your token is bad" verdict.
func TestValidateTokenServerSide_FaultInjection(t *testing.T) {
	tw := twinapi.Start(t)
	fix := tw.WithAuthenticatedUser("fault@example.com", "Fault User")

	// verify it works first
	err := auth.ValidateTokenServerSide(tw.URL(), fix.JWT)
	require.NoError(t, err)

	// inject a 503 on introspection
	tw.InjectFault(auth.IntrospectEndpoint, 503)

	err = auth.ValidateTokenServerSide(tw.URL(), fix.JWT)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "server rejected token")
	// the injected status must reach the caller — a bodyless failure has no
	// description, so the status code is the only detail available
	assert.Contains(t, err.Error(), "503")

	// clear and verify recovery
	tw.ClearFaults()

	err = auth.ValidateTokenServerSide(tw.URL(), fix.JWT)
	assert.NoError(t, err)
}
