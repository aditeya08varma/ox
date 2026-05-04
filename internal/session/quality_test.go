package session

import "testing"

func TestEvaluateQuality(t *testing.T) {
	tests := []struct {
		name    string
		score   float64
		upload  float64
		discard float64
		want    QualityDisposition
	}{
		// Regression for #525: a real LLM score of 0 (e.g. empty session
		// correctly scored 0) must flow through the discard gate. The function
		// no longer has an "unscored sentinel" branch — "unscored" decisions
		// are the caller's responsibility.
		{"explicit zero is discarded below discard threshold", 0.0, 0.3, 0.1, QualityDiscard},
		{"below discard threshold", 0.05, 0.3, 0.1, QualityDiscard},
		{"at discard threshold boundary", 0.1, 0.3, 0.1, QualityLocalOnly},
		{"between thresholds", 0.2, 0.3, 0.1, QualityLocalOnly},
		{"at upload threshold", 0.3, 0.3, 0.1, QualityUpload},
		{"above upload threshold", 0.8, 0.3, 0.1, QualityUpload},
		{"perfect score", 1.0, 0.3, 0.1, QualityUpload},
		{"custom high thresholds", 0.5, 0.7, 0.3, QualityLocalOnly},
		{"custom high thresholds above", 0.8, 0.7, 0.3, QualityUpload},
		{"custom high thresholds below", 0.2, 0.7, 0.3, QualityDiscard},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EvaluateQuality(tt.score, tt.upload, tt.discard)
			if got != tt.want {
				t.Errorf("EvaluateQuality(%f, %f, %f) = %q, want %q",
					tt.score, tt.upload, tt.discard, got, tt.want)
			}
		})
	}
}

// TestEvaluateQualityCategory pins the categorical → disposition mapping.
// Failure prevented: a future change to category names or to the
// disposition mapping would silently misroute sessions (e.g. skip
// being treated as upload would leak useless stubs onto the team
// ledger; share being treated as discard would lose real work).
func TestEvaluateQualityCategory(t *testing.T) {
	tests := []struct {
		category string
		want     QualityDisposition
	}{
		{"skip", QualityDiscard},
		{"local_only", QualityLocalOnly},
		{"share", QualityUpload},
		// Defensive defaults — unknown / future / empty categories must
		// preserve the artifact (upload) rather than silently discard.
		// "Don't lose work" beats "be strict about labels."
		{"", QualityUpload},
		{"unknown_future_category", QualityUpload},
		{"SHARE", QualityUpload}, // case-sensitive on purpose; LLM was instructed to emit lowercase
	}

	for _, tt := range tests {
		t.Run(tt.category, func(t *testing.T) {
			got := EvaluateQualityCategory(tt.category)
			if got != tt.want {
				t.Errorf("EvaluateQualityCategory(%q) = %q, want %q",
					tt.category, got, tt.want)
			}
		})
	}
}
