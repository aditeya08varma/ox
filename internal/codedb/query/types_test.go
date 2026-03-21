package query

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMarshalEventClusters_FlatArray(t *testing.T) {
	t.Parallel()

	result := &ActivityResult{
		PRClusters: []PRCluster{{
			Number:             42,
			Title:              "Add feature",
			Description:        "A new feature",
			Author:             "alice",
			Status:             "merged",
			Commits:            []CommitEntry{},
			Reviews:            []ReviewGroup{},
			DiscussionComments: []Comment{},
		}},
		StandaloneIssues: []StandaloneIssue{{
			Number:   10,
			Title:    "Bug report",
			Body:     "Something broke",
			Author:   "bob",
			State:    "open",
			Comments: []Comment{},
		}},
		StandaloneCommits: []StandaloneCommit{{
			SHA:       "abc123",
			Message:   "fix typo",
			Author:    "carol",
			Timestamp: "2026-03-18T10:00:00Z",
		}},
	}

	data, err := result.MarshalEventClusters()
	require.NoError(t, err)

	var elements []map[string]any
	err = json.Unmarshal(data, &elements)
	require.NoError(t, err)

	assert.Len(t, elements, 3)
	assert.Equal(t, "pull_request", elements[0]["type"])
	assert.Equal(t, "issue", elements[1]["type"])
	assert.Equal(t, "commit", elements[2]["type"])
}

func TestMarshalEventClusters_TypeDiscriminators(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		result   ActivityResult
		expected string
	}{
		{
			name: "PR type",
			result: ActivityResult{
				PRClusters: []PRCluster{{
					Number: 1, Commits: []CommitEntry{},
					Reviews: []ReviewGroup{}, DiscussionComments: []Comment{},
				}},
			},
			expected: "pull_request",
		},
		{
			name: "issue type",
			result: ActivityResult{
				StandaloneIssues: []StandaloneIssue{{
					Number: 1, Comments: []Comment{},
				}},
			},
			expected: "issue",
		},
		{
			name: "commit type",
			result: ActivityResult{
				StandaloneCommits: []StandaloneCommit{{SHA: "abc"}},
			},
			expected: "commit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			data, err := tt.result.MarshalEventClusters()
			require.NoError(t, err)

			var elements []map[string]any
			require.NoError(t, json.Unmarshal(data, &elements))
			assert.Len(t, elements, 1)
			assert.Equal(t, tt.expected, elements[0]["type"])
		})
	}
}

func TestMarshalEventClusters_FieldNameAlignment(t *testing.T) {
	t.Parallel()

	mergedAt := "2026-03-18T14:30:00Z"
	result := &ActivityResult{
		PRClusters: []PRCluster{{
			Number:             42,
			Title:              "Test PR",
			Description:        "PR body text",
			Author:             "alice",
			Status:             "merged",
			MergedAt:           &mergedAt,
			Commits:            []CommitEntry{},
			Reviews:            []ReviewGroup{},
			DiscussionComments: []Comment{},
		}},
	}

	data, err := result.MarshalEventClusters()
	require.NoError(t, err)

	var elements []map[string]any
	require.NoError(t, json.Unmarshal(data, &elements))

	pr := elements[0]
	// Must use "description" not "body"
	assert.Equal(t, "PR body text", pr["description"])
	_, hasBody := pr["body"]
	assert.False(t, hasBody, "PR should not have 'body' key")

	// Must use "status" not "state"
	assert.Equal(t, "merged", pr["status"])
	_, hasState := pr["state"]
	assert.False(t, hasState, "PR should not have 'state' key")

	// Must NOT have "related_issue" or "files_changed"
	_, hasRelated := pr["related_issue"]
	assert.False(t, hasRelated, "PR should not have 'related_issue' key")
	_, hasFiles := pr["files_changed"]
	assert.False(t, hasFiles, "PR should not have 'files_changed' key")
}

func TestMarshalEventClusters_Empty(t *testing.T) {
	t.Parallel()

	result := &ActivityResult{}
	data, err := result.MarshalEventClusters()
	require.NoError(t, err)
	assert.Equal(t, "[]", string(data))
}

func TestMarshalEventClusters_EmptySlicesNotNil(t *testing.T) {
	t.Parallel()

	result := &ActivityResult{
		PRClusters: []PRCluster{{
			Number:             1,
			Commits:            []CommitEntry{},
			Reviews:            []ReviewGroup{},
			DiscussionComments: []Comment{},
		}},
	}

	data, err := result.MarshalEventClusters()
	require.NoError(t, err)

	// Verify empty arrays serialize as [] not null
	raw := string(data)
	assert.Contains(t, raw, `"commits":[]`)
	assert.Contains(t, raw, `"reviews":[]`)
	assert.Contains(t, raw, `"discussion_comments":[]`)
}

func TestMarshalEventClusters_NullableLine(t *testing.T) {
	t.Parallel()

	path := "api/handler.go"
	result := &ActivityResult{
		PRClusters: []PRCluster{{
			Number: 1,
			Reviews: []ReviewGroup{{
				Reviewer: "bob",
				Comments: []ReviewComment{{
					Body: "looks good",
					Path: &path,
					Line: nil, // nullable line
				}},
			}},
			Commits:            []CommitEntry{},
			DiscussionComments: []Comment{},
		}},
	}

	data, err := result.MarshalEventClusters()
	require.NoError(t, err)

	// Line should be null, not 0
	raw := string(data)
	assert.Contains(t, raw, `"line":null`)
	assert.NotContains(t, raw, `"line":0`)
}

func TestPRCluster_RoundTrip(t *testing.T) {
	t.Parallel()

	mergedAt := "2026-03-18T14:30:00Z"
	path := "api/handler.go"
	line := 42
	msg := "feat: add rate limiter"
	author := "sarah"
	ts := "2026-03-18T10:00:00Z"

	original := PRCluster{
		Number:      100,
		Title:       "Add rate limiting",
		Description: "Implements token bucket",
		Author:      "sarah",
		Status:      "merged",
		MergedAt:    &mergedAt,
		URL:         "https://github.com/org/repo/pull/100",
		Commits: []CommitEntry{
			{SHA: "abc123", Message: &msg, Author: &author, Timestamp: &ts},
		},
		Reviews: []ReviewGroup{{
			Reviewer: "jake",
			Comments: []ReviewComment{
				{Body: "Switch to token bucket?", Path: &path, Line: &line},
			},
		}},
		DiscussionComments: []Comment{
			{Author: "sarah", Body: "Good point.", CreatedAt: "2026-03-18T12:00:00Z"},
		},
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var restored PRCluster
	require.NoError(t, json.Unmarshal(data, &restored))

	assert.Equal(t, original.Number, restored.Number)
	assert.Equal(t, original.Description, restored.Description)
	assert.Equal(t, original.Status, restored.Status)
	assert.Equal(t, *original.MergedAt, *restored.MergedAt)
	assert.Len(t, restored.Commits, 1)
	assert.Len(t, restored.Reviews, 1)
	assert.Len(t, restored.DiscussionComments, 1)
	assert.Equal(t, *original.Reviews[0].Comments[0].Line, *restored.Reviews[0].Comments[0].Line)
}
