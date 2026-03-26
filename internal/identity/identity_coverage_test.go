package identity

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- parseAWSArn ---
// Prevents mishandling of ARN formats (IAM user vs assumed role vs malformed)

func TestParseAWSArn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		arn         string
		wantAccount string
		wantName    string
	}{
		{
			name:        "iam user",
			arn:         "arn:aws:iam::123456789012:user/alice",
			wantAccount: "123456789012",
			wantName:    "alice",
		},
		{
			name:        "assumed role",
			arn:         "arn:aws:sts::123456789012:assumed-role/dev-role/session-name",
			wantAccount: "123456789012",
			wantName:    "dev-role",
		},
		{
			name:        "root account (no slash in resource)",
			arn:         "arn:aws:iam::123456789012:root",
			wantAccount: "123456789012",
			wantName:    "",
		},
		{
			name:        "too few parts",
			arn:         "arn:aws:iam",
			wantAccount: "",
			wantName:    "",
		},
		{
			name:        "empty string",
			arn:         "",
			wantAccount: "",
			wantName:    "",
		},
		{
			name:        "exactly 6 parts with user path",
			arn:         "arn:aws:iam::999999999999:user/bob",
			wantAccount: "999999999999",
			wantName:    "bob",
		},
		{
			name:        "federated user with nested path",
			arn:         "arn:aws:sts::111111111111:federated-user/devops/extra",
			wantAccount: "111111111111",
			wantName:    "devops",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			account, name := parseAWSArn(tt.arn)
			assert.Equal(t, tt.wantAccount, account)
			assert.Equal(t, tt.wantName, name)
		})
	}
}

// --- parseGCloudConfigAccount ---
// Prevents INI parsing regressions: section boundaries, missing values, whitespace

func TestParseGCloudConfigAccount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name: "standard config",
			content: `[core]
account = user@example.com
project = my-project
`,
			want: "user@example.com",
		},
		{
			name: "account in non-core section is ignored",
			content: `[compute]
account = wrong@example.com

[core]
account = right@example.com
`,
			want: "right@example.com",
		},
		{
			name: "no core section",
			content: `[compute]
region = us-central1
`,
			want: "",
		},
		{
			name:    "empty content",
			content: "",
			want:    "",
		},
		{
			name: "core section with no account key",
			content: `[core]
project = my-project
`,
			want: "",
		},
		{
			name: "account with extra whitespace",
			content: `[core]
account =   padded@example.com
`,
			want: "padded@example.com",
		},
		{
			name: "core section followed by another section resets scope",
			content: `[core]
project = p1

[other]
account = wrong@example.com
`,
			want: "",
		},
		{
			name: "no equals sign in account line",
			content: `[core]
account user@example.com
`,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, parseGCloudConfigAccount(tt.content))
		})
	}
}

// --- normalizeGiteaURL ---
// Prevents URL comparison mismatches (trailing slashes, scheme differences)

func TestNormalizeGiteaURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
		want string
	}{
		{"https url", "https://gitea.example.com/user/repo", "gitea.example.com"},
		{"http url", "http://gitea.example.com/user/repo", "gitea.example.com"},
		{"with port", "https://gitea.example.com:3000/user/repo", "gitea.example.com:3000"},
		{"trailing slash", "https://gitea.example.com/", "gitea.example.com"},
		{"empty", "", ""},
		{"just host", "https://gitea.local", "gitea.local"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, normalizeGiteaURL(tt.url))
		})
	}
}

func TestTrimTrailingSlash(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"https://example.com/", "https://example.com"},
		{"https://example.com", "https://example.com"},
		{"/", ""},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, trimTrailingSlash(tt.input))
		})
	}
}

// --- xdgConfigHome ---
// Prevents config file lookups in wrong directory when XDG_CONFIG_HOME is set vs unset

