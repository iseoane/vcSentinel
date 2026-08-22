package execution

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

type failThenSucceedAdapter struct {
	mu       sync.Mutex
	failures int
	calls    int
	attempts []uint32
}

func (a *failThenSucceedAdapter) Execute(_ context.Context, _ agentrun.LogicalJob, invocation agentrun.InvocationEnvelope, _ string) (AdapterResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	call := a.calls
	a.calls++
	a.attempts = append(a.attempts, invocation.Attempt())
	if call < a.failures {
		return AdapterResult{}, errors.New("attempt failed")
	}
	return AdapterResult{Output: fmt.Sprintf("attempt %d output", invocation.Attempt())}, nil
}

type failThenBlockAdapter struct {
	mu      sync.Mutex
	calls   int
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (a *failThenBlockAdapter) Execute(ctx context.Context, _ agentrun.LogicalJob, _ agentrun.InvocationEnvelope, _ string) (AdapterResult, error) {
	a.mu.Lock()
	call := a.calls
	a.calls++
	a.mu.Unlock()
	if call == 0 {
		return AdapterResult{}, errors.New("first attempt failed")
	}
	a.once.Do(func() { close(a.started) })
	select {
	case <-a.release:
		return AdapterResult{Output: "retried output"}, nil
	case <-ctx.Done():
		return AdapterResult{}, ctx.Err()
	}
}

func TestRetryExtendsOriginalRunWithDurableAttemptLineage(t *testing.T) {
	adapter := &failThenSucceedAdapter{failures: 1}
	controller := NewControllerWithClock(store.NuevoStore(t.TempDir()), adapter, fixedClock())
	handle, err := controller.Start(context.Background(), testRequest("retry"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	completion, err := handle.Wait(context.Background())
	if err != nil || completion.State != agentrun.StateFailed || completion.Outcome != agentrun.OutcomeFailure {
		t.Fatalf("first attempt = %+v, %v; want failure", completion, err)
	}

	retried, err := controller.Retry(context.Background(), handle.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if retried.RunID != handle.RunID || retried.JobID != handle.JobID {
		t.Fatalf("retry identity = %s/%s, want the original run %s/%s", retried.RunID, retried.JobID, handle.RunID, handle.JobID)
	}
	if retried.InvocationID == handle.InvocationID {
		t.Fatal("a retry must launch a new attempt envelope")
	}
	retryCompletion, err := retried.Wait(context.Background())
	if err != nil || retryCompletion.State != agentrun.StateSucceeded || retryCompletion.InvocationID != retried.InvocationID {
		t.Fatalf("retry completion = %+v, %v; want success on the new attempt", retryCompletion, err)
	}
	adapter.mu.Lock()
	grew := fmt.Sprint(adapter.attempts) == "[1 2]"
	adapter.mu.Unlock()
	if !grew {
		t.Fatal("the retried invocation must grow the attempt count of the same run")
	}

	inspection, err := NewController(controller.store, nil).Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Projection.State != agentrun.StateSucceeded || len(inspection.Events) != 6 {
		t.Fatalf("inspection = %+v, want six events ending succeeded", inspection)
	}
	retryEvent := inspection.Events[4]
	if retryEvent.Decision != agentrun.DecisionRetry || retryEvent.From != agentrun.StateFailed ||
		retryEvent.To != agentrun.StateRunning || retryEvent.InvocationID != string(retried.InvocationID) {
		t.Fatalf("retry event = %+v, want a failed-to-running relaunch bound to the new attempt", retryEvent)
	}
	if len(inspection.Outcomes) != 2 ||
		inspection.Outcomes[0].Class != agentrun.OutcomeFailure || inspection.Outcomes[0].InvocationID != string(handle.InvocationID) ||
		inspection.Outcomes[1].Class != agentrun.OutcomeSuccess {
		t.Fatalf("outcomes = %+v, want the preserved failure followed by the retry success", inspection.Outcomes)
	}

	if _, err := controller.Retry(context.Background(), handle.RunID, 0); !errors.Is(err, ErrRunNotRetryable) {
		t.Fatalf("retrying a succeeded run error = %v, want ErrRunNotRetryable", err)
	}
}

func TestRetryRejectsRunsOutsideRetryableTerminalEvidence(t *testing.T) {
	tests := []struct {
		name      string
		adapter   func() Adapter
		run       func(t *testing.T, controller *Controller) agentrun.Identity
		wantError error
	}{
		{
			name:    "active run",
			adapter: func() Adapter { return &blockingAdapter{started: make(chan struct{}), release: make(chan struct{})} },
			run: func(t *testing.T, controller *Controller) agentrun.Identity {
				t.Helper()
				handle, err := controller.Start(context.Background(), testRequest("active"), testPolicy())
				if err != nil {
					t.Fatal(err)
				}
				blocker := controller.adapter.(*blockingAdapter)
				<-blocker.started
				t.Cleanup(func() {
					close(blocker.release)
					_, _ = handle.Wait(context.Background())
				})
				return handle.RunID
			},
			wantError: ErrRunNotActive,
		},
		{
			name:    "awaiting decision run",
			adapter: func() Adapter { return &responseAdapter{} },
			run: func(t *testing.T, controller *Controller) agentrun.Identity {
				t.Helper()
				handle, err := controller.Start(context.Background(), testRequest("awaiting"), testPolicy())
				if err != nil {
					t.Fatal(err)
				}
				waitForState(t, controller, handle.RunID, agentrun.StateAwaitingDecision)
				return handle.RunID
			},
			wantError: ErrRunNotActive,
		},
		{
			name:    "succeeded run",
			adapter: func() Adapter { return &scriptedAdapter{result: AdapterResult{Output: "done"}} },
			run: func(t *testing.T, controller *Controller) agentrun.Identity {
				t.Helper()
				handle, err := controller.Start(context.Background(), testRequest("succeeded"), testPolicy())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := handle.Wait(context.Background()); err != nil {
					t.Fatal(err)
				}
				return handle.RunID
			},
			wantError: ErrRunNotRetryable,
		},
		{
			name: "unavailable run",
			adapter: func() Adapter {
				return &scriptedAdapter{adapterErr: NewAdapterError(agentrun.OutcomeUnavailable, errors.New("binary missing"))}
			},
			run: func(t *testing.T, controller *Controller) agentrun.Identity {
				t.Helper()
				handle, err := controller.Start(context.Background(), testRequest("unavailable"), testPolicy())
				if err != nil {
					t.Fatal(err)
				}
				completion, err := handle.Wait(context.Background())
				if err != nil || completion.State != agentrun.StateUnavailable {
					t.Fatalf("setup completion = %+v, %v; want unavailable", completion, err)
				}
				return handle.RunID
			},
			wantError: ErrRunNotRetryable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller := NewControllerWithClock(store.NuevoStore(t.TempDir()), tt.adapter(), fixedClock())
			runID := tt.run(t, controller)
			if _, err := controller.Retry(context.Background(), runID, 0); !errors.Is(err, tt.wantError) {
				t.Fatalf("Retry() error = %v, want %v", err, tt.wantError)
			}
		})
	}
}

func TestRetryingAnAlreadyRelaunchedRunFailsExplicitly(t *testing.T) {
	adapter := &failThenBlockAdapter{started: make(chan struct{}), release: make(chan struct{})}
	controller := NewControllerWithClock(store.NuevoStore(t.TempDir()), adapter, fixedClock())
	handle, err := controller.Start(context.Background(), testRequest("double-retry"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if completion, err := handle.Wait(context.Background()); err != nil || completion.State != agentrun.StateFailed {
		t.Fatalf("first attempt = %+v, %v; want failure", completion, err)
	}
	retried, err := controller.Retry(context.Background(), handle.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	<-adapter.started
	before, err := controller.Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := controller.Retry(context.Background(), handle.RunID, 0); !errors.Is(err, ErrRunNotActive) {
		t.Fatalf("duplicate retry error = %v, want ErrRunNotActive", err)
	}
	after, err := controller.Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Events) != len(before.Events) {
		t.Fatalf("duplicate retry appended events (%d -> %d); it must refuse without double-applying", len(before.Events), len(after.Events))
	}

	close(adapter.release)
	completion, err := retried.Wait(context.Background())
	if err != nil || completion.State != agentrun.StateSucceeded {
		t.Fatalf("released retry completion = %+v, %v; want success", completion, err)
	}
}

func TestFreshControllerRetriesARunItNeverStarted(t *testing.T) {
	backingStore := store.NuevoStore(t.TempDir())
	first := NewControllerWithClock(backingStore, &scriptedAdapter{adapterErr: errors.New("worker crashed")}, fixedClock())
	handle, err := first.Start(context.Background(), testRequest("cross-process"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if completion, err := handle.Wait(context.Background()); err != nil || completion.State != agentrun.StateFailed {
		t.Fatalf("first attempt = %+v, %v; want failure", completion, err)
	}

	fresh := NewControllerWithClock(backingStore, &scriptedAdapter{result: AdapterResult{Output: "fresh output"}}, fixedClock())
	retried, err := fresh.Retry(context.Background(), handle.RunID, 0)
	if err != nil {
		t.Fatalf("fresh-controller retry error = %v, want reconstructed relaunch", err)
	}
	if retried.RunID != handle.RunID || retried.JobID != handle.JobID {
		t.Fatalf("reconstructed retry identity = %s/%s, want the original run %s/%s", retried.RunID, retried.JobID, handle.RunID, handle.JobID)
	}
	if completion, err := retried.Wait(context.Background()); err != nil || completion.State != agentrun.StateSucceeded {
		t.Fatalf("reconstructed retry completion = %+v, %v; want success", completion, err)
	}

	inspection, err := fresh.Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	retryEvent := inspection.Events[4]
	if retryEvent.Decision != agentrun.DecisionRetry || retryEvent.From != agentrun.StateFailed || retryEvent.To != agentrun.StateRunning {
		t.Fatalf("durable retry evidence = %+v, want the recorded retry decision", retryEvent)
	}
	if len(inspection.Outcomes) != 2 || inspection.Outcomes[0].Class != agentrun.OutcomeFailure ||
		inspection.Outcomes[0].InvocationID != string(handle.InvocationID) || inspection.Outcomes[1].Class != agentrun.OutcomeSuccess {
		t.Fatalf("outcomes = %+v, want prior attempts preserved beside the retry", inspection.Outcomes)
	}
}

func TestRetryHonorsTheExpectedRevision(t *testing.T) {
	backingStore := store.NuevoStore(t.TempDir())
	failed := NewControllerWithClock(backingStore, &scriptedAdapter{adapterErr: errors.New("attempt failed")}, fixedClock())
	handle, err := failed.Start(context.Background(), testRequest("revision"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	inspection, err := failed.Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}

	fresh := NewControllerWithClock(backingStore, &scriptedAdapter{result: AdapterResult{Output: "revision output"}}, fixedClock())
	if _, err := fresh.Retry(context.Background(), handle.RunID, 1); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("stale revision error = %v, want ErrStaleRevision", err)
	}
	retried, err := fresh.Retry(context.Background(), handle.RunID, inspection.Projection.Revision)
	if err != nil {
		t.Fatalf("current revision retry error = %v, want acceptance", err)
	}
	if completion, err := retried.Wait(context.Background()); err != nil || completion.State != agentrun.StateSucceeded {
		t.Fatalf("current revision completion = %+v, %v; want success", completion, err)
	}
}
