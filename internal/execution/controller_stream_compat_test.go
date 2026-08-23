package execution

import (
	"context"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// TestNormalSuccessWritesOnlyLegacyEventKinds is the additive-compatibility
// guard for R7 slice 1: a plain successful run must produce exactly the
// pre-R7 stream shape, so every existing consumer keeps reading it unchanged.
func TestNormalSuccessWritesOnlyLegacyEventKinds(t *testing.T) {
	controller := NewControllerWithClock(store.NuevoStore(t.TempDir()), &scriptedAdapter{result: AdapterResult{Output: "legacy output"}}, fixedClock())
	handle, err := controller.Start(context.Background(), testRequest("legacy-success"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	completion, err := handle.Wait(context.Background())
	if err != nil || completion.State != agentrun.StateSucceeded {
		t.Fatalf("completion = %+v, %v; want plain success", completion, err)
	}

	inspection, err := NewController(controller.store, nil).Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(inspection.Events) != 4 {
		t.Fatalf("events = %d, want the four pre-R7 lifecycle events", len(inspection.Events))
	}
	wantStates := []agentrun.LifecycleState{
		agentrun.StateQueued, agentrun.StateAdmitted, agentrun.StateRunning, agentrun.StateSucceeded,
	}
	for index, frame := range inspection.Events {
		switch frame.Decision {
		case agentrun.DecisionStart, agentrun.DecisionComplete:
		default:
			t.Fatalf("event %d decision = %q, want only legacy start/complete kinds on the success path", frame.Sequence, frame.Decision)
		}
		if want := wantStates[index]; frame.To != want {
			t.Fatalf("event %d state = %q, want %q", frame.Sequence, frame.To, want)
		}
		terminal := agentrun.TerminalNone
		if index == len(inspection.Events)-1 {
			terminal = agentrun.TerminalSuccess
		}
		if frame.Terminal != terminal {
			t.Fatalf("event %d terminal = %q, want %q", frame.Sequence, frame.Terminal, terminal)
		}
	}
	if len(inspection.Outcomes) != 1 || inspection.Outcomes[0].Class != agentrun.OutcomeSuccess {
		t.Fatalf("outcomes = %+v, want exactly one success outcome", inspection.Outcomes)
	}
}

// TestAbortedAttemptAppendsCancellationEvidenceExactlyOnce pins the durable
// evidence contract: aborting one running attempt appends exactly one
// cancellation settlement event with its attempt outcome, and no other event
// kind joins the stream because of the abort.
func TestAbortedAttemptAppendsCancellationEvidenceExactlyOnce(t *testing.T) {
	adapter := &contextIgnoringAdapter{started: make(chan struct{}), release: make(chan struct{}), returned: make(chan struct{})}
	controller := NewControllerWithClock(store.NuevoStore(t.TempDir()), adapter, fixedClock())
	handle, err := controller.Start(context.Background(), testRequest("cancel-evidence"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	<-adapter.started
	if _, err := controller.Apply(context.Background(), handle.RunID, ControlAction{Kind: ActionAbort}); err != nil {
		t.Fatal(err)
	}
	waitContext, cancelWait := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelWait()
	close(adapter.release)
	if _, err := handle.Wait(waitContext); err != nil {
		t.Fatal(err)
	}

	inspection, err := NewController(controller.store, nil).Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	cancellations := 0
	for _, frame := range inspection.Events {
		if frame.To == agentrun.StateCanceled {
			cancellations++
			if frame.Decision != agentrun.DecisionAbort {
				t.Fatalf("cancellation event %d decision = %q, want abort", frame.Sequence, frame.Decision)
			}
			continue
		}
		switch frame.Decision {
		case agentrun.DecisionStart:
		default:
			t.Fatalf("event %d decision = %q, want no new kind beyond start on an aborted run", frame.Sequence, frame.Decision)
		}
	}
	if cancellations != 1 {
		t.Fatalf("cancellation events = %d, want exactly one per aborted attempt", cancellations)
	}
	if len(inspection.Outcomes) != 1 || inspection.Outcomes[0].Class != agentrun.OutcomeCancellation {
		t.Fatalf("outcomes = %+v, want exactly one durable cancellation outcome", inspection.Outcomes)
	}
}