func TestXdgConfigHome(t *testing.T) {
	t.Run("respects XDG_CONFIG_HOME env", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "/custom/config")
		assert.Equal(t, "/custom/config", xdgConfigHome())
	})

	t.Run("falls back to home/.config when unset", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")
		result := xdgConfigHome()
		// should end with .config
		assert.Contains(t, result, ".config")
	})
}

// --- readServiceAccountCredentials ---
// Prevents accepting non-service-account files or files missing required fields

func TestReadServiceAccountCredentials(t *testing.T) {
	t.Parallel()

	t.Run("valid service account", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "sa.json")

		creds := map[string]string{
			"type":         "service_account",
			"client_email": "sa@project.iam.gserviceaccount.com",
			"project_id":   "my-project",
		}
		data, _ := json.Marshal(creds)
		require.NoError(t, os.WriteFile(path, data, 0o644))

		identity, err := readServiceAccountCredentials(path)
		require.NoError(t, err)
		assert.Equal(t, "gcp:sa@project.iam.gserviceaccount.com", identity.UserID)
		assert.Equal(t, "sa@project.iam.gserviceaccount.com", identity.Email)
		assert.Equal(t, "gcp", identity.Source)
		assert.False(t, identity.Verified)
	})

	t.Run("wrong type rejected", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "sa.json")

		creds := map[string]string{
			"type":         "authorized_user",
			"client_email": "user@example.com",
		}
		data, _ := json.Marshal(creds)
		require.NoError(t, os.WriteFile(path, data, 0o644))

		_, err := readServiceAccountCredentials(path)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not a service account")
	})

	t.Run("missing client_email rejected", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "sa.json")

		creds := map[string]string{"type": "service_account"}
		data, _ := json.Marshal(creds)
		require.NoError(t, os.WriteFile(path, data, 0o644))

		_, err := readServiceAccountCredentials(path)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "missing client_email")
	})

	t.Run("file not found", func(t *testing.T) {
		t.Parallel()
		_, err := readServiceAccountCredentials("/nonexistent/path.json")
		assert.Error(t, err)
	})

	t.Run("invalid json", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "bad.json")
		require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o644))

		_, err := readServiceAccountCredentials(path)
		assert.Error(t, err)
	})
}

// --- readGCloudConfig (file-based) ---
// Prevents silent failures when gcloud config is missing or malformed

func TestReadGCloudConfig(t *testing.T) {
	t.Run("reads valid config", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)

		configDir := filepath.Join(dir, "gcloud", "configurations")
		require.NoError(t, os.MkdirAll(configDir, 0o755))
		content := "[core]\naccount = dev@example.com\nproject = my-proj\n"
		require.NoError(t, os.WriteFile(filepath.Join(configDir, "config_default"), []byte(content), 0o644))

		identity := readGCloudConfig()
		require.NotNil(t, identity)
		assert.Equal(t, "gcp:dev@example.com", identity.UserID)
		assert.Equal(t, "dev@example.com", identity.Email)
		assert.False(t, identity.Verified)
	})

	t.Run("returns nil when config missing", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		assert.Nil(t, readGCloudConfig())
	})

	t.Run("returns nil when no account in config", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)

		configDir := filepath.Join(dir, "gcloud", "configurations")
		require.NoError(t, os.MkdirAll(configDir, 0o755))
		content := "[compute]\nregion = us-west1\n"
		require.NoError(t, os.WriteFile(filepath.Join(configDir, "config_default"), []byte(content), 0o644))

		assert.Nil(t, readGCloudConfig())
	})
}

// --- readApplicationDefaultCredentials ---
// Prevents ignoring service account ADC or accepting authorized_user (which needs API)

