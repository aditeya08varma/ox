package viz

import (
	"encoding/json"
	"os"
	"testing"
)

func TestPRContractEvalCorpusRoutesToExpectedMediumAndPattern(t *testing.T) {
	raw, err := os.ReadFile("testdata/pr_contract_eval/corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus []struct {
		ID              string   `json:"id"`
		PR              int      `json:"pr"`
		Intent          string   `json:"intent"`
		ExpectedMedium  PRMedium `json:"expected_medium"`
		ExpectedPattern string   `json:"expected_pattern"`
		Evidence        []string `json:"evidence"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	if len(corpus) < 6 {
		t.Fatalf("eval corpus has %d cases, want at least 6", len(corpus))
	}
	seenPR := map[int]bool{}
	seenPattern := map[string]bool{}
	for _, tc := range corpus {
		t.Run(tc.ID, func(t *testing.T) {
			if tc.ID == "" || tc.PR == 0 || tc.Intent == "" || len(tc.Evidence) < 2 {
				t.Fatalf("corpus case is not review-grounded: %+v", tc)
			}
			if seenPR[tc.PR] {
				t.Fatalf("PR #%d appears twice", tc.PR)
			}
			seenPR[tc.PR] = true
			got := DecidePRMedium(tc.Intent)
			if got.Medium != tc.ExpectedMedium || got.Primary != tc.ExpectedPattern {
				t.Fatalf("DecidePRMedium() = %+v, want medium=%s pattern=%q", got, tc.ExpectedMedium, tc.ExpectedPattern)
			}
			if got.Medium == PRMediumRich {
				if got.VisualContract == nil {
					t.Fatal("rich corpus decision has no visual contract")
				}
				seenPattern[got.Primary] = true
			}
		})
	}
	if len(seenPattern) < 5 {
		t.Fatalf("corpus exercises only %d rich grammars: %v", len(seenPattern), seenPattern)
	}
}
