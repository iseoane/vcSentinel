package main

import (
	"context"
	"errors"
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vcSentinel/internal/execution"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
)

// fakeInspector reproduces what a lock-free reader sees: a few observations
// of the tail caught mid-append and then the real state.
type fakeInspector struct {
	pendingTails    int
	calls           int
	finalState      agentrun.LifecycleState
	persistentError error
}

func (i *fakeInspector) Inspect(context.Context, agentrun.Identity) (execution.Inspection, error) {
	i.calls++
	if i.persistentError != nil {
		return execution.Inspection{}, i.persistentError
	}
	if i.pendingTails > 0 {
		i.pendingTails--
		return execution.Inspection{}, store.IncompleteEventTailError{RunID: "test-run"}
	}
	return execution.Inspection{Projection: store.RunProjection{State: i.finalState}}, nil
}

// TestObserveUntilSettledToleratesMidWriteTail covers a real flake: the
// full suite failed ~1 in 5 runs with "never reached a stable state
// after recover: store: incomplete final execution event".
//
// Store readers are deliberately lock-free (see the ReadAttemptOutcomes
// comment) while the writer does take the lock, so a reader can observe the
// last JSONL record mid-append. The store itself calls that state
// "recoverable": it resolves as soon as the write finishes. But
// observeUntilSettled exited with an error on ANY Inspect failure, so it
// turned a "not yet" into an infrastructure failure.
func TestObserveUntilSettledToleratesMidWriteTail(t *testing.T) {
	inspector := &fakeInspector{pendingTails: 3, finalState: agentrun.StateSucceeded}

	projection, err := observeUntilSettled(context.Background(), inspector, "test-run")

	if err != nil {
		t.Fatalf("err = %v; a mid-write tail is transient, not a failure", err)
	}
	if projection.State != agentrun.StateSucceeded {
		t.Errorf("state = %q, expected %q", projection.State, agentrun.StateSucceeded)
	}
	if inspector.calls != 4 {
		t.Errorf("calls = %d, expected 4 (three tails and the good observation)", inspector.calls)
	}
}

// TestObserveUntilSettledDoesNotSwallowATailThatNeverCompletes pins the other
// side: a genuinely truncated tail, left behind by a process that died
// mid-write, does NOT resolve by itself. Tolerating it without bound would
// hang the command forever.
func TestObserveUntilSettledDoesNotSwallowATailThatNeverCompletes(t *testing.T) {
	inspector := &fakeInspector{persistentError: store.IncompleteEventTailError{RunID: "test-run"}}

	_, err := observeUntilSettled(context.Background(), inspector, "test-run")

	if err == nil {
		t.Fatal("a tail that never completes must eventually be reported, not awaited forever")
	}
	if !errors.Is(err, store.ErrIncompleteEventTail) {
		t.Errorf("err = %v, expected it to preserve ErrIncompleteEventTail", err)
	}
}

// TestObserveUntilSettledDoesNotRetryOtherErrors: only the incomplete tail is
// transient. Any other failure exits immediately, as before.
func TestObserveUntilSettledDoesNotRetryOtherErrors(t *testing.T) {
	inspector := &fakeInspector{persistentError: errors.New("corrupt store")}

	if _, err := observeUntilSettled(context.Background(), inspector, "test-run"); err == nil {
		t.Fatal("expected an error")
	}
	if inspector.calls != 1 {
		t.Errorf("calls = %d, expected 1: a non-transient error is not retried", inspector.calls)
	}
}
