package reviewexec

import (
	"context"
	"errors"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/acpadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
)

type richReviewStub struct {
	result acpadapter.Result
	err    error
}

func (s richReviewStub) ReviewWithContextResult(context.Context, string, string, []string) (acpadapter.Result, error) {
	return s.result, s.err
}

func (s richReviewStub) ReviewWithPolicy(string, string, []string, reviewcontract.ToolPolicy) (string, error) {
	return s.result.Output, s.err
}
func (s richReviewStub) ReviewWithContextAndPolicyResult(context.Context, string, string, []string, reviewcontract.ToolPolicy) (acpadapter.Result, error) {
	return s.result, s.err
}

func (s richReviewStub) RunReview(string, string, []string) (string, error) {
	return s.result.Output, s.err
}

func testJob(t *testing.T) agentrun.LogicalJob {
	t.Helper()
	request := agentrun.NewRunRequest(agentrun.Candidate("candidate"), agentrun.Prompt("prompt"), nil)
	return agentrun.NewLogicalJob(request)
}

func mustInvocation(t *testing.T) agentrun.InvocationEnvelope {
	t.Helper()
	invocation, err := agentrun.NewRootInvocation(testJob(t), 1, agentrun.DecisionStart)
	if err != nil {
		t.Fatal(err)
	}
	return invocation
}

func TestReviewAdapterPreservesPartialProviderObservationOnError(t *testing.T) {
	input := int64(4)
	stubErr := &acpadapter.OutcomeError{Class: agentrun.OutcomeTimeout, Detail: "timeout after output"}
	adapter := NewReviewAdapterWithPolicy(
		richReviewStub{result: acpadapter.Result{
			Output:         "partial",
			ObservedModel:  "wire/model",
			RequestedModel: "configured/model",
			StopReason:     "",
			Usage:          &acpadapter.Usage{InputTokens: &input},
		}, err: stubErr},
		"sha", []string{"x.go"}, reviewcontract.ToolPolicy{}, nil,
	)
	got, err := adapter.Execute(context.Background(), testJob(t), mustInvocation(t), "")
	if !errors.Is(err, stubErr) {
		t.Fatalf("error = %v, want wrapped provider error", err)
	}
	if got.Output != "partial" {
		t.Fatalf("output = %q, want partial output preserved", got.Output)
	}
	if got.Observation == nil || got.Observation.Model != "wire/model" || got.Observation.RequestedModel != "configured/model" {
		t.Fatalf("observation = %+v, want requested/observed identity", got.Observation)
	}
	if class := err.(*execution.AdapterError).Class; class != agentrun.OutcomeTimeout {
		t.Fatalf("class = %q, want timeout", class)
	}
}

func TestReviewAdapterUsesObservedStopReasonWithoutGuessing(t *testing.T) {
	adapter := NewReviewAdapter(
		richReviewStub{result: acpadapter.Result{Output: "ok", StopReason: "end_turn"}},
		"sha", nil, nil,
	)
	got, err := adapter.Execute(context.Background(), testJob(t), mustInvocation(t), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Observation == nil || got.Observation.StopReason != "end_turn" {
		t.Fatalf("observation = %+v, want observed stop reason", got.Observation)
	}
}

// TestReviewAdapterCarriesObservedTurnCount pins the mapping this task adds:
// an OpenCode review's observed turn count must survive
// observationFromResult into execution.AdapterObservation.Turns, as a
// non-nil pointer independent of the provider result's own pointer (a clone,
// per cloneInt's contract).
func TestReviewAdapterCarriesObservedTurnCount(t *testing.T) {
	turns := 2
	adapter := NewReviewAdapter(
		richReviewStub{result: acpadapter.Result{Output: "ok", StopReason: "end_turn", Turns: &turns}},
		"sha", nil, nil,
	)
	got, err := adapter.Execute(context.Background(), testJob(t), mustInvocation(t), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Observation == nil || got.Observation.Turns == nil {
		t.Fatalf("observation = %+v, want a non-nil observed turn count", got.Observation)
	}
	if *got.Observation.Turns != 2 {
		t.Errorf("Turns = %d, want 2", *got.Observation.Turns)
	}
	if got.Observation.Turns == &turns {
		t.Error("Turns aliases the provider result's own pointer, want a clone")
	}
}

// TestReviewAdapterLeavesTurnCountNilWhenUnobserved pins the negative case:
// a provider result with no observed turn count (Claude, or a generic
// provider) must map to a nil Turns, never a fabricated zero.
func TestReviewAdapterLeavesTurnCountNilWhenUnobserved(t *testing.T) {
	adapter := NewReviewAdapter(
		richReviewStub{result: acpadapter.Result{Output: "ok", StopReason: "end_turn"}},
		"sha", nil, nil,
	)
	got, err := adapter.Execute(context.Background(), testJob(t), mustInvocation(t), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Observation == nil {
		t.Fatal("observation = nil, want a non-nil observation")
	}
	if got.Observation.Turns != nil {
		t.Errorf("Turns = %d, want nil", *got.Observation.Turns)
	}
}

// TestReviewAdapterPreservesObservedZeroTurnCount pins the nil-vs-zero
// invariant that is the entire point of this task, at the
// observationFromResult mapping layer specifically: a pointer to zero (a
// legitimately observed zero-turn review) must survive as a non-nil pointer
// to zero, never collapse to nil.
func TestReviewAdapterPreservesObservedZeroTurnCount(t *testing.T) {
	zero := 0
	adapter := NewReviewAdapter(
		richReviewStub{result: acpadapter.Result{Output: "ok", StopReason: "end_turn", Turns: &zero}},
		"sha", nil, nil,
	)
	got, err := adapter.Execute(context.Background(), testJob(t), mustInvocation(t), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Observation == nil {
		t.Fatal("observation = nil, want a non-nil observation")
	}
	if got.Observation.Turns == nil {
		t.Fatal("Turns = nil, want a non-nil pointer to the observed zero, not a collapse to unknown")
	}
	if *got.Observation.Turns != 0 {
		t.Errorf("Turns = %d, want 0", *got.Observation.Turns)
	}
}
