package gitserver

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCredentialStatus_FormatExpiry(t *testing.T) {
	tests := []struct {
		name            string
		timeUntilExpiry time.Duration
		want            string
	}{
		{
			name:            "expired",
			timeUntilExpiry: -1 * time.Hour,
			want:            "expired",
		},
		{
			name:            "seconds remaining",
			timeUntilExpiry: 45 * time.Second,
			want:            "45s",
		},
		{
			name:            "minutes remaining",
			timeUntilExpiry: 30 * time.Minute,
			want:            "30m",
		},
		{
			name:            "hours remaining",
			timeUntilExpiry: 5 * time.Hour,
			want:            "5h",
		},
		{
			name:            "days remaining",
			timeUntilExpiry: 72 * time.Hour,
			want:            "3d",
		},
		{
			name:            "just under a minute",
			timeUntilExpiry: 59 * time.Second,
			want:            "59s",
		},
		{
			name:            "just under an hour",
			timeUntilExpiry: 59 * time.Minute,
			want:            "59m",
		},
		{
			name:            "just under a day",
			timeUntilExpiry: 23 * time.Hour,
			want:            "23h",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := CredentialStatus{TimeUntilExpiry: tt.timeUntilExpiry}
			assert.Equal(t, tt.want, s.FormatExpiry())
		})
	}
}

func TestCredentialStatus_NeedsRefresh(t *testing.T) {
	tests := []struct {
		name  string
		valid bool
		want  bool
	}{
		{"valid credentials don't need refresh", true, false},
		{"invalid credentials need refresh", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := CredentialStatus{Valid: tt.valid}
			assert.Equal(t, tt.want, s.NeedsRefresh())
		})
	}
}

func TestCheckCredentialStatusForEndpoint(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T) // configure credential files
		endpoint   string
		wantValid  bool
		wantReason string
	}{
		{
			name:       "missing credentials",
			setup:      func(t *testing.T) { setupTestDir(t) },
			endpoint:   "https://sageox.ai",
			wantValid:  false,
			wantReason: "missing",
		},
		{
			name: "expired credentials",
			setup: func(t *testing.T) {
				setupTestDir(t)
				creds := createTestCredentials(-1 * time.Hour) // expired 1h ago
				require.NoError(t, SaveCredentialsForEndpoint("https://sageox.ai", creds))
			},
			endpoint:   "https://sageox.ai",
			wantValid:  false,
			wantReason: "expired",
		},
		{
			name: "expiring soon (under NearExpiryThreshold)",
			setup: func(t *testing.T) {
				setupTestDir(t)
				creds := createTestCredentials(30 * time.Minute) // 30m < 1h threshold
				require.NoError(t, SaveCredentialsForEndpoint("https://sageox.ai", creds))
			},
			endpoint:   "https://sageox.ai",
			wantValid:  false,
			wantReason: "expiring soon",
		},
		{
			name: "valid credentials",
			setup: func(t *testing.T) {
				setupTestDir(t)
				creds := createTestCredentials(24 * time.Hour)
				require.NoError(t, SaveCredentialsForEndpoint("https://sageox.ai", creds))
			},
			endpoint:  "https://sageox.ai",
			wantValid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup(t)
			status := CheckCredentialStatusForEndpoint(tt.endpoint)
			assert.Equal(t, tt.wantValid, status.Valid)
			if tt.wantReason != "" {
				assert.Contains(t, status.Reason, tt.wantReason)
			}
		})
	}
}

func TestRefreshCredentialsForEndpoint_SkipsWhenValid(t *testing.T) {
	setupTestDir(t)
	creds := createTestCredentials(24 * time.Hour)
	require.NoError(t, SaveCredentialsForEndpoint("https://sageox.ai", creds))

	fetcherCalled := false
	fetcher := func() (*GitCredentials, error) {
		fetcherCalled = true
		return nil, nil
	}

	result := RefreshCredentialsForEndpoint("https://sageox.ai", fetcher, false)
	assert.True(t, result.Skipped, "should skip when credentials are valid")
	assert.False(t, fetcherCalled, "fetcher should not be called when credentials are valid")
}

func TestRefreshCredentialsForEndpoint_ForceRefresh(t *testing.T) {
	setupTestDir(t)
	creds := createTestCredentials(24 * time.Hour)
	require.NoError(t, SaveCredentialsForEndpoint("https://sageox.ai", creds))

	newCreds := createTestCredentials(48 * time.Hour)
	newCreds.Token = "refreshed-token"
	fetcher := func() (*GitCredentials, error) {
		return &newCreds, nil
	}

	result := RefreshCredentialsForEndpoint("https://sageox.ai", fetcher, true)
	assert.True(t, result.Refreshed)
	assert.NoError(t, result.Error)

	// verify new token was saved
	loaded, err := LoadCredentialsForEndpoint("https://sageox.ai")
	require.NoError(t, err)
	assert.Equal(t, "refreshed-token", loaded.Token)
}

