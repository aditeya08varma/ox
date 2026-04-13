package main

import (
	"testing"
	"time"
)

// skipIfNearUTCMidnight skips the test if `now` is within 10 seconds of
// a UTC day boundary. The harness captures `now` once and the production
// code (in-proc runDistillHistory*InProc or the slow-tagged subprocess) captures
// its own time.Now() a few milliseconds later; when those straddle a day
// boundary the day-rounded window in the envelope disagrees with the
// test's expected bounds. Ten seconds is far more margin than the
// command-layer budget so the skip is rare in practice.
//
// This helper is untagged so both the fast in-proc distill history tests and the
// slow-tagged end-to-end distill history tests share one source of truth. Do not
// duplicate this function into tagged test files — add the call site here.
func skipIfNearUTCMidnight(t *testing.T, now time.Time) {
	t.Helper()
	u := now.UTC()
	sod := time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
	eod := sod.Add(24 * time.Hour)
	if u.Sub(sod) < 10*time.Second || eod.Sub(u) < 10*time.Second {
		t.Skip("within 10s of UTC midnight — window bounds may straddle a day boundary")
	}
}
