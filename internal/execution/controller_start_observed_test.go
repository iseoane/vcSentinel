// Ordering-contract tests for Controller.StartObserved: the additive seam that
// lets an admitted-run observer fire strictly AFTER durable admission (the run
// is created, its lifecycle events are persisted, and the in-memory handle is
// registered) and strictly BEFORE the detached worker goroutine can enter the
// provider adapter. Existing Start behavior delegates to StartObserved with a
// nil callback and is exercised unchanged by the rest of this package's suite.
package execution

import (
	"context"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// entryBarrierAdapter blocks the worker INSIDE Execute on an unbuffered send
// as its very first action, so a non-blocking probe from the observer proves
// deterministically whether the worker has been released into the adapter.
type entryBarrierAdapter struct {
	entered chan struct{}
}

func (a *entryBarrierAdapter) Execute(context.Context, agentrun.LogicalJob, agentrun.InvocationEnvelope, string) (AdapterResult, error) {
	a.entered <- struct{}{}
	return AdapterResult{Output: "completed after observation"}, nil
}

func TestStartObservedCallbackRunsAfterAdmissionBeforeWorker(t *testing.T) {
	barrier := &entryBarrierAdapter{entered: make(chan struct{})}
	controller := NewControllerWithClock(store.NuevoStore(t.TempDir()), barrier, fixedClock())

	var observedHandle Handle
	handle, err := controller.StartObserved(context.Background(), testRequest("ordering"), testPolicy(),
		func(admitted Handle) {
			observedHandle = admitted
			// Durable admission already happened: the run is fully inspectable
			// from the store alone at callback time, in Running state.
			inspection, inspectErr := NewController(controller.store, nil).Inspect(context.Background(), admitted.RunID)
			if inspectErr != nil {
				t.Errorf("callback run %s not inspectable after admission: %v", admitted.RunID, inspectErr)
				return
			}
			if inspection.Projection.State != agentrun.StateRunning {
				t.Errorf("callback projection state = %q, want %q (admission persisted before observation)", inspection.Projection.State, agentrun.StateRunning)
			}
			// And the provider worker cannot have been entered yet: the seam
			// guarantees observation precedes the worker launch.
			select {
			case <-barrier.entered:
				t.Error("worker entered the adapter before the admitted-run callback fired")
			default:
			}
		})
	if err != nil {
		t.Fatal(err)
	}
	if !announced(observedHandle, handle) {
		t.Fatalf("callback received %+v, want the same handle Start returned (%+v)", observedHandle, handle)
	}

	// Release the blocked worker so the run settles cleanly.
	<-barrier.entered
	completion, err := handle.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if completion.State != agentrun.StateSucceeded || completion.Output != "completed after observation" {
		t.Fatalf("completion = %+v, want a normal successful settlement after observation", completion)
	}
}

func announced(observed, returned Handle) bool {
	return observed.RunID == returned.RunID &&
		observed.InvocationID == returned.InvocationID &&
		observed.JobID == returned.JobID
}

// TestStartObservedNilCallbackMatchesStart pins the delegation contract: a nil
// observer must behave exactly like Start — admit, execute, settle, inspect.
func TestStartObservedNilCallbackMatchesStart(t *testing.T) {
	adapter := &scriptedAdapter{result: AdapterResult{Output: "plain start"}}
	controller := NewControllerWithClock(store.NuevoStore(t.TempDir()), adapter, fixedClock())

	handle, err := controller.StartObserved(context.Background(), testRequest("nil-callback"), testPolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}
	completion, err := handle.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if completion.State != agentrun.StateSucceeded || completion.Output != "plain start" {
		t.Fatalf("completion = %+v, want identical Start behavior with a nil observer", completion)
	}
	inspection, err := NewController(controller.store, nil).Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Projection.State != agentrun.StateSucceeded || len(inspection.Outcomes) != 1 {
		t.Fatalf("inspection = %+v, want the standard durable success shape", inspection)
	}
}
