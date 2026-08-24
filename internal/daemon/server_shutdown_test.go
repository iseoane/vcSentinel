package daemon

import (
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// startGracedServer mirrors startTestServer but exposes the server itself and
// the Serve return value, which the graceful-shutdown contract pins (Serve
// must come back nil). grace <= 0 selects DefaultGracePeriod.
func startGracedServer(t *testing.T, adapter execution.Adapter, grace time.Duration) (*Server, *execution.Controller, *store.Store, Endpoint, <-chan error) {
	t.Helper()
	st := store.NuevoStore(t.TempDir())
	controller := execution.NewController(st, adapter)
	ep := DefaultEndpoint(t.TempDir() + "/daemon")
	listener, err := Listen(ep)
	if err != nil {
		if ep.Network == "unix" {
			t.Skipf("unix domain sockets unavailable: %v", err)
		}
		t.Fatalf("listen %s endpoint: %v", ep.Network, err)
	}
	var server *Server
	if grace <= 0 {
		server = NewServer(controller, testFingerprint())
	} else {
		server = NewServerWithGrace(controller, testFingerprint(), grace)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	// Deterministic lifecycle ordering: never return before Serve registered
	// its listener, or a shutdown could finalize an unregistered one.
	deadline := time.Now().Add(5 * time.Second)
	for {
		server.mu.Lock()
		registered := server.listener != nil
		server.mu.Unlock()
		if registered {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Serve never registered its listener")
		}
		time.Sleep(pollInterval)
	}
	t.Cleanup(server.Close)
	t.Cleanup(func() { _ = listener.Close() })
	return server, controller, st, ep, serveErr
}

// tryCallOp is callOp for goroutines: identical framing without testing.Fatal.
func tryCallOp(conn net.Conn, op string, body any) (wireResponse, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return wireResponse{}, err
	}
	request, err := json.Marshal(wireRequest{Op: op, Body: raw})
	if err != nil {
		return wireResponse{}, err
	}
	if err := WriteFrame(conn, request); err != nil {
		return wireResponse{}, err
	}
	frame, err := ReadFrame(conn)
	if err != nil {
		return wireResponse{}, err
	}
	var response wireResponse
	if err := json.Unmarshal(frame, &response); err != nil {
		return wireResponse{}, err
	}
	return response, nil
}

func await[T any](t *testing.T, ch <-chan T, timeout time.Duration) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(timeout):
		t.Fatalf("channel did not deliver within %s", timeout)
		var zero T
		return zero
	}
}

// errorText renders a wire error for diagnostics; empty when absent.
func errorText(err *wireError) string {
	if err == nil {
		return ""
	}
	return err.Code + ": " + err.Message
}

func TestGracefulShutdownCompletesFastAndReleasesEndpoint(t *testing.T) {
	_, _, _, ep, serveErr := startGracedServer(t, immediateAdapter("done"), 0)
	conn := connectClient(t, ep, testFingerprint(), "")

	started := time.Now()
	response := callOp(t, conn, OpShutdown, ShutdownRequest{AuthContext: testAuth()})
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("idle graceful shutdown took %s, want a fast drain", elapsed)
	}
	if !response.OK {
		t.Fatalf("shutdown rejected: %+v", response.Error)
	}
	var result ShutdownResult
	decodeBodyInto(t, response.Body, &result)
	if result.OrphanedRuns != 0 {
		t.Fatalf("orphaned runs = %d, want zero for an idle server", result.OrphanedRuns)
	}

	if err := await(t, serveErr, 5*time.Second); err != nil {
		t.Fatalf("Serve returned %v, want nil after graceful shutdown", err)
	}
	if _, err := Dial(ep); err == nil {
		t.Fatal("dial succeeded after graceful shutdown; endpoint must stop answering")
	}

	// A fresh owner can take the endpoint over cleanly (stale socket reclaim
	// on unix; a fresh bind elsewhere), proving no lifecycle residue.
	relistener, err := Listen(ep)
	if err != nil {
		t.Fatalf("fresh Listen after graceful shutdown: %v", err)
	}
	defer func() { _ = relistener.Close() }()
	fresh := NewServer(execution.NewController(store.NuevoStore(t.TempDir()), immediateAdapter("done")), testFingerprint())
	t.Cleanup(fresh.Close)
	go func() { _ = fresh.Serve(relistener) }()
	reconnect := connectClient(t, ep, testFingerprint(), "")
	callOp(t, reconnect, OpSubscribe, execution.SubscribeRequest{
		RunID: agentrun.Identity("no-such-run"), AuthContext: testAuth(),
	})
}

