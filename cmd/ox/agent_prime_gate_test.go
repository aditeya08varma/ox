package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRequireDetectedOrExplicitAgentAcceptsOMP(t *testing.T) {
	t.Setenv("AGENT_ENV", "")
	require.NoError(t, requireDetectedOrExplicitAgent("omp"))
}

func TestOnlyOMPBypassesAgentDetection(t *testing.T) {
	if bypassAgentDetection("typo") {
		t.Fatal("arbitrary --agent value bypassed agent detection")
	}
	if !bypassAgentDetection("Oh My Pi") {
		t.Fatal("OMP display name did not bypass agent detection")
	}
}

func TestSessionWatchModeUsesTailForOMP(t *testing.T) {
	if got := sessionWatchMode("omp"); got != "tail" {
		t.Fatalf("sessionWatchMode(omp) = %q, want tail", got)
	}
}
