package reviewexec

import (
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/execution"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
)

// TestWithCancellationEscalationOptionArmsDisabledMode pins the option seam
// slice 3 wires from yaml: the constructor default keeps bounded escalation,
// an explicit false policy restricts every kill to the direct child.
func TestWithCancellationEscalationOptionArmsDisabledMode(t *testing.T) {
	disabled := NewDurableTransport(nil, store.RunPolicy{ID: "policy:test"}, "sha", nil,
		WithCancellationEscalation(execution.EscalationPolicy{Disabled: true}))
	if !disabled.escalationPolicy.Disabled {
		t.Fatal("WithCancellationEscalation(Disabled: true) did not disarm whole-tree escalation")
	}
	if disabled.escalationPolicy.Grace != 0 || disabled.escalationPolicy.FinalBudget != 0 {
		t.Fatal("Disabled policy must keep zero budgets so the controller normalizes its defaults")
	}

	def := NewDurableTransport(nil, store.RunPolicy{ID: "policy:test"}, "sha", nil)
	if def.escalationPolicy.Disabled {
		t.Fatal("the zero-value escalation policy must stay enabled (default-on rollback seam)")
	}
}