func TestReadApplicationDefaultCredentials(t *testing.T) {
	t.Run("reads service account ADC", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)

		adcDir := filepath.Join(dir, "gcloud")
		require.NoError(t, os.MkdirAll(adcDir, 0o755))

		creds := map[string]string{
			"type":         "service_account",
			"client_email": "sa@proj.iam.gserviceaccount.com",
		}
		data, _ := json.Marshal(creds)
		require.NoError(t, os.WriteFile(filepath.Join(adcDir, "application_default_credentials.json"), data, 0o644))

		identity := readApplicationDefaultCredentials()
		require.NotNil(t, identity)
		assert.Equal(t, "sa@proj.iam.gserviceaccount.com", identity.Email)
	})

	t.Run("authorized_user returns nil (needs API)", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)

		adcDir := filepath.Join(dir, "gcloud")
		require.NoError(t, os.MkdirAll(adcDir, 0o755))

		creds := map[string]string{
			"type":      "authorized_user",
			"client_id": "1234.apps.googleusercontent.com",
		}
		data, _ := json.Marshal(creds)
		require.NoError(t, os.WriteFile(filepath.Join(adcDir, "application_default_credentials.json"), data, 0o644))

		assert.Nil(t, readApplicationDefaultCredentials())
	})

	t.Run("missing file returns nil", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		assert.Nil(t, readApplicationDefaultCredentials())
	})
}

// --- readGHHostsConfig ---
// Prevents losing GitHub token when hosts.yml exists but has no github.com entry

func TestReadGHHostsConfig(t *testing.T) {
	t.Run("reads github.com token", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)

		ghDir := filepath.Join(dir, "gh")
		require.NoError(t, os.MkdirAll(ghDir, 0o755))

		content := `github.com:
  user: testuser
  oauth_token: ghp_test123
  git_protocol: https
`
		require.NoError(t, os.WriteFile(filepath.Join(ghDir, "hosts.yml"), []byte(content), 0o644))

		assert.Equal(t, "ghp_test123", readGHHostsConfig())
	})

	t.Run("returns empty when no github.com entry", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)

		ghDir := filepath.Join(dir, "gh")
		require.NoError(t, os.MkdirAll(ghDir, 0o755))

		content := `gitlab.com:
  user: testuser
  oauth_token: glpat_test
`
		require.NoError(t, os.WriteFile(filepath.Join(ghDir, "hosts.yml"), []byte(content), 0o644))

		assert.Empty(t, readGHHostsConfig())
	})

	t.Run("returns empty when file missing", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		assert.Empty(t, readGHHostsConfig())
	})

	t.Run("returns empty on invalid yaml", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)

		ghDir := filepath.Join(dir, "gh")
		require.NoError(t, os.MkdirAll(ghDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(ghDir, "hosts.yml"), []byte("{{{{"), 0o644))

		assert.Empty(t, readGHHostsConfig())
	})
}

// --- readGLabConfig ---
// Prevents losing GitLab token when config exists but has no gitlab.com host

func TestReadGLabConfig(t *testing.T) {
	t.Run("reads gitlab.com token", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)

		glabDir := filepath.Join(dir, "glab-cli")
		require.NoError(t, os.MkdirAll(glabDir, 0o755))

		content := `hosts:
  gitlab.com:
    token: glpat_test456
    api_host: gitlab.com
    user: testuser
`
		require.NoError(t, os.WriteFile(filepath.Join(glabDir, "config.yml"), []byte(content), 0o644))

		assert.Equal(t, "glpat_test456", readGLabConfig())
	})

	t.Run("returns empty when no gitlab.com host", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)

		glabDir := filepath.Join(dir, "glab-cli")
		require.NoError(t, os.MkdirAll(glabDir, 0o755))

		content := `hosts:
  gitlab.internal.com:
    token: glpat_internal
`
		require.NoError(t, os.WriteFile(filepath.Join(glabDir, "config.yml"), []byte(content), 0o644))

		assert.Empty(t, readGLabConfig())
	})

	t.Run("returns empty when file missing", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		assert.Empty(t, readGLabConfig())
	})
}

// --- readTeaConfig ---
// Prevents token lookup failures when instance URL has scheme/slash variations

