package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vcSentinel/internal/execution"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
)

// tryHandshake performs one handshake attempt without failing the test so
// guard cases can assert rejection details.
func tryHandshake(t *testing.T, ep Endpoint, revision int, fingerprint, token string) handshakeResponse {
	t.Helper()
	conn, err := Dial(ep)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	handshake, err := json.Marshal(handshakeRequest{ProtocolRevision: revision, Repository: fingerprint, Token: token})
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
	return response
}

func TestServerHandshakeGuards(t *testing.T) {
	controller := newTestController(t, immediateAdapter("done"))
	ep := startTestServer(t, controller)
	fingerprint := testFingerprint()

	wrongRevision := tryHandshake(t, ep, ProtocolRevision+1, fingerprint, "")
	if wrongRevision.OK || wrongRevision.Error == nil || wrongRevision.Error.Code != codeRevisionMismatch {
		t.Fatalf("wrong revision handshake = %+v, want rejection with %q", wrongRevision, codeRevisionMismatch)
	}
	wrongRepository := tryHandshake(t, ep, ProtocolRevision, FingerprintRepository("/some-other-repo"), "")
	if wrongRepository.OK || wrongRepository.Error == nil || wrongRepository.Error.Code != codeRepositoryMismatch {
		t.Fatalf("wrong repository handshake = %+v, want rejection with %q", wrongRepository, codeRepositoryMismatch)
	}
	conn := connectClient(t, ep, fingerprint, "") // correct handshake still works
	// The op loop serves normally after the guards; inspecting an unknown run
	// answers with its classified store sentinel rather than a transport error.
	assertRemoteSentinel(t, callOp(t, conn, OpInspect,
		execution.InspectRequest{RunID: "x", AuthContext: testAuth()}), store.ErrExecutionNotFound)
}

// TestServerTCPTokenEnforcedOnAnyPlatform forces Endpoint{Network:"tcp"} so
// the loopback-plus-bearer-token path — Windows' v1 transport — is exercised
// on Linux CI as well.
func TestServerTCPTokenEnforcedOnAnyPlatform(t *testing.T) {
	daemonDir := filepath.Join(t.TempDir(), "daemon")
	ep := Endpoint{
		Network:   "tcp",
		Address:   "127.0.0.1:0",
		TokenFile: filepath.Join(daemonDir, "bearer-token"),
	}
	listener, err := Listen(ep)
	if err != nil {
		t.Fatalf("tcp listen: %v", err)
	}
	srv := NewServer(newTestController(t, immediateAdapter("done")), testFingerprint())
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(srv.Close)
	t.Cleanup(func() { _ = listener.Close() })

	// An ephemeral ":0" bind must be resolved through the live listener
	// before clients (and SaveEndpoint) can name the real address; this
	// mirrors the slice 3 CLI flow of listen, resolve, persist.
	bound, ok := listener.(interface{ Addr() net.Addr })
	if !ok {
		t.Fatal("tcp listener does not expose its bound address")
	}
	dialable := Endpoint{Network: "tcp", Address: bound.Addr().String(), TokenFile: ep.TokenFile}

	token, err := ReadBearerToken(ep)
	if err != nil {
		t.Fatalf("read persisted bearer token: %v", err)
	}

	noToken := tryHandshake(t, dialable, ProtocolRevision, testFingerprint(), "")
	if noToken.OK || noToken.Error == nil || noToken.Error.Code != codeUnauthorized {
		t.Fatalf("tokenless handshake = %+v, want rejection with %q", noToken, codeUnauthorized)
	}
	wrongToken := tryHandshake(t, dialable, ProtocolRevision, testFingerprint(), "not-the-token")
	if wrongToken.OK || wrongToken.Error == nil || wrongToken.Error.Code != codeUnauthorized {
		t.Fatalf("wrong-token handshake = %+v, want rejection with %q", wrongToken, codeUnauthorized)
	}

	conn := connectClient(t, dialable, testFingerprint(), token)
	// The authenticated op loop serves normally; the unknown run answers with
	// its classified store sentinel instead of a transport failure.
	assertRemoteSentinel(t, callOp(t, conn, OpInspect,
		execution.InspectRequest{RunID: "x", AuthContext: testAuth()}), store.ErrExecutionNotFound)
}

