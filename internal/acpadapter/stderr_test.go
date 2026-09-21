package acpadapter

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
)

// The tests in this file pin the bounded stderr enrichment of FAILURE and
// TIMEOUT outcome details: a failing turn carries the child's diagnostic
// output, quiet failures stay byte-identical to the previous detail, and
// success/cancellation outcomes never surface stderr regardless of what the
// child wrote.

func outcomeOf(t *testing.T, err error) *OutcomeError {
	t.Helper()
	var oe *OutcomeError
	if !errors.As(err, &oe) {
		t.Fatalf("error type = %T (%v), want *OutcomeError", err, err)
	}
	return oe
}

// TestFailureOutcomeCarriesStderrExcerpt pins that a FAILURE turn with
// captured stderr appends it as "stderr: <excerpt>" after the classification
// detail.
func TestFailureOutcomeCarriesStderrExcerpt(t *testing.T) {
	a := spawnHelper(t, helperModeFailNoisy)
	res, runErr := a.Run(context.Background(), "say PROBE")
	if runErr == nil {
		t.Fatal("Run must classify a stopReason-error turn as failure")
	}
	if res.Output != "" {
		t.Errorf("Output = %q, want empty on non-success", res.Output)
	}
	oe := outcomeOf(t, runErr)
	if oe.Outcome() != agentrun.OutcomeFailure {
		t.Errorf("Outcome() = %q, want %q", oe.Outcome(), agentrun.OutcomeFailure)
	}
	want := `acpx: terminal stopReason "error": stderr: BOOM simulated provider crash`
	if oe.Detail != want {
		t.Errorf("Detail = %q, want %q", oe.Detail, want)
	}
}

// TestFailureWithoutStderrKeepsDetailUnchanged pins that a failing turn whose
// child wrote nothing to stderr keeps exactly the pre-enrichment detail.
func TestFailureWithoutStderrKeepsDetailUnchanged(t *testing.T) {
	a := spawnHelper(t, helperModeFailQuiet)
	_, runErr := a.Run(context.Background(), "say PROBE")
	if runErr == nil {
		t.Fatal("Run must classify a stopReason-error turn as failure")
	}
	oe := outcomeOf(t, runErr)
	want := `acpx: terminal stopReason "error"`
	if oe.Detail != want {
		t.Errorf("Detail = %q, want the unchanged %q", oe.Detail, want)
	}
}

// TestFailureStderrExcerptCapsAtLastFiveHundredCharacters pins the bound:
// only the LAST 500 characters of stderr survive into the detail.
func TestFailureStderrExcerptCapsAtLastFiveHundredCharacters(t *testing.T) {
	a := spawnHelper(t, helperModeFailLong)
	_, runErr := a.Run(context.Background(), "say PROBE")
	if runErr == nil {
		t.Fatal("Run must classify a stopReason-error turn as failure")
	}
	oe := outcomeOf(t, runErr)
	// The helper writes 600 'a' characters followed by TAIL-MARKER; the last
	// 500 characters are 489 'a's plus the marker.
	wantExcerpt := strings.Repeat("a", 489) + "TAIL-MARKER"
	if !strings.HasSuffix(oe.Detail, "stderr: "+wantExcerpt) {
		t.Errorf("Detail = %q, want it to end with the bounded tail excerpt", oe.Detail)
	}
	if strings.Contains(oe.Detail, strings.Repeat("a", 490)) {
		t.Error("Detail carries more than the 500-character stderr cap")
	}
}

// TestCancellationAndSuccessOutcomesNeverSurfaceStderr pins that noisy
// cancellation turns keep their exact pre-enrichment detail and successful
// turns still return without any classified error.
func TestCancellationAndSuccessOutcomesNeverSurfaceStderr(t *testing.T) {
	a := spawnHelper(t, helperModeCancelNoisy)
	_, runErr := a.Run(context.Background(), "say PROBE")
	if runErr == nil {
		t.Fatal("Run must classify a cancelled turn as cancellation")
	}
	oe := outcomeOf(t, runErr)
	if oe.Outcome() != agentrun.OutcomeCancellation {
		t.Errorf("Outcome() = %q, want %q", oe.Outcome(), agentrun.OutcomeCancellation)
	}
	want := `acpx: terminal stopReason "cancelled"`
	if oe.Detail != want {
		t.Errorf("Detail = %q, want the unchanged %q with no stderr leak", oe.Detail, want)
	}

	ok := spawnHelper(t, helperModeOK)
	if _, err := ok.Run(context.Background(), "say PROBE"); err != nil {
		t.Fatalf("successful turn returned error: %v", err)
	}
}
