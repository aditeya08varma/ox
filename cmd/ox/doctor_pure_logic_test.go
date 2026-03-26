package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCheckResultToJSON verifies status derivation logic for all check states
func TestCheckResultToJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		check      checkResult
		wantStatus string
		wantSlug   string
		wantAgent  bool
	}{
		{
			name:       "passed check",
			check:      PassedCheck("auth", "ok"),
			wantStatus: "passed",
		},
		{
			name:       "failed check",
			check:      FailedCheck("auth", "expired", "run ox login"),
			wantStatus: "failed",
		},
		{
			name:       "warning check",
			check:      WarningCheck("config", "outdated", "update"),
			wantStatus: "warning",
		},
		{
			name:       "skipped check",
			check:      SkippedCheck("daemon", "not running", ""),
			wantStatus: "skipped",
		},
		{
			// skipped takes priority over passed=false
			name: "skipped overrides failed",
			check: checkResult{
				name:    "test",
				skipped: true,
				passed:  false,
			},
			wantStatus: "skipped",
		},
		{
			name:       "critical check maps to failed",
			check:      CriticalCheck("auth", "expired", "login required"),
			wantStatus: "failed",
		},
		{
			name:       "info check maps to warning",
			check:      InfoCheck("update", "available", "run ox update"),
			wantStatus: "warning",
		},
		{
			name:      "agent required check preserves flag",
			check:     AgentRequiredCheck("session", "incomplete", "run ox agent doctor"),
			wantAgent: true,
		},
		{
			name:     "WithFixInfo attaches metadata",
			check:    FailedCheck("ledger", "missing", "fix it").WithFixInfo("ledger-path", FixLevelSuggested),
			wantSlug: "ledger-path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := checkResultToJSON(tt.check)

			if tt.wantStatus != "" {
				assert.Equal(t, tt.wantStatus, result.Status)
			}
			assert.Equal(t, tt.check.name, result.Name)
			assert.Equal(t, tt.check.message, result.Message)
			assert.Equal(t, tt.check.detail, result.Detail)
			if tt.wantSlug != "" {
				assert.Equal(t, tt.wantSlug, result.Slug)
			}
			if tt.wantAgent {
				assert.True(t, result.RequiresAgent)
			}
		})
	}
}

// TestCheckResultToJSON_Children verifies recursive child conversion
func TestCheckResultToJSON_Children(t *testing.T) {
	t.Parallel()
	parent := checkResult{
		name:    "parent",
		passed:  true,
		message: "ok",
		children: []checkResult{
			PassedCheck("child1", "ok"),
			FailedCheck("child2", "broken", "fix"),
		},
	}

	result := checkResultToJSON(parent)

	require.Len(t, result.Children, 2)
	assert.Equal(t, "child1", result.Children[0].Name)
	assert.Equal(t, "passed", result.Children[0].Status)
	assert.Equal(t, "child2", result.Children[1].Name)
	assert.Equal(t, "failed", result.Children[1].Status)
}

// TestCheckResultToJSON_NoChildren leaves Children nil when empty
func TestCheckResultToJSON_NoChildren(t *testing.T) {
	t.Parallel()
	result := checkResultToJSON(PassedCheck("test", "ok"))
	assert.Nil(t, result.Children)
}

// TestCollectFixableSlugs verifies which checks get collected
func TestCollectFixableSlugs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		check     checkResult
		wantCount int
	}{
		{
			name:      "passed check is not collected",
			check:     PassedCheck("test", "ok").WithFixInfo("test-slug", FixLevelSuggested),
			wantCount: 0,
		},
		{
			name:      "failed check with fix info is collected",
			check:     FailedCheck("test", "broken", "fix").WithFixInfo("test-slug", FixLevelSuggested),
			wantCount: 1,
		},
		{
			name:      "warning check with fix info is collected",
			check:     WarningCheck("test", "warn", "detail").WithFixInfo("test-slug", FixLevelSuggested),
			wantCount: 1,
		},
		{
			name:      "skipped check is not collected",
			check:     SkippedCheck("test", "skip", "").WithFixInfo("test-slug", FixLevelSuggested),
			wantCount: 0,
		},
		{
			name:      "failed check with check-only level is not collected",
			check:     FailedCheck("test", "broken", "fix").WithFixInfo("test-slug", FixLevelCheckOnly),
			wantCount: 0,
		},
		{
			name:      "failed check without slug is not collected",
			check:     FailedCheck("test", "broken", "fix"),
			wantCount: 0,
		},
		{
			name:      "failed check without fix level is not collected",
			check:     FailedCheck("test", "broken", "fix").WithFixInfo("test-slug", ""),
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var slugs []fixSlugInfo
			collectFixableSlugs(tt.check, &slugs)
			assert.Len(t, slugs, tt.wantCount)
		})
	}
}

