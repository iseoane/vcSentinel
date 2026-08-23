package execution

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

func TestRepositoryHostStartSettlesThroughWait(t *testing.T) {
	backingStore := store.NuevoStore(t.TempDir())
	host := NewInProcessHost(NewControllerWithClock(backingStore, &scriptedAdapter{result: AdapterResult{Output: "host output"}}, fixedClock()))

	handle, err := host.Start(context.Background(), StartRequest{Request: testRequest("host-start"), Policy: testPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	completion, err := handle.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if completion.State != agentrun.StateSucceeded || completion.Outcome != agentrun.OutcomeSuccess || completion.Output != "host output" {
		t.Fatalf("completion = %+v, want successful admitted output", completion)
	}
}

func TestRepositoryHostInspectMatchesTheDirectControllerProjection(t *testing.T) {
	backingStore := store.NuevoStore(t.TempDir())
	adapter := &scriptedAdapter{result: AdapterResult{Output: "parity output"}}
	host := NewInProcessHost(NewControllerWithClock(backingStore, adapter, fixedClock()))
	handle, err := host.Start(context.Background(), StartRequest{Request: testRequest("parity"), Policy: testPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}

	viaHost, err := host.Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := NewController(backingStore, nil).Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if viaHost.Projection != direct.Projection {
		t.Fatalf("projection via host = %+v, want %+v", viaHost.Projection, direct.Projection)
	}
	if len(viaHost.Events) != len(direct.Events) || len(viaHost.Outcomes) != len(direct.Outcomes) || len(viaHost.Responses) != len(direct.Responses) {
		t.Fatalf("inspection via host = %+v, want the same evidence as %+v", viaHost, direct)
	}
}

func TestRepositoryHostSubscribePagesAfterCursor(t *testing.T) {
	backingStore := store.NuevoStore(t.TempDir())
	host := NewInProcessHost(NewControllerWithClock(backingStore, &scriptedAdapter{result: AdapterResult{Output: "paged output"}}, fixedClock()))
	handle, err := host.Start(context.Background(), StartRequest{Request: testRequest("subscribe"), Policy: testPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}

	tests := []struct {
		name        string
		afterCursor uint64
		limit       int
		wantEvents  int
		wantHasMore bool
	}{
		{name: "first page honors limit", afterCursor: 0, limit: 2, wantEvents: 2, wantHasMore: true},
		{name: "zero limit pages with the standard size", afterCursor: 0, limit: 0, wantEvents: 4, wantHasMore: false},
		{name: "cursor past head is empty", afterCursor: 4, limit: 2, wantEvents: 0, wantHasMore: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, err := host.Subscribe(context.Background(), SubscribeRequest{RunID: handle.RunID, AfterCursor: tt.afterCursor, Limit: tt.limit})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Events) != tt.wantEvents || page.HasMore != tt.wantHasMore {
				t.Fatalf("page = %d events, hasMore = %v, want %d events and hasMore = %v", len(page.Events), page.HasMore, tt.wantEvents, tt.wantHasMore)
			}
			for _, frame := range page.Events {
				if frame.Revision <= tt.afterCursor {
					t.Fatalf("event revision %d is not strictly after cursor %d", frame.Revision, tt.afterCursor)
				}
			}
		})
	}

	first, err := host.Subscribe(context.Background(), SubscribeRequest{RunID: handle.RunID, AfterCursor: 0, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	second, err := host.Subscribe(context.Background(), SubscribeRequest{RunID: handle.RunID, AfterCursor: first.NextRevision, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Events) != 2 || second.HasMore {
		t.Fatalf("second page = %d events, hasMore = %v, want the remaining two events", len(second.Events), second.HasMore)
	}
	if second.Events[0].Revision <= first.Events[len(first.Events)-1].Revision {
		t.Fatalf("second page starts at revision %d, want strictly after %d", second.Events[0].Revision, first.Events[len(first.Events)-1].Revision)
	}
}

func TestRepositoryHostApplyRespondReachesAwaitingRun(t *testing.T) {
	adapter := &responseAdapter{}
	host := NewInProcessHost(NewControllerWithClock(store.NuevoStore(t.TempDir()), adapter, fixedClock()))
	handle, err := host.Start(context.Background(), StartRequest{Request: testRequest("host-respond"), Policy: testPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	waitForStateViaHost(t, host, handle.RunID, agentrun.StateAwaitingDecision)

	result, err := host.Apply(context.Background(), ApplyRequest{
		RunID:  handle.RunID,
		Action: ControlAction{Kind: ActionRespond, Response: "continue"},
	})
	if err != nil || !result.Accepted || result.InvocationID == handle.InvocationID {
		t.Fatalf("Apply(respond) via host = %+v, %v, want an accepted child invocation", result, err)
	}
	completion, err := handle.Wait(context.Background())
	if err != nil || completion.State != agentrun.StateSucceeded || completion.InvocationID != result.InvocationID {
		t.Fatalf("response completion via host = %+v, %v, want child success", completion, err)
	}
}

func TestRepositoryHostApplyAbortCancelsRunningRun(t *testing.T) {
	adapter := &blockingAdapter{started: make(chan struct{}), release: make(chan struct{})}
	host := NewInProcessHost(NewControllerWithClock(store.NuevoStore(t.TempDir()), adapter, fixedClock()))
	handle, err := host.Start(context.Background(), StartRequest{Request: testRequest("host-abort"), Policy: testPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	<-adapter.started
	defer close(adapter.release)

	result, err := host.Apply(context.Background(), ApplyRequest{RunID: handle.RunID, Action: ControlAction{Kind: ActionAbort}})
	if err != nil || !result.Accepted {
		t.Fatalf("Apply(abort) via host = %+v, %v", result, err)
	}
	completion, err := handle.Wait(context.Background())
	if err != nil || completion.State != agentrun.StateCanceled || completion.Outcome != agentrun.OutcomeCancellation {
		t.Fatalf("abort completion via host = %+v, %v, want cooperative cancellation", completion, err)
	}
}

func TestRepositoryHostSurfacesDocumentedSentinelErrors(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T) error
		want error
	}{
		{
			name: "duplicate admission",
			run: func(t *testing.T) error {
				host := NewInProcessHost(NewControllerWithClock(store.NuevoStore(t.TempDir()), &scriptedAdapter{}, fixedClock()))
				request := StartRequest{Request: testRequest("duplicate"), Policy: testPolicy()}
				handle, err := host.Start(context.Background(), request)
				if err != nil {
					t.Fatal(err)
				}
				_, dupErr := host.Start(context.Background(), request)
				if _, waitErr := handle.Wait(context.Background()); waitErr != nil {
					t.Fatalf("Wait() error = %v", waitErr)
				}
				return dupErr
			},
			want: ErrRunAlreadyExists,
		},
		{
			name: "respond while running",
			run: func(t *testing.T) error {
				adapter := &blockingAdapter{started: make(chan struct{}), release: make(chan struct{})}
				host := NewInProcessHost(NewControllerWithClock(store.NuevoStore(t.TempDir()), adapter, fixedClock()))
				handle, err := host.Start(context.Background(), StartRequest{Request: testRequest("running-respond"), Policy: testPolicy()})
				if err != nil {
					t.Fatal(err)
				}
				<-adapter.started
				_, applyErr := host.Apply(context.Background(), ApplyRequest{
					RunID:  handle.RunID,
					Action: ControlAction{Kind: ActionRespond, Response: "too early"},
				})
				close(adapter.release)
				if _, err := handle.Wait(context.Background()); err != nil {
					t.Fatalf("Wait() error = %v", err)
				}
				return applyErr
			},
			want: ErrDecisionNotPending,
		},
		{
			name: "unsupported action",
			run: func(t *testing.T) error {
				host := NewInProcessHost(NewControllerWithClock(store.NuevoStore(t.TempDir()), &scriptedAdapter{}, fixedClock()))
				handle, err := host.Start(context.Background(), StartRequest{Request: testRequest("unsupported"), Policy: testPolicy()})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := handle.Wait(context.Background()); err != nil {
					t.Fatal(err)
				}
				_, err = host.Apply(context.Background(), ApplyRequest{
					RunID:  handle.RunID,
					Action: ControlAction{Kind: Action("purge")},
				})
				return err
			},
			want: ErrUnsupportedAction,
		},
		{
			name: "control action on settled run",
			run: func(t *testing.T) error {
				host := NewInProcessHost(NewControllerWithClock(store.NuevoStore(t.TempDir()), &scriptedAdapter{}, fixedClock()))
				handle, err := host.Start(context.Background(), StartRequest{Request: testRequest("settled"), Policy: testPolicy()})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := handle.Wait(context.Background()); err != nil {
					t.Fatal(err)
				}
				_, err = host.Apply(context.Background(), ApplyRequest{RunID: handle.RunID, Action: ControlAction{Kind: ActionAbort}})
				return err
			},
			want: ErrRunNotActive,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(t); !errors.Is(err, tt.want) {
				t.Fatalf("error via host = %v, want errors.Is %v", err, tt.want)
			}
		})
	}
}

func TestStaleRevisionSentinelRemainsOutsidePortUntilSliceTwo(t *testing.T) {
	backingStore := store.NuevoStore(t.TempDir())
	failed := NewControllerWithClock(backingStore, &scriptedAdapter{adapterErr: errors.New("attempt failed")}, fixedClock())
	handle, err := failed.Start(context.Background(), testRequest("stale"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}

	fresh := NewControllerWithClock(backingStore, &scriptedAdapter{result: AdapterResult{Output: "stale output"}}, fixedClock())
	if _, err := fresh.Retry(context.Background(), handle.RunID, 1); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("stale revision error = %v, want errors.Is ErrStaleRevision", err)
	}
	host := NewInProcessHost(fresh)
	if _, err := host.Inspect(context.Background(), handle.RunID); err != nil {
		t.Fatalf("Inspect via host after stale rejection error = %v", err)
	}
}

func TestInProcessHostSurfacesControllerNotReadyThroughPort(t *testing.T) {
	host := NewInProcessHost(&Controller{})

	if _, err := host.Start(context.Background(), StartRequest{Request: testRequest("not-ready"), Policy: testPolicy()}); !errors.Is(err, ErrControllerNotReady) {
		t.Fatalf("Start via unready host = %v, want errors.Is ErrControllerNotReady", err)
	}
	if _, err := host.Apply(context.Background(), ApplyRequest{RunID: "not-ready", Action: ControlAction{Kind: ActionAbort}}); !errors.Is(err, ErrControllerNotReady) {
		t.Fatalf("Apply via unready host = %v, want errors.Is ErrControllerNotReady", err)
	}
	if _, err := host.Subscribe(context.Background(), SubscribeRequest{RunID: "not-ready"}); !errors.Is(err, ErrControllerNotReady) {
		t.Fatalf("Subscribe via unready host = %v, want errors.Is ErrControllerNotReady", err)
	}
}

func waitForStateViaHost(t *testing.T, host *InProcessHost, runID agentrun.Identity, want agentrun.LifecycleState) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		inspection, err := host.Inspect(context.Background(), runID)
		if err == nil && inspection.Projection.State == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("state never reached %q through the repository host", want)
}