func TestGraceHonoredThenSurvivorOrphanedWithEvidence(t *testing.T) {
	release := make(chan struct{})
	// releaseAdapter is idempotent so the deterministic pre-return drain
	// below and this failure-path safety net can both fire without panicking.
	releaseAdapter := sync.OnceFunc(func() { close(release) })
	defer releaseAdapter()
	server, controller, st, ep, _ := startGracedServer(t, blockingAdapter(release), 250*time.Millisecond)

	conn := connectClient(t, ep, testFingerprint(), "")
	response := callOp(t, conn, OpStart, testStartEnvelope())
	if !response.OK {
		t.Fatalf("start failed: %+v", response.Error)
	}
	var handle execution.Handle
	decodeBodyInto(t, response.Body, &handle)
	waitForStateViaWire(t, conn, handle.RunID, agentrun.StateRunning)

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- server.Shutdown() }()

	time.Sleep(80 * time.Millisecond)
	select {
	case err := <-shutdownDone:
		t.Fatalf("Shutdown returned %v before the grace budget expired", err)
	default:
	}
	inspection, err := controller.Inspect(t.Context(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Projection.Terminal != agentrun.TerminalNone {
		t.Fatalf("projection = %+v, want the blocked invocation untouched within budget", inspection.Projection)
	}

	if err := await(t, shutdownDone, 5*time.Second); err != nil {
		t.Fatalf("Shutdown error = %v", err)
	}

	inspection, err = controller.Inspect(t.Context(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	last := inspection.Outcomes[len(inspection.Outcomes)-1]
	if inspection.Projection.State != agentrun.StateCanceled || last.Class != agentrun.OutcomeCancellation ||
		!strings.Contains(last.Error, OrphanedByShutdown) {
		t.Fatalf("survivor = %+v / %+v, want canceled with orphaned reason", inspection.Projection, last)
	}
	entries, err := store.ScanRecoveries(st)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.RunID == string(handle.RunID) {
			t.Fatalf("ScanRecoveries still reports the orphaned run: %+v", entry)
		}
	}

	// The repository accepts work again immediately after the settlement: a
	// fresh owner takes over the endpoint and starts a new run cleanly.
	relistener, err := Listen(ep)
	if err != nil {
		t.Fatalf("fresh Listen after orphaning: %v", err)
	}
	defer func() { _ = relistener.Close() }()
	fresh := NewServer(controller, testFingerprint())
	t.Cleanup(fresh.Close)
	go func() { _ = fresh.Serve(relistener) }()
	reconnect := connectClient(t, ep, testFingerprint(), "")
	next := testStartEnvelope()
	next.Candidate = "candidate:after-orphaning"
	next.Prompt = "prompt:after-orphaning"
	if response := callOp(t, reconnect, OpStart, next); !response.OK {
		t.Fatalf("post-shutdown start failed: %+v", response.Error)
	} else {
		var successor execution.Handle
		decodeBodyInto(t, response.Body, &successor)

		// Teardown drain (slice-2b discipline): this successor blocks inside
		// blockingAdapter on release; if the test returned with it still
		// blocked, the deferred close would wake its controller worker mid
		// t.TempDir removal and it would persist settlement evidence into a
		// directory being deleted. Release deterministically here, observe
		// the settlement through the wire, then require the store to prove
		// quiescence before returning.
		releaseAdapter()
		waitForStateViaWire(t, reconnect, successor.RunID, agentrun.StateSucceeded)
		awaitStoreQuiescence(t, controller, handle.RunID, successor.RunID)
	}
}

// awaitStoreQuiescence proves no writer is left touching the run directory:
// it polls store-side inspections until two consecutive snapshots taken one
// full poll cycle apart are identical, so every settlement writer has
// quiesced before t.TempDir removes the store root.
func awaitStoreQuiescence(t *testing.T, controller *execution.Controller, ids ...agentrun.Identity) {
	t.Helper()
	snapshot := inspectAll(t, controller, ids)
	deadline := time.Now().Add(5 * time.Second)
	for {
		time.Sleep(pollInterval)
		next := inspectAll(t, controller, ids)
		if reflect.DeepEqual(snapshot, next) {
			return
		}
		snapshot = next
		if time.Now().After(deadline) {
			t.Fatal("run store never quiesced before teardown")
		}
	}
}

// inspectAll reads the durable inspection of every named run from the store.
func inspectAll(t *testing.T, controller *execution.Controller, ids []agentrun.Identity) []execution.Inspection {
	t.Helper()
	snapshots := make([]execution.Inspection, 0, len(ids))
	for _, id := range ids {
		inspection, err := controller.Inspect(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		snapshots = append(snapshots, inspection)
	}
	return snapshots
}

func TestInFlightDispatchDrainsBeforeClosure(t *testing.T) {
	server, _, st, ep, serveErr := startGracedServer(t, immediateAdapter("done"), DefaultGracePeriod)
	conn := connectClient(t, ep, testFingerprint(), "")

	// Hold the admission mutex so one OpStart dispatch stays in flight.
	server.admission.Lock()
	startReply := make(chan wireResponse, 1)
	go func() {
		reply, err := tryCallOp(conn, OpStart, testStartEnvelope())
		if err == nil {
			startReply <- reply
		}
	}()

	// Deterministically wait until the dispatch actually entered the server
	// (it blocks on the admission mutex inside handleStart) before
	// initiating the shutdown, so the drain observation is never a race.
	deadline := time.Now().Add(5 * time.Second)
	for {
		server.mu.Lock()
		counted := server.inflightCount
		server.mu.Unlock()
		if counted >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("start dispatch was never counted as in-flight")
		}
		time.Sleep(pollInterval)
	}

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- server.Shutdown() }()

	time.Sleep(150 * time.Millisecond)
	select {
	case reply := <-startReply:
		t.Fatalf("in-flight start answered during drain: %+v", reply)
	case err := <-shutdownDone:
		t.Fatalf("Shutdown returned %v while a dispatch was still in flight", err)
	default:
	}

	server.admission.Unlock()

	// Drain completion is proven durably: the blocked dispatch must have
	// finished admitting its run (a canceled context would have created
	// nothing), and only then may Shutdown come back.
	pollDeadline := time.Now().Add(5 * time.Second)
	for {
		ids, err := st.ListExecutionIDs()
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) == 1 {
			break
		}
		if time.Now().After(pollDeadline) {
			t.Fatalf("drained start never admitted its run; executions = %v", ids)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := await(t, shutdownDone, 5*time.Second); err != nil {
		t.Fatalf("Shutdown error = %v", err)
	}
	if err := await(t, serveErr, 5*time.Second); err != nil {
		t.Fatalf("Serve returned %v, want nil", err)
	}

	entries, err := store.ScanRecoveries(st)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Class == store.RecoveryOperatorRequired {
			t.Fatalf("shutdown left operator_required residue: %+v", entry)
		}
	}
}

func TestShutdownOpRequiresPrincipalWithoutTriggeringDrain(t *testing.T) {
	_, controller, _, ep, _ := startGracedServer(t, immediateAdapter("done"), DefaultGracePeriod)
	handle, err := controller.Start(t.Context(),
		execution.ResolveAdmissionRequest(testStartEnvelope()), testStartEnvelope().Policy)
	if err != nil {
		t.Fatal(err)
	}
	conn := connectClient(t, ep, testFingerprint(), "")

	response := callOp(t, conn, OpShutdown, ShutdownRequest{})
	if response.OK || response.Error == nil || response.Error.Code != "execution.missing_principal" {
		t.Fatalf("unauthenticated shutdown = %+v, want execution.missing_principal rejection", response)
	}

	// No drain happened: existing operations and new connections still work.
	probe := callOp(t, conn, OpInspect, execution.InspectRequest{RunID: handle.RunID, AuthContext: testAuth()})
	if !probe.OK || strings.Contains(errorText(probe.Error), "shutting_down") {
		t.Fatalf("inspect after rejected shutdown = %+v (%s), want a normally served operation", probe, errorText(probe.Error))
	}
	connectClient(t, ep, testFingerprint(), "")
}

func TestDoubleAndPostCloseShutdownAreDeterministic(t *testing.T) {
	server, _, _, _, serveErr := startGracedServer(t, immediateAdapter("done"), time.Millisecond)
	first := server.Shutdown()
	if first != nil {
		t.Fatalf("first Shutdown error = %v", first)
	}
	if err := await(t, serveErr, 5*time.Second); err != nil {
		t.Fatalf("Serve returned %v, want nil", err)
	}
	second := server.Shutdown()
	if !errors.Is(second, errServerClosed) {
		t.Fatalf("double Shutdown error = %v, want errServerClosed", second)
	}

	closed := NewServer(execution.NewController(store.NuevoStore(t.TempDir()), immediateAdapter("done")), testFingerprint())
	closed.Close()
	if err := closed.Shutdown(); !errors.Is(err, errServerClosed) {
		t.Fatalf("Shutdown after Close error = %v, want errServerClosed", err)
	}

	// Concurrent racers: exactly one wins the sequence, every loser gets a
	// deterministic refusal, nothing panics.
	racing := NewServer(execution.NewController(store.NuevoStore(t.TempDir()), immediateAdapter("done")), testFingerprint())
	t.Cleanup(racing.Close)
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { results <- racing.Shutdown() }()
	}
	wins, losses := 0, 0
	for i := 0; i < 2; i++ {
		err := await(t, results, 5*time.Second)
		switch {
		case err == nil:
			wins++
		case errors.Is(err, ErrDaemonShuttingDown) || errors.Is(err, errServerClosed):
			losses++
		default:
			t.Fatalf("racer got unexpected error %v", err)
		}
	}
	if wins != 1 || losses != 1 {
		t.Fatalf("races: %d wins / %d losses, want exactly one winner", wins, losses)
	}
}