func TestRefreshCredentialsForEndpoint_FetcherError(t *testing.T) {
	setupTestDir(t)
	// no existing creds — status will be "missing" so refresh is needed

	fetcher := func() (*GitCredentials, error) {
		return nil, fmt.Errorf("network error")
	}

	result := RefreshCredentialsForEndpoint("https://sageox.ai", fetcher, false)
	assert.False(t, result.Refreshed)
	assert.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "network error")
}

func TestRefreshCredentialsForEndpoint_FetcherReturnsNil(t *testing.T) {
	setupTestDir(t)

	fetcher := func() (*GitCredentials, error) {
		return nil, nil // no error but nil creds
	}

	result := RefreshCredentialsForEndpoint("https://sageox.ai", fetcher, false)
	assert.False(t, result.Refreshed)
	assert.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "nil credentials")
}

func TestEnsureValidCredentialsForEndpoint(t *testing.T) {
	setupTestDir(t)

	// save valid creds
	creds := createTestCredentials(24 * time.Hour)
	require.NoError(t, SaveCredentialsForEndpoint("https://sageox.ai", creds))

	fetcher := func() (*GitCredentials, error) {
		return nil, fmt.Errorf("should not be called")
	}

	status, err := EnsureValidCredentialsForEndpoint("https://sageox.ai", fetcher)
	require.NoError(t, err)
	assert.True(t, status.Valid)
}

func TestEnsureValidCredentialsForEndpoint_Refreshes(t *testing.T) {
	setupTestDir(t)
	// no existing creds — will trigger refresh

	newCreds := createTestCredentials(24 * time.Hour)
	newCreds.Token = "fresh-token"
	fetcher := func() (*GitCredentials, error) {
		return &newCreds, nil
	}

	status, err := EnsureValidCredentialsForEndpoint("https://sageox.ai", fetcher)
	require.NoError(t, err)
	assert.True(t, status.Valid)
}

func TestRemoveCredentialsForEndpoint(t *testing.T) {
	setupTestDir(t)

	// save creds for two endpoints
	creds1 := createTestCredentials(24 * time.Hour)
	creds1.Token = "token-alpha"
	require.NoError(t, SaveCredentialsForEndpoint("https://sageox.ai", creds1))

	creds2 := createTestCredentials(24 * time.Hour)
	creds2.Token = "token-beta"
	require.NoError(t, SaveCredentialsForEndpoint("https://test.sageox.ai", creds2))

	// remove only one endpoint
	require.NoError(t, RemoveCredentialsForEndpoint("https://sageox.ai"))

	// verify removed
	loaded, err := LoadCredentialsForEndpoint("https://sageox.ai")
	require.NoError(t, err)
	assert.Nil(t, loaded, "removed endpoint should have no credentials")

	// verify other endpoint untouched
	loaded2, err := LoadCredentialsForEndpoint("https://test.sageox.ai")
	require.NoError(t, err)
	require.NotNil(t, loaded2)
	assert.Equal(t, "token-beta", loaded2.Token)
}

func TestRemoveCredentialsForEndpoint_NonExistent(t *testing.T) {
	setupTestDir(t)
	// removing non-existent credentials should not error
	assert.NoError(t, RemoveCredentialsForEndpoint("https://sageox.ai"))
}

func TestCheckCredentialStatusForEndpoint_RepoCount(t *testing.T) {
	setupTestDir(t)

	creds := GitCredentials{
		Token:     "test-token",
		ServerURL: "https://git.example.com",
		Username:  "oauth2",
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Repos: map[string]RepoEntry{
			"team_a": {Name: "A", Type: "team-context", URL: "https://git.example.com/a.git"},
			"team_b": {Name: "B", Type: "team-context", URL: "https://git.example.com/b.git"},
			"team_c": {Name: "C", Type: "team-context", URL: "https://git.example.com/c.git"},
		},
	}
	require.NoError(t, SaveCredentialsForEndpoint("https://example.com", creds))

	status := CheckCredentialStatusForEndpoint("https://example.com")
	assert.True(t, status.Valid)
	assert.Equal(t, 3, status.RepoCount)
	assert.False(t, status.ExpiresAt.IsZero())
}
