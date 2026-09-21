package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
)

func TestRecoversAnAwaitingRunAndResumesItThroughRespond(t *testing.T) {
	backingStore := store.NewStore(t.TempDir())
	first := NewControllerWithClock(backingStore, &responseAdapter{}, fixedClock())
	handle, err := first.Start(context.Background(), testRequest("recover-awaiting"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	waitForState(t, first, handle.RunID, agentrun.StateAwaitingDecision)

	fresh := NewControllerWithClock(backingStore, &responseAdapter{}, fixedClock())
	recovered, err := fresh.Recover(context.Background(), handle.RunID, 0)
	if err != nil {
		t.Fatalf("fresh-controller recover error = %v, want a reconstructed awaiting handle", err)
	}
	if recovered.RunID != handle.RunID || recovered.JobID != handle.JobID || recovered.InvocationID != handle.InvocationID {
		t.Fatalf("recovered identity = %s/%s/%s, want the original head %s/%s/%s",
			recovered.RunID, recovered.JobID, recovered.InvocationID, handle.RunID, handle.JobID, handle.InvocationID)
	}

	result, err := fresh.Apply(context.Background(), handle.RunID, ControlAction{Kind: ActionRespond, Response: "operator answer"})
	if err != nil || !result.Accepted {
		t.Fatalf("recovered Apply(respond) = %+v, %v; want accepted resumption", result, err)
	}
	completion, err := recovered.Wait(context.Background())
	if err != nil || completion.State != agentrun.StateSucceeded {
		t.Fatalf("resumed completion = %+v, %v; want success", completion, err)
	}
	resumedAdapter := fresh.adapter.(*responseAdapter)
	resumedAdapter.mu.Lock()
	called := len(resumedAdapter.responses) == 1 && resumedAdapter.responses[0] == "operator answer"
	resumedAdapter.mu.Unlock()
	if !called {
		t.Fatal("the resumed response must reach the adapter exactly once")
	}

	inspection, err := fresh.Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Projection.State != agentrun.StateSucceeded || len(inspection.Events) != 6 {
		t.Fatalf("durable evidence = %+v, want six events ending succeeded", inspection.Projection)
	}
	response := inspection.Events[4]
	if response.Decision != agentrun.DecisionRespond ||
		response.InvocationID != string(result.InvocationID) || response.ParentInvocationID != string(handle.InvocationID) {
		t.Fatalf("continuation event = %+v, want a faithful child lineage inside the original run", response)
	}
	if verification, err := fresh.Verify(context.Background(), handle.RunID); err != nil || !verification.Valid {
		t.Fatalf("resumed stream verification = %+v, %v; want an intact lineage chain", verification, err)
	}
}

func TestRecoverRefusesUnrecoverableEvidence(t *testing.T) {
	tests := []struct {
		name      string
		run       func(t *testing.T, controller *Controller) agentrun.Identity
		wantError error
	}{
		{
			name: "succeeded outcome is final",
			run: func(t *testing.T, c *Controller) agentrun.Identity {
				return startAndWaitTerminal(t, c, "recover-success")
			},
			wantError: ErrRunNotRecoverable,
		},
		{
			name: "unavailable outcome is final",
			run: func(t *testing.T, c *Controller) agentrun.Identity {
				return startWithAdapterAndWait(t, c,
					&scriptedAdapter{adapterErr: NewAdapterError(agentrun.OutcomeUnavailable, errors.New("binary missing"))},
					"recover-unavailable", agentrun.StateUnavailable)
			},
			wantError: ErrRunNotRecoverable,
		},
		{
			name:      "empty stream has no lifecycle evidence",
			run:       func(t *testing.T, c *Controller) agentrun.Identity { return createEmptyRun(t, c, "recover-empty") },
			wantError: ErrRunNotRecoverable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller := NewControllerWithClock(store.NewStore(t.TempDir()), &scriptedAdapter{}, fixedClock())
			runID := tt.run(t, controller)
			before, err := controller.Inspect(context.Background(), runID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := controller.Recover(context.Background(), runID, 0); !errors.Is(err, tt.wantError) {
				t.Fatalf("Recover() error = %v, want %v", err, tt.wantError)
			}
			after, err := controller.Inspect(context.Background(), runID)
			if err != nil || len(after.Events) != len(before.Events) {
				t.Fatalf("refused recovery must not mutate durable evidence: %v, %v", err, after.Events)
			}
		})
	}

	t.Run("missing execution record refuses explicitly", func(t *testing.T) {
		controller := NewControllerWithClock(store.NewStore(t.TempDir()), &scriptedAdapter{}, fixedClock())
		if _, err := controller.Recover(context.Background(), agentrun.Identity("never-admitted"), 0); err == nil {
			t.Fatal("recovering a missing run must fail explicitly")
		}
	})

	t.Run("respond on a recovered run without an adapter fails instead of panicking", func(t *testing.T) {
		backingStore := store.NewStore(t.TempDir())
		first := NewControllerWithClock(backingStore, &responseAdapter{}, fixedClock())
		handle, err := first.Start(context.Background(), testRequest("recover-no-adapter"), testPolicy())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if busy := first.WaitForActiveRuns(2 * time.Second); busy != 0 {
				t.Errorf("WaitForActiveRuns = %d, want 0 before test cleanup", busy)
			}
		})
		waitForState(t, first, handle.RunID, agentrun.StateAwaitingDecision)

		fresh := NewControllerWithClock(backingStore, nil, fixedClock())
		if _, err := fresh.Recover(context.Background(), handle.RunID, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := fresh.Apply(context.Background(), handle.RunID, ControlAction{Kind: ActionRespond, Response: "answer"}); !errors.Is(err, ErrControllerNotReady) {
			t.Fatalf("respond without an adapter error = %v, want ErrControllerNotReady", err)
		}
	})

	t.Run("corrupt log propagates the store corruption error", func(t *testing.T) {
		storeRoot := t.TempDir()
		controller := NewControllerWithClock(store.NewStore(storeRoot), &scriptedAdapter{result: AdapterResult{Output: "done"}}, fixedClock())
		runID := startAndWaitTerminal(t, controller, "recover-corrupt")

		file, err := os.OpenFile(eventsPath(storeRoot, runID), os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString("{oops\n"); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}

		fresh := NewControllerWithClock(store.NewStore(storeRoot), &scriptedAdapter{}, fixedClock())
		if _, err := fresh.Recover(context.Background(), runID, 0); !errors.Is(err, store.ErrEventCorrupt) {
			t.Fatalf("corrupt-evidence recover error = %v, want the store corruption error", err)
		}
	})
}

func TestRecoverRelaunchesRetryableTerminalEvidenceThroughTheRetryContract(t *testing.T) {
	backingStore := store.NewStore(t.TempDir())
	failed := NewControllerWithClock(backingStore, &scriptedAdapter{adapterErr: errors.New("worker crashed")}, fixedClock())
	handle, err := failed.Start(context.Background(), testRequest("recover-retry"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if completion, err := handle.Wait(context.Background()); err != nil || completion.State != agentrun.StateFailed {
		t.Fatalf("first attempt = %+v, %v; want failure", completion, err)
	}

	fresh := NewControllerWithClock(backingStore, &scriptedAdapter{result: AdapterResult{Output: "recovered output"}}, fixedClock())
	recovered, err := fresh.Recover(context.Background(), handle.RunID, 0)
	if err != nil {
		t.Fatalf("retryable-terminal recover error = %v, want a relaunched attempt", err)
	}
	if completion, err := recovered.Wait(context.Background()); err != nil || completion.State != agentrun.StateSucceeded {
		t.Fatalf("relaunched completion = %+v, %v; want success", completion, err)
	}
	inspection, err := fresh.Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	retryEvent := inspection.Events[4]
	if retryEvent.Decision != agentrun.DecisionRetry || retryEvent.From != agentrun.StateFailed || retryEvent.To != agentrun.StateRunning {
		t.Fatalf("recovery relaunch event = %+v, want the recorded retry decision", retryEvent)
	}
}

func TestRecoveringALiveOrAlreadyRecoveredRunFailsExplicitly(t *testing.T) {
	t.Run("live running run refuses without appending evidence", func(t *testing.T) {
		blocker := &blockingAdapter{started: make(chan struct{}), release: make(chan struct{})}
		controller := NewControllerWithClock(store.NewStore(t.TempDir()), blocker, fixedClock())
		handle, err := controller.Start(context.Background(), testRequest("recover-live"), testPolicy())
		if err != nil {
			t.Fatal(err)
		}
		<-blocker.started
		t.Cleanup(func() {
			close(blocker.release)
			_, _ = handle.Wait(context.Background())
		})

		before, err := controller.Inspect(context.Background(), handle.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := controller.Recover(context.Background(), handle.RunID, 0); !errors.Is(err, ErrRunNotActive) {
			t.Fatalf("live-run recover error = %v, want ErrRunNotActive", err)
		}
		after, err := controller.Inspect(context.Background(), handle.RunID)
		if err != nil || len(after.Events) != len(before.Events) {
			t.Fatalf("live-run refusal appended events (%d -> %d)", len(before.Events), len(after.Events))
		}
	})

	t.Run("already recovered awaiting run refuses a second recovery", func(t *testing.T) {
		backingStore := store.NewStore(t.TempDir())
		first := NewControllerWithClock(backingStore, &responseAdapter{}, fixedClock())
		handle, err := first.Start(context.Background(), testRequest("recover-twice"), testPolicy())
		if err != nil {
			t.Fatal(err)
		}
		waitForState(t, first, handle.RunID, agentrun.StateAwaitingDecision)

		fresh := NewControllerWithClock(backingStore, &responseAdapter{}, fixedClock())
		if _, err := fresh.Recover(context.Background(), handle.RunID, 0); err != nil {
			t.Fatal(err)
		}
		before, err := fresh.Inspect(context.Background(), handle.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fresh.Recover(context.Background(), handle.RunID, 0); !errors.Is(err, ErrRunNotActive) {
			t.Fatalf("double recover error = %v, want ErrRunNotActive", err)
		}
		after, err := fresh.Inspect(context.Background(), handle.RunID)
		if err != nil || len(after.Events) != len(before.Events) {
			t.Fatalf("double recover appended events (%d -> %d)", len(before.Events), len(after.Events))
		}
	})
}

func startAndWaitTerminal(t *testing.T, controller *Controller, name string) agentrun.Identity {
	t.Helper()
	return startWithAdapterAndWait(t, controller, &scriptedAdapter{result: AdapterResult{Output: name + " output"}}, name, agentrun.StateSucceeded)
}

func startWithAdapterAndWait(t *testing.T, controller *Controller, adapter Adapter, name string, want agentrun.LifecycleState) agentrun.Identity {
	t.Helper()
	previous := controller.adapter
	controller.adapter = adapter
	defer func() { controller.adapter = previous }()
	handle, err := controller.Start(context.Background(), testRequest(name), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if completion, err := handle.Wait(context.Background()); err != nil || completion.State != want {
		t.Fatalf("setup completion = %+v, %v; want state %q", completion, err, want)
	}
	return handle.RunID
}

func createEmptyRun(t *testing.T, controller *Controller, name string) agentrun.Identity {
	t.Helper()
	job := agentrun.NewLogicalJob(testRequest(name))
	if err := controller.store.CreateRun(job, testPolicy()); err != nil {
		t.Fatal(err)
	}
	return job.RunID()
}

func eventsPath(storeRoot string, runID agentrun.Identity) string {
	return filepath.Join(storeRoot, "vcsentinel", "executions", "v1", string(runID), "events.jsonl")
}

func appendString(t *testing.T, path, value string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(value); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRecoversAnAwaitingRunAndResumesItThroughAbort(t *testing.T) {
	backingStore := store.NewStore(t.TempDir())
	first := NewControllerWithClock(backingStore, &responseAdapter{}, fixedClock())
	handle, err := first.Start(context.Background(), testRequest("recover-abort"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	waitForState(t, first, handle.RunID, agentrun.StateAwaitingDecision)

	fresh := NewControllerWithClock(backingStore, &responseAdapter{}, fixedClock())
	recovered, err := fresh.Recover(context.Background(), handle.RunID, 0)
	if err != nil {
		t.Fatalf("fresh-controller recover error = %v, want a reconstructed awaiting handle", err)
	}
	result, err := fresh.Apply(context.Background(), handle.RunID, ControlAction{Kind: ActionAbort})
	if err != nil || !result.Accepted {
		t.Fatalf("recovered Apply(abort) = %+v, %v; want accepted abort of the waiting head", result, err)
	}
	completion, err := recovered.Wait(context.Background())
	if err != nil || completion.State != agentrun.StateCanceled {
		t.Fatalf("aborted completion = %+v, %v; want cancellation without any adapter relaunch", completion, err)
	}
	resumedAdapter := fresh.adapter.(*responseAdapter)
	resumedAdapter.mu.Lock()
	called := len(resumedAdapter.responses)
	resumedAdapter.mu.Unlock()
	if called != 0 {
		t.Fatal("aborting a recovered awaiting run must not execute an adapter call")
	}
	inspection, err := fresh.Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Projection.State != agentrun.StateCanceled ||
		len(inspection.Outcomes) != 1 || inspection.Outcomes[0].Class != agentrun.OutcomeCancellation {
		t.Fatalf("durable evidence = %+v outcomes %+v, want a canceled terminal with one cancellation outcome", inspection.Projection, inspection.Outcomes)
	}
}

func TestRecoverRelaunchesEveryRetryableTerminalClass(t *testing.T) {
	tests := []struct {
		name       string
		adapterErr error
		headState  agentrun.LifecycleState
	}{
		{name: "canceled terminal", adapterErr: NewAdapterError(agentrun.OutcomeCancellation, errors.New("operator aborted")), headState: agentrun.StateCanceled},
		{name: "timed out terminal", adapterErr: context.DeadlineExceeded, headState: agentrun.StateTimedOut},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backingStore := store.NewStore(t.TempDir())
			first := NewControllerWithClock(backingStore, &scriptedAdapter{adapterErr: tt.adapterErr}, fixedClock())
			handle, err := first.Start(context.Background(), testRequest(tt.name), testPolicy())
			if err != nil {
				t.Fatal(err)
			}
			if completion, err := handle.Wait(context.Background()); err != nil || completion.State != tt.headState {
				t.Fatalf("first attempt = %+v, %v; want %s", completion, err, tt.headState)
			}

			fresh := NewControllerWithClock(backingStore, &scriptedAdapter{result: AdapterResult{Output: "recovered"}}, fixedClock())
			recovered, err := fresh.Recover(context.Background(), handle.RunID, 0)
			if err != nil {
				t.Fatalf("retryable-terminal recover error = %v, want a relaunched attempt", err)
			}
			if completion, err := recovered.Wait(context.Background()); err != nil || completion.State != agentrun.StateSucceeded {
				t.Fatalf("relaunched completion = %+v, %v; want success", completion, err)
			}
			inspection, err := fresh.Inspect(context.Background(), handle.RunID)
			if err != nil {
				t.Fatal(err)
			}
			retryEvent := inspection.Events[4]
			if retryEvent.Decision != agentrun.DecisionRetry || retryEvent.From != tt.headState || retryEvent.To != agentrun.StateRunning {
				t.Fatalf("recovery relaunch event = %+v, want the recorded retry decision from %s", retryEvent, tt.headState)
			}
		})
	}
}
