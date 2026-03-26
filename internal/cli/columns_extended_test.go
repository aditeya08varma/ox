package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestColumnWidths_EdgeCases(t *testing.T) {
	tests := []struct {
		name       string
		rows       [][]string
		minWidths  []int
		maxWidths  []int
		wantWidths []int
	}{
		{
			name: "no min or max constraints",
			rows: [][]string{
				{"hello", "world"},
				{"hi", "there"},
			},
			minWidths:  nil,
			maxWidths:  nil,
			wantWidths: []int{5, 5},
		},
		{
			name: "max width of zero means unlimited",
			rows: [][]string{
				{"very long content here", "short"},
			},
			minWidths:  []int{0, 0},
			maxWidths:  []int{0, 0},
			wantWidths: []int{22, 5},
		},
		{
			name: "single row single column",
			rows: [][]string{
				{"test"},
			},
			minWidths:  []int{2},
			maxWidths:  []int{10},
			wantWidths: []int{4},
		},
		{
			name: "fewer min/max than columns",
			rows: [][]string{
				{"a", "bb", "ccc"},
			},
			minWidths:  []int{5},
			maxWidths:  []int{10},
			wantWidths: []int{5, 2, 3},
		},
		{
			name: "empty cells",
			rows: [][]string{
				{"", "", ""},
				{"a", "", "bc"},
			},
			minWidths:  nil,
			maxWidths:  nil,
			wantWidths: []int{1, 0, 2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ColumnWidths(tt.rows, tt.minWidths, tt.maxWidths)
			assert.Equal(t, tt.wantWidths, got)
		})
	}
}

func TestFormatRow_EdgeCases(t *testing.T) {
	tests := []struct {
		name       string
		row        []string
		widths     []int
		wantPrefix string // check start of output instead of exact match
	}{
		{
			name:       "empty row",
			row:        []string{},
			widths:     []int{},
			wantPrefix: "",
		},
		{
			name:       "more cells than widths uses default width 10",
			row:        []string{"a", "b", "extra"},
			widths:     []int{5, 5},
			wantPrefix: "a     b     extra     ",
		},
		{
			name:       "single cell",
			row:        []string{"hello"},
			widths:     []int{10},
			wantPrefix: "hello     ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatRow(tt.row, tt.widths)
			assert.Equal(t, tt.wantPrefix, got)
		})
	}
}
