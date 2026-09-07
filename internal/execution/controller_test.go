package execution

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

func TestStartPersistsAdmissionAndSuccessForFreshInspection(t *testing.T) {
	adapter := &scriptedAdapter{result: AdapterResult{Output: "admitted output"}}
	controller := NewControllerWithClock(store.NewStore(t.TempDir()), adapter, fixedClock())

	handle, err := controller.Start(context.Background(), testRequest("success"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	completion, err := handle.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if completion.State != agentrun.StateSucceeded || completion.Outcome != agentrun.OutcomeSuccess || completion.Output != "admitted output" {
		t.Fatalf("completion = %+v, want successful admitted output", completion)
	}
	if completion.OutputHash == "" {
		t.Fatal("successful output must have an admitted hash")
	}

	inspection, err := NewController(controller.store, nil).Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Projection.State != agentrun.StateSucceeded || len(inspection.Events) != 4 || len(inspection.Outcomes) != 1 {
		t.Fatalf("inspection = %+v, want four lifecycle events and one outcome", inspection)
	}
	wantStates := []agentrun.LifecycleState{
		agentrun.StateQueued, agentrun.StateAdmitted, agentrun.StateRunning, agentrun.StateSucceeded,
	}
	for index, want := range wantStates {
		if inspection.Events[index].To != want {
			t.Fatalf("event %d state = %q, want %q", index, inspection.Events[index].To, want)
		}
	}
	if inspection.Outcomes[0].Class != agentrun.OutcomeSuccess || inspection.Outcomes[0].OutputHash != completion.OutputHash {
		t.Fatalf("outcome = %+v, want success bound to completion hash", inspection.Outcomes[0])
	}
}

func TestAdapterFailuresRemainInspectableAfterWorkerCompletion(t *testing.T) {
	tests := []struct {
		name       string
		adapterErr error
		state      agentrun.LifecycleState
		class      agentrun.OutcomeClass
	}{
		{name: "failure", adapterErr: errors.New("provider rejected request"), state: agentrun.StateFailed, class: agentrun.OutcomeFailure},
		{name: "unavailable", adapterErr: NewAdapterError(agentrun.OutcomeUnavailable, errors.New("binary missing")), state: agentrun.StateUnavailable, class: agentrun.OutcomeUnavailable},
		{name: "timeout", adapterErr: context.DeadlineExceeded, state: agentrun.StateTimedOut, class: agentrun.OutcomeTimeout},
		{name: "process error", adapterErr: NewAdapterError(agentrun.OutcomeProcessError, errors.New("child exited 23")), state: agentrun.StateFailed, class: agentrun.OutcomeProcessError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := &scriptedAdapter{adapterErr: tt.adapterErr}
			controller := NewControllerWithClock(store.NewStore(t.TempDir()), adapter, fixedClock())
			handle, err := controller.Start(context.Background(), testRequest(tt.name), testPolicy())
			if err != nil {
				t.Fatal(err)
			}
			completion, err := handle.Wait(context.Background())
			if err != nil {
				t.Fatalf("Wait() error = %v", err)
			}
			if completion.State != tt.state || completion.Outcome != tt.class || !strings.Contains(completion.Error, errorText(tt.adapterErr)) {
				t.Fatalf("completion = %+v, want state %q, class %q, and evidence", completion, tt.state, tt.class)
			}
			inspection, err := NewController(controller.store, nil).Inspect(context.Background(), handle.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if inspection.Projection.State != tt.state || len(inspection.Outcomes) != 1 || inspection.Outcomes[0].Class != tt.class {
				t.Fatalf("inspection = %+v, want durable %q outcome", inspection, tt.class)
			}
			if inspection.Outcomes[0].Error != errorText(tt.adapterErr) {
				t.Fatalf("durable error = %q, want %q", inspection.Outcomes[0].Error, errorText(tt.adapterErr))
			}
		})
	}
}

func TestApplyAbortPersistsCooperativeCancellation(t *testing.T) {
	adapter := &blockingAdapter{started: make(chan struct{}), release: make(chan struct{})}
	controller := NewControllerWithClock(store.NewStore(t.TempDir()), adapter, fixedClock())
	handle, err := controller.Start(context.Background(), testRequest("abort"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	<-adapter.started
	result, err := controller.Apply(context.Background(), handle.RunID, ControlAction{Kind: ActionAbort})
	if err != nil || !result.Accepted {
		t.Fatalf("Apply(abort) = %+v, %v", result, err)
	}
	completion, err := handle.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if completion.State != agentrun.StateCanceled || completion.Outcome != agentrun.OutcomeCancellation {
		t.Fatalf("completion = %+v, want cooperative cancellation", completion)
	}
	inspection, err := NewController(controller.store, nil).Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Projection.State != agentrun.StateCanceled || inspection.Outcomes[0].Class != agentrun.OutcomeCancellation {
		t.Fatalf("inspection = %+v, want durable cancellation", inspection)
	}
}

func TestCallerContextCancellationDetachesObservation(t *testing.T) {
	adapter := &blockingAdapter{started: make(chan struct{}), release: make(chan struct{})}
	controller := NewControllerWithClock(store.NewStore(t.TempDir()), adapter, fixedClock())
	callerContext, cancelCaller := context.WithCancel(context.Background())
	handle, err := controller.Start(callerContext, testRequest("detach"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	<-adapter.started
	cancelCaller()
	if _, err := handle.Wait(callerContext); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() error = %v, want detached observation cancellation", err)
	}
	close(adapter.release)
	completion, err := handle.Wait(context.Background())
	if err != nil || completion.State != agentrun.StateSucceeded {
		t.Fatalf("detached run completion = %+v, %v", completion, err)
	}
}

func TestApplyResponseCreatesDurableChildInvocation(t *testing.T) {
	adapter := &responseAdapter{}
	controller := NewControllerWithClock(store.NewStore(t.TempDir()), adapter, fixedClock())
	handle, err := controller.Start(context.Background(), testRequest("response"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	waitForState(t, controller, handle.RunID, agentrun.StateAwaitingDecision)
	result, err := controller.Apply(context.Background(), handle.RunID, ControlAction{Kind: ActionRespond, Response: "continue"})
	if err != nil || !result.Accepted || result.InvocationID == handle.InvocationID {
		t.Fatalf("Apply(response) = %+v, %v, want a child invocation", result, err)
	}
	completion, err := handle.Wait(context.Background())
	if err != nil || completion.State != agentrun.StateSucceeded || completion.InvocationID != result.InvocationID {
		t.Fatalf("response completion = %+v, %v, want child success", completion, err)
	}
	inspection, err := NewController(controller.store, nil).Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(inspection.Responses) != 1 || inspection.Responses[0].ParentInvocationID != string(handle.InvocationID) || inspection.Responses[0].InvocationID != string(result.InvocationID) {
		t.Fatalf("responses = %+v, want parent/child lineage", inspection.Responses)
	}
	if len(inspection.Events) != 6 || inspection.Events[4].InvocationID != string(result.InvocationID) {
		t.Fatalf("events = %+v, want child running continuation", inspection.Events)
	}
	if len(adapter.responses) != 2 || adapter.responses[1] != "continue" {
		t.Fatalf("adapter responses = %q, want explicit control response", adapter.responses)
	}
}

func TestApplyAbortReconstructsAwaitingDecisionAfterControllerRestart(t *testing.T) {
	backingStore := store.NewStore(t.TempDir())
	firstController := NewControllerWithClock(backingStore, &responseAdapter{}, fixedClock())
	handle, err := firstController.Start(context.Background(), testRequest("restart-abort"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	waitForState(t, firstController, handle.RunID, agentrun.StateAwaitingDecision)

	restartedController := NewControllerWithClock(backingStore, nil, fixedClock())
	result, err := restartedController.Apply(context.Background(), handle.RunID, ControlAction{Kind: ActionAbort})
	if err != nil || !result.Accepted {
		t.Fatalf("restarted Apply(abort) = %+v, %v, want accepted persisted control action", result, err)
	}
	inspection, err := restartedController.Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Projection.State != agentrun.StateCanceled || len(inspection.Outcomes) != 1 || inspection.Outcomes[0].Class != agentrun.OutcomeCancellation {
		t.Fatalf("restarted inspection = %+v, want durable cancellation", inspection)
	}
}

func TestInspectionUsesEmbeddedTerminalOutcomeWhenLegacyOutcomeSurfaceIsCorrupt(t *testing.T) {
	storeRoot := t.TempDir()
	backingStore := store.NewStore(storeRoot)
	controller := NewControllerWithClock(backingStore, &scriptedAdapter{result: AdapterResult{Output: "durable output"}}, fixedClock())
	handle, err := controller.Start(context.Background(), testRequest("missing-outcome-surface"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	completion, err := handle.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(storeRoot, "vas-sentinel", "executions", "v1", string(handle.RunID))
	if err := os.WriteFile(filepath.Join(directory, "outcomes", string(handle.InvocationID)+".json"), []byte("not-json"), 0600); err != nil {
		t.Fatal(err)
	}

	inspection, err := NewController(backingStore, nil).Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Projection.State != agentrun.StateSucceeded || len(inspection.Outcomes) != 1 || inspection.Outcomes[0].Class != agentrun.OutcomeSuccess || inspection.Outcomes[0].OutputHash != completion.OutputHash {
		t.Fatalf("inspection = %+v, want event-authoritative terminal evidence", inspection)
	}
}

func TestAdapterProcessBoundaryFailureIsDurable(t *testing.T) {
	if os.Getenv("EXECUTION_ADAPTER_CHILD") == "1" {
		os.Exit(23)
	}
	controller := NewControllerWithClock(store.NewStore(t.TempDir()), processAdapter{}, fixedClock())
	handle, err := controller.Start(context.Background(), testRequest("process-boundary"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	completion, err := handle.Wait(context.Background())
	if err != nil || completion.Outcome != agentrun.OutcomeProcessError {
		t.Fatalf("process completion = %+v, %v, want process error", completion, err)
	}
	inspection, err := NewController(controller.store, nil).Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Projection.State != agentrun.StateFailed || len(inspection.Outcomes) != 1 || inspection.Outcomes[0].Class != agentrun.OutcomeProcessError {
		t.Fatalf("process inspection = %+v, want durable process failure", inspection)
	}
}

type scriptedAdapter struct {
	result     AdapterResult
	adapterErr error
}

func (a *scriptedAdapter) Execute(context.Context, agentrun.LogicalJob, agentrun.InvocationEnvelope, string) (AdapterResult, error) {
	return a.result, a.adapterErr
}

type blockingAdapter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (a *blockingAdapter) Execute(ctx context.Context, _ agentrun.LogicalJob, _ agentrun.InvocationEnvelope, _ string) (AdapterResult, error) {
	a.once.Do(func() { close(a.started) })
	select {
	case <-a.release:
		return AdapterResult{Output: "completed after observation detached"}, nil
	case <-ctx.Done():
		return AdapterResult{}, ctx.Err()
	}
}

type responseAdapter struct {
	mu        sync.Mutex
	responses []string
}

func (a *responseAdapter) Execute(_ context.Context, _ agentrun.LogicalJob, _ agentrun.InvocationEnvelope, response string) (AdapterResult, error) {
	a.mu.Lock()
	a.responses = append(a.responses, response)
	a.mu.Unlock()
	if response == "" {
		return AdapterResult{AwaitingDecision: true}, nil
	}
	return AdapterResult{Output: "response accepted"}, nil
}

type processAdapter struct{}

func (processAdapter) Execute(context.Context, agentrun.LogicalJob, agentrun.InvocationEnvelope, string) (AdapterResult, error) {
	command := exec.Command(os.Args[0], "-test.run", "^TestAdapterProcessBoundaryFailureIsDurable$")
	command.Env = append(os.Environ(), "EXECUTION_ADAPTER_CHILD=1")
	if err := command.Run(); err != nil {
		return AdapterResult{}, NewAdapterError(agentrun.OutcomeProcessError, fmt.Errorf("adapter process failed: %w", err))
	}
	return AdapterResult{}, nil
}

func testRequest(name string) agentrun.RunRequest {
	return agentrun.NewRunRequest(agentrun.Candidate("candidate:"+name), agentrun.Prompt("prompt:"+name), nil)
}

func testPolicy() store.RunPolicy { return store.RunPolicy{ID: "policy:test"} }

func fixedClock() func() time.Time {
	return func() time.Time { return time.Unix(1700000000, 0).UTC() }
}

func waitForState(t *testing.T, controller *Controller, runID agentrun.Identity, want agentrun.LifecycleState) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		inspection, err := controller.Inspect(context.Background(), runID)
		if err == nil && inspection.Projection.State == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	inspection, err := controller.Inspect(context.Background(), runID)
	t.Fatalf("state = %q, error = %v, want %q; inspection = %+v", inspection.Projection.State, err, want, inspection)
}

// TestFinishReconcilesALostTerminalAppendRace covers the open criterion of
// card 18: "A controller that loses a terminal append race must reconcile the
// durable terminal event instead of reporting an infrastructure error".
//
// This is the worst way to lose work in the system: the provider ALREADY
// answered, the tokens were spent and the response existed, but another
// writer settled the run first and the worker throws everything away with a
// revision conflict the user sees as `store: expected execution revision N,
// found M`. The foreign result is the authority —already written and
// immutable— but that means the run is SETTLED, not that the execution failed.
func TestFinishReconcilesALostTerminalAppendRace(t *testing.T) {
	backing := store.NewStore(t.TempDir())
	adapter := &blockingAdapter{started: make(chan struct{}), release: make(chan struct{})}
	controller := NewControllerWithClock(backing, adapter, fixedClock())

	handle, err := controller.Start(context.Background(), testRequest("race"), testPolicy())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-adapter.started

	// Another writer settles the run while the worker is still executing:
	// that is what the daemon shutdown sweep used to do, and any process with
	// store access can do it.
	other := NewControllerWithClock(backing, &scriptedAdapter{}, fixedClock())
	settled, err := other.OrphanRun(handle.RunID, "settled by another writer during execution")
	if err != nil || !settled {
		t.Fatalf("could not settle the run from outside: settled=%v err=%v", settled, err)
	}

	close(adapter.release)

	completion, waitErr := handle.Wait(context.Background())
	if waitErr != nil {
		t.Fatalf("Wait = %v; losing the settlement race must leave the run SETTLED, not failed: the worker must reconcile the durable terminal event", waitErr)
	}
	if completion.State.TerminalClass() == agentrun.TerminalNone {
		t.Errorf("state = %q, want the terminal class left by the other writer", completion.State)
	}
}
