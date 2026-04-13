package main

import (
	"errors"
	"testing"
	"time"
)

// TestResolveJournalWindow — table test for the CLI-facing
// --since/--until/--tz resolver. Every row pins one input combination
// against the day-rounded UTC window the reader expects. The resolver
// is the only place the distill history command layer touches time.Location,
// so this is the single point where timezone + rounding bugs can
// surface, and therefore the single point where they must be caught.
//
// Failure prevented: any of the grammar rules in distill-history-read-plan.md
// §3 Unit 3 silently break on a well-formed input, or a malformed
// input silently returns a zero time instead of usage_error.
func TestResolveJournalWindow(t *testing.T) {
	t.Parallel()

	// Reference instant for every row that uses the default --until=now
	// path. 2026-04-12T15:30:45Z is chosen deliberately: mid-day UTC
	// (so day-floor/ceil rounding is obvious), not near a DST boundary
	// in America/Los_Angeles, and far from any filesystem clock drift.
	now := time.Date(2026, 4, 12, 15, 30, 45, 0, time.UTC)

	mustLoad := func(name string) *time.Location {
		loc, err := time.LoadLocation(name)
		if err != nil {
			t.Fatalf("LoadLocation(%q): %v", name, err)
		}
		return loc
	}
	la := mustLoad("America/Los_Angeles")

	type wantWindow struct {
		since time.Time // raw resolved since (UTC, pre-rounding)
		until time.Time // raw resolved until (UTC, pre-rounding)
	}
	cases := []struct {
		name        string
		sinceStr    string
		untilStr    string
		tzStr       string
		wantErr     bool   // a usage_error is expected
		wantErrCode string // if wantErr, expected code
		wantErrSub  string // if wantErr, substring that must appear in the message
		want        wantWindow
	}{
		{
			name:     "relative duration 24h",
			sinceStr: "24h",
			want: wantWindow{
				since: now.Add(-24 * time.Hour),
				until: now,
			},
		},
		{
			name:     "relative duration 1h",
			sinceStr: "1h",
			want: wantWindow{
				since: now.Add(-1 * time.Hour),
				until: now,
			},
		},
		{
			name:     "relative duration 6h",
			sinceStr: "6h",
			want: wantWindow{
				since: now.Add(-6 * time.Hour),
				until: now,
			},
		},
		{
			name:     "absolute UTC naked since only defaults until=now",
			sinceStr: "2026-04-10T09:00",
			want: wantWindow{
				since: time.Date(2026, 4, 10, 9, 0, 0, 0, time.UTC),
				until: now,
			},
		},
		{
			name:     "absolute UTC naked both bounds",
			sinceStr: "2026-04-10T09:00",
			untilStr: "2026-04-10T15:00",
			want: wantWindow{
				since: time.Date(2026, 4, 10, 9, 0, 0, 0, time.UTC),
				until: time.Date(2026, 4, 10, 15, 0, 0, 0, time.UTC),
			},
		},
		{
			name:     "absolute UTC with Z suffix",
			sinceStr: "2026-04-10T09:00Z",
			untilStr: "2026-04-10T15:00Z",
			want: wantWindow{
				since: time.Date(2026, 4, 10, 9, 0, 0, 0, time.UTC),
				until: time.Date(2026, 4, 10, 15, 0, 0, 0, time.UTC),
			},
		},
		{
			name:     "absolute with offset since only",
			sinceStr: "2026-04-10T09:00-07:00",
			want: wantWindow{
				since: time.Date(2026, 4, 10, 16, 0, 0, 0, time.UTC),
				until: now,
			},
		},
		{
			name:     "absolute with offset both bounds",
			sinceStr: "2026-04-10T09:00-07:00",
			untilStr: "2026-04-10T15:00-07:00",
			want: wantWindow{
				since: time.Date(2026, 4, 10, 16, 0, 0, 0, time.UTC),
				until: time.Date(2026, 4, 10, 22, 0, 0, 0, time.UTC),
			},
		},
		{
			name:     "tz + naked timestamp converts to UTC",
			sinceStr: "2026-04-10T00:00",
			untilStr: "2026-04-10T23:59",
			tzStr:    "America/Los_Angeles",
			want: wantWindow{
				since: time.Date(2026, 4, 10, 0, 0, 0, 0, la).UTC(),
				until: time.Date(2026, 4, 10, 23, 59, 0, 0, la).UTC(),
			},
		},
		{
			name:     "date only since only defaults until=now",
			sinceStr: "2026-04-10",
			want: wantWindow{
				since: time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
				until: now,
			},
		},
		{
			name:     "date only with tz",
			sinceStr: "2026-04-10",
			untilStr: "2026-04-10",
			tzStr:    "America/Los_Angeles",
			want: wantWindow{
				since: time.Date(2026, 4, 10, 0, 0, 0, 0, la).UTC(),
				until: time.Date(2026, 4, 10, 0, 0, 0, 0, la).UTC(),
			},
		},

		// ------ usage errors ------
		{
			name:        "empty since is usage error",
			sinceStr:    "",
			wantErr:     true,
			wantErrCode: "usage_error",
			wantErrSub:  "--since",
		},
		{
			name:        "until without since is usage error",
			sinceStr:    "",
			untilStr:    "2026-04-10T09:00",
			wantErr:     true,
			wantErrCode: "usage_error",
			wantErrSub:  "--since",
		},
		{
			name:        "invalid duration literal",
			sinceStr:    "not-a-duration",
			wantErr:     true,
			wantErrCode: "usage_error",
			wantErrSub:  "--since",
		},
		{
			name:        "invalid absolute timestamp",
			sinceStr:    "2026-13-99T25:99",
			wantErr:     true,
			wantErrCode: "usage_error",
			wantErrSub:  "--since",
		},
		{
			name:        "invalid tz zone",
			sinceStr:    "2026-04-10T09:00",
			tzStr:       "Not/A/Real/Zone",
			wantErr:     true,
			wantErrCode: "usage_error",
			wantErrSub:  "invalid timezone",
		},
		{
			name:        "tz plus offset-bearing since conflicts",
			sinceStr:    "2026-04-10T09:00-07:00",
			tzStr:       "America/Los_Angeles",
			wantErr:     true,
			wantErrCode: "usage_error",
			wantErrSub:  "conflicting timezone",
		},
		{
			name:        "tz plus offset-bearing until conflicts",
			sinceStr:    "2026-04-10T00:00",
			untilStr:    "2026-04-10T15:00-07:00",
			tzStr:       "America/Los_Angeles",
			wantErr:     true,
			wantErrCode: "usage_error",
			wantErrSub:  "conflicting timezone",
		},
		{
			name:        "tz plus Z-suffixed since conflicts",
			sinceStr:    "2026-04-10T09:00Z",
			tzStr:       "America/Los_Angeles",
			wantErr:     true,
			wantErrCode: "usage_error",
			wantErrSub:  "conflicting timezone",
		},
		{
			name:        "invalid until timestamp",
			sinceStr:    "2026-04-10T09:00",
			untilStr:    "not-a-time",
			wantErr:     true,
			wantErrCode: "usage_error",
			wantErrSub:  "--until",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			since, until, err := resolveDistillHistoryWindow(tc.sinceStr, tc.untilStr, tc.tzStr, now)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("want usage error, got nil (since=%v until=%v)", since, until)
				}
				var uerr *distillHistoryUsageError
				if !errors.As(err, &uerr) {
					t.Fatalf("want *distillHistoryUsageError, got %T: %v", err, err)
				}
				if uerr.Code != tc.wantErrCode {
					t.Fatalf("error code = %q, want %q", uerr.Code, tc.wantErrCode)
				}
				if tc.wantErrSub != "" && !containsSubstr(uerr.Message, tc.wantErrSub) {
					t.Fatalf("error message %q does not contain %q", uerr.Message, tc.wantErrSub)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !since.Equal(tc.want.since) {
				t.Fatalf("since = %v, want %v", since, tc.want.since)
			}
			if !until.Equal(tc.want.until) {
				t.Fatalf("until = %v, want %v", until, tc.want.until)
			}
			if since.Location() != time.UTC {
				t.Fatalf("since Location = %v, want UTC", since.Location())
			}
			if until.Location() != time.UTC {
				t.Fatalf("until Location = %v, want UTC", until.Location())
			}
		})
	}
}

func containsSubstr(hay, needle string) bool {
	if needle == "" {
		return true
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
