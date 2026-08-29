package main

import (
	"context"
	"errors"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/acpadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
)

type richPromptDelegate struct {
	result acpadapter.Result
	err    error
}

func (d richPromptDelegate) EjecutarPrompt(string) (string, error) {
	return d.result.Output, d.err
}

func (d richPromptDelegate) EjecutarPromptWithContext(context.Context, string) (string, error) {
	return d.result.Output, d.err
}

func (d richPromptDelegate) Run(context.Context, string) (acpadapter.Result, error) {
	return d.result, d.err
}

func TestPromptRunAdapterPreservesPartialACPObservation(t *testing.T) {
	output := "partial"
	stubErr := &acpadapter.OutcomeError{Class: agentrun.OutcomeFailure, Detail: "provider failed"}
	adapter, err := newPromptRunAdapter(richPromptDelegate{result: acpadapter.Result{
		Output:         output,
		ObservedModel:  "wire/model",
		RequestedModel: "configured/model",
		StopReason:     "max_tokens",
	}, err: stubErr})
	if err != nil {
		t.Fatal(err)
	}
	job := agentrun.NewLogicalJob(agentrun.NewRunRequest(agentrun.Candidate("candidate"), agentrun.Prompt("prompt"), nil))
	invocation, err := agentrun.NewRootInvocation(job, 1, agentrun.DecisionStart)
	if err != nil {
		t.Fatal(err)
	}
	got, err := adapter.Execute(context.Background(), job, invocation, "")
	if !errors.Is(err, stubErr) {
		t.Fatalf("error = %v, want wrapped provider error", err)
	}
	if got.Output != output || got.Observation == nil {
		t.Fatalf("result = %+v, want partial output and observation", got)
	}
	if got.Observation.Model != "wire/model" || got.Observation.RequestedModel != "configured/model" {
		t.Fatalf("observation = %+v, want requested and observed model", got.Observation)
	}
}