func TestReadTeaConfig(t *testing.T) {
	t.Run("matches instance URL", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)

		teaDir := filepath.Join(dir, "tea")
		require.NoError(t, os.MkdirAll(teaDir, 0o755))

		content := `logins:
  - name: corp
    url: https://gitea.corp.com
    token: tea_token_123
    default: false
  - name: personal
    url: https://gitea.home.net
    token: tea_token_456
    default: true
`
		require.NoError(t, os.WriteFile(filepath.Join(teaDir, "config.yml"), []byte(content), 0o644))

		assert.Equal(t, "tea_token_123", readTeaConfig("https://gitea.corp.com"))
	})

	t.Run("falls back to default login when no match", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)

		teaDir := filepath.Join(dir, "tea")
		require.NoError(t, os.MkdirAll(teaDir, 0o755))

		content := `logins:
  - name: personal
    url: https://gitea.home.net
    token: tea_default_tok
    default: true
`
		require.NoError(t, os.WriteFile(filepath.Join(teaDir, "config.yml"), []byte(content), 0o644))

		assert.Equal(t, "tea_default_tok", readTeaConfig("https://unknown.host.com"))
	})

	t.Run("returns empty when no match and no default", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)

		teaDir := filepath.Join(dir, "tea")
		require.NoError(t, os.MkdirAll(teaDir, 0o755))

		content := `logins:
  - name: corp
    url: https://gitea.corp.com
    token: tea_token_123
    default: false
`
		require.NoError(t, os.WriteFile(filepath.Join(teaDir, "config.yml"), []byte(content), 0o644))

		assert.Empty(t, readTeaConfig("https://other.host.com"))
	})

	t.Run("returns empty when config missing", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		assert.Empty(t, readTeaConfig("https://any.host.com"))
	})
}

// --- readTeaConfigToken ---
// Prevents host matching failures (e.g., stored as URL vs bare host)

func TestReadTeaConfigToken(t *testing.T) {
	t.Run("finds token for matching host", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)

		teaDir := filepath.Join(dir, "tea")
		require.NoError(t, os.MkdirAll(teaDir, 0o755))

		content := `logins:
  - url: https://gitea.example.com
    token: matched_token
`
		require.NoError(t, os.WriteFile(filepath.Join(teaDir, "config.yml"), []byte(content), 0o644))

		assert.Equal(t, "matched_token", readTeaConfigToken("gitea.example.com"))
	})

	t.Run("returns empty for non-matching host", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)

		teaDir := filepath.Join(dir, "tea")
		require.NoError(t, os.MkdirAll(teaDir, 0o755))

		content := `logins:
  - url: https://gitea.example.com
    token: some_token
`
		require.NoError(t, os.WriteFile(filepath.Join(teaDir, "config.yml"), []byte(content), 0o644))

		assert.Empty(t, readTeaConfigToken("other.host.com"))
	})

	t.Run("returns empty when config missing", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		assert.Empty(t, readTeaConfigToken("any.host"))
	})
}

// --- readAzureCliConfig ---
// Prevents token lookup failures when azure config is missing or malformed

func TestReadAzureCliConfig(t *testing.T) {
	t.Run("reads pat_token from config", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("HOME", dir)

		azureDir := filepath.Join(dir, ".azure")
		require.NoError(t, os.MkdirAll(azureDir, 0o755))

		content := `devops:
  pat_token: azure_pat_123
`
		require.NoError(t, os.WriteFile(filepath.Join(azureDir, "config"), []byte(content), 0o644))

		assert.Equal(t, "azure_pat_123", readAzureCliConfig())
	})

	t.Run("returns empty when config missing", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		assert.Empty(t, readAzureCliConfig())
	})
}

// --- token env var functions ---
// Prevents breaking CI/CD flows that rely on specific env var names

func TestGetGitHubToken_EnvVars(t *testing.T) {
	t.Run("GITHUB_TOKEN takes precedence", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "gh_tok_1")
		t.Setenv("GH_TOKEN", "gh_tok_2")
		// clear config path so file-based lookup fails
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		assert.Equal(t, "gh_tok_1", GetGitHubToken())
	})

	t.Run("GH_TOKEN used when GITHUB_TOKEN empty", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "")
		t.Setenv("GH_TOKEN", "gh_tok_2")
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		assert.Equal(t, "gh_tok_2", GetGitHubToken())
	})
}

