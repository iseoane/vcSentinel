package daemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// startTestServerForRemote is startTestServer with the endpoint resolved for
// RemoteHost clients: an ephemeral ":0" tcp bind is rewritten to its live
// bound address before any client names it.
func startTestServerForRemote(t *testing.T, controller *execution.Controller) Endpoint {
	t.Helper()
	ep := DefaultEndpoint(filepath.Join(t.TempDir(), "daemon"))
	listener, err := Listen(ep)
	if err != nil {
		if ep.Network == "unix" {
			t.Skipf("unix domain sockets unavailable: %v", err)
		}
		t.Fatalf("listen %s endpoint: %v", ep.Network, err)
	}
	srv := NewServer(controller, testFingerprint())
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(srv.Close)
	t.Cleanup(func() { _ = listener.Close() })
	if ep.Network == "tcp" {
		ep.Address = listener.Addr().String()
	}
	return ep
}

func dialRemoteHostForTest(t *testing.T, ep Endpoint) *RemoteHost {
	t.Helper()
	host, err := DialRemoteHost(ep, testFingerprint())
	if err != nil {
		t.Fatalf("DialRemoteHost(%s://%s): %v", ep.Network, ep.Address, err)
	}
	t.Cleanup(func() { _ = host.Close() })
	return host
}

// exerciseRemoteFourOpRoundTrip drives the full operation cycle through the
// CLIENT adapter: explicit-payload Start, Inspect polling, Subscribe paging,
// and Apply abort — then spot-checks the sentinel parity contract
// (ErrMissingPrincipal, store.ErrExecutionNotFound, ErrDuplicateAction
// replay) through errors.Is on the client side.
func exerciseRemoteFourOpRoundTrip(t *testing.T, host *RemoteHost) {
	t.Helper()

	started, err := host.Start(context.Background(), execution.StartRequest{
		Candidate:   "candidate:remote-client",
		Prompt:      "prompt:remote-client",
		Policy:      store.RunPolicy{ID: "wire-client-policy"},
		AuthContext: testAuth(),
	})
	if err != nil {
		t.Fatalf("remote Start: %v", err)
	}
	if started.RunID == "" || started.JobID == "" {
		t.Fatalf("remote Start handle = %+v, want a named run and job", started)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		inspection, inspectErr := host.Inspect(context.Background(), execution.InspectRequest{
			RunID:       started.RunID,
			AuthContext: testAuth(),
		})
		if inspectErr == nil && inspection.Projection.State == agentrun.StateRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("run never reached StateRunning through the remote client")
		}
		time.Sleep(5 * time.Millisecond)
	}

	page, err := host.Subscribe(context.Background(), execution.SubscribeRequest{
		RunID:       started.RunID,
		AfterCursor: 0,
		Limit:       10,
		AuthContext: testAuth(),
	})
	if err != nil || len(page.Events) < 3 {
		t.Fatalf("remote Subscribe = %d events, %v, want at least the three admission transitions", len(page.Events), err)
	}

	abort := execution.ApplyRequest{
		RunID:       started.RunID,
		Action:      execution.ControlAction{Kind: execution.ActionAbort},
		ActionID:    "action:remote-abort",
		AuthContext: testAuth(),
	}
	result, err := host.Apply(context.Background(), abort)
	if err != nil || !result.Accepted {
		t.Fatalf("remote Apply(abort) = %+v, %v, want accepted", result, err)
	}

	// Sentinel parity through the CLIENT, not raw frames.
	if _, replayErr := host.Apply(context.Background(), abort); !errors.Is(replayErr, execution.ErrDuplicateAction) {
		t.Fatalf("replayed remote Apply error = %v, want errors.Is ErrDuplicateAction", replayErr)
	}
	principalless := testStartEnvelope()
	principalless.AuthContext = execution.AuthContext{}
	if _, principalErr := host.Start(context.Background(), principalless); !errors.Is(principalErr, execution.ErrMissingPrincipal) {
		t.Fatalf("principalless remote Start error = %v, want errors.Is ErrMissingPrincipal", principalErr)
	}
	if _, notFoundErr := host.Inspect(context.Background(), execution.InspectRequest{
		RunID: "no-such-run", AuthContext: testAuth(),
	}); !errors.Is(notFoundErr, store.ErrExecutionNotFound) {
		t.Fatalf("unknown-run remote Inspect error = %v, want errors.Is store.ErrExecutionNotFound", notFoundErr)
	}
}

