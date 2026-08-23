package process

import (
	"time"
)

// EscalationOutcome classifies how one bounded escalation resolved. It is the
// honest result vocabulary between cancellation and reap confirmation:
// cooperative exit needs no termination evidence, reaped-after-termination
// records both transitions, and orphaned means exit could not be confirmed.
type EscalationOutcome int

const (
	// OutcomeCooperative: the tree exited inside the grace budget without
	// any termination signal being issued.
	OutcomeCooperative EscalationOutcome = iota
	// OutcomeReaped: the grace budget expired, Terminate ran, and exit was
	// confirmed within the final budget.
	OutcomeReaped
	// OutcomeOrphaned: even after Terminate, exit could not be confirmed
	// within the final budget; the attempt must settle as orphaned.
	OutcomeOrphaned
)

// EscalationHooks receive each durable-evidence transition exactly once. The
// linear escalation flow guarantees single invocation: hooks are called only
// from RunEscalation's own goroutine and never repeated on any path.
type EscalationHooks struct {
	// OnTerminateAttempted fires immediately before Terminate, once per
	// attempt, only when the grace budget expired first.
	OnTerminateAttempted func(at time.Time)
	// OnReaped fires once when exit is confirmed after termination.
	OnReaped func(at time.Time)
}

// Escalator is one bounded escalation run. After is injectable so unit tests
// drive clock expiry deterministically; nil selects time.After.
type Escalator struct {
	// Exit is closed by the spawner once the direct child's exit is
	// confirmed (Tree.MarkExited).
	Exit <-chan struct{}
	// Grace is the cooperative window after cancellation before escalation.
	Grace time.Duration
	// FinalBudget bounds confirmation of exit after hard termination.
	FinalBudget time.Duration
	// Terminate performs the hard whole-tree kill.
	Terminate func() error
	// After produces expiry channels; injectable for tests.
	After func(time.Duration) <-chan time.Time
	// Hooks receive exactly-once transition notifications.
	Hooks EscalationHooks
}

// RunEscalation executes the bounded escalation state machine to completion:
//
//	cancel -> [grace] --expired--> terminate-attempted hook -> Terminate
//	                                 |                              |
//	                   exit confirmed|                    exit confirmed or
//	                   (cooperative) |                     final budget expires
//	                                 v                              v
//	                        OutcomeCooperative          reaped hook / OutcomeOrphaned
func RunEscalation(e Escalator) EscalationOutcome {
	after := e.After
	if after == nil {
		after = time.After
	}
	select {
	case <-e.Exit:
		return OutcomeCooperative
	case <-after(e.Grace):
	}
	if e.Hooks.OnTerminateAttempted != nil {
		e.Hooks.OnTerminateAttempted(time.Now())
	}
	if e.Terminate != nil {
		_ = e.Terminate()
	}
	select {
	case <-e.Exit:
		if e.Hooks.OnReaped != nil {
			e.Hooks.OnReaped(time.Now())
		}
		return OutcomeReaped
	case <-after(e.FinalBudget):
		return OutcomeOrphaned
	}
}
