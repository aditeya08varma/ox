package plan

import "testing"

func TestShouldUseStoredHTML(t *testing.T) {
	authored := []byte(`<!doctype html><html><body><svg role="img" aria-label="System map"></svg></body></html>`)
	generated := []byte(`<!doctype html><html><head><meta name="generator" content="ox plan markdown renderer"></head><body></body></html>`)
	legacyGenerated := []byte(`<div class="brand">OX · PLAN</div><div class="eyebrow">SageOx · enriched plan</div>`)

	tests := []struct {
		name string
		meta Meta
		html []byte
		want bool
	}{
		{name: "explicit HTML primary", meta: Meta{Primary: PrimaryHTML}, html: authored, want: true},
		{name: "legacy authored HTML", meta: Meta{}, html: authored, want: true},
		{name: "stamped markdown render", meta: Meta{}, html: generated, want: false},
		{name: "legacy markdown render", meta: Meta{}, html: legacyGenerated, want: false},
		{name: "unknown future primary", meta: Meta{Primary: "other"}, html: authored, want: false},
		{name: "missing HTML", meta: Meta{Primary: PrimaryHTML}, html: nil, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShouldUseStoredHTML(tt.meta, tt.html); got != tt.want {
				t.Fatalf("ShouldUseStoredHTML() = %v, want %v", got, tt.want)
			}
		})
	}
}