func TestGetGitLabToken_EnvVars(t *testing.T) {
	t.Run("GITLAB_TOKEN takes precedence", func(t *testing.T) {
		t.Setenv("GITLAB_TOKEN", "gl_tok_1")
		t.Setenv("GITLAB_PRIVATE_TOKEN", "gl_tok_2")
		assert.Equal(t, "gl_tok_1", getGitLabToken())
	})

	t.Run("GITLAB_PRIVATE_TOKEN used as fallback", func(t *testing.T) {
		t.Setenv("GITLAB_TOKEN", "")
		t.Setenv("GITLAB_PRIVATE_TOKEN", "gl_tok_2")
		assert.Equal(t, "gl_tok_2", getGitLabToken())
	})
}

func TestGetBitbucketToken_EnvVars(t *testing.T) {
	t.Run("BITBUCKET_TOKEN takes precedence", func(t *testing.T) {
		t.Setenv("BITBUCKET_TOKEN", "bb_tok_1")
		t.Setenv("BITBUCKET_ACCESS_TOKEN", "bb_tok_2")
		assert.Equal(t, "bb_tok_1", getBitbucketToken())
	})

	t.Run("BITBUCKET_ACCESS_TOKEN used as fallback", func(t *testing.T) {
		t.Setenv("BITBUCKET_TOKEN", "")
		t.Setenv("BITBUCKET_ACCESS_TOKEN", "bb_tok_2")
		assert.Equal(t, "bb_tok_2", getBitbucketToken())
	})

	t.Run("returns empty when neither set", func(t *testing.T) {
		t.Setenv("BITBUCKET_TOKEN", "")
		t.Setenv("BITBUCKET_ACCESS_TOKEN", "")
		assert.Empty(t, getBitbucketToken())
	})
}

func TestGetAzureDevOpsToken_EnvVars(t *testing.T) {
	t.Run("AZURE_DEVOPS_EXT_PAT takes precedence", func(t *testing.T) {
		t.Setenv("AZURE_DEVOPS_EXT_PAT", "az_pat_1")
		t.Setenv("SYSTEM_ACCESSTOKEN", "az_sys_1")
		assert.Equal(t, "az_pat_1", getAzureDevOpsToken())
	})

	t.Run("SYSTEM_ACCESSTOKEN used as fallback", func(t *testing.T) {
		t.Setenv("AZURE_DEVOPS_EXT_PAT", "")
		t.Setenv("SYSTEM_ACCESSTOKEN", "az_sys_1")
		t.Setenv("HOME", t.TempDir()) // prevent reading real azure config
		assert.Equal(t, "az_sys_1", getAzureDevOpsToken())
	})
}

func TestGetGiteaToken_EnvVars(t *testing.T) {
	t.Run("GITEA_TOKEN from env", func(t *testing.T) {
		t.Setenv("GITEA_TOKEN", "tea_env_tok")
		assert.Equal(t, "tea_env_tok", getGiteaToken("https://any.instance.com"))
	})

	t.Run("falls back to tea config", func(t *testing.T) {
		t.Setenv("GITEA_TOKEN", "")
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		assert.Empty(t, getGiteaToken("https://gitea.example.com"))
	})
}

// --- fetchGitHubUser (HTTP) ---
// Prevents silent failures on API errors or malformed responses

