package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestPiBlockAlreadyPresent_RecognizesAllMarkerGenerations guards the
// backward-compat legs of the #527 marker rename. The in-process
// installPiHooks path must recognize repos carrying markers from any
// of three install eras so it doesn't stack a duplicate block on top.
//
// Failure prevented: users upgrading through multiple ox versions end up
// with two or three overlapping Pi blocks in their AGENTS.md, each from
// a different install generation.
func TestPiBlockAlreadyPresent_RecognizesAllMarkerGenerations(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"empty file", "", false},
		{"unrelated content", "# readme\n\nhello", false},
		{"current marker", piPrimeMarkerStart + "\n...\n" + piPrimeMarkerEnd, true},
		{"legacy in-process marker", piLegacyInProcessMarkerStart + "\n...\n" + piLegacyInProcessMarkerEnd, true},
		{"legacy generic marker", piLegacyGenericMarkerStart + "\n...\n" + piLegacyGenericMarkerEnd, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := piBlockAlreadyPresent(tc.content)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestPiMarkers_UseUniqueNamespace ensures the in-process marker agrees
// with the external adapter's unique namespace so both paths converge
// on one block per AGENTS.md.
// Failure prevented: in-process and external Pi install paths write
// different markers, producing two blocks when both run.
func TestPiMarkers_UseUniqueNamespace(t *testing.T) {
	assert.Contains(t, piPrimeMarkerStart, ":pi:",
		"in-process Pi marker must carry :pi: namespace matching the external adapter")
	assert.NotEqual(t, "<!-- ox:prime:start -->", piPrimeMarkerStart,
		"must not revert to the generic pre-#527 pair")
}
