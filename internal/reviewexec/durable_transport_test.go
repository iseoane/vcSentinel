package reviewexec

import (
	"errors"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

func TestDurableTransportReturnsOutputOnSuccess(t *testing.T) {
	backing := store.NuevoStore(t.TempDir())
	transport := NewDurableTransport(backing, store.RunPolicy{ID: "policy:test"}, "sha123", []string{"x.go"})
	reviewer := &scriptedReviewer{name: "dimension-logic", output: "raw verdict"}

	output, err := transport.Run(reviewer, "quality/logic", "the prompt")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := "the prompt|sha123|x.go|raw verdict"
	if output != want {
		t.Fatalf("output = %q, want %q", output, want)
	}
}

func TestDurableTransportPreservesFailureEvidenceAsTerminalError(t *testing.T) {
	backing := store.NuevoStore(t.TempDir())
	transport := NewDurableTransport(backing, store.RunPolicy{ID: "policy:test"}, "sha456", nil)
	failing := &scriptedReviewer{name: "dimension-style", err: errors.New("provider exploded")}

	output, err := transport.Run(failing, "style/design", "prompt A")
	if output != "" || err == nil {
		t.Fatalf("Run() = %q, %v, want empty output with terminal error", output, err)
	}
	var terminal *TerminalError
	if !errors.As(err, &terminal) {
		t.Fatalf("err = %T(%v), want TerminalError", err, err)
	}
	if terminal.Class != agentrun.OutcomeFailure || terminal.Identity != "style/design" || terminal.Text != "provider exploded" {
		t.Fatalf("terminal = %+v, want failure class, identity, and exact provider text", terminal)
	}

	succeeding := &scriptedReviewer{name: "dimension-style", output: "ok"}
	if _, err := transport.Run(succeeding, "style/design", "prompt B"); err != nil {
		t.Fatalf("second Run with same identity key failed = %v, want nanosecond salt to prevent candidate collision", err)
	}
	if !strings.Contains(terminal.Error(), "provider exploded") {
		t.Fatalf("Error() = %q, want preserved text", terminal.Error())
	}
}
