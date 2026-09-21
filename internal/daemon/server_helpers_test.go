package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vcSentinel/internal/execution"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
)

// testStartEnvelope carries the explicit Candidate/Prompt admission form
// (slice 2b): raw prompts travel as the transport-safe string pair and the
// server reconstructs the canonical agentrun.RunRequest via
// execution.ResolveAdmissionRequest. The payload is constant on purpose, so
// replaying the envelope on one store deterministically collides with
// ErrRunAlreadyExists by construction.
func testStartEnvelope() execution.StartRequest {
	return execution.StartRequest{
		Candidate:   "candidate:wire-test",
		Prompt:      "prompt:wire-test",
		Policy:      store.RunPolicy{ID: "wire-test-policy"},
		AuthContext: execution.AuthContext{Principal: testPrincipal},
	}
}

const (
	testPrincipal       = "test-principal"
	testFingerprintRoot = "/vcsentinel-wire-test-repo"
)

// funcAdapter turns closures into execution.Adapter implementations.
type funcAdapter func(ctx context.Context, job agentrun.LogicalJob, env agentrun.InvocationEnvelope, response string) (execution.AdapterResult, error)

func (f funcAdapter) Execute(ctx context.Context, job agentrun.LogicalJob, env agentrun.InvocationEnvelope, response string) (execution.AdapterResult, error) {
	return f(ctx, job, env, response)
}

func immediateAdapter(output string) funcAdapter {
	return funcAdapter(func(context.Context, agentrun.LogicalJob, agentrun.InvocationEnvelope, string) (execution.AdapterResult, error) {
		return execution.AdapterResult{Output: output}, nil
	})
}

// blockingAdapter keeps the run in StateRunning until release closes, which
// gives abort and decision-pending scenarios a stable running target.
func blockingAdapter(release <-chan struct{}) funcAdapter {
	return funcAdapter(func(ctx context.Context, _ agentrun.LogicalJob, _ agentrun.InvocationEnvelope, _ string) (execution.AdapterResult, error) {
		select {
		case <-release:
			return execution.AdapterResult{Output: "released"}, nil
		case <-ctx.Done():
			return execution.AdapterResult{}, ctx.Err()
		}
	})
}

func newTestController(t *testing.T, adapter execution.Adapter) *execution.Controller {
	t.Helper()
	return execution.NewController(store.NewStore(t.TempDir()), adapter)
}

// startTestServer binds the platform default endpoint for a fresh daemon
// directory, serves it in the background, and wires cleanup. It skips when
// the platform cannot provide its default transport (unix sockets).
func startTestServer(t *testing.T, controller *execution.Controller) Endpoint {
	t.Helper()
	ep := DefaultEndpoint(filepath.Join(t.TempDir(), "daemon"))
	listener, err := Listen(ep)
	if err != nil {
		if ep.Network == "unix" {
			t.Skipf("unix domain sockets unavailable: %v", err)
		}
		t.Fatalf("listen %s endpoint: %v", ep.Network, err)
	}
	srv := NewServer(controller, FingerprintRepository(testFingerprintRoot))
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() {
		// Graceful shutdown drains in-flight dispatches and settles any
		// detached controller workers before the controller's TempDir cleanup
		// removes the backing store. Immediate Close alone can leave an
		// observation/finalization write racing that teardown.
		if err := srv.Shutdown(); err != nil {
			srv.Close()
		}
	})
	t.Cleanup(func() { _ = listener.Close() })
	return ep
}

func testFingerprint() string { return FingerprintRepository(testFingerprintRoot) }

func connectClient(t *testing.T, ep Endpoint, fingerprint, token string) net.Conn {
	t.Helper()
	conn, err := Dial(ep)
	if err != nil {
		t.Fatalf("dial %s://%s: %v", ep.Network, ep.Address, err)
	}
	handshake, err := json.Marshal(handshakeRequest{
		ProtocolRevision: ProtocolRevision,
		Repository:       fingerprint,
		Token:            token,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteFrame(conn, handshake); err != nil {
		t.Fatalf("send handshake: %v", err)
	}
	frame, err := ReadFrame(conn)
	if err != nil {
		t.Fatalf("read handshake response: %v", err)
	}
	var response handshakeResponse
	if err := json.Unmarshal(frame, &response); err != nil {
		t.Fatalf("decode handshake response: %v", err)
	}
	if !response.OK {
		detail := ""
		if response.Error != nil {
			detail = fmt.Sprintf(" code=%q message=%q", response.Error.Code, response.Error.Message)
		}
		t.Fatalf("handshake rejected:%s", detail)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func callOp(t *testing.T, conn net.Conn, op string, body any) wireResponse {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(wireRequest{Op: op, Body: raw})
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteFrame(conn, request); err != nil {
		t.Fatalf("send %s request: %v", op, err)
	}
	frame, err := ReadFrame(conn)
	if err != nil {
		t.Fatalf("read %s response: %v", op, err)
	}
	var response wireResponse
	if err := json.Unmarshal(frame, &response); err != nil {
		t.Fatalf("decode %s response: %v", op, err)
	}
	return response
}

func decodeBodyInto(t *testing.T, raw json.RawMessage, target any) {
	t.Helper()
	if len(raw) == 0 {
		t.Fatal("response body empty")
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
}

func testAuth() execution.AuthContext { return execution.AuthContext{Principal: testPrincipal} }

// waitForStateViaWire polls inspect until the run reaches want or the
// deadline expires.
func waitForStateViaWire(t *testing.T, conn net.Conn, runID agentrun.Identity, want agentrun.LifecycleState) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response := callOp(t, conn, OpInspect, execution.InspectRequest{RunID: runID, AuthContext: testAuth()})
		if response.OK {
			var inspection execution.Inspection
			decodeBodyInto(t, response.Body, &inspection)
			if inspection.Projection.State == want {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("run %s never reached state %q through the wire", runID, want)
}
