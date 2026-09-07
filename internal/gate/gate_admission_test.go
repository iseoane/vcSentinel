package gate

// Focused tests for ticket 07 slice 3: admission failures surface through the
// gate's public result shape distinctly from infrastructure failures. Exit
// codes and terminal states are untouched; only the evidence classification
// in the messages differs.

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewexec"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

func TestGateSurfacesAdmissionDistinctFromInfrastructure(t *testing.T) {
	cfg := cfgWithProfile("lint", "echo ok")
	opts := baseOptions(t, cfg, func(string) (int, string, error) { return 0, "ok", nil },
		countingFactory(new(int), "", nil))
	// This test injects its own transport at the engine seam, so clear the
	// default production-style factory wired by baseOptions.
	opts.DurableReviewTransportFactory = nil
	opts.RunValidation = func(_ string, _ []string, _ validation.RunOptions) ([]validation.ValidationRun, error) {
		return nil, nil // validation green: the review stage is what this test exercises
	}
	opts.ReviewOptions.Bundles = []review.ReviewBundle{
		{Name: "test", Dimensions: []string{review.DimLogic, review.DimSecurity}, Priority: 1, Cost: 1},
	}
	opts.ReviewOptions.ReviewTransport = func(_ string, dimension, _ string, _ review.AgentReviewer) (string, string, error) {
		if dimension == review.DimLogic {
			return "", "", &reviewexec.AdmissionError{
				Identity: "quality/logic",
				Reason:   "output hash mismatch for the admitted invocation",
			}
		}
		return "", "", errors.New("durable store is unreachable")
	}

	result := RunGate(opts)

	if result.State != StateReviewInfrastructureError {
		t.Fatalf("state = %q, want %q (exit-code contracts unchanged)", result.State, StateReviewInfrastructureError)
	}
	joined := strings.Join(result.Messages, "\n")
	if !strings.Contains(joined, `dimension="logic" reason="admission: output hash mismatch for the admitted invocation" class=admission`) {
		t.Fatalf("messages = %q, want the logic dimension labeled class=admission with its evidence prefix intact", joined)
	}
	if !strings.Contains(joined, `dimension="security" reason="durable store is unreachable" class=infrastructure`) {
		t.Fatalf("messages = %q, want the security dimension labeled class=infrastructure", joined)
	}
	if strings.Count(joined, "class=admission") != 1 || strings.Count(joined, "class=infrastructure") != 1 {
		t.Fatalf("messages = %q, want exactly one label per failure class", joined)
	}
}

// TestFailureClassPrefersTypedErrorThenReason pins the classification order:
// the typed transport error wins when the engine retained it; once only the
// persisted reason survives, the literal admission prefix decides.
func TestFailureClassPrefersTypedErrorThenReason(t *testing.T) {
	admitted := review.DimensionOutcome{
		Error:  fmt.Errorf("review run failed: %w", &reviewexec.AdmissionError{Reason: "stale snapshot"}),
		Result: &review.DimensionResult{Verdict: review.VerdictUnavailable},
	}
	if got := failureClass(admitted); got != "admission" {
		t.Fatalf("failureClass(typed admission error) = %q, want admission", got)
	}

	persisted := review.DimensionOutcome{
		Result: &review.DimensionResult{Verdict: review.VerdictUnavailable, Reason: "admission: prompt identity diverged"},
	}
	if got := failureClass(persisted); got != "admission" {
		t.Fatalf("failureClass(persisted admission reason) = %q, want admission", got)
	}

	outage := review.DimensionOutcome{
		Error:  errors.New("durable store is unreachable"),
		Result: &review.DimensionResult{Verdict: review.VerdictUnavailable, Reason: "durable store is unreachable"},
	}
	if got := failureClass(outage); got != "infrastructure" {
		t.Fatalf("failureClass(infrastructure failure) = %q, want infrastructure", got)
	}
}