// TestServerConcurrentAppliesSerializeExactlyOnce fires concurrent duplicate
// applies at one running run under -race: exactly one racer may win, every
// other must observe ErrDuplicateAction remotely.
func TestServerConcurrentAppliesSerializeExactlyOnce(t *testing.T) {
	fingerprint := testFingerprint()
	release := make(chan struct{})
	defer close(release)
	controller := newTestController(t, blockingAdapter(release))
	ep := startTestServer(t, controller)
	conn := connectClient(t, ep, fingerprint, "")

	started := callOp(t, conn, OpStart, testStartEnvelope())
	var handle execution.Handle
	decodeBodyInto(t, started.Body, &handle)
	waitForStateViaWire(t, conn, handle.RunID, agentrun.StateRunning)

	const racers = 16
	var wg sync.WaitGroup
	results := make([]wireResponse, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			racerConn, err := Dial(ep)
			if err != nil {
				results[slot] = wireResponse{Error: &wireError{Message: fmt.Sprintf("dial: %v", err)}}
				return
			}
			defer func() { _ = racerConn.Close() }()
			if hs, hsErr := json.Marshal(handshakeRequest{ProtocolRevision: ProtocolRevision, Repository: fingerprint}); hsErr != nil {
				results[slot] = wireResponse{Error: &wireError{Message: hsErr.Error()}}
				return
			} else if frameErr := WriteFrame(racerConn, hs); frameErr != nil {
				results[slot] = wireResponse{Error: &wireError{Message: frameErr.Error()}}
				return
			}
			if _, frameErr := ReadFrame(racerConn); frameErr != nil {
				results[slot] = wireResponse{Error: &wireError{Message: frameErr.Error()}}
				return
			}
			body, bodyErr := json.Marshal(execution.ApplyRequest{
				RunID: handle.RunID, Action: execution.ControlAction{Kind: execution.ActionAbort},
				ActionID: "exactly-once", AuthContext: testAuth(),
			})
			if bodyErr != nil {
				results[slot] = wireResponse{Error: &wireError{Message: bodyErr.Error()}}
				return
			}
			if reqErr := WriteFrame(racerConn, mustMarshal(wireRequest{Op: OpApply, Body: body})); reqErr != nil {
				results[slot] = wireResponse{Error: &wireError{Message: reqErr.Error()}}
				return
			}
			frame, readErr := ReadFrame(racerConn)
			if readErr != nil {
				results[slot] = wireResponse{Error: &wireError{Message: readErr.Error()}}
				return
			}
			_ = json.Unmarshal(frame, &results[slot])
		}(i)
	}
	wg.Wait()

	accepted := 0
	duplicates := 0
	for _, result := range results {
		switch {
		case result.OK:
			accepted++
		case result.Error != nil && errors.Is(&RemoteError{Code: result.Error.Code, Message: result.Error.Message},
			execution.ErrDuplicateAction):
			// Decode the wire error through RemoteError so identity comes
			// from the registry, mirroring assertRemoteSentinel parity.
			duplicates++
		default:
			t.Fatalf("unexpected racer outcome: ok=%v err=%+v", result.OK, result.Error)
		}
	}
	if accepted != 1 || duplicates != racers-1 {
		t.Fatalf("concurrent replays produced accepted=%d duplicates=%d, want exactly 1 and %d",
			accepted, duplicates, racers-1)
	}
	waitForStateViaWire(t, conn, handle.RunID, agentrun.StateCanceled)
}

func TestServerMalformedEnvelopeClosesConnectionAndUnknownOpSurvives(t *testing.T) {
	controller := newTestController(t, immediateAdapter("done"))
	ep := startTestServer(t, controller)
	conn := connectClient(t, ep, testFingerprint(), "")

	// Unknown ops are a non-fatal application-level failure: the connection
	// keeps serving afterwards.
	response := callOp(t, conn, "teleport", map[string]string{})
	if response.OK || response.Error == nil || response.Error.Code != "" {
		t.Fatalf("unknown op response = %+v, want unclassified failure", response)
	}

	// A malformed request envelope is a protocol violation: the server
	// answers once and closes, and the next read hits clean EOF.
	if err := WriteFrame(conn, []byte("{not json")); err != nil {
		t.Fatalf("send malformed envelope: %v", err)
	}
	frame, err := ReadFrame(conn)
	if err != nil {
		t.Fatalf("read malformed-envelope response: %v", err)
	}
	var decoded wireResponse
	if jsonErr := json.Unmarshal(frame, &decoded); jsonErr != nil {
		t.Fatal(jsonErr)
	}
	if decoded.OK || decoded.Error == nil || decoded.Error.Code != "" {
		t.Fatalf("malformed envelope response = %+v, want unclassified failure", decoded)
	}
	if _, eof := ReadFrame(conn); !errors.Is(eof, io.EOF) {
		t.Fatalf("connection stayed open after malformed envelope; next read = %v, want EOF", eof)
	}
}