func TestFetchGitHubUser(t *testing.T) {
	t.Parallel()

	t.Run("successful response", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(gitHubUserResponse{
				ID:    12345,
				Login: "testdev",
				Name:  "Test Dev",
				Email: "dev@example.com",
			})
		}))
		defer srv.Close()

		// override the URL by calling the server directly
		identity, err := fetchGitHubUserFromURL(srv.URL, "test-token")
		require.NoError(t, err)
		assert.Equal(t, "github:12345", identity.UserID)
		assert.Equal(t, "testdev", identity.Username)
		assert.Equal(t, "dev@example.com", identity.Email)
		assert.True(t, identity.Verified)
	})

	t.Run("non-200 response", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"message":"Bad credentials"}`))
		}))
		defer srv.Close()

		_, err := fetchGitHubUserFromURL(srv.URL, "bad-token")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "401")
	})

	t.Run("malformed json response", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{not json`))
		}))
		defer srv.Close()

		_, err := fetchGitHubUserFromURL(srv.URL, "test-token")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "decode")
	})
}

// fetchGitHubUserFromURL is a test helper that lets us override the API URL.
// This avoids hitting the real GitHub API in tests.
func fetchGitHubUserFromURL(apiURL, token string) (*Identity, error) {
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github api request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		buf := make([]byte, 1024)
		n, _ := resp.Body.Read(buf)
		return nil, fmt.Errorf("github api returned status %d: %s", resp.StatusCode, string(buf[:n]))
	}

	var user gitHubUserResponse
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("failed to decode github response: %w", err)
	}

	return &Identity{
		UserID:   fmt.Sprintf("github:%d", user.ID),
		Email:    user.Email,
		Name:     user.Name,
		Username: user.Login,
		Source:   "github",
		Verified: true,
	}, nil
}

// --- fetchGitLabUser (HTTP) ---

func TestFetchGitLabUser(t *testing.T) {
	t.Parallel()

	t.Run("successful response", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "glpat_test", r.Header.Get("PRIVATE-TOKEN"))
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(gitLabUserResponse{
				ID:       42,
				Username: "gluser",
				Name:     "GL User",
				Email:    "gl@example.com",
			})
		}))
		defer srv.Close()

		identity, err := fetchGitLabUserFromURL(srv.URL, "glpat_test")
		require.NoError(t, err)
		assert.Equal(t, "gitlab:42", identity.UserID)
		assert.Equal(t, "gluser", identity.Username)
	})

	t.Run("non-200 response", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()

		_, err := fetchGitLabUserFromURL(srv.URL, "bad")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "403")
	})
}

func fetchGitLabUserFromURL(apiURL, token string) (*Identity, error) {
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gitlab api returned status %d", resp.StatusCode)
	}

	var user gitLabUserResponse
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, err
	}

	return &Identity{
		UserID:   fmt.Sprintf("gitlab:%d", user.ID),
		Username: user.Username,
		Name:     user.Name,
		Email:    user.Email,
		Source:   "gitlab",
		Verified: true,
	}, nil
}

// --- fetchBitbucketUser / fetchBitbucketEmail (HTTP) ---

func TestFetchBitbucketEmail_FindsPrimary(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(bitbucketEmailResponse{
			Values: []struct {
				Email     string `json:"email"`
				IsPrimary bool   `json:"is_primary"`
			}{
				{Email: "secondary@example.com", IsPrimary: false},
				{Email: "primary@example.com", IsPrimary: true},
			},
		})
	}))
	defer srv.Close()

	email := fetchBitbucketEmailFromURL(srv.URL, "tok", &http.Client{})
	assert.Equal(t, "primary@example.com", email)
}

func TestFetchBitbucketEmail_FallsBackToFirst(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(bitbucketEmailResponse{
			Values: []struct {
				Email     string `json:"email"`
				IsPrimary bool   `json:"is_primary"`
			}{
				{Email: "only@example.com", IsPrimary: false},
			},
		})
	}))
	defer srv.Close()

	email := fetchBitbucketEmailFromURL(srv.URL, "tok", &http.Client{})
	assert.Equal(t, "only@example.com", email)
}

func TestFetchBitbucketEmail_EmptyList(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(bitbucketEmailResponse{})
	}))
	defer srv.Close()

	email := fetchBitbucketEmailFromURL(srv.URL, "tok", &http.Client{})
	assert.Empty(t, email)
}