// TestHasRequiresAgentIssues verifies detection of agent-required issues
func TestHasRequiresAgentIssues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		categories []checkCategory
		want       bool
	}{
		{
			name:       "empty categories",
			categories: nil,
			want:       false,
		},
		{
			name: "all passed, no agent flags",
			categories: []checkCategory{
				{name: "Test", checks: []checkResult{PassedCheck("check", "ok")}},
			},
			want: false,
		},
		{
			name: "agent-required warning at top level",
			categories: []checkCategory{
				{name: "Test", checks: []checkResult{
					AgentRequiredCheck("session", "incomplete", "run agent doctor"),
				}},
			},
			want: true,
		},
		{
			name: "agent-required in child",
			categories: []checkCategory{
				{name: "Test", checks: []checkResult{
					{
						name:   "parent",
						passed: true,
						children: []checkResult{
							AgentRequiredCheck("child", "needs agent", "detail"),
						},
					},
				}},
			},
			want: true,
		},
		{
			name: "agent-required but skipped does not count",
			categories: []checkCategory{
				{name: "Test", checks: []checkResult{
					{name: "skipped-agent", skipped: true, requiresAgent: true},
				}},
			},
			want: false,
		},
		{
			name: "agent-required passed without warning does not count",
			categories: []checkCategory{
				{name: "Test", checks: []checkResult{
					{name: "ok-agent", passed: true, warning: false, requiresAgent: true},
				}},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, hasRequiresAgentIssues(tt.categories))
		})
	}
}

// TestCategorizeCheck verifies priority bucket assignment
func TestCategorizeCheck(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		check          checkResult
		wantCritical   int
		wantAttention  int
		wantOptional   int
		wantAgent      int
		wantHasFailed  bool
	}{
		{
			name:          "skipped check goes nowhere",
			check:         SkippedCheck("test", "skip", ""),
			wantHasFailed: false,
		},
		{
			name:           "critical failure goes to critical bucket",
			check:          CriticalCheck("auth", "expired", "login"),
			wantCritical:   1,
			wantHasFailed:  true,
		},
		{
			name:           "normal failure goes to attention bucket",
			check:          FailedCheck("config", "missing", "fix"),
			wantAttention:  1,
			wantHasFailed:  true,
		},
		{
			name:          "warning goes to attention bucket",
			check:         WarningCheck("update", "available", "update"),
			wantAttention: 1,
		},
		{
			name:         "info-priority warning goes to optional bucket",
			check:        InfoCheck("tip", "new feature", "try it"),
			wantOptional: 1,
		},
		{
			name:      "agent-required warning goes to agent bucket",
			check:     AgentRequiredCheck("session", "incomplete", "agent doctor"),
			wantAgent: 1,
		},
		{
			name: "agent-required failure goes to agent bucket and sets hasFailed",
			check: checkResult{
				name: "session", passed: false, requiresAgent: true,
				message: "broken", detail: "fix",
			},
			wantAgent:     1,
			wantHasFailed: true,
		},
		{
			name:  "passed check without warning goes to no bucket",
			check: PassedCheck("healthy", "ok"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var critical, attention, optional, agentRequired []checkResult
			hasFailed := false

			categorizeCheck(tt.check, &critical, &attention, &optional, &agentRequired, &hasFailed)

			assert.Len(t, critical, tt.wantCritical, "critical bucket")
			assert.Len(t, attention, tt.wantAttention, "attention bucket")
			assert.Len(t, optional, tt.wantOptional, "optional bucket")
			assert.Len(t, agentRequired, tt.wantAgent, "agent bucket")
			assert.Equal(t, tt.wantHasFailed, hasFailed, "hasFailed")
		})
	}
}

// TestWithFixInfo verifies metadata attachment returns a new value
func TestWithFixInfo(t *testing.T) {
	t.Parallel()
	original := FailedCheck("test", "broken", "fix")
	enriched := original.WithFixInfo("test-slug", FixLevelConfirm)

	assert.Equal(t, "", original.slug, "original should be unchanged")
	assert.Equal(t, "test-slug", enriched.slug)
	assert.Equal(t, FixLevelConfirm, enriched.fixLevel)
	// other fields preserved
	assert.Equal(t, "test", enriched.name)
	assert.Equal(t, "broken", enriched.message)
}

