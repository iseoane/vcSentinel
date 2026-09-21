package daemon

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vcSentinel/internal/execution"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
)

// settleBlockedRun releases a blockingAdapter run and waits until its worker
// finished every durable write, so teardown cannot race an unsettled run.
// safeClose makes an early test failure through t.Fatal still close exactly
// once via the deferred call.
func settleBlockedRun(t *testing.T, conn net.Conn, release chan struct{}, runID agentrun.Identity, safeClose *sync.Once) {
	t.Helper()
	safeClose.Do(func() { close(release) })
	waitForStateViaWire(t, conn, runID, agentrun.StateSucceeded)
}

// assertRemoteSentinel proves remote identity equals local identity: the
// decoded wire error must satisfy errors.Is against the very sentinel the
// server-side failure carried.
func assertRemoteSentinel(t *testing.T, response wireResponse, sentinel error) {
	t.Helper()
	if response.OK || response.Error == nil {
		t.Fatalf("operation succeeded, want failure carrying %v", sentinel)
	}
	remote := &RemoteError{Code: response.Error.Code, Message: response.Error.Message}
	if !errors.Is(remote, sentinel) {
		t.Fatalf("remote error code %q does not resolve errors.Is against %v (message %q)",
			response.Error.Code, sentinel, response.Error.Message)
	}
}

// TestServerErrorVocabularyParityOverWire proves, sentinel by sentinel, that
// errors.Is REMOTELY equals locally: each case reproduces the failure both
// through the wire (asserting RemoteError resolves the sentinel) and through
// the direct controller or InProcessHost twin (asserting the same identity).
func TestServerErrorVocabularyParityOverWire(t *testing.T) {
	fingerprint := testFingerprint()

	t.Run("missing principal", func(t *testing.T) {
		controller := newTestController(t, immediateAdapter("done"))
		ep := startTestServer(t, controller)
		conn := connectClient(t, ep, fingerprint, "")

		envelope := testStartEnvelope()
		envelope.AuthContext = execution.AuthContext{}
		assertRemoteSentinel(t, callOp(t, conn, OpStart, envelope), execution.ErrMissingPrincipal)

		host := execution.NewInProcessHost(newTestController(t, immediateAdapter("done")))
		_, localErr := host.Start(context.Background(), envelope)
		if !errors.Is(localErr, execution.ErrMissingPrincipal) {
			t.Fatalf("local twin error = %v, want ErrMissingPrincipal", localErr)
		}
	})

	t.Run("unsupported action", func(t *testing.T) {
		release := make(chan struct{})
		var safeClose sync.Once
		defer safeClose.Do(func() { close(release) })
		controller := newTestController(t, blockingAdapter(release))
		ep := startTestServer(t, controller)
		conn := connectClient(t, ep, fingerprint, "")

		started := callOp(t, conn, OpStart, testStartEnvelope())
		var handle execution.Handle
		decodeBodyInto(t, started.Body, &handle)

		request := execution.ApplyRequest{
			RunID: handle.RunID, Action: execution.ControlAction{Kind: "detonate"},
			ActionID: "unsupported-1", AuthContext: testAuth(),
		}
		assertRemoteSentinel(t, callOp(t, conn, OpApply, request), execution.ErrUnsupportedAction)

		_, localErr := controller.Apply(context.Background(), request.RunID, request.Action)
		if !errors.Is(localErr, execution.ErrUnsupportedAction) {
			t.Fatalf("local error = %v, want ErrUnsupportedAction", localErr)
		}
		settleBlockedRun(t, conn, release, handle.RunID, &safeClose)
	})

	t.Run("duplicate action replay", func(t *testing.T) {
		release := make(chan struct{})
		defer close(release)
		controller := newTestController(t, blockingAdapter(release))
		ep := startTestServer(t, controller)
		conn := connectClient(t, ep, fingerprint, "")

		started := callOp(t, conn, OpStart, testStartEnvelope())
		var handle execution.Handle
		decodeBodyInto(t, started.Body, &handle)

		first := execution.ApplyRequest{
			RunID: handle.RunID, Action: execution.ControlAction{Kind: execution.ActionAbort},
			ActionID: "dup-1", AuthContext: testAuth(),
		}
		if response := callOp(t, conn, OpApply, first); !response.OK {
			t.Fatalf("first apply failed: %+v", response.Error)
		}
		assertRemoteSentinel(t, callOp(t, conn, OpApply, first), execution.ErrDuplicateAction)

		// Local parity through InProcessHost on a twin fixture.
		twinRelease := make(chan struct{})
		defer close(twinRelease)
		twinHost := execution.NewInProcessHost(newTestController(t, blockingAdapter(twinRelease)))
		twinHandle, err := twinHost.Start(context.Background(), execution.StartRequest{
			Request:     agentrun.NewRunRequest(agentrun.Candidate("c"), agentrun.Prompt("p"), nil),
			Policy:      store.RunPolicy{ID: "twin-policy"},
			AuthContext: testAuth(),
		})
		if err != nil {
			t.Fatalf("twin start: %v", err)
		}
		replay := execution.ApplyRequest{
			RunID: twinHandle.RunID, Action: execution.ControlAction{Kind: execution.ActionAbort},
			ActionID: "dup-1", AuthContext: testAuth(),
		}
		if _, err := twinHost.Apply(context.Background(), replay); err != nil {
			t.Fatalf("twin first apply: %v", err)
		}
		if _, replayErr := twinHost.Apply(context.Background(), replay); !errors.Is(replayErr, execution.ErrDuplicateAction) {
			t.Fatalf("twin replay error = %v, want ErrDuplicateAction", replayErr)
		}
	})

	t.Run("decision not pending while running", func(t *testing.T) {
		release := make(chan struct{})
		var safeClose sync.Once
		defer safeClose.Do(func() { close(release) })
		controller := newTestController(t, blockingAdapter(release))
		ep := startTestServer(t, controller)
		conn := connectClient(t, ep, fingerprint, "")

		started := callOp(t, conn, OpStart, testStartEnvelope())
		var handle execution.Handle
		decodeBodyInto(t, started.Body, &handle)
		waitForStateViaWire(t, conn, handle.RunID, agentrun.StateRunning)

		action := execution.ControlAction{Kind: execution.ActionRespond, Response: "continue"}
		request := execution.ApplyRequest{
			RunID: handle.RunID, Action: action,
			ActionID: "pending-1", AuthContext: testAuth(),
		}
		assertRemoteSentinel(t, callOp(t, conn, OpApply, request), execution.ErrDecisionNotPending)

		_, localErr := controller.Apply(context.Background(), request.RunID, action)
		if !errors.Is(localErr, execution.ErrDecisionNotPending) {
			t.Fatalf("local error = %v, want ErrDecisionNotPending", localErr)
		}
		settleBlockedRun(t, conn, release, handle.RunID, &safeClose)
	})

	t.Run("execution not found for unknown run", func(t *testing.T) {
		controller := newTestController(t, immediateAdapter("done"))
		ep := startTestServer(t, controller)
		conn := connectClient(t, ep, fingerprint, "")

		assertRemoteSentinel(t, callOp(t, conn, OpApply, execution.ApplyRequest{
			RunID: "no-such-run", Action: execution.ControlAction{Kind: execution.ActionAbort},
			ActionID: "notfound-1", AuthContext: testAuth(),
		}), store.ErrExecutionNotFound)

		_, localErr := controller.Apply(context.Background(), "no-such-run",
			execution.ControlAction{Kind: execution.ActionAbort})
		if !errors.Is(localErr, store.ErrExecutionNotFound) {
			t.Fatalf("local error = %v, want ErrExecutionNotFound", localErr)
		}
	})

	t.Run("run already exists on identical wire request", func(t *testing.T) {
		controller := newTestController(t, immediateAdapter("done"))
		ep := startTestServer(t, controller)
		conn := connectClient(t, ep, fingerprint, "")

		started := callOp(t, conn, OpStart, testStartEnvelope())
		if !started.OK {
			t.Fatalf("first wire start failed: %+v", started.Error)
		}
		var handle execution.Handle
		decodeBodyInto(t, started.Body, &handle)
		// Let the winner settle before replaying so no worker write races
		// the test's temp-dir teardown.
		waitForStateViaWire(t, conn, handle.RunID, agentrun.StateSucceeded)
		assertRemoteSentinel(t, callOp(t, conn, OpStart, testStartEnvelope()), execution.ErrRunAlreadyExists)

		host := execution.NewInProcessHost(newTestController(t, immediateAdapter("done")))
		envelope := testStartEnvelope()
		twinHandle, err := host.Start(context.Background(), envelope)
		if err != nil {
			t.Fatalf("twin first start: %v", err)
		}
		// Settle the twin worker so its durable writes cannot race the
		// test's temp-dir teardown.
		if _, err := twinHandle.Wait(context.Background()); err != nil {
			t.Fatalf("twin wait: %v", err)
		}
		if _, secondErr := host.Start(context.Background(), envelope); !errors.Is(secondErr, execution.ErrRunAlreadyExists) {
			t.Fatalf("twin second start error = %v, want ErrRunAlreadyExists", secondErr)
		}
	})

	t.Run("controller not ready", func(t *testing.T) {
		broken := execution.NewController(nil, nil)
		ep := startTestServer(t, broken)
		conn := connectClient(t, ep, fingerprint, "")

		assertRemoteSentinel(t, callOp(t, conn, OpStart, testStartEnvelope()), execution.ErrControllerNotReady)

		_, localErr := broken.Start(context.Background(), agentrun.RunRequest{}, store.RunPolicy{ID: "p"})
		if !errors.Is(localErr, execution.ErrControllerNotReady) {
			t.Fatalf("local error = %v, want ErrControllerNotReady", localErr)
		}
	})
}

