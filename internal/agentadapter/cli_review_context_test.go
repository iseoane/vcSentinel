package agentadapter

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
)

// truncationStream builds a minimal OpenCode --format json review stream
// whose LAST step_finish event carries the given reason, exercising
// reviewWithContextResultPolicy's truncation classification without
// depending on the full redacted probe fixture.
func truncationStream(reason string) string {
	return "{\"type\":\"text\",\"part\":{\"type\":\"text\",\"text\":\"partial narration\"}}\n" +
		"{\"type\":\"step_finish\",\"part\":{\"type\":\"step-finish\",\"reason\":\"" + reason + "\"}}\n"
}

// TestReviewWithContextResultTruncatedTurnErrorsButKeepsEvidence pins the
// exact truncation classification described in the task: an empty stop
// reason, "end_turn", and "cancelled" must NOT error (an empty reason means
// the provider format carries none at all; "end_turn" is a completed turn;
// "cancelled" is a cancellation handled elsewhere), while any other
// non-empty stop reason (like OpenCode's "tool-calls") means the turn did
// not complete and must return a *TruncatedTurnError. In every case the wire
// evidence (Output, Usage, UsageJSON, StopReason) must still land on the
// result: only the answer text becomes untrusted.
func TestReviewWithContextResultTruncatedTurnErrorsButKeepsEvidence(t *testing.T) {
	cases := []struct {
		name          string
		stopReason    string
		wantTruncated bool
	}{
		{name: "empty stop reason is not truncation", stopReason: "", wantTruncated: false},
		{name: "end_turn is a completed turn", stopReason: "stop", wantTruncated: false},
		{name: "cancelled is handled elsewhere", stopReason: "cancelled", wantTruncated: false},
		{name: "tool-calls is a truncated turn", stopReason: "tool-calls", wantTruncated: true},
		{name: "any other unfinished reason is a truncated turn", stopReason: "length", wantTruncated: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("VAS_SENTINEL_TEST_OUTPUT", truncationStream(tc.stopReason))
			adapter := CLIAdapter{
				BinaryName: compileAgentBinary(t, "opencode"),
				Config:     config.AgentConfig{Model: "opencode-go/glm-5.3-flash"},
				Timeout:    10 * time.Second,
			}

			result, err := adapter.ReviewWithContextResult(context.Background(), "review SNAPSHOT", headSha(t), []string{reviewFixturePath})

			var truncated *TruncatedTurnError
			isTruncated := errors.As(err, &truncated)
			if isTruncated != tc.wantTruncated {
				t.Fatalf("errors.As(err, *TruncatedTurnError) = %t, err = %v, want truncated=%t", isTruncated, err, tc.wantTruncated)
			}
			if tc.wantTruncated {
				if !strings.Contains(err.Error(), "review turn truncated") {
					t.Errorf("err = %q, want it to contain the literal substring %q", err, "review turn truncated")
				}
			} else if err != nil {
				t.Fatalf("ReviewWithContextResult() unexpected error = %v", err)
			}
			// Evidence that crossed the wire must survive regardless of the
			// classification: only the answer text becomes untrusted.
			if result.Output != "partial narration" {
				t.Errorf("Output = %q, want the wire narration to survive", result.Output)
			}
			wantStop := mapOpenCodeStopReason(tc.stopReason)
			if result.StopReason != wantStop {
				t.Errorf("StopReason = %q, want %q", result.StopReason, wantStop)
			}
		})
	}
}

// TestDefaultReviewToolCallsIsTheProvisionalRaisedBudget pins the provisional
// OpenCode Steps turn budget raised from 8 to 16 after measuring that 21% of
// reviews were truncated at 8 (docs/issues/actionable.md item 1 tracks
// calibrating it from measured data instead of a guess).
func TestDefaultReviewToolCallsIsTheProvisionalRaisedBudget(t *testing.T) {
	if defaultReviewToolCalls != 16 {
		t.Errorf("defaultReviewToolCalls = %d, want the provisional raised budget of 16", defaultReviewToolCalls)
	}
}

// TestTruncatedTurnErrorMessage pins the exact wording the durable evidence
// and any human reading it depend on.
func TestTruncatedTurnErrorMessage(t *testing.T) {
	err := &TruncatedTurnError{StopReason: "tool-calls", Steps: 8}
	got := err.Error()
	want := "review turn truncated after 8 turn(s) (stop reason: tool-calls): the reviewer exhausted its turn budget before returning a verdict"
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}