func fetchBitbucketEmailFromURL(apiURL, token string, client *http.Client) string {
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ""
	}

	var emails bitbucketEmailResponse
	if err := json.NewDecoder(resp.Body).Decode(&emails); err != nil {
		return ""
	}

	for _, e := range emails.Values {
		if e.IsPrimary {
			return e.Email
		}
	}
	if len(emails.Values) > 0 {
		return emails.Values[0].Email
	}
	return ""
}

// --- fetchGiteaUser (HTTP) ---

func TestFetchGiteaUser(t *testing.T) {
	t.Parallel()

	t.Run("successful response", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/api/v1/user", r.URL.Path)
			assert.Equal(t, "token tea_tok", r.Header.Get("Authorization"))
			json.NewEncoder(w).Encode(giteaUserResponse{
				ID:       7,
				Login:    "teauser",
				FullName: "Tea User",
				Email:    "tea@example.com",
			})
		}))
		defer srv.Close()

		identity, err := fetchGiteaUser(srv.URL, "tea_tok")
		require.NoError(t, err)
		assert.Contains(t, identity.UserID, "gitea:")
		assert.Contains(t, identity.UserID, ":7")
		assert.Equal(t, "teauser", identity.Username)
		assert.Equal(t, "Tea User", identity.Name)
		assert.Equal(t, "tea@example.com", identity.Email)
	})

	t.Run("empty instance URL rejected", func(t *testing.T) {
		t.Parallel()
		_, err := fetchGiteaUser("", "tok")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "empty")
	})

	t.Run("non-200 response", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer srv.Close()

		_, err := fetchGiteaUser(srv.URL, "bad")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "401")
	})

	t.Run("trailing slash stripped from instance URL", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// should be /api/v1/user, not //api/v1/user
			assert.Equal(t, "/api/v1/user", r.URL.Path)
			json.NewEncoder(w).Encode(giteaUserResponse{ID: 1, Login: "u"})
		}))
		defer srv.Close()

		_, err := fetchGiteaUser(srv.URL+"/", "tok")
		require.NoError(t, err)
	})
}

// --- fetchAzureDevOpsUser (HTTP) ---

func TestFetchAzureDevOpsUser(t *testing.T) {
	t.Parallel()

	t.Run("successful response", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// verify basic auth with empty username
			user, pass, ok := r.BasicAuth()
			assert.True(t, ok)
			assert.Empty(t, user)
			assert.Equal(t, "az_pat", pass)

			json.NewEncoder(w).Encode(azureDevOpsProfileResponse{
				ID:           "az-id-1",
				DisplayName:  "Az User",
				EmailAddress: "az@example.com",
				PublicAlias:  "azuser",
			})
		}))
		defer srv.Close()

		identity, err := fetchAzureDevOpsUserFromURL(srv.URL, "az_pat")
		require.NoError(t, err)
		assert.Equal(t, "azure-devops:az-id-1", identity.UserID)
		assert.Equal(t, "Az User", identity.Name)
		assert.Equal(t, "az@example.com", identity.Email)
		assert.Equal(t, "azuser", identity.Username)
	})

	t.Run("non-200 response", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()

		_, err := fetchAzureDevOpsUserFromURL(srv.URL, "bad")
		assert.Error(t, err)
	})
}

func fetchAzureDevOpsUserFromURL(apiURL, token string) (*Identity, error) {
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth("", token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("azure devops api returned status %d", resp.StatusCode)
	}

	var profile azureDevOpsProfileResponse
	if err := json.NewDecoder(resp.Body).Decode(&profile); err != nil {
		return nil, err
	}

	return &Identity{
		UserID:   fmt.Sprintf("azure-devops:%s", profile.ID),
		Email:    profile.EmailAddress,
		Name:     profile.DisplayName,
		Username: profile.PublicAlias,
		Source:   "azure-devops",
		Verified: true,
	}, nil
}

// --- IsGitHubAvailable ---

