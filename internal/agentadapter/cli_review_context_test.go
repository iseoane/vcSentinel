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

// TestDefaultReviewToolCallsSurvivesOpenCodeTruncationBelowIt is a behavior
// test replacing a weak one: the previous test only pinned the literal
// constant value (defaultReviewToolCalls == 16), which fails the instant the
// budget is recalibrated even though nothing would actually be broken. This
// instead exercises what the constant is FOR: a review that completes in
// fewer turns than the configured Steps budget must not be misclassified as
// truncated, regardless of what the budget's numeric value is.
func TestDefaultReviewToolCallsSurvivesOpenCodeTruncationBelowIt(t *testing.T) {
	if defaultReviewToolCalls < 2 {
		t.Fatalf("defaultReviewToolCalls = %d, this test needs room for at least 2 turns", defaultReviewToolCalls)
	}
	stream := "{\"type\":\"text\",\"part\":{\"type\":\"text\",\"text\":\"complete answer\"}}\n" +
		"{\"type\":\"step_finish\",\"part\":{\"type\":\"step-finish\",\"reason\":\"stop\"}}\n"
	t.Setenv("VAS_SENTINEL_TEST_OUTPUT", stream)
	adapter := CLIAdapter{
		BinaryName: compileAgentBinary(t, "opencode"),
		Config:     config.AgentConfig{Model: "opencode-go/glm-5.3-flash"},
		Timeout:    10 * time.Second,
	}

	result, err := adapter.ReviewWithContextResult(context.Background(), "review SNAPSHOT", headSha(t), []string{reviewFixturePath})
	if err != nil {
		t.Fatalf("ReviewWithContextResult() error = %v, want a completed turn well under the %d-turn budget", err, defaultReviewToolCalls)
	}
	if result.Output != "complete answer" {
		t.Errorf("Output = %q, want the wire narration", result.Output)
	}
}

// TestReviewWithContextResultTreatsNoTerminalEventAsTheMostSevereTruncation
// covers the bug found in an earlier commit's review: an empty StopReason
// was treated as a completed turn on every path, but a stream that never
// produced a single step_finish event is worse than one that produced a
// labeled reason like "tool-calls" — it means even the provider's own
// end-of-turn signal never arrived. Distinguishing this from the (different,
// already-covered) case of an empty stop REASON on an observed step_finish
// event is exactly what opencodeReviewScan.TerminalEventObserved is for.
func TestReviewWithContextResultTreatsNoTerminalEventAsTheMostSevereTruncation(t *testing.T) {
	stream := "{\"type\":\"text\",\"part\":{\"type\":\"text\",\"text\":\"orphan answer\"}}\n"
	t.Setenv("VAS_SENTINEL_TEST_OUTPUT", stream)
	adapter := CLIAdapter{
		BinaryName: compileAgentBinary(t, "opencode"),
		Config:     config.AgentConfig{Model: "opencode-go/glm-5.3-flash"},
		Timeout:    10 * time.Second,
	}

	result, err := adapter.ReviewWithContextResult(context.Background(), "review SNAPSHOT", headSha(t), []string{reviewFixturePath})

	var truncated *TruncatedTurnError
	if !errors.As(err, &truncated) {
		t.Fatalf("err = %v, want *TruncatedTurnError for a stream with no terminal event at all", err)
	}
	if truncated.TerminalEventObserved {
		t.Errorf("TerminalEventObserved = true, want false: the stream never produced a step_finish event")
	}
	if result.Output != "orphan answer" {
		t.Errorf("Output = %q, want the wire narration to survive as evidence", result.Output)
	}
}

// TestReviewWithContextEmptiesOutputOnTruncation pins the legacy string
// contract's own documented promise ("answer text only, empty output on
// error"): a previous version assigned result.Output before the truncation
// check and then returned result.Output unconditionally, so a truncated
// answer text still crossed the legacy boundary as if it were trustworthy.
// The rich ReviewWithContextResult surface keeps Output as evidence
// regardless (see the test above); only the legacy string surface must empty
// it on error.
func TestReviewWithContextEmptiesOutputOnTruncation(t *testing.T) {
	t.Setenv("VAS_SENTINEL_TEST_OUTPUT", truncationStream("tool-calls"))
	adapter := CLIAdapter{
		BinaryName: compileAgentBinary(t, "opencode"),
		Config:     config.AgentConfig{Model: "opencode-go/glm-5.3-flash"},
		Timeout:    10 * time.Second,
	}

	output, err := adapter.ReviewWithContext(context.Background(), "review SNAPSHOT", headSha(t), []string{reviewFixturePath})

	var truncated *TruncatedTurnError
	if !errors.As(err, &truncated) {
		t.Fatalf("err = %v, want *TruncatedTurnError", err)
	}
	if output != "" {
		t.Errorf("output = %q, want empty output on error (legacy contract)", output)
	}
}

