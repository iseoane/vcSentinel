package execution

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// testPrincipal is the authenticated principal every harness-built envelope
// carries; only dedicated validation tests exercise the empty case.
const testPrincipal = "test-harness"

func TestRepositoryHostStartSettlesThroughWait(t *testing.T) {
	backingStore := store.NuevoStore(t.TempDir())
	host := NewInProcessHost(NewControllerWithClock(backingStore, &scriptedAdapter{result: AdapterResult{Output: "host output"}}, fixedClock()))

	handle, err := host.Start(context.Background(), StartRequest{Request: testRequest("host-start"), Policy: testPolicy(), AuthContext: AuthContext{Principal: testPrincipal}})
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
	handle, err := host.Start(context.Background(), StartRequest{Request: testRequest("parity"), Policy: testPolicy(), AuthContext: AuthContext{Principal: testPrincipal}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}

	viaHost, err := host.Inspect(context.Background(), InspectRequest{RunID: handle.RunID, AuthContext: AuthContext{Principal: testPrincipal}})
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
	handle, err := host.Start(context.Background(), StartRequest{Request: testRequest("subscribe"), Policy: testPolicy(), AuthContext: AuthContext{Principal: testPrincipal}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}

	subscribe := func(afterCursor uint64, limit int) (store.EventPage, error) {
		return host.Subscribe(context.Background(), SubscribeRequest{
			RunID:       handle.RunID,
			AfterCursor: afterCursor,
			Limit:       limit,
			AuthContext: AuthContext{Principal: testPrincipal},
		})
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
			page, err := subscribe(tt.afterCursor, tt.limit)
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

	first, err := subscribe(0, 2)
	if err != nil {
		t.Fatal(err)
	}
	second, err := subscribe(first.NextRevision, 2)
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
	handle, err := host.Start(context.Background(), StartRequest{Request: testRequest("host-respond"), Policy: testPolicy(), AuthContext: AuthContext{Principal: testPrincipal}})
	if err != nil {
		t.Fatal(err)
	}
	waitForStateViaHost(t, host, handle.RunID, agentrun.StateAwaitingDecision)

	result, err := host.Apply(context.Background(), ApplyRequest{
		RunID:       handle.RunID,
		Action:      ControlAction{Kind: ActionRespond, Response: "continue"},
		ActionID:    "action:host-respond",
		AuthContext: AuthContext{Principal: testPrincipal},
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
	handle, err := host.Start(context.Background(), StartRequest{Request: testRequest("host-abort"), Policy: testPolicy(), AuthContext: AuthContext{Principal: testPrincipal}})
	if err != nil {
		t.Fatal(err)
	}
	<-adapter.started
	defer close(adapter.release)

	result, err := host.Apply(context.Background(), ApplyRequest{
		RunID:       handle.RunID,
		Action:      ControlAction{Kind: ActionAbort},
		ActionID:    "action:host-abort",
		AuthContext: AuthContext{Principal: testPrincipal},
	})
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
				request := StartRequest{Request: testRequest("duplicate"), Policy: testPolicy(), AuthContext: AuthContext{Principal: testPrincipal}}
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
				handle, err := host.Start(context.Background(), StartRequest{Request: testRequest("running-respond"), Policy: testPolicy(), AuthContext: AuthContext{Principal: testPrincipal}})
				if err != nil {
					t.Fatal(err)
				}
				<-adapter.started
				_, applyErr := host.Apply(context.Background(), ApplyRequest{
					RunID:       handle.RunID,
					Action:      ControlAction{Kind: ActionRespond, Response: "too early"},
					ActionID:    "action:running-respond",
					AuthContext: AuthContext{Principal: testPrincipal},
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
				handle, err := host.Start(context.Background(), StartRequest{Request: testRequest("unsupported"), Policy: testPolicy(), AuthContext: AuthContext{Principal: testPrincipal}})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := handle.Wait(context.Background()); err != nil {
					t.Fatal(err)
				}
				_, err = host.Apply(context.Background(), ApplyRequest{
					RunID:       handle.RunID,
					Action:      ControlAction{Kind: Action("purge")},
					ActionID:    "action:unsupported",
					AuthContext: AuthContext{Principal: testPrincipal},
				})
				return err
			},
			want: ErrUnsupportedAction,
		},
		{
			name: "control action on settled run",
			run: func(t *testing.T) error {
				host := NewInProcessHost(NewControllerWithClock(store.NuevoStore(t.TempDir()), &scriptedAdapter{}, fixedClock()))
				handle, err := host.Start(context.Background(), StartRequest{Request: testRequest("settled"), Policy: testPolicy(), AuthContext: AuthContext{Principal: testPrincipal}})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := handle.Wait(context.Background()); err != nil {
					t.Fatal(err)
				}
				_, err = host.Apply(context.Background(), ApplyRequest{
					RunID:       handle.RunID,
					Action:      ControlAction{Kind: ActionAbort},
					ActionID:    "action:settled",
					AuthContext: AuthContext{Principal: testPrincipal},
				})
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

// The stale-revision sentinel stays outside the port: Apply envelopes carry
// no ExpectedRevision field, and adding one is a future envelope evolution,
// not slice-two scope. This proves a stale retry cannot surface through the
// seam.
func TestStaleRevisionSentinelRemainsOutsidePortUntilEnvelopesCarryExpectedRevisions(t *testing.T) {
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
	_, err = host.Inspect(context.Background(), InspectRequest{RunID: handle.RunID, AuthContext: AuthContext{Principal: testPrincipal}})
	if err != nil {
		t.Fatalf("Inspect via host after stale rejection error = %v", err)
	}
}

func TestInProcessHostSurfacesControllerNotReadyThroughPort(t *testing.T) {
	host := NewInProcessHost(&Controller{})

	if _, err := host.Start(context.Background(), StartRequest{Request: testRequest("not-ready"), Policy: testPolicy(), AuthContext: AuthContext{Principal: testPrincipal}}); !errors.Is(err, ErrControllerNotReady) {
		t.Fatalf("Start via unready host = %v, want errors.Is ErrControllerNotReady", err)
	}
	if _, err := host.Apply(context.Background(), ApplyRequest{RunID: "not-ready", Action: ControlAction{Kind: ActionAbort}, ActionID: "action:not-ready", AuthContext: AuthContext{Principal: testPrincipal}}); !errors.Is(err, ErrControllerNotReady) {
		t.Fatalf("Apply via unready host = %v, want errors.Is ErrControllerNotReady", err)
	}
	if _, err := host.Subscribe(context.Background(), SubscribeRequest{RunID: "not-ready", AuthContext: AuthContext{Principal: testPrincipal}}); !errors.Is(err, ErrControllerNotReady) {
		t.Fatalf("Subscribe via unready host = %v, want errors.Is ErrControllerNotReady", err)
	}
}

func TestRepositoryHostRejectsAMissingPrincipalBeforeControllerContact(t *testing.T) {
	// A zero-value Controller cannot serve any operation without panicking or
	// reporting ErrControllerNotReady, so an ErrMissingPrincipal result here
	// proves the seam validated the envelope before any store contact.
	host := NewInProcessHost(&Controller{})
	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "start",
			call: func() error {
				_, err := host.Start(context.Background(), StartRequest{Request: testRequest("no-principal"), Policy: testPolicy()})
				return err
			},
		},
		{
			name: "inspect",
			call: func() error {
				_, err := host.Inspect(context.Background(), InspectRequest{RunID: "no-principal"})
				return err
			},
		},
		{
			name: "subscribe",
			call: func() error {
				_, err := host.Subscribe(context.Background(), SubscribeRequest{RunID: "no-principal"})
				return err
			},
		},
		{
			name: "apply",
			call: func() error {
				_, err := host.Apply(context.Background(), ApplyRequest{
					RunID:    "no-principal",
					Action:   ControlAction{Kind: ActionAbort},
					ActionID: "action:no-principal",
				})
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); !errors.Is(err, ErrMissingPrincipal) {
				t.Fatalf("missing-principal error = %v, want errors.Is ErrMissingPrincipal", err)
			}
		})
	}
}

func TestRepositoryHostApplyRejectsReplayedActionIdentity(t *testing.T) {
	// The always-awaiting scripted adapter brings every attempt back to
	// StateAwaitingDecision, so repeated applies stay actionable without
	// timing races between observation and application.
	host := NewInProcessHost(NewControllerWithClock(store.NuevoStore(t.TempDir()), &scriptedAdapter{result: AdapterResult{AwaitingDecision: true}}, fixedClock()))
	auth := AuthContext{Principal: testPrincipal}
	apply := func(runID agentrun.Identity, actionID string) error {
		_, err := host.Apply(context.Background(), ApplyRequest{
			RunID:       runID,
			Action:      ControlAction{Kind: ActionRespond, Response: "continue"},
			ActionID:    actionID,
			AuthContext: auth,
		})
		return err
	}

	handleA, err := host.Start(context.Background(), StartRequest{Request: testRequest("replay-a"), Policy: testPolicy(), AuthContext: auth})
	if err != nil {
		t.Fatal(err)
	}
	waitForStateViaHost(t, host, handleA.RunID, agentrun.StateAwaitingDecision)
	if err := apply(handleA.RunID, "x"); err != nil {
		t.Fatalf("first Apply with identity x = %v, want accepted", err)
	}
	waitForStateViaHost(t, host, handleA.RunID, agentrun.StateAwaitingDecision)

	if err := apply(handleA.RunID, "x"); !errors.Is(err, ErrDuplicateAction) {
		t.Fatalf("replayed Apply with identity x = %v, want errors.Is ErrDuplicateAction", err)
	}
	if err := apply(handleA.RunID, "y"); err != nil {
		t.Fatalf("fresh identity y on the same run = %v, want accepted", err)
	}
	waitForStateViaHost(t, host, handleA.RunID, agentrun.StateAwaitingDecision)

	handleB, err := host.Start(context.Background(), StartRequest{Request: testRequest("replay-b"), Policy: testPolicy(), AuthContext: auth})
	if err != nil {
		t.Fatal(err)
	}
	waitForStateViaHost(t, host, handleB.RunID, agentrun.StateAwaitingDecision)
	if err := apply(handleB.RunID, "x"); err != nil {
		t.Fatalf("identity x under a different run = %v, want accepted (identities are scoped per run)", err)
	}
	// The accepted apply spawned an always-awaiting child worker whose
	// awaiting transition is the last durable write it performs; block until
	// that write lands so no worker races TempDir cleanup after the return.
	waitForStateViaHost(t, host, handleB.RunID, agentrun.StateAwaitingDecision)
}

func TestRepositoryHostApplyRequiresAnActionIdentity(t *testing.T) {
	// The zero-value Controller proves this rejection also happens before any
	// controller contact: an empty identity never reaches the delegate.
	host := NewInProcessHost(&Controller{})
	_, err := host.Apply(context.Background(), ApplyRequest{
		RunID:       "no-action-id",
		Action:      ControlAction{Kind: ActionAbort},
		AuthContext: AuthContext{Principal: testPrincipal},
	})
	const want = "execution: control action identity is empty"
	if err == nil || err.Error() != want {
		t.Fatalf("empty identity error = %v, want the plain inline error %q", err, want)
	}
}

func TestRepositoryHostRequestEnvelopesRoundTripJSON(t *testing.T) {
	start := StartRequest{Policy: testPolicy(), AuthContext: AuthContext{Principal: testPrincipal}}
	inspect := InspectRequest{RunID: "run-123", AuthContext: AuthContext{Principal: testPrincipal}}
	apply := ApplyRequest{
		RunID:       "run-123",
		Action:      ControlAction{Kind: ActionRespond, Response: "continue"},
		ActionID:    "action-1",
		AuthContext: AuthContext{Principal: testPrincipal},
	}
	subscribe := SubscribeRequest{RunID: "run-123", AfterCursor: 7, Limit: 25, AuthContext: AuthContext{Principal: testPrincipal}}

	tests := []struct {
		name     string
		value    any
		fresh    func() any
		wantKeys []string
	}{
		{name: "start request", value: start, fresh: func() any { return &StartRequest{} }, wantKeys: []string{"auth_context", "policy", "request"}},
		{name: "inspect request", value: inspect, fresh: func() any { return &InspectRequest{} }, wantKeys: []string{"auth_context", "run_id"}},
		{name: "apply request", value: apply, fresh: func() any { return &ApplyRequest{} }, wantKeys: []string{"action", "action_id", "auth_context", "run_id"}},
		{name: "subscribe request", value: subscribe, fresh: func() any { return &SubscribeRequest{} }, wantKeys: []string{"after_cursor", "auth_context", "limit", "run_id"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal(tt.value)
			if err != nil {
				t.Fatal(err)
			}
			pointer := tt.fresh()
			if err := json.Unmarshal(raw, pointer); err != nil {
				t.Fatal(err)
			}
			decoded := reflect.ValueOf(pointer).Elem().Interface()
			if !reflect.DeepEqual(tt.value, decoded) {
				t.Fatalf("round-tripped envelope = %+v, want %+v", decoded, tt.value)
			}
			var envelope map[string]json.RawMessage
			if err := json.Unmarshal(raw, &envelope); err != nil {
				t.Fatal(err)
			}
			keys := make([]string, 0, len(envelope))
			for key := range envelope {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			if !reflect.DeepEqual(keys, tt.wantKeys) {
				t.Fatalf("marshaled keys = %v, want stable tags %v", keys, tt.wantKeys)
			}
		})
	}
	// Request stays the zero agentrun.RunRequest on purpose: its fields are
	// unexported by design because raw prompts never travel as JSON — they
	// reach durable storage as derived canonical identities.
}

func waitForStateViaHost(t *testing.T, host *InProcessHost, runID agentrun.Identity, want agentrun.LifecycleState) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		inspection, err := host.Inspect(context.Background(), InspectRequest{RunID: runID, AuthContext: AuthContext{Principal: testPrincipal}})
		if err == nil && inspection.Projection.State == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("state never reached %q through the repository host", want)
}
