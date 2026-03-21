package query

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/sageox/ox/internal/codedb/store"
)

// AssembleActivity queries CodeDB for GitHub activity within the given time
// window and returns structured event clusters for the fact extractor.
func AssembleActivity(ctx context.Context, s *store.Store, since, until time.Time) (*ActivityResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	sinceUnix := since.Unix()
	untilUnix := until.Unix()

	result := &ActivityResult{}

	// Step 2: Query PRs in time window
	prs, err := queryPRs(ctx, s, sinceUnix, untilUnix)
	if err != nil {
		return nil, fmt.Errorf("query PRs: %w", err)
	}

	// Steps 3-4: Enrich each PR with comments and commits
	for i := range prs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		prID := prs[i].dbID

		// Step 3: Split comments into review vs discussion
		reviews, discussions, err := queryPRComments(ctx, s, prID)
		if err != nil {
			slog.Warn("query PR comments failed", "number", prs[i].Number, "error", err)
		}
		prs[i].Reviews = reviews
		prs[i].DiscussionComments = discussions

		// Step 4: PR commits via LEFT JOIN
		commits, err := queryPRCommits(ctx, s, prID)
		if err != nil {
			slog.Warn("query PR commits failed", "number", prs[i].Number, "error", err)
		}
		prs[i].Commits = commits
	}
	clusters := make([]PRCluster, len(prs))
	for i := range prs {
		clusters[i] = prs[i].PRCluster
	}
	result.PRClusters = clusters

	// Step 5: Standalone issues
	issues, err := queryIssues(ctx, s, sinceUnix, untilUnix)
	if err != nil {
		return nil, fmt.Errorf("query issues: %w", err)
	}
	result.StandaloneIssues = issues

	// Step 6: Standalone commits
	commits, err := queryStandaloneCommits(ctx, s, sinceUnix, untilUnix)
	if err != nil {
		return nil, fmt.Errorf("query standalone commits: %w", err)
	}
	result.StandaloneCommits = commits

	// Step 7: Metadata
	result.Metadata = ActivityMetadata{
		Since:       since,
		Until:       until,
		PRCount:     len(result.PRClusters),
		IssueCount:  len(result.StandaloneIssues),
		CommitCount: len(result.StandaloneCommits),
	}

	return result, nil
}

// prWithID pairs a PRCluster with its database ID to avoid N+1 lookups.
type prWithID struct {
	dbID int64
	PRCluster
}