// TestWithRequiresAgent verifies the modifier
func TestWithRequiresAgent(t *testing.T) {
	t.Parallel()
	original := WarningCheck("session", "issue", "detail")
	enriched := original.WithRequiresAgent()

	assert.False(t, original.requiresAgent)
	assert.True(t, enriched.requiresAgent)
	assert.Equal(t, "session", enriched.name)
}

// TestFixableSlugsHint verifies hint generation
func TestFixableSlugsHint(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "", fixableSlugsHint(nil))
	assert.Equal(t, "", fixableSlugsHint([]fixSlugInfo{}))
	assert.Contains(t, fixableSlugsHint([]fixSlugInfo{
		{slug: "test", fixLevel: FixLevelSuggested, name: "Test"},
	}), "--fix")
}

// TestGetMissingSageoxGitattributes verifies detection of missing entries
func TestGetMissingSageoxGitattributes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		content     string
		wantMissing int
	}{
		{
			name:        "empty content has all missing",
			content:     "",
			wantMissing: len(sageoxGitattributesEntries),
		},
		{
			name:        "all entries present",
			content:     ".sageox/** linguist-language=SageOx\n*.ox linguist-language=SageOx\n",
			wantMissing: 0,
		},
		{
			name:        "one entry present",
			content:     ".sageox/** linguist-language=SageOx\n",
			wantMissing: 1,
		},
		{
			name:        "entries among other content",
			content:     "*.go linguist-vendored=false\n.sageox/** linguist-language=SageOx\n*.ox linguist-language=SageOx\n",
			wantMissing: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			missing := getMissingSageoxGitattributes(tt.content)
			assert.Len(t, missing, tt.wantMissing)
		})
	}
}

// TestAddSageoxGitattributesEntries verifies entry appending behavior
func TestAddSageoxGitattributesEntries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		missing []string
		check   func(t *testing.T, result string)
	}{
		{
			name:    "no missing entries returns content unchanged",
			content: "existing content\n",
			missing: nil,
			check: func(t *testing.T, result string) {
				assert.Equal(t, "existing content\n", result)
			},
		},
		{
			name:    "empty content with missing entries",
			content: "",
			missing: []string{".sageox/** linguist-language=SageOx"},
			check: func(t *testing.T, result string) {
				assert.Contains(t, result, sageoxGitattributesComment)
				assert.Contains(t, result, ".sageox/** linguist-language=SageOx")
			},
		},
		{
			name:    "existing content with missing entries appends after separator",
			content: "*.go linguist-vendored=false\n",
			missing: []string{"*.ox linguist-language=SageOx"},
			check: func(t *testing.T, result string) {
				assert.Contains(t, result, "*.go linguist-vendored=false")
				assert.Contains(t, result, sageoxGitattributesComment)
				assert.Contains(t, result, "*.ox linguist-language=SageOx")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := addSageoxGitattributesEntries(tt.content, tt.missing)
			tt.check(t, result)
		})
	}
}

// TestValidateRepoPath verifies filesystem validation of repo paths
func TestValidateRepoPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		setup func(t *testing.T) string
		want  string
	}{
		{
			name: "missing path",
			setup: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "nonexistent")
			},
			want: "missing",
		},
		{
			name: "empty directory",
			setup: func(t *testing.T) string {
				dir := filepath.Join(t.TempDir(), "empty")
				require.NoError(t, os.MkdirAll(dir, 0755))
				return dir
			},
			want: "empty-dir",
		},
		{
			name: "directory without .git (not a repo)",
			setup: func(t *testing.T) string {
				dir := filepath.Join(t.TempDir(), "notgit")
				require.NoError(t, os.MkdirAll(dir, 0755))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "file.txt"), []byte("x"), 0644))
				return dir
			},
			want: "not-git-repo",
		},
		{
			name: "directory with .git is valid",
			setup: func(t *testing.T) string {
				dir := filepath.Join(t.TempDir(), "repo")
				require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0755))
				return dir
			},
			want: "",
		},
		{
			name: "file instead of directory",
			setup: func(t *testing.T) string {
				f := filepath.Join(t.TempDir(), "afile")
				require.NoError(t, os.WriteFile(f, []byte("x"), 0644))
				return f
			},
			want: "not-directory",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := tt.setup(t)
			assert.Equal(t, tt.want, validateRepoPath(path))
		})
	}
}