// TestRemoteClientRoundTripOverEveryTransport proves the four-op round trip
// plus sentinel parity over BOTH real transports: the platform default and
// forced loopback tcp with its bearer-token handshake path.
func TestRemoteClientRoundTripOverEveryTransport(t *testing.T) {
	t.Run("platform default transport", func(t *testing.T) {
		release := make(chan struct{})
		defer close(release)
		controller := newTestController(t, blockingAdapter(release))
		ep := startTestServerForRemote(t, controller)
		exerciseRemoteFourOpRoundTrip(t, dialRemoteHostForTest(t, ep))
	})

	t.Run("forced loopback tcp with bearer token", func(t *testing.T) {
		daemonDirectory := filepath.Join(t.TempDir(), "daemon")
		listenEp := Endpoint{
			Network:   "tcp",
			Address:   "127.0.0.1:0",
			TokenFile: filepath.Join(daemonDirectory, "bearer-token"),
		}
		listener, err := Listen(listenEp)
		if err != nil {
			t.Fatalf("tcp listen: %v", err)
		}
		release := make(chan struct{})
		defer close(release)
		srv := NewServer(newTestController(t, blockingAdapter(release)), testFingerprint())
		go func() { _ = srv.Serve(listener) }()
		t.Cleanup(srv.Close)
		t.Cleanup(func() { _ = listener.Close() })

		ep := Endpoint{Network: "tcp", Address: listener.Addr().String(), TokenFile: listenEp.TokenFile}
		if _, tokenErr := ReadBearerToken(listenEp); tokenErr != nil {
			t.Fatalf("read persisted bearer token: %v", tokenErr)
		}
		exerciseRemoteFourOpRoundTrip(t, dialRemoteHostForTest(t, ep))
	})
}