// TestStaleRevisionVocabularyLocallyAndOnTheCodec pins the stale-revision
// registry entry end to end: driving controller.Retry directly with a wrong
// expected revision produces the local identity, and the codec half proves
// the code round-trips so the retry op inherits remote identity for free
// (the full remote path is proven against the real socket in
// server_recover_retry_test.go). The same holds for daemon-owned failures,
// which never traverse this transport (a live owner refuses binds before any
// connection exists).
func TestStaleRevisionVocabularyLocallyAndOnTheCodec(t *testing.T) {
	controller := newTestController(t, immediateAdapter("done"))
	host := execution.NewInProcessHost(controller)
	handle, err := host.Start(context.Background(), testStartEnvelope())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatalf("wait: %v", err)
	}

	_, staleErr := controller.Retry(context.Background(), handle.RunID, 9999)
	if !errors.Is(staleErr, execution.ErrStaleRevision) {
		t.Fatalf("retry with wrong revision error = %v, want ErrStaleRevision chain", staleErr)
	}

	code := errorCodeFor(staleErr)
	if code != "execution.stale_revision" {
		t.Fatalf("stale revision classified as %q, want execution.stale_revision", code)
	}
	remote := &RemoteError{Code: code, Message: staleErr.Error()}
	if !errors.Is(remote, execution.ErrStaleRevision) {
		t.Fatal("remote decoding of the stale-revision code lost sentinel identity")
	}

	if ownedCode := errorCodeFor(ErrDaemonOwned); ownedCode != "daemon.daemon_owned" {
		t.Fatalf("daemon-owned classified as %q, want daemon.daemon_owned", ownedCode)
	}
}
