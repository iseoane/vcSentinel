package agentadapter

import (
	"errors"
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/config"
)

// stubAdapter simulates a chain child that always answers or always fails.
type stubAdapter struct {
	name   string
	model  string
	effort string
	fails  bool
}

func (a *stubAdapter) RunPrompt(string) (string, error) {
	if a.fails {
		return "", errors.New("not available")
	}
	return "ok", nil
}

func (a *stubAdapter) GetCommitMessage([]string, string, int) (string, error) {
	if a.fails {
		return "", errors.New("not available")
	}
	return "chore: something", nil
}

func (a *stubAdapter) EffectiveAgent() (EffectiveAgent, bool) {
	return EffectiveAgent{Binary: a.name, Model: a.model, Effort: a.effort}, true
}

func (a *stubAdapter) String() string { return a.name }

// TestChainRecordsRespondingAdapter covers the T0.2 acceptance: if the first
// fails and the second answers, the recorded author must be the second one.
func TestChainRecordsRespondingAdapter(t *testing.T) {
	chain := &AdapterChain{adapters: []completeAdapter{
		&stubAdapter{name: "claude", model: "opus", fails: true},
		&stubAdapter{name: "opencode", model: "sonnet", effort: "high"},
	}}

	if _, err := chain.RunPrompt("hola"); err != nil {
		t.Fatalf("the chain should have answered with the second one: %v", err)
	}

	effective, ok := chain.EffectiveAgent()
	if !ok {
		t.Fatal("the chain reported no effective agent after answering")
	}
	if effective.Binary != "opencode" {
		t.Errorf("binary = %q, want opencode (the one that answered)", effective.Binary)
	}
	if effective.Model != "sonnet" || effective.Effort != "high" {
		t.Errorf("model/effort = %q/%q, want sonnet/high", effective.Model, effective.Effort)
	}
}

// TestChainWithoutAnswerReportsNoAgent: if nobody answered, there is no author
// to record. Inventing one would be the same defect T0.2 corrects.
func TestChainWithoutAnswerReportsNoAgent(t *testing.T) {
	chain := &AdapterChain{adapters: []completeAdapter{
		&stubAdapter{name: "claude", fails: true},
		&stubAdapter{name: "opencode", fails: true},
	}}

	if _, err := chain.RunPrompt("hola"); err == nil {
		t.Fatal("an error was expected: no adapter answers")
	}
	if _, ok := chain.EffectiveAgent(); ok {
		t.Error("reported an effective agent even though none answered")
	}
}

// TestChainUpdatesAgentPerRequest: the fallback is per request and never
// cached, so the recorded author must track the latest one.
func TestChainUpdatesAgentPerRequest(t *testing.T) {
	first := &stubAdapter{name: "claude", model: "opus"}
	second := &stubAdapter{name: "opencode", model: "sonnet"}
	chain := &AdapterChain{adapters: []completeAdapter{first, second}}

	if _, err := chain.RunPrompt("one"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if effective, _ := chain.EffectiveAgent(); effective.Binary != "claude" {
		t.Fatalf("first request: binary = %q, want claude", effective.Binary)
	}

	first.fails = true
	if _, err := chain.RunPrompt("dos"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if effective, _ := chain.EffectiveAgent(); effective.Binary != "opencode" {
		t.Errorf("second request: binary = %q, want opencode", effective.Binary)
	}
}

// TestCLIAdapterReportsItsConfiguration: a CLIAdapter's effective agent is its
// binary with the model and effort it was invoked with.
func TestCLIAdapterReportsItsConfiguration(t *testing.T) {
	adapter := &CLIAdapter{
		BinaryName: "claude",
		Config:     config.AgentConfig{Model: "opus", ReasoningEffort: "xhigh"},
	}
	effective, ok := adapter.EffectiveAgent()
	if !ok {
		t.Fatal("the CLIAdapter reported no effective agent")
	}
	if effective.Binary != "claude" || effective.Model != "opus" || effective.Effort != "xhigh" {
		t.Errorf("effective agent = %+v", effective)
	}
}