// TestRemoteStartExplicitPayloadPreservesIdentityAcrossTheWire proves the
// slice 2b transport contract: a wire start carrying the explicit
// Candidate/Prompt form produces a run whose job/run identities match a
// local twin built via agentrun.NewRunRequest with identical inputs, and the
// durable request record stores exactly those candidate/prompt identities.
func TestRemoteStartExplicitPayloadPreservesIdentityAcrossTheWire(t *testing.T) {
	storeDir := t.TempDir()
	release := make(chan struct{})
	var safeClose sync.Once
	// The started run stays blocked until release closes; settle it BEFORE
	// returning so its final durable writes cannot race TempDir cleanup.
	defer safeClose.Do(func() { close(release) })
	controller := execution.NewController(store.NuevoStore(storeDir), blockingAdapter(release))
	ep := startTestServerForRemote(t, controller)
	host := dialRemoteHostForTest(t, ep)

	handle, err := host.Start(context.Background(), execution.StartRequest{
		Candidate:   "candidate:identity-wire",
		Prompt:      "prompt:identity-wire",
		Policy:      store.RunPolicy{ID: "wire-identity-policy"},
		AuthContext: testAuth(),
	})
	if err != nil {
		t.Fatalf("remote Start: %v", err)
	}

	twinRequest := agentrun.NewRunRequest(
		agentrun.Candidate("candidate:identity-wire"), agentrun.Prompt("prompt:identity-wire"), nil)
	twinJob := agentrun.NewLogicalJob(twinRequest)
	if handle.JobID != twinJob.ID() || handle.RunID != twinJob.RunID() {
		t.Fatalf("wire identities = {job:%s run:%s}, want the NewRunRequest twin identities {job:%s run:%s}",
			handle.JobID, handle.RunID, twinJob.ID(), twinJob.RunID())
	}

	durable, err := store.NuevoStore(storeDir).ReadExecutionRequest(string(handle.RunID))
	if err != nil {
		t.Fatal(err)
	}
	if durable.CandidateID != string(twinRequest.Candidate().Identity()) ||
		durable.PromptID != string(twinRequest.Prompt().Identity()) {
		t.Fatalf("durable request identities = {candidate:%s prompt:%s}, want the twin's {candidate:%s prompt:%s}",
			durable.CandidateID, durable.PromptID,
			twinRequest.Candidate().Identity(), twinRequest.Prompt().Identity())
	}

	safeClose.Do(func() { close(release) })
	deadline := time.Now().Add(5 * time.Second)
	for {
		inspection, inspectErr := host.Inspect(context.Background(), execution.InspectRequest{
			RunID:       handle.RunID,
			AuthContext: testAuth(),
		})
		if inspectErr == nil && inspection.Projection.State.TerminalClass() != agentrun.TerminalNone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("wire-started run never settled before teardown")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestRemoteClientCancellationClosesTheWholeConnection pins the one-shot CLI
// discipline: a canceled context fails the call and best-effort closes the
// persistent connection instead of leaving a half-open socket behind.
func TestRemoteClientCancellationClosesTheWholeConnection(t *testing.T) {
	controller := newTestController(t, immediateAdapter("done"))
	ep := startTestServerForRemote(t, controller)
	host := dialRemoteHostForTest(t, ep)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	envelope := testStartEnvelope()
	envelope.AuthContext = testAuth()
	if _, err := host.Start(ctx, envelope); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start under canceled context = %v, want context.Canceled", err)
	}
	// The connection is gone: the next call must fail explicitly instead of
	// silently reusing a closed socket.
	if _, err := host.Inspect(context.Background(), execution.InspectRequest{
		RunID: "x", AuthContext: testAuth(),
	}); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("Inspect after cancellation = %v, want an explicit closed-connection failure", err)
	}
}

// TestRemoteClientMidExchangeFailureYieldsDeterministicClosedError pins the
// post-mortem contract: when the server side drops the connection between
// frames, the failing exchange surfaces its own transport cause — never the
// closed-connection error itself — every later call fails with the
// deterministic closed-connection error, and a following Close still reports
// idempotent success instead of touching the dead socket twice.
func TestRemoteClientMidExchangeFailureYieldsDeterministicClosedError(t *testing.T) {
	ep := DefaultEndpoint(filepath.Join(t.TempDir(), "daemon"))
	listener, err := Listen(ep)
	if err != nil {
		if ep.Network == "unix" {
			t.Skipf("unix domain sockets unavailable: %v", err)
		}
		t.Fatalf("listen %s endpoint: %v", ep.Network, err)
	}
	srv := NewServer(newTestController(t, immediateAdapter("done")), testFingerprint())
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(srv.Close)
	t.Cleanup(func() { _ = listener.Close() })

	host := dialRemoteHostForTest(t, ep)

	// Kill every server-side connection between frames: the next exchange
	// must die mid-exchange on the wire, not at admission logic.
	srv.Close()

	envelope := testStartEnvelope()
	if _, err := host.Start(context.Background(), envelope); err == nil || errors.Is(err, errConnClosed) {
		t.Fatalf("Start against a freshly killed server = %v, want a mid-exchange transport failure", err)
	}
	if _, err := host.Start(context.Background(), envelope); !errors.Is(err, errConnClosed) {
		t.Fatalf("Start after the dead exchange = %v, want the deterministic closed-connection error", err)
	}
	if err := host.Close(); err != nil {
		t.Fatalf("Close after a dead connection = %v, want idempotent success", err)
	}
}

