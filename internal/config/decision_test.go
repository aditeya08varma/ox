package config

import (
	"strings"
	"testing"
)

func TestValidateDecisionConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *DecisionConfig
		wantErr string // substring; "" = valid
	}{
		{"nil config", nil, ""},
		{"empty paths", &DecisionConfig{}, ""},
		{"valid dirs and globs", &DecisionConfig{Paths: []string{"docs/adr", "eng/**/decisions/*.md"}}, ""},
		{"empty entry", &DecisionConfig{Paths: []string{"docs/adr", "  "}}, "empty path"},
		{"absolute path", &DecisionConfig{Paths: []string{"/etc/adr"}}, "relative"},
		{"parent traversal", &DecisionConfig{Paths: []string{"../other/adr"}}, "traverse"},
		{"embedded traversal", &DecisionConfig{Paths: []string{"docs/../../adr"}}, "traverse"},
		{"bare dotdot", &DecisionConfig{Paths: []string{".."}}, "traverse"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDecisionConfig(tt.cfg)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("got %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestDecisionConfigIsEmpty(t *testing.T) {
	var nilCfg *DecisionConfig
	if !nilCfg.IsEmpty() {
		t.Error("nil should be empty")
	}
	if !(&DecisionConfig{}).IsEmpty() {
		t.Error("zero-value should be empty")
	}
	if (&DecisionConfig{Paths: []string{"docs/adr"}}).IsEmpty() {
		t.Error("populated should not be empty")
	}
}
