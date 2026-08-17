package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func isolateDetectEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("AGENT_ENV", "")
	t.Setenv("PI_CODING_AGENT", "")
	t.Setenv("PI_CONFIG_DIR", "")
	return home
}

func TestDetectExplicitOMPEnvironment(t *testing.T) {
	isolateDetectEnv(t)
	t.Setenv("AGENT_ENV", "omp")
	resp, err := handleDetect()
	require.NoError(t, err)
	assert.True(t, resp.Detected)
	assert.Equal(t, "AGENT_ENV=omp", resp.Reason)
}

func TestDetectDoesNotClaimPiRuntimeMarker(t *testing.T) {
	isolateDetectEnv(t)
	t.Setenv("PI_CODING_AGENT", "true")
	resp, err := handleDetect()
	require.NoError(t, err)
	assert.False(t, resp.Detected,
		"OMP and upstream Pi share transcript ancestry, but PI_CODING_AGENT identifies Pi")
}

func TestDetectFindsOMPConfigRoot(t *testing.T) {
	home := isolateDetectEnv(t)
	require.NoError(t, os.Mkdir(filepath.Join(home, ".omp"), 0o755))
	resp, err := handleDetect()
	require.NoError(t, err)
	assert.True(t, resp.Detected)
	assert.Contains(t, resp.Reason, ".omp")
}

func TestDetectHonorsPIConfigDir(t *testing.T) {
	home := isolateDetectEnv(t)
	t.Setenv("PI_CONFIG_DIR", ".custom-omp")
	require.NoError(t, os.Mkdir(filepath.Join(home, ".custom-omp"), 0o755))
	resp, err := handleDetect()
	require.NoError(t, err)
	assert.True(t, resp.Detected)
	assert.Contains(t, resp.Reason, ".custom-omp")
}

func TestDiagnoseMissingNativeContextOffersSafeFix(t *testing.T) {
	isolateDetectEnv(t)
	repo := t.TempDir()
	res, err := handleDiagnose(adapterprotocol.DiagnoseParams{RepoRoot: repo})
	require.NoError(t, err)
	issue := issueBySlugOMP(res.Issues, "omp:hooks-missing")
	require.NotNil(t, issue)
	assert.Equal(t, []string{"ox", "integrate", "install", "--omp"}, issue.FixArgv)
	assert.True(t, issue.FixSafe)
}

func TestDiagnoseUnreadableNativeContextIsNotAutoFixed(t *testing.T) {
	isolateDetectEnv(t)
	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".omp", "AGENTS.md"), 0o755))
	res, err := handleDiagnose(adapterprotocol.DiagnoseParams{RepoRoot: repo})
	require.NoError(t, err)
	assert.Nil(t, issueBySlugOMP(res.Issues, "omp:hooks-missing"))
	issue := issueBySlugOMP(res.Issues, "omp:agents-md-unreadable")
	require.NotNil(t, issue)
	assert.False(t, issue.FixSafe)
}

func issueBySlugOMP(issues []adapterprotocol.DiagnoseIssue, slug string) *adapterprotocol.DiagnoseIssue {
	for i := range issues {
		if issues[i].Slug == slug {
			return &issues[i]
		}
	}
	return nil
}

func TestDiagnoseUnsupportedTranscriptVersion(t *testing.T) {
	home := isolateDetectEnv(t)
	clearOMPPathEnv(t)
	direct := filepath.Join(home, "sessions")
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", direct)
	require.NoError(t, os.MkdirAll(direct, 0o755))
	repo := filepath.Join(home, "project")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	path := filepath.Join(direct, "2026-08-17_unknown.jsonl")
	body := `{"type":"session","version":4,"id":"unknown","timestamp":"2026-08-17T16:55:12Z","cwd":` +
		quoteJSON(repo) + `}` + "\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

	res, err := handleDiagnose(adapterprotocol.DiagnoseParams{RepoRoot: repo})
	require.NoError(t, err)
	issue := issueBySlugOMP(res.Issues, "omp:format-unsupported")
	require.NotNil(t, issue)
	assert.Contains(t, issue.Detail, "version 4")
}