// TestRemoteClientCloseIsIdempotent pins that closing twice is a no-op and
// stays successful, matching io.Closer expectations for cleanup paths.
func TestRemoteClientCloseIsIdempotent(t *testing.T) {
	controller := newTestController(t, immediateAdapter("done"))
	ep := startTestServerForRemote(t, controller)
	host := dialRemoteHostForTest(t, ep)

	if err := host.Close(); err != nil {
		t.Fatalf("first Close = %v, want success", err)
	}
	if err := host.Close(); err != nil {
		t.Fatalf("second Close = %v, want idempotent success", err)
	}
}

// TestRemoteClientHandshakeRejectionSurfacesTypedError proves a guard
// rejection (wrong repository fingerprint) fails DialRemoteHost with a
// deterministic *HandshakeError carrying the server's classification code.
func TestRemoteClientHandshakeRejectionSurfacesTypedError(t *testing.T) {
	controller := newTestController(t, immediateAdapter("done"))
	ep := startTestServerForRemote(t, controller)

	wrongFingerprint := FingerprintRepository(filepath.Join(testFingerprintRoot, "elsewhere"))
	host, err := DialRemoteHost(ep, wrongFingerprint)
	var rejected *HandshakeError
	if !errors.As(err, &rejected) {
		t.Fatalf("DialRemoteHost with wrong fingerprint = %v, want a typed *HandshakeError", err)
	}
	if rejected.Code != codeRepositoryMismatch {
		t.Fatalf("handshake rejection code = %q, want %q", rejected.Code, codeRepositoryMismatch)
	}
	if rejected.Message == "" {
		t.Fatal("handshake rejection carries no diagnosable message")
	}
	if host != nil {
		_ = host.Close()
	}
}

// TestRemoteClientApplyObservesErrDaemonShuttingDownDuringDrain proves the
// graceful-shutdown refusal survives the wire through the CLIENT adapter:
// once the graceful sequence began, an Apply issued over an established
// RemoteHost connection fails with a *RemoteError whose Unwrap resolves
// errors.Is against ErrDaemonShuttingDown remotely — the same sentinel
// identity a local caller would observe.
func TestRemoteClientApplyObservesErrDaemonShuttingDownDuringDrain(t *testing.T) {
	release := make(chan struct{})
	var safeClose sync.Once
	defer safeClose.Do(func() { close(release) })
	server, _, _, ep, serveErr := startGracedServer(t, blockingAdapter(release), 5*time.Second)
	host := dialRemoteHostForTest(t, ep)

	handle, err := host.Start(context.Background(), execution.StartRequest{
		Candidate:   "candidate:shutdown-refusal",
		Prompt:      "prompt:shutdown-refusal",
		Policy:      store.RunPolicy{ID: "wire-shutdown-policy"},
		AuthContext: testAuth(),
	})
	if err != nil {
		t.Fatalf("remote start: %v", err)
	}

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- server.Shutdown() }()

	// Poll with a side-effect-free probe until the refusal carries the
	// classified sentinel: respond on a running run fails without mutating
	// durable state, so pre-flip attempts cannot settle anything and the
	// observation never depends on winning the beginShutdown race.
	deadline := time.Now().Add(2 * time.Second)
	for attempt := 0; ; attempt++ {
		_, applyErr := host.Apply(context.Background(), execution.ApplyRequest{
			RunID:       handle.RunID,
			Action:      execution.ControlAction{Kind: execution.ActionRespond, Response: "probe"},
			ActionID:    fmt.Sprintf("action:shutdown-probe-%d", attempt),
			AuthContext: testAuth(),
		})
		if errors.Is(applyErr, ErrDaemonShuttingDown) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Apply during drain never surfaced ErrDaemonShuttingDown; last error = %v", applyErr)
		}
		time.Sleep(pollInterval)
	}

	safeClose.Do(func() { close(release) })
	if err := await(t, shutdownDone, 10*time.Second); err != nil {
		t.Fatalf("Shutdown error = %v", err)
	}
	if err := await(t, serveErr, 5*time.Second); err != nil {
		t.Fatalf("Serve returned %v, want nil", err)
	}
}
