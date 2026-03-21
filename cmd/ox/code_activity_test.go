package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSinceFlag_Duration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		check func(t *testing.T, result time.Time)
	}{
		{
			name:  "7d duration",
			input: "7d",
			check: func(t *testing.T, result time.Time) {
				expected := time.Now().UTC().AddDate(0, 0, -7)
				assert.InDelta(t, expected.Unix(), result.Unix(), 2)
			},
		},
		{
			name:  "24h duration",
			input: "24h",
			check: func(t *testing.T, result time.Time) {
				expected := time.Now().UTC().Add(-24 * time.Hour)
				assert.InDelta(t, expected.Unix(), result.Unix(), 2)
			},
		},
		{
			name:  "30d duration",
			input: "30d",
			check: func(t *testing.T, result time.Time) {
				expected := time.Now().UTC().AddDate(0, 0, -30)
				assert.InDelta(t, expected.Unix(), result.Unix(), 2)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := parseSinceFlag(tt.input)
			require.NoError(t, err)
			tt.check(t, result)
		})
	}
}

func TestParseSinceFlag_Date(t *testing.T) {
	t.Parallel()

	result, err := parseSinceFlag("2026-03-15")
	require.NoError(t, err)
	assert.Equal(t, 2026, result.Year())
	assert.Equal(t, time.March, result.Month())
	assert.Equal(t, 15, result.Day())
}

func TestParseSinceFlag_RFC3339(t *testing.T) {
	t.Parallel()

	result, err := parseSinceFlag("2026-03-15T10:00:00Z")
	require.NoError(t, err)
	assert.Equal(t, 2026, result.Year())
	assert.Equal(t, 10, result.Hour())
}

func TestParseSinceFlag_Invalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{name: "banana", input: "banana"},
		{name: "empty", input: ""},
		{name: "negative days", input: "-7d"},
		{name: "negative hours", input: "-24h"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := parseSinceFlag(tt.input)
			assert.Error(t, err)
		})
	}
}
