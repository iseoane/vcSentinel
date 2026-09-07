package main

// Focused tests for ticket 07 slice 3 on the pr review path: the public
// --json result shape counts admission failures separately from
// infrastructure failures instead of folding both into generic unavailability.

import (
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

func TestPrReviewJSONOutputCountsAdmissionApartFromInfrastructure(t *testing.T) {
	res := &review.BranchResult{
		Branch:   "feature/branch",
		Volume:   10,
		Decision: "single",
		Records: []review.Record{
			{
				SHA: "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111",
				Revisions: []review.Revision{
					{
						Result: review.VerdictUnavailable,
						Dims: []review.DimensionResult{
							{Dim: "logic", Verdict: review.VerdictUnavailable, Reason: "admission: output hash mismatch for invocation inv-1"},
							{Dim: "style", Verdict: review.VerdictUnavailable, Reason: "provider rate limit"},
						},
					},
					// A later revision of the same commit keeps the split
					// honest across the append-only history.
					{
						Result: review.VerdictUnavailable,
						Dims: []review.DimensionResult{
							{Dim: "security", Verdict: review.VerdictUnavailable, Reason: "admission: stale snapshot for audited sha"},
						},
					},
				},
			},
			{
				SHA: "bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222",
				Revisions: []review.Revision{
					{
						Result: review.VerdictUnavailable,
						Dims: []review.DimensionResult{
							{Dim: "tests", Verdict: review.VerdictUnavailable, Reason: "durable store is unreachable"},
						},
					},
				},
			},
		},
	}

	output := prReviewJSONOutput("main", res)

	if got := output["review_admission_failures"]; got != 2 {
		t.Fatalf("review_admission_failures = %v, want 2 admission records counted separately", got)
	}
	if got := output["review_infrastructure_failures"]; got != 2 {
		t.Fatalf("review_infrastructure_failures = %v, want 2 infrastructure records", got)
	}
	if _, ok := output["fichas"]; !ok {
		t.Fatal("output missing fichas: the classification must extend, not replace, the existing result shape")
	}
}

func TestPrReviewJSONOutputWithoutFailuresKeepsZeroCounts(t *testing.T) {
	res := &review.BranchResult{Records: []review.Record{{
		SHA: "cccc3333cccc3333cccc3333cccc3333cccc3333",
		Revisions: []review.Revision{
			{Result: review.VerdictOK, Dims: []review.DimensionResult{{Dim: "logic", Verdict: review.VerdictOK}}},
		},
	}}}

	output := prReviewJSONOutput("main", res)

	if got := output["review_admission_failures"]; got != 0 {
		t.Fatalf("review_admission_failures = %v, want 0 when every dimension succeeded", got)
	}
	if got := output["review_infrastructure_failures"]; got != 0 {
		t.Fatalf("review_infrastructure_failures = %v, want 0 when every dimension succeeded", got)
	}
}
