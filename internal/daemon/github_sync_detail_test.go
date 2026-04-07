package daemon

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// --- extractGitHubDetail ---
// Failure prevented: generic "GitHub authentication failed" shown instead of
// the specific API error (e.g., "Resource not accessible by personal access token").

func TestExtractGitHubDetail_JSONBody(t *testing.T) {
	err := fmt.Errorf(`sync PRs: list PRs: github authentication failed: status 403: {"message":"Resource not accessible by personal access token","documentation_url":"https://docs.github.com/rest/pulls/pulls#list-pull-requests","status":"403"}`)
	detail := extractGitHubDetail(err)
	assert.Equal(t, "Resource not accessible by personal access token", detail)
}

func TestExtractGitHubDetail_401(t *testing.T) {
	err := fmt.Errorf(`github authentication failed: status 401: {"message":"Bad credentials","documentation_url":"https://docs.github.com/rest"}`)
	detail := extractGitHubDetail(err)
	assert.Equal(t, "Bad credentials", detail)
}

func TestExtractGitHubDetail_NoJSON(t *testing.T) {
	err := fmt.Errorf(`github authentication failed: status 403: Forbidden`)
	detail := extractGitHubDetail(err)
	assert.Equal(t, "Forbidden", detail)
}

func TestExtractGitHubDetail_MalformedJSON(t *testing.T) {
	// Truncated JSON body — should fall through to text-based fallback.
	err := fmt.Errorf(`github authentication failed: status 403: {"message":"trunc`)
	detail := extractGitHubDetail(err)
	assert.Equal(t, `{"message":"trunc`, detail)
}

func TestExtractGitHubDetail_PlainError(t *testing.T) {
	err := fmt.Errorf("some unrelated error")
	detail := extractGitHubDetail(err)
	assert.Equal(t, "", detail)
}
