package viz

import "testing"

func TestDecidePRMedium(t *testing.T) {
	for _, tc := range []struct {
		intent  string
		medium  PRMedium
		primary string
	}{
		{"move validation from Orders to Gateway", PRMediumMermaid, ""},
		{"show p99 latency and CPU saturation around a deploy marker", PRMediumRich, "operational-time-series"},
		{"show work executing on two CPU cores at the same time", PRMediumRich, "execution-trace"},
		{"show durable replay with independent event consumers", PRMediumRich, "event-stream"},
		{"show the pause resume lifecycle including orphan recovery", PRMediumRich, "state-machine"},
		{"show conformance coverage across agents", PRMediumRich, "coverage-matrix"},
		{"show the multi-component architecture for external adapters", PRMediumRich, "architecture"},
		{"show the multi-writer recovery sequence after a push failure", PRMediumRich, "sequence-diagram"},
		{"show the work queue atomic claim lease expiry and reclaim", PRMediumRich, "sequence-diagram"},
		{"show the streaming data flow from LFS source through cache to consumers", PRMediumRich, "data-flow"},
	} {
		got := DecidePRMedium(tc.intent)
		if got.Medium != tc.medium || got.Primary != tc.primary {
			t.Errorf("%q = %+v", tc.intent, got)
		}
		if tc.medium == PRMediumRich && (got.VisualContract == nil || got.Variant == "") {
			t.Errorf("%q rich decision lacks executable contract: %+v", tc.intent, got)
		}
	}
	if got := DecidePRMedium("rename a config field"); got.Medium != PRMediumNone {
		t.Errorf("rename = %+v, want none", got)
	}
}
