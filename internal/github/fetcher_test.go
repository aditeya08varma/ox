package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sageox/ox/internal/ledger"
)

func TestFetcherListPullRequests(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	merged := now.Add(-time.Hour)
	prs := []PullRequest{
		{
			Number:    42,
			Title:     "Add feature",
			Body:      "Description here",
			State:     "closed",
			User:      GitHubUser{Login: "dev1"},
			Labels:    []Label{{Name: "enhancement"}, {Name: "ready"}},
			CreatedAt: now.Add(-24 * time.Hour),
			UpdatedAt: now,
			MergedAt:  &merged,
			MergeSHA:  "abc123",
			HTMLURL:   "https://github.com/o/r/pull/42",
		},
		{
			Number:    43,
			Title:     "No labels",
			State:     "open",
			User:      GitHubUser{Login: "dev2"},
			Labels:    nil,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(prs)
	}))
	defer srv.Close()

	client := NewClient("tok")
	client.baseURL = srv.URL
	fetcher := NewFetcher(client)

	got, rl, err := fetcher.ListPullRequests(context.Background(), "o", "r", ledger.ListPRsOptions{State: "all"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("got %d PRs, want 2", len(got))
	}

	// verify field mapping for first PR
	pr := got[0]
	if pr.Number != 42 {
		t.Errorf("Number = %d, want 42", pr.Number)
	}
	if pr.Title != "Add feature" {
		t.Errorf("Title = %q, want %q", pr.Title, "Add feature")
	}
	if pr.Author != "dev1" {
		t.Errorf("Author = %q, want %q", pr.Author, "dev1")
	}
	if len(pr.Labels) != 2 || pr.Labels[0] != "enhancement" || pr.Labels[1] != "ready" {
		t.Errorf("Labels = %v, want [enhancement ready]", pr.Labels)
	}
	if pr.MergedAt == nil || !pr.MergedAt.Equal(merged) {
		t.Errorf("MergedAt = %v, want %v", pr.MergedAt, merged)
	}
	if pr.MergeSHA != "abc123" {
		t.Errorf("MergeSHA = %q, want %q", pr.MergeSHA, "abc123")
	}
	if pr.HTMLURL != "https://github.com/o/r/pull/42" {
		t.Errorf("HTMLURL = %q, want expected URL", pr.HTMLURL)
	}

	// second PR: nil labels should result in nil (not empty slice)
	if got[1].Labels != nil {
		t.Errorf("Labels = %v, want nil for PR with no labels", got[1].Labels)
	}

	// rl should be nil when no rate limit headers present
	if rl != nil {
		t.Errorf("expected nil rate limit, got %+v", rl)
	}
}

func TestFetcherListPullRequestsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "10")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"bad creds"}`))
	}))
	defer srv.Close()

	client := NewClient("tok")
	client.baseURL = srv.URL
	fetcher := NewFetcher(client)

	_, rl, err := fetcher.ListPullRequests(context.Background(), "o", "r", ledger.ListPRsOptions{State: "all"})
	if err == nil {
		t.Fatal("expected error")
	}
	// rate limit should still be converted even on error
	if rl == nil {
		t.Fatal("rate limit should be returned even on error")
	}
	if rl.Remaining != 10 {
		t.Errorf("remaining = %d, want 10", rl.Remaining)
	}
}

func TestFetcherListIssues(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	closed := now.Add(-time.Hour)
	issues := []Issue{
		{
			Number:    1,
			Title:     "Bug report",
			Body:      "It's broken",
			State:     "closed",
			User:      GitHubUser{Login: "reporter"},
			Labels:    []Label{{Name: "bug"}},
			CreatedAt: now.Add(-48 * time.Hour),
			UpdatedAt: now,
			ClosedAt:  &closed,
			HTMLURL:   "https://github.com/o/r/issues/1",
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(issues)
	}))
	defer srv.Close()

	client := NewClient("tok")
	client.baseURL = srv.URL
	fetcher := NewFetcher(client)

	got, _, err := fetcher.ListIssues(context.Background(), "o", "r", ledger.ListIssuesOptions{State: "all"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d issues, want 1", len(got))
	}
	if got[0].Author != "reporter" {
		t.Errorf("Author = %q, want %q", got[0].Author, "reporter")
	}
	if got[0].ClosedAt == nil || !got[0].ClosedAt.Equal(closed) {
		t.Errorf("ClosedAt = %v, want %v", got[0].ClosedAt, closed)
	}
	if len(got[0].Labels) != 1 || got[0].Labels[0] != "bug" {
		t.Errorf("Labels = %v, want [bug]", got[0].Labels)
	}
}

func TestFetcherListIssuesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "5")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"forbidden"}`))
	}))
	defer srv.Close()

	client := NewClient("tok")
	client.baseURL = srv.URL
	fetcher := NewFetcher(client)

	_, rl, err := fetcher.ListIssues(context.Background(), "o", "r", ledger.ListIssuesOptions{State: "all"})
	if err == nil {
		t.Fatal("expected error")
	}
	if rl == nil {
		t.Fatal("rate limit should be returned on error")
	}
}

