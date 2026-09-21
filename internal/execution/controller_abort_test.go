package execution

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
)

// contextIgnoringAdapter simulates the legacy reviewer contract: it blocks
// until released no matter what its context does, then returns the scripted
// result. It reproduces the JD-A1 race where a late provider answer raced the
// intended cancellation settlement.
type contextIgnoringAdapter struct {
	started      chan struct{}
	release      chan struct{}
	returned     chan struct{}
	returnedOnce sync.Once
	result       AdapterResult
	err          error
	once         sync.Once
}

func (a *contextIgnoringAdapter) Execute(_ context.Context, _ agentrun.LogicalJob, _ agentrun.InvocationEnvelope, _ string) (AdapterResult, error) {
	a.once.Do(func() { close(a.started) })
	<-a.release
	defer a.returnedOnce.Do(func() { close(a.returned) })
	return a.result, a.err
}

// TestApplyAbortRunningSettlesCanceledBeforeAnyLateAdapterResult is the JD-A1
// regression guard: aborting a running review must settle the durable record
// canceled promptly WITHOUT waiting for the adapter release, and a late
// adapter result must never author a different terminal outcome afterwards.
func TestApplyAbortRunningSettlesCanceledBeforeAnyLateAdapterResult(t *testing.T) {
	tests := []struct {
		name          string
		releaseResult AdapterResult
		releaseErr    error
	}{
		{name: "late success", releaseResult: AdapterResult{Output: "late success output"}},
		{name: "late failure", releaseErr: errors.New("late provider failure")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := &contextIgnoringAdapter{started: make(chan struct{}), release: make(chan struct{}), returned: make(chan struct{}), result: tt.releaseResult, err: tt.releaseErr}
			controller := NewControllerWithClock(store.NewStore(t.TempDir()), adapter, fixedClock())
			handle, err := controller.Start(context.Background(), testRequest("late-"+tt.name), testPolicy())
			if err != nil {
				t.Fatal(err)
			}
			<-adapter.started

			result, err := controller.Apply(context.Background(), handle.RunID, ControlAction{Kind: ActionAbort})
			if err != nil || !result.Accepted {
				t.Fatalf("Apply(abort) = %+v, %v; want accepted controller-authored settlement", result, err)
			}

			waitContext, cancelWait := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancelWait()
			completion, err := handle.Wait(waitContext)
			if err != nil {
				t.Fatalf("Wait() after abort = %v; settlement must not wait for the adapter release", err)
			}
			if completion.State != agentrun.StateCanceled || completion.Outcome != agentrun.OutcomeCancellation ||
				!strings.Contains(completion.Error, "aborted while running") {
				t.Fatalf("completion = %+v, want prompt canceled settlement authored by the controller", completion)
			}

			// The blocked adapter finally returns its late result. The test
			// waits for that late result to reach the controller (Execute
			// returned and finish() ran its no-op path) before asserting the
			// settlement stayed canceled.
			close(adapter.release)
			select {
			case <-adapter.returned:
			case <-time.After(2 * time.Second):
				t.Fatal("late adapter result never reached the controller after release")
			}

			inspection, err := NewController(controller.store, nil).Inspect(context.Background(), handle.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if inspection.Projection.State != agentrun.StateCanceled {
				t.Fatalf("projection state = %q, want canceled after the late adapter result", inspection.Projection.State)
			}
			if len(inspection.Outcomes) != 1 || inspection.Outcomes[0].Class != agentrun.OutcomeCancellation ||
				inspection.Outcomes[0].Error != "aborted while running" {
				t.Fatalf("outcomes = %+v, want exactly one cancellation outcome authored by the controller", inspection.Outcomes)
			}
			last := inspection.Events[len(inspection.Events)-1]
			if last.From != agentrun.StateRunning || last.To != agentrun.StateCanceled || last.Decision != agentrun.DecisionAbort {
				t.Fatalf("terminal event = %s->%s/%s, want running->canceled by abort decision", last.From, last.To, last.Decision)
			}
			for _, frame := range inspection.Events {
				if frame.To == agentrun.StateSucceeded || frame.To == agentrun.StateFailed {
					t.Fatalf("event %d settled %q; a late adapter result must never re-settle the attempt", frame.Sequence, frame.To)
				}
			}
		})
	}
}

// TestApplyDoubleAbortIsIdempotent proves the second Apply(ActionAbort) appends
// no duplicate evidence event and returns accepted instead of an error.
func TestApplyDoubleAbortIsIdempotent(t *testing.T) {
	adapter := &contextIgnoringAdapter{started: make(chan struct{}), release: make(chan struct{}), returned: make(chan struct{})}
	controller := NewControllerWithClock(store.NewStore(t.TempDir()), adapter, fixedClock())
	handle, err := controller.Start(context.Background(), testRequest("double-abort"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	<-adapter.started

	first, err := controller.Apply(context.Background(), handle.RunID, ControlAction{Kind: ActionAbort})
	if err != nil || !first.Accepted {
		t.Fatalf("first Apply(abort) = %+v, %v", first, err)
	}
	waitContext, cancelWait := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelWait()
	if _, err := handle.Wait(waitContext); err != nil {
		t.Fatal(err)
	}

	before, err := NewController(controller.store, nil).Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}

	second, err := controller.Apply(context.Background(), handle.RunID, ControlAction{Kind: ActionAbort})
	if err != nil || !second.Accepted {
		t.Fatalf("second Apply(abort) = %+v, %v; want idempotent acceptance without error", second, err)
	}

	after, err := NewController(controller.store, nil).Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Events) != len(before.Events) || len(after.Outcomes) != len(before.Outcomes) {
		t.Fatalf("events/outcomes = %d/%d, want unchanged %d/%d after double abort",
			len(after.Events), len(after.Outcomes), len(before.Events), len(before.Outcomes))
	}
	if after.Projection.State != agentrun.StateCanceled {
		t.Fatalf("projection state = %q, want still canceled", after.Projection.State)
	}
	close(adapter.release)
}
