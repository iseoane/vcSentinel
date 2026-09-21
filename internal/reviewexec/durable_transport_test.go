package reviewexec

import (
	"errors"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
)

func TestDurableTransportReturnsOutputOnSuccess(t *testing.T) {
	backing := store.NewStore(t.TempDir())
	transport := NewDurableTransport(backing, store.RunPolicy{ID: "policy:test"}, "sha123", []string{"x.go"})
	reviewer := &scriptedReviewer{name: "dimension-logic", output: "raw verdict"}

	output, evidence, err := transport.Run(reviewer, "quality/logic", "the prompt")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := "the prompt|sha123|x.go|raw verdict"
	if output != want {
		t.Fatalf("output = %q, want %q", output, want)
	}
	outcomes, err := backing.ReadAttemptOutcomes(evidence.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var durable *store.AttemptOutcome
	for i := range outcomes {
		if outcomes[i].InvocationID == evidence.InvocationID {
			durable = &outcomes[i]
			break
		}
	}
	if durable == nil {
		t.Fatalf("durable outcomes = %+v, want one record matching evidence invocation %s", outcomes, evidence.InvocationID)
	}
	wantEvidence := evidenceFromOutcome(durable)
	if evidence != wantEvidence {
		t.Fatalf("evidence = %+v, want %+v copied from the durable outcome", evidence, wantEvidence)
	}
	if durable.Class != agentrun.OutcomeSuccess || durable.OutputHash == "" {
		t.Fatalf("durable outcome = %+v, want recorded success with an output hash", durable)
	}
}

func TestDurableTransportPreservesFailureEvidenceAsTerminalError(t *testing.T) {
	backing := store.NewStore(t.TempDir())
	transport := NewDurableTransport(backing, store.RunPolicy{ID: "policy:test"}, "sha456", nil)
	failing := &scriptedReviewer{name: "dimension-style", err: errors.New("provider exploded")}

	output, evidence, err := transport.Run(failing, "style/design", "prompt A")
	if output != "" || evidence != (Evidence{}) || err == nil {
		t.Fatalf("Run() = %q, %+v, %v, want empty output and evidence with terminal error", output, evidence, err)
	}
	var terminal *TerminalError
	if !errors.As(err, &terminal) {
		t.Fatalf("err = %T(%v), want TerminalError", err, err)
	}
	if terminal.Class != agentrun.OutcomeFailure || terminal.Identity != "style/design" || terminal.Text != "provider exploded" {
		t.Fatalf("terminal = %+v, want failure class, identity, and exact provider text", terminal)
	}

	succeeding := &scriptedReviewer{name: "dimension-style", output: "ok"}
	if _, _, err := transport.Run(succeeding, "style/design", "prompt B"); err != nil {
		t.Fatalf("second Run with same identity key failed = %v, want nanosecond salt to prevent candidate collision", err)
	}
	if !strings.Contains(terminal.Error(), "provider exploded") {
		t.Fatalf("Error() = %q, want preserved text", terminal.Error())
	}
}
