package acpadapter

import (
	"strings"
	"testing"
)

func TestParseStreamNormalizesObservedUsage(t *testing.T) {
	stream := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"result":{"configOptions":[{"id":"model","value":"observed/model"}]}}`,
		`{"jsonrpc":"2.0","id":2,"result":{"stopReason":"end_turn","usage":{"inputTokens":0,"outputTokens":3,"totalTokens":3}}}`,
	}, "\n") + "\n"
	got := ParseStream(strings.NewReader(stream), DefaultLineCapBytes)
	if got.Usage == nil {
		t.Fatal("Usage = nil, want known terminal usage")
	}
	if got.Usage.InputTokens == nil || *got.Usage.InputTokens != 0 {
		t.Fatalf("InputTokens = %#v, want observed zero", got.Usage.InputTokens)
	}
	if got.Usage.OutputTokens == nil || *got.Usage.OutputTokens != 3 {
		t.Fatalf("OutputTokens = %#v, want 3", got.Usage.OutputTokens)
	}
	if got.Usage.TotalTokens == nil || *got.Usage.TotalTokens != 3 {
		t.Fatalf("TotalTokens = %#v, want 3", got.Usage.TotalTokens)
	}
}

func TestResultAdapterObservationSeparatesRequestedAndObservedIdentity(t *testing.T) {
	input, output := int64(1), int64(2)
	result := Result{
		ObservedModel:  "wire/model",
		RequestedModel: "configured/model",
		ObservedEffort: "high",
		StopReason:     "end_turn",
		Enforcement:    EnforcementNone,
		Usage:          &Usage{InputTokens: &input, OutputTokens: &output},
	}
	observation := result.AdapterObservation()
	if observation.Model != "wire/model" {
		t.Fatalf("observed model = %q, want wire/model", observation.Model)
	}
	if observation.RequestedModel != "configured/model" {
		t.Fatalf("requested model = %q, want configured/model", observation.RequestedModel)
	}
	if observation.Effort != "high" {
		t.Fatalf("observed effort = %q, want high", observation.Effort)
	}
	if observation.Usage == nil || observation.Usage.InputTokens == nil || *observation.Usage.InputTokens != 1 {
		t.Fatalf("usage = %#v, want input token evidence", observation.Usage)
	}
}
