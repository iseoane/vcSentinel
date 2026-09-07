package execution

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

func TestVerifyConfirmsIntactStreamsAndDetectsCorruption(t *testing.T) {
	newVerifiedController := func(t *testing.T, adapter Adapter) (*Controller, agentrun.Identity, string) {
		t.Helper()
		storeRoot := t.TempDir()
		controller := NewControllerWithClock(store.NewStore(storeRoot), adapter, fixedClock())
		return controller, startAndWaitTerminal(t, controller, "verify"), storeRoot
	}

	t.Run("intact terminal stream verifies against its replay", func(t *testing.T) {
		controller, runID, _ := newVerifiedController(t, &scriptedAdapter{result: AdapterResult{Output: "done"}})
		verification, err := controller.Verify(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		if !verification.Valid || verification.Events != 4 || verification.Reason != "" {
			t.Fatalf("verification = %+v, want valid four-event replay", verification)
		}
	})

	t.Run("respond continuation keeps lineage boundaries consistent", func(t *testing.T) {
		storeRoot := t.TempDir()
		controller := NewControllerWithClock(store.NewStore(storeRoot), &responseAdapter{}, fixedClock())
		handle, err := controller.Start(context.Background(), testRequest("verify-lineage"), testPolicy())
		if err != nil {
			t.Fatal(err)
		}
		waitForState(t, controller, handle.RunID, agentrun.StateAwaitingDecision)
		if _, err := controller.Apply(context.Background(), handle.RunID, ControlAction{Kind: ActionRespond, Response: "continue"}); err != nil {
			t.Fatal(err)
		}
		if completion, err := handle.Wait(context.Background()); err != nil || completion.State != agentrun.StateSucceeded {
			t.Fatalf("completion = %+v, %v; want success", completion, err)
		}

		fresh := NewController(controller.store, nil)
		verification, err := fresh.Verify(context.Background(), handle.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if !verification.Valid || verification.Events != 6 {
			t.Fatalf("lineage verification = %+v, want a valid six-event chain across two invocations", verification)
		}
	})

	corruptionCases := []struct {
		name   string
		tamper func(t *testing.T, path string)
	}{
		{
			name:   "broken JSON line",
			tamper: func(t *testing.T, path string) { appendString(t, path, "{oops\n") },
		},
		{
			name:   "bogus well-formed frame",
			tamper: func(t *testing.T, path string) { appendString(t, path, `{"sequence":99}`+"\n") },
		},
		{
			name: "truncated final record",
			tamper: func(t *testing.T, path string) {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data[:len(data)-1], 0600); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tc := range corruptionCases {
		t.Run(tc.name, func(t *testing.T) {
			controller, runID, storeRoot := newVerifiedController(t, &scriptedAdapter{result: AdapterResult{Output: "done"}})
			tc.tamper(t, eventsPath(storeRoot, runID))

			verification, err := controller.Verify(context.Background(), runID)
			if err != nil {
				t.Fatalf("corruption must report an invalid verdict, not a Go error: %v", err)
			}
			if verification.Valid || strings.TrimSpace(verification.Reason) == "" {
				t.Fatalf("verification = %+v, want invalid with a concrete reason", verification)
			}
		})
	}

	t.Run("missing execution record reports why it cannot verify", func(t *testing.T) {
		controller := NewControllerWithClock(store.NewStore(t.TempDir()), &scriptedAdapter{}, fixedClock())
		verification, err := controller.Verify(context.Background(), agentrun.Identity("never-admitted"))
		if err != nil || verification.Valid || verification.Reason == "" {
			t.Fatalf("verification = %+v, %v; want invalid with a reason", verification, err)
		}
	})
}