func TestFetcherListPRComments(t *testing.T) {
	line := 42
	comments := []Comment{
		{
			ID:        1,
			User:      GitHubUser{Login: "reviewer"},
			Body:      "nit: rename this",
			Path:      "main.go",
			Line:      &line,
			CreatedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(comments)
	}))
	defer srv.Close()

	client := NewClient("tok")
	client.baseURL = srv.URL
	fetcher := NewFetcher(client)

	got, err := fetcher.ListPRComments(context.Background(), "o", "r", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d comments, want 1", len(got))
	}
	if got[0].Author != "reviewer" {
		t.Errorf("Author = %q, want %q", got[0].Author, "reviewer")
	}
	if got[0].Path != "main.go" {
		t.Errorf("Path = %q, want %q", got[0].Path, "main.go")
	}
	if got[0].Line == nil || *got[0].Line != 42 {
		t.Errorf("Line = %v, want 42", got[0].Line)
	}
}

func TestFetcherListPRCommentsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer srv.Close()

	client := NewClient("tok")
	client.baseURL = srv.URL
	fetcher := NewFetcher(client)

	_, err := fetcher.ListPRComments(context.Background(), "o", "r", 999)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestFetcherListIssueComments(t *testing.T) {
	comments := []Comment{
		{
			ID:        100,
			User:      GitHubUser{Login: "commenter"},
			Body:      "Thanks for the fix!",
			CreatedAt: time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC),
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(comments)
	}))
	defer srv.Close()

	client := NewClient("tok")
	client.baseURL = srv.URL
	fetcher := NewFetcher(client)

	got, err := fetcher.ListIssueComments(context.Background(), "o", "r", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d comments, want 1", len(got))
	}
	// issue comments should NOT have path/line
	if got[0].Path != "" {
		t.Errorf("Path = %q, want empty for issue comment", got[0].Path)
	}
	if got[0].Author != "commenter" {
		t.Errorf("Author = %q, want %q", got[0].Author, "commenter")
	}
}

func TestFetcherListIssueCommentsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"message":"error"}`))
	}))
	defer srv.Close()

	client := NewClient("tok")
	client.baseURL = srv.URL
	fetcher := NewFetcher(client)

	_, err := fetcher.ListIssueComments(context.Background(), "o", "r", 5)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestFetcherListPRCommits(t *testing.T) {
	commits := []prCommitJSON{
		{
			SHA: "sha1",
			Commit: struct {
				Message string `json:"message"`
				Author  struct {
					Name string    `json:"name"`
					Date time.Time `json:"date"`
				} `json:"author"`
			}{
				Message: "initial commit",
				Author: struct {
					Name string    `json:"name"`
					Date time.Time `json:"date"`
				}{
					Name: "Dev",
					Date: time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC),
				},
			},
			Author: &struct {
				Login string `json:"login"`
			}{Login: "dev-user"},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(commits)
	}))
	defer srv.Close()

	client := NewClient("tok")
	client.baseURL = srv.URL
	fetcher := NewFetcher(client)

	got, err := fetcher.ListPRCommits(context.Background(), "o", "r", 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d commits, want 1", len(got))
	}
	if got[0].SHA != "sha1" {
		t.Errorf("SHA = %q, want %q", got[0].SHA, "sha1")
	}
	if got[0].Author != "dev-user" {
		t.Errorf("Author = %q, want %q", got[0].Author, "dev-user")
	}
	if got[0].Msg != "initial commit" {
		t.Errorf("Msg = %q, want %q", got[0].Msg, "initial commit")
	}
}

func TestFetcherListPRCommitsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer srv.Close()

	client := NewClient("tok")
	client.baseURL = srv.URL
	fetcher := NewFetcher(client)

	_, err := fetcher.ListPRCommits(context.Background(), "o", "r", 999)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestConvertRL(t *testing.T) {
	t.Run("nil input", func(t *testing.T) {
		got := convertRL(nil)
		if got != nil {
			t.Errorf("expected nil, got %+v", got)
		}
	})

	t.Run("non-nil input", func(t *testing.T) {
		reset := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		rl := &RateLimit{Remaining: 100, Limit: 5000, Reset: reset}
		got := convertRL(rl)
		if got == nil {
			t.Fatal("expected non-nil")
		}
		if got.Remaining != 100 {
			t.Errorf("Remaining = %d, want 100", got.Remaining)
		}
		if got.Limit != 5000 {
			t.Errorf("Limit = %d, want 5000", got.Limit)
		}
		if !got.Reset.Equal(reset) {
			t.Errorf("Reset = %v, want %v", got.Reset, reset)
		}
	})
}
