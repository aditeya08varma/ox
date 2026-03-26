package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTruncateID_BoundaryAndEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		verbose  bool
		expected string
	}{
		{
			name:     "exactly 16 chars - no truncation",
			id:       "1234567890123456",
			verbose:  false,
			expected: "1234567890123456",
		},
		{
			name:     "17 chars generic - truncated",
			id:       "12345678901234567",
			verbose:  false,
			expected: "12345678...",
		},
		{
			name:     "exactly 12 chars generic - no truncation",
			id:       "123456789012",
			verbose:  false,
			expected: "123456789012",
		},
		{
			name:     "13 chars generic - within 16 char threshold so not truncated",
			id:       "1234567890123",
			verbose:  false,
			expected: "1234567890123",
		},
		{
			name:     "empty string",
			id:       "",
			verbose:  false,
			expected: "",
		},
		{
			name:     "single char",
			id:       "a",
			verbose:  false,
			expected: "a",
		},
		{
			name:     "prefixed ID at exactly 16 chars",
			id:       "prj_123456789012",
			verbose:  false,
			expected: "prj_123456789012",
		},
		{
			name:     "prefixed ID at 17 chars",
			id:       "prj_1234567890123",
			verbose:  false,
			expected: "prj_12345678...0123",
		},
		{
			name:     "tm_ prefix truncation",
			id:       "tm_01234567890123456789",
			verbose:  false,
			expected: "tm_012345678...6789",
		},
		{
			name:     "org_ prefix truncation",
			id:       "org_01234567890123456789",
			verbose:  false,
			expected: "org_01234567...6789",
		},
		{
			name:     "underscore late in string - treated as generic",
			id:       "abcdefg_hijklmnopqrst",
			verbose:  false,
			expected: "abcdefg_...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := TruncateID(tt.id, tt.verbose)
			assert.Equal(t, tt.expected, result, "TruncateID(%q, %v)", tt.id, tt.verbose)
		})
	}
}

func TestTruncateUUID_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		uuid     string
		verbose  bool
		expected string
	}{
		{
			name:     "empty string",
			uuid:     "",
			verbose:  false,
			expected: "",
		},
		{
			name:     "exactly 12 chars - no truncation",
			uuid:     "123456789012",
			verbose:  false,
			expected: "123456789012",
		},
		{
			name:     "13 chars - truncated",
			uuid:     "1234567890123",
			verbose:  false,
			expected: "12345678...",
		},
		{
			name:     "8 chars - no truncation",
			uuid:     "12345678",
			verbose:  false,
			expected: "12345678",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := TruncateUUID(tt.uuid, tt.verbose)
			assert.Equal(t, tt.expected, result, "TruncateUUID(%q, %v)", tt.uuid, tt.verbose)
		})
	}
}

func TestMin(t *testing.T) {
	tests := []struct {
		a, b, want int
	}{
		{1, 2, 1},
		{2, 1, 1},
		{0, 0, 0},
		{-1, 1, -1},
		{5, 5, 5},
	}

	for _, tt := range tests {
		got := min(tt.a, tt.b)
		assert.Equal(t, tt.want, got, "min(%d, %d)", tt.a, tt.b)
	}
}
