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