// queryPRs fetches PRs where updated_at, merged_at, or created_at falls within [since, until].
func queryPRs(ctx context.Context, s *store.Store, sinceUnix, untilUnix int64) ([]prWithID, error) {
	rows, err := s.QueryContext(ctx, `
		SELECT id, number, title, COALESCE(body, ''), COALESCE(author, ''), state,
		       merged_at, updated_at, COALESCE(url, ''), COALESCE(labels, '[]')
		FROM pull_requests
		WHERE (updated_at >= ? AND updated_at <= ?)
		   OR (merged_at >= ? AND merged_at <= ?)
		   OR (created_at >= ? AND created_at <= ?)
		ORDER BY COALESCE(merged_at, updated_at, created_at) DESC`,
		sinceUnix, untilUnix,
		sinceUnix, untilUnix,
		sinceUnix, untilUnix,
	)
	if err != nil {
		return nil, fmt.Errorf("query pull_requests: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only query

	var prs []prWithID
	for rows.Next() {
		var p prWithID
		var mergedAtUnix, updatedAtUnix *int64
		var labelsJSON string

		if err := rows.Scan(&p.dbID, &p.Number, &p.Title, &p.Description, &p.Author,
			&p.Status, &mergedAtUnix, &updatedAtUnix, &p.URL, &labelsJSON); err != nil {
			return nil, fmt.Errorf("scan PR: %w", err)
		}

		if mergedAtUnix != nil {
			ts := time.Unix(*mergedAtUnix, 0).UTC().Format(time.RFC3339)
			p.MergedAt = &ts
		}
		if updatedAtUnix != nil {
			ts := time.Unix(*updatedAtUnix, 0).UTC().Format(time.RFC3339)
			p.UpdatedAt = &ts
		}

		// Parse JSON-encoded labels
		if labelsJSON != "" && labelsJSON != "[]" {
			var labels []string
			if err := json.Unmarshal([]byte(labelsJSON), &labels); err == nil && len(labels) > 0 {
				p.Labels = labels
			}
		}

		// Initialize empty slices so JSON serializes as [] not null
		p.Commits = []CommitEntry{}
		p.Reviews = []ReviewGroup{}
		p.DiscussionComments = []Comment{}

		prs = append(prs, p)
	}

	return prs, rows.Err()
}

// queryPRComments fetches and splits PR comments into review groups and discussion comments.
func queryPRComments(ctx context.Context, s *store.Store, prID int64) ([]ReviewGroup, []Comment, error) {
	rows, err := s.QueryContext(ctx, `
		SELECT COALESCE(author, ''), COALESCE(body, ''), path, line, created_at
		FROM pr_comments
		WHERE pr_id = ?
		ORDER BY created_at ASC`,
		prID,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("query pr_comments: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only query

	reviewsByAuthor := make(map[string][]ReviewComment)
	var reviewerOrder []string
	var discussions []Comment

	for rows.Next() {
		var author, body string
		var path *string
		var line *int
		var createdAt *int64

		if err := rows.Scan(&author, &body, &path, &line, &createdAt); err != nil {
			return nil, nil, fmt.Errorf("scan comment: %w", err)
		}

		if path != nil && *path != "" {
			// Review comment (has path)
			rc := ReviewComment{
				Body: body,
				Path: path,
				Line: line,
			}
			if _, seen := reviewsByAuthor[author]; !seen {
				reviewerOrder = append(reviewerOrder, author)
			}
			reviewsByAuthor[author] = append(reviewsByAuthor[author], rc)
		} else {
			// Discussion comment (no path)
			var ts string
			if createdAt != nil {
				ts = time.Unix(*createdAt, 0).UTC().Format(time.RFC3339)
			}
			discussions = append(discussions, Comment{
				Author:    author,
				Body:      body,
				CreatedAt: ts,
			})
		}
	}

	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	// Build review groups preserving insertion order
	var reviews []ReviewGroup
	for _, reviewer := range reviewerOrder {
		reviews = append(reviews, ReviewGroup{
			Reviewer: reviewer,
			Comments: reviewsByAuthor[reviewer],
		})
	}

	if reviews == nil {
		reviews = []ReviewGroup{}
	}
	if discussions == nil {
		discussions = []Comment{}
	}

	return reviews, discussions, nil
}

// queryPRCommits fetches commits linked to a PR via LEFT JOIN with the commits table.
func queryPRCommits(ctx context.Context, s *store.Store, prID int64) ([]CommitEntry, error) {
	rows, err := s.QueryContext(ctx, `
		SELECT pc.sha, c.message, c.author, c.timestamp
		FROM pr_commits pc
		LEFT JOIN commits c ON c.hash = pc.sha
		WHERE pc.pr_id = ?
		ORDER BY COALESCE(c.timestamp, 0) ASC`,
		prID,
	)
	if err != nil {
		return nil, fmt.Errorf("query pr_commits: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only query

	var commits []CommitEntry
	for rows.Next() {
		var sha string
		var message, author sql.NullString
		var timestamp sql.NullInt64

		if err := rows.Scan(&sha, &message, &author, &timestamp); err != nil {
			return nil, fmt.Errorf("scan commit: %w", err)
		}

		entry := CommitEntry{SHA: sha}
		if message.Valid {
			entry.Message = &message.String
		}
		if author.Valid {
			entry.Author = &author.String
		}
		if timestamp.Valid {
			ts := time.Unix(timestamp.Int64, 0).UTC().Format(time.RFC3339)
			entry.Timestamp = &ts
		}

		commits = append(commits, entry)
	}

	if commits == nil {
		commits = []CommitEntry{}
	}

	return commits, rows.Err()
}

// queryIssues fetches issues within the time window with their comments.
func queryIssues(ctx context.Context, s *store.Store, sinceUnix, untilUnix int64) ([]StandaloneIssue, error) {
	rows, err := s.QueryContext(ctx, `
		SELECT id, number, title, COALESCE(body, ''), COALESCE(author, ''), state,
		       COALESCE(url, ''), updated_at
		FROM issues
		WHERE (updated_at >= ? AND updated_at <= ?)
		   OR (created_at >= ? AND created_at <= ?)
		ORDER BY COALESCE(updated_at, created_at) DESC`,
		sinceUnix, untilUnix,
		sinceUnix, untilUnix,
	)
	if err != nil {
		return nil, fmt.Errorf("query issues: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only query

	var issues []StandaloneIssue
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		var issue StandaloneIssue
		var issueID int64
		var updatedAtUnix *int64

		if err := rows.Scan(&issueID, &issue.Number, &issue.Title, &issue.Body,
			&issue.Author, &issue.State, &issue.URL, &updatedAtUnix); err != nil {
			return nil, fmt.Errorf("scan issue: %w", err)
		}

		if updatedAtUnix != nil {
			ts := time.Unix(*updatedAtUnix, 0).UTC().Format(time.RFC3339)
			issue.UpdatedAt = &ts
		}

		comments, err := queryIssueComments(ctx, s, issueID)
		if err != nil {
			slog.Warn("query issue comments failed", "number", issue.Number, "error", err)
			comments = []Comment{}
		}
		issue.Comments = comments

		issues = append(issues, issue)
	}

	if issues == nil {
		issues = []StandaloneIssue{}
	}

	return issues, rows.Err()
}

// queryIssueComments fetches comments for a given issue.
func queryIssueComments(ctx context.Context, s *store.Store, issueID int64) ([]Comment, error) {
	rows, err := s.QueryContext(ctx, `
		SELECT COALESCE(author, ''), COALESCE(body, ''), created_at
		FROM issue_comments
		WHERE issue_id = ?
		ORDER BY created_at ASC`,
		issueID,
	)
	if err != nil {
		return nil, fmt.Errorf("query issue_comments: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only query

	var comments []Comment
	for rows.Next() {
		var c Comment
		var createdAt *int64

		if err := rows.Scan(&c.Author, &c.Body, &createdAt); err != nil {
			return nil, fmt.Errorf("scan issue comment: %w", err)
		}

		if createdAt != nil {
			c.CreatedAt = time.Unix(*createdAt, 0).UTC().Format(time.RFC3339)
		}

		comments = append(comments, c)
	}

	if comments == nil {
		comments = []Comment{}
	}

	return comments, rows.Err()
}

// queryStandaloneCommits fetches commits in the time window not linked to any PR.
func queryStandaloneCommits(ctx context.Context, s *store.Store, sinceUnix, untilUnix int64) ([]StandaloneCommit, error) {
	rows, err := s.QueryContext(ctx, `
		SELECT hash, COALESCE(message, ''), COALESCE(author, ''), timestamp
		FROM commits
		WHERE timestamp >= ? AND timestamp <= ?
		  AND NOT EXISTS (SELECT 1 FROM pr_commits WHERE sha = commits.hash)
		ORDER BY timestamp DESC`,
		sinceUnix, untilUnix,
	)
	if err != nil {
		return nil, fmt.Errorf("query standalone commits: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only query

	var commits []StandaloneCommit
	for rows.Next() {
		var c StandaloneCommit
		var ts int64

		if err := rows.Scan(&c.SHA, &c.Message, &c.Author, &ts); err != nil {
			return nil, fmt.Errorf("scan commit: %w", err)
		}

		c.Timestamp = time.Unix(ts, 0).UTC().Format(time.RFC3339)
		commits = append(commits, c)
	}

	if commits == nil {
		commits = []StandaloneCommit{}
	}

	return commits, rows.Err()
}