// TestEnrichCheckResult verifies that enrichment populates slug and fixLevel from registry
func TestEnrichCheckResult(t *testing.T) {
	t.Parallel()

	// pick a known registered check
	dc := GetDoctorCheck(CheckSlugGitignore)
	require.NotNil(t, dc, "gitignore check must be registered")

	t.Run("enriches matching check by name", func(t *testing.T) {
		t.Parallel()
		check := checkResult{name: dc.Name, passed: true, message: "ok"}
		enrichCheckResult(&check)
		assert.Equal(t, CheckSlugGitignore, check.slug)
		assert.Equal(t, dc.FixLevel, check.fixLevel)
	})

	t.Run("skips already enriched check", func(t *testing.T) {
		t.Parallel()
		check := checkResult{
			name: dc.Name, passed: true, message: "ok",
			slug: "custom-slug", fixLevel: FixLevelConfirm,
		}
		enrichCheckResult(&check)
		assert.Equal(t, "custom-slug", check.slug, "should not overwrite existing slug")
		assert.Equal(t, FixLevelConfirm, check.fixLevel, "should not overwrite existing fixLevel")
	})

	t.Run("no match leaves check unenriched", func(t *testing.T) {
		t.Parallel()
		check := checkResult{name: "completely-unknown-check-name", passed: true}
		enrichCheckResult(&check)
		assert.Equal(t, "", check.slug)
	})
}

// TestEnrichWithFixMetadata verifies batch enrichment including children
func TestEnrichWithFixMetadata(t *testing.T) {
	t.Parallel()

	dc := GetDoctorCheck(CheckSlugGitignore)
	require.NotNil(t, dc)

	categories := []checkCategory{
		{
			name: "Test",
			checks: []checkResult{
				{
					name:    dc.Name,
					passed:  true,
					message: "ok",
					children: []checkResult{
						{name: "unknown-child", passed: true},
					},
				},
			},
		},
	}

	enriched := enrichWithFixMetadata(categories)

	require.Len(t, enriched, 1)
	require.Len(t, enriched[0].checks, 1)
	assert.Equal(t, CheckSlugGitignore, enriched[0].checks[0].slug)
	// child with unknown name stays unenriched
	assert.Equal(t, "", enriched[0].checks[0].children[0].slug)
}

// TestCriticalCheck_Constructor verifies priority field
func TestCriticalCheck_Constructor(t *testing.T) {
	t.Parallel()
	c := CriticalCheck("auth", "expired", "login")
	assert.False(t, c.passed)
	assert.Equal(t, "critical", c.priority)
	assert.Equal(t, "auth", c.name)
	assert.Equal(t, "expired", c.message)
	assert.Equal(t, "login", c.detail)
}

// TestInfoCheck_Constructor verifies info priority and warning flag
func TestInfoCheck_Constructor(t *testing.T) {
	t.Parallel()
	c := InfoCheck("updates", "new version", "update now")
	assert.True(t, c.passed)
	assert.True(t, c.warning)
	assert.Equal(t, "info", c.priority)
}

// TestAgentRequiredCheck_Constructor verifies requiresAgent flag
func TestAgentRequiredCheck_Constructor(t *testing.T) {
	t.Parallel()
	c := AgentRequiredCheck("session", "incomplete", "run agent doctor")
	assert.True(t, c.passed)
	assert.True(t, c.warning)
	assert.True(t, c.requiresAgent)
}

// TestMergeGitignoreEntries_ConflictRemoval verifies that conflicting
// ignore entries are removed when a "!" keep entry is required
func TestMergeGitignoreEntries_ConflictRemoval(t *testing.T) {
	t.Parallel()
	// create content with a conflicting entry: "discovered.jsonl" should be
	// removed because "!discovered.jsonl" is in requiredGitignoreEntries
	content := "logs/\ncache/\ndiscovered.jsonl\n"
	merged, changed := mergeGitignoreEntries(content)

	assert.True(t, changed, "should detect change when conflicts need removal")
	assert.NotContains(t, merged, "\ndiscovered.jsonl\n", "conflicting entry should be removed")
	assert.Contains(t, merged, "!discovered.jsonl", "keep entry should be present")
}

// TestMergeGitignoreEntries_NoChangesWhenComplete returns unchanged
func TestMergeGitignoreEntries_NoChangesWhenComplete_PureLogic(t *testing.T) {
	t.Parallel()
	// build content that has all required entries
	content := ""
	for _, e := range requiredGitignoreEntries {
		content += e + "\n"
	}

	_, changed := mergeGitignoreEntries(content)
	assert.False(t, changed, "should not change when all entries present")
}
