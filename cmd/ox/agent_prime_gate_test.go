package main

import (
	"testing"

	"github.com/sageox/ox/internal/prime"
	"github.com/stretchr/testify/require"
)

// An explicit, recognized --agent satisfies the prime gate without ambient
// detection — including agents (OMP) that export no marker ox can detect from
// its own process. Supported agents return nil regardless of AGENT_ENV.
func TestRequireDetectedOrExplicitAgentAcceptsSupportedAgents(t *testing.T) {
	t.Setenv("AGENT_ENV", "")
	for _, agent := range []string{"omp", "Oh My Pi", "claude-code", "pi"} {
		require.NoError(t, requireDetectedOrExplicitAgent(agent),
			"explicit --agent %q should satisfy the gate", agent)
	}
}

// A typo/unknown --agent must NOT get a free pass: it is not "supported", so
// the gate falls through to ambient detection (which fails outside an agent).
// Asserted via the predicate to stay independent of the host's agent markers.
func TestUnsupportedExplicitAgentDoesNotAutoPass(t *testing.T) {
	require.False(t, prime.IsAgentSupported("typo"))
	require.True(t, prime.IsAgentSupported("Oh My Pi"))
}

func TestSessionWatchModeUsesTailForOMP(t *testing.T) {
	if got := sessionWatchMode("omp"); got != "tail" {
		t.Fatalf("sessionWatchMode(omp) = %q, want tail", got)
	}
}