// TestTruncatedTurnErrorMessage pins the exact wording the durable evidence
// and any human reading it depend on. The message asserts only what was
// observed — that the turn ended without completing, after how many turns,
// with which stop reason, and which tool calls were denied if any — never a
// specific cause it cannot know: measured evidence showed truncations at 4
// and 5 turns out of a 16-turn budget, disproving the previous wording's
// "exhausted its turn budget" claim.
func TestTruncatedTurnErrorMessage(t *testing.T) {
	cases := []struct {
		name string
		err  *TruncatedTurnError
		want string
	}{
		{
			name: "stop reason without denial detail",
			err:  &TruncatedTurnError{StopReason: "tool-calls", Steps: 8, TerminalEventObserved: true},
			want: "review turn truncated: the turn ended without completing after 8 turn(s) (stop reason: tool-calls)",
		},
		{
			name: "no terminal event was observed at all",
			err:  &TruncatedTurnError{Steps: 0, TerminalEventObserved: false},
			want: "review turn truncated: the turn ended without completing before any terminal event was observed",
		},
		{
			name: "denied tool calls name the cause",
			err: &TruncatedTurnError{StopReason: "tool-calls", Steps: 5, TerminalEventObserved: true, ToolCallErrors: []DeniedToolCall{
				{Tool: "read", Error: "The user rejected permission to use this specific tool call."},
			}},
			want: "review turn truncated: the turn ended without completing after 5 turn(s) (stop reason: tool-calls): denied tool call — read: The user rejected permission to use this specific tool call.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Errorf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestTruncatedTurnErrorMessageContainsThePermanentFailureLiteral binds the
// literal substring internal/review's permanentProviderFailures matches
// ("review turn truncated") directly to TruncatedTurnError.Error(), so the
// two cannot silently drift apart — a previous review flagged that nothing
// enforced this. See also
// internal/review.TestTruncatedTurnErrorIsPermanentAcrossTheRealMessage for
// the classification side of the same binding.
func TestTruncatedTurnErrorMessageContainsThePermanentFailureLiteral(t *testing.T) {
	err := &TruncatedTurnError{StopReason: "tool-calls", Steps: 4, TerminalEventObserved: true}
	if !strings.Contains(err.Error(), "review turn truncated") {
		t.Fatalf("Error() = %q, must contain the literal substring %q that internal/review matches on to keep this failure non-retryable", err.Error(), "review turn truncated")
	}
}

// TestReviewWithContextResultCarriesOpenCodeTurnCount pins the persistence
// gap this change closes: a completed OpenCode review's observed turn count
// (one per step_finish event, exactly opencodeReviewScan.Steps) must reach
// the returned acpadapter.Result rather than being silently dropped on the
// success path. The probe fixture carries exactly 2 step_finish events (see
// TestOpenCodeReviewStepsCountsStepFinishEvents), so Turns must be a non-nil
// pointer to 2, never a bare zero value that would be indistinguishable from
// "unknown".
func TestReviewWithContextResultCarriesOpenCodeTurnCount(t *testing.T) {
	t.Setenv("VAS_SENTINEL_TEST_OUTPUT", loadOpenCodeProbeFixture(t))
	adapter := CLIAdapter{
		BinaryName: compileAgentBinary(t, "opencode"),
		Config:     config.AgentConfig{Model: "opencode-go/glm-5.3-flash"},
		Timeout:    10 * time.Second,
	}

	result, err := adapter.ReviewWithContextResult(context.Background(), "review SNAPSHOT", headSha(t), []string{reviewFixturePath})
	if err != nil {
		t.Fatalf("ReviewWithContextResult() error = %v", err)
	}
	if result.Turns == nil {
		t.Fatal("Turns = nil, want a non-nil observed turn count for a completed OpenCode review")
	}
	if *result.Turns != 2 {
		t.Errorf("Turns = %d, want 2 (one per step_finish event in the probe fixture)", *result.Turns)
	}
}

// TestReviewWithContextResultLeavesClaudeTurnCountNil pins the regression
// this task calls out explicitly: Claude Code exposes no comparable per-turn
// step count, so Turns must be nil, NOT a plain zero. A plain int field with
// omitempty would make "Claude answered this review" indistinguishable from
// "the provider reports no turn count", which is exactly what a later
// recalibration must never read as a real measurement.
func TestReviewWithContextResultLeavesClaudeTurnCountNil(t *testing.T) {
	t.Setenv("VAS_SENTINEL_TEST_OUTPUT", loadClaudeProbeFixture(t))
	adapter := CLIAdapter{
		BinaryName: compileAgentBinary(t, "claude"),
		Config:     config.AgentConfig{Model: "claude-haiku-4-5"},
		Timeout:    10 * time.Second,
	}

	result, err := adapter.ReviewWithContextResult(context.Background(), "review SNAPSHOT", headSha(t), []string{reviewFixturePath})
	if err != nil {
		t.Fatalf("ReviewWithContextResult() error = %v", err)
	}
	if result.Turns != nil {
		t.Errorf("Turns = %d, want nil (Claude Code exposes no comparable turn count)", *result.Turns)
	}
}

// Note: the third provider path (a generic plain-text provider with no
// structured stream to scan) is not exercised here because reviewCommand
// already rejects any binary that is neither Claude nor OpenCode before a
// review can be spawned at all ("semantic review is unavailable:
// path-confined tool permissions are not configured for this provider") —
// that rejection predates this change and is out of scope for it. The
// default branch of runBoundedReview's provider switch (cli_review_context.go)
// returns a reviewExecution{output: ..., terminalEventObserved: true}
// literal that never sets the turns field, so it stays nil by Go's
// zero-value semantics; there is no reachable path to regress here.
