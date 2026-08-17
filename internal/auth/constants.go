package auth

// ClientID is the OAuth client identifier for the ox CLI
const ClientID = "ox"

// DefaultScopes are the OAuth scopes requested during authentication
var DefaultScopes = []string{"user:profile", "sageox:write"}

// Device Flow endpoints (RFC 8628)
const (
	DeviceCodeEndpoint  = "/api/auth/device/code" //nolint:gosec // not a credential
	DeviceTokenEndpoint = "/api/v1/device/token"  //nolint:gosec // not a credential
	UserInfoEndpoint    = "/oauth2/userinfo"
)

// OAuth 2.0 endpoints
const (
	TokenEndpoint  = "/oauth2/token" //nolint:gosec // not a credential
	RevokeEndpoint = "/oauth2/revoke"
)

// PATPrefix marks a SageOx Personal Access Token (oxp_<body>_<crc>) — "acts as
// me". Used for local format checks (CRC precheck, redaction, display) only —
// never for server-side routing. Both PATs and team tokens validate through
// the same IntrospectEndpoint below.
const PATPrefix = "oxp_" //nolint:gosec // not a credential

// TeamTokenPrefix marks a SageOx Team Access Token (oxt_<body>_<crc>) — "acts
// as the team". Same grammar as a PAT (CRC-checked body), same validation door
// (IntrospectEndpoint) — the family only matters for local format checks and
// identity display.
const TeamTokenPrefix = "oxt_" //nolint:gosec // not a credential

// IntrospectEndpoint is the single, family-agnostic endpoint that validates
// ANY bearer credential — session/JWT, personal oxp_, or team oxt_ — and
// reports what/who it authenticates as. This replaced a hand-rolled
// oxp_-vs-/oauth2/userinfo branch: the CLI no longer needs to know a token's
// family before validating it.
//
// The server's response shape is frozen and mirrored by IntrospectResult in
// validate.go: an `active` flag, a `principal_kind` discriminator ("user" or
// "team-service"), `scope`, `token_type`, `expires_at`, and — depending on the
// principal — a `user`, `team`, or `token` object. Adding a field is
// backward-compatible; changing the meaning of one is not.
const IntrospectEndpoint = "/api/v1/auth/introspect" //nolint:gosec // not a credential