func TestIsGitHubAvailable(t *testing.T) {
	t.Run("returns true when token available", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "ghp_test")
		assert.True(t, IsGitHubAvailable())
	})

	t.Run("returns false when no token", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "")
		t.Setenv("GH_TOKEN", "")
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		// note: might still find gh CLI — that's ok, we're testing the env path
	})
}

// --- GCP env var path ---

func TestGetGCPIdentity_ServiceAccountEnv(t *testing.T) {
	t.Run("reads from GOOGLE_APPLICATION_CREDENTIALS", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "sa.json")

		creds := map[string]string{
			"type":         "service_account",
			"client_email": "sa@proj.iam.gserviceaccount.com",
		}
		data, _ := json.Marshal(creds)
		require.NoError(t, os.WriteFile(path, data, 0o644))

		t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", path)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // prevent other lookups

		identity, err := getGCPIdentity()
		require.NoError(t, err)
		assert.Equal(t, "sa@proj.iam.gserviceaccount.com", identity.Email)
	})

	t.Run("falls through on invalid credentials file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "bad.json")
		require.NoError(t, os.WriteFile(path, []byte("{}"), 0o644))

		t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", path)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())

		_, err := getGCPIdentity()
		assert.Error(t, err) // no valid creds found anywhere
	})
}

// --- AWS env var path ---

func TestGetAWSCredentials_EnvVars(t *testing.T) {
	t.Run("reads from env vars", func(t *testing.T) {
		t.Setenv("AWS_ACCESS_KEY_ID", "AKIAIOSFODNN7EXAMPLE")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY")

		ak, sk := getAWSCredentials()
		assert.Equal(t, "AKIAIOSFODNN7EXAMPLE", ak)
		assert.Equal(t, "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", sk)
	})

	t.Run("returns empty when neither set", func(t *testing.T) {
		t.Setenv("AWS_ACCESS_KEY_ID", "")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "")
		t.Setenv("HOME", t.TempDir()) // prevent reading real credentials

		ak, sk := getAWSCredentials()
		assert.Empty(t, ak)
		assert.Empty(t, sk)
	})
}

// --- parseGCloudProperties ---

func TestParseGCloudProperties(t *testing.T) {
	t.Run("reads properties file", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)

		propsDir := filepath.Join(dir, "gcloud")
		require.NoError(t, os.MkdirAll(propsDir, 0o755))

		content := "[core]\naccount = props@example.com\n"
		require.NoError(t, os.WriteFile(filepath.Join(propsDir, "properties"), []byte(content), 0o644))

		identity := parseGCloudProperties()
		require.NotNil(t, identity)
		assert.Equal(t, "gcp:props@example.com", identity.UserID)
	})

	t.Run("returns nil when properties missing", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		assert.Nil(t, parseGCloudProperties())
	})
}

// --- person_info edge cases not in existing tests ---

func TestFormatNameAsDisplay_Unicode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		input string
		want  string
	}{
		{"cjk name", "太郎 山田", "太郎 山."},
		{"accented name", "José García", "José G."},
		{"single unicode char", "李", "李"},
		{"emoji name gracefully handled", "🎉 Party", "🎉 P."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, formatNameAsDisplay(tt.input))
		})
	}
}

func TestCapitalize_Unicode(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "Über", capitalize("über"))
	assert.Equal(t, "Ñoño", capitalize("ñoño"))
}

// --- Identity / ResolvedIdentities struct edge cases ---

func TestIdentity_ZeroValue(t *testing.T) {
	t.Parallel()

	// zero-value Identity should not panic when accessed
	var id Identity
	assert.Empty(t, id.UserID)
	assert.Empty(t, id.Source)
	assert.False(t, id.Verified)
}

func TestResolvedIdentities_ZeroValue(t *testing.T) {
	t.Parallel()

	var ri ResolvedIdentities
	assert.Nil(t, ri.Primary)
	assert.Nil(t, ri.GitHub)
}
