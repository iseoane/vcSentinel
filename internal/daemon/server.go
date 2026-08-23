package daemon

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
)

// Server serves the four repository-host operations over framed connections
// against one execution.Controller. It is transport-agnostic: Serve accepts
// any listener produced by Listen.
//
// Serialization contract: Start and Apply run under one admission mutex so
// two control decisions can never interleave; Inspect and Subscribe serve
// concurrently because they only read durable state. Apply additionally owns
// replay detection of idempotency identities (mirroring InProcessHost), so a
// replayed ActionID fails with ErrDuplicateAction even under concurrency.
// Cross-restart persistence of that replay table arrives with the daemon
// lifecycle slice; for now it lives for this process lifetime, which is
// strictly stronger than per-command InProcessHost semantics.
type Server struct {
	controller *execution.Controller
	repository string // expected repository fingerprint

	// admission serializes controller.Start and controller.Apply and guards
	// the consumed-idempotency table below.
	admission sync.Mutex
	actionIDs map[appliedKey]struct{}

	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once

	mu       sync.Mutex
	closed   bool
	listener net.Listener
	conns    map[net.Conn]struct{}
}

// appliedKey keys one consumed Apply idempotency identity to its run: the
// same ActionID under a different run is a distinct identity.
type appliedKey struct {
	runID    agentrun.Identity
	actionID string
}

// NewServer binds the daemon server to controller and expects handshakes to
// carry exactly repositoryFingerprint (see FingerprintRepository).
func NewServer(controller *execution.Controller, repositoryFingerprint string) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		controller: controller,
		repository: repositoryFingerprint,
		actionIDs:  make(map[appliedKey]struct{}),
		ctx:        ctx,
		cancel:     cancel,
		conns:      make(map[net.Conn]struct{}),
	}
}

// Serve accepts connections on l until Close is called or l fails. Every
// accepted connection is served on its own goroutine: it performs one
// handshake and then a request loop until the peer disconnects or an error
// frame aborts it. A tcp listener's bearer token, when present, becomes
// mandatory in every handshake on that listener.
func (s *Server) Serve(l net.Listener) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("daemon: server is closed")
	}
	s.listener = l
	s.mu.Unlock()

	requiredToken := ""
	if tcp, ok := l.(*tcpListener); ok {
		requiredToken = tcp.bearerToken()
	}

	for {
		conn, err := l.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return nil
			}
			return err
		}
		s.trackConn(conn)
		go s.serveConn(conn, requiredToken)
	}
}

// Close stops accepting and closes all open connections best-effort, which
// cancels every per-request dispatch context derived from the server base
// context. It does NOT cancel detached run workers: Controller.Start spawns
// them from context.Background() by design so an admitted run survives a
// server stop and settles independently against durable state. Graceful
// draining of connections and reconciliation of such orphaned workers arrive
// with the daemon lifecycle slice; this is the honest minimal stop.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		s.cancel()
		s.mu.Lock()
		s.closed = true
		listener := s.listener
		conns := make([]net.Conn, 0, len(s.conns))
		for conn := range s.conns {
			conns = append(conns, conn)
		}
		s.mu.Unlock()
		if listener != nil {
			_ = listener.Close()
		}
		for _, conn := range conns {
			_ = conn.Close()
		}
	})
}

func (s *Server) trackConn(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conns[conn] = struct{}{}
}

func (s *Server) untrackConn(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, conn)
}

// serveConn runs the full lifecycle of one connection. The connection
// context derives from the server base context, so Close reaches in-flight
// controller operations immediately.
func (s *Server) serveConn(conn net.Conn, requiredToken string) {
	defer s.untrackConn(conn)
	defer func() { _ = conn.Close() }()

	connCtx, cancelConn := context.WithCancel(s.ctx)
	defer cancelConn()

	if !s.handshake(conn, requiredToken) {
		return
	}
	for {
		frame, err := ReadFrame(conn)
		if err != nil {
			return
		}
		response, fatal := s.dispatch(connCtx, frame)
		if err := WriteFrame(conn, response); err != nil {
			return
		}
		if fatal {
			return
		}
	}
}

// handshake reads and validates the opening frame. On any failure it sends
// the negative handshakeResponse before closing so clients get a diagnosable
// answer instead of bare EOF.
func (s *Server) handshake(conn net.Conn, requiredToken string) bool {
	frame, err := ReadFrame(conn)
	if err != nil {
		return false
	}
	var request handshakeRequest
	if err := json.Unmarshal(frame, &request); err != nil {
		_ = WriteFrame(conn, mustMarshal(handshakeResponse{
			Error: &wireError{Message: fmt.Sprintf("daemon: malformed handshake: %v", err)},
		}))
		return false
	}
	switch {
	case request.ProtocolRevision != ProtocolRevision:
		_ = WriteFrame(conn, mustMarshal(handshakeResponse{
			Error: &wireError{Code: codeRevisionMismatch, Message: fmt.Sprintf(
				"daemon: protocol revision %d unsupported, want %d", request.ProtocolRevision, ProtocolRevision)},
		}))
		return false
	case request.Repository != s.repository:
		_ = WriteFrame(conn, mustMarshal(handshakeResponse{
			Error: &wireError{Code: codeRepositoryMismatch, Message: "daemon: repository fingerprint mismatch"},
		}))
		return false
	case requiredToken != "" && subtle.ConstantTimeCompare([]byte(request.Token), []byte(requiredToken)) != 1:
		_ = WriteFrame(conn, mustMarshal(handshakeResponse{
			Error: &wireError{Code: codeUnauthorized, Message: "daemon: missing or invalid bearer token"},
		}))
		return false
	}
	return WriteFrame(conn, mustMarshal(handshakeResponse{OK: true})) == nil
}

// dispatch decodes one framed wireRequest and routes it. The second return
// reports a protocol violation that must end the connection (malformed
// envelope or undecodable operation body): framing alignment survives, but
// a client whose bytes cannot be decoded has no defined continuation.
func (s *Server) dispatch(ctx context.Context, frame []byte) (encoded []byte, fatal bool) {
	var request wireRequest
	if err := json.Unmarshal(frame, &request); err != nil {
		return errorResponse(fmt.Errorf("daemon: malformed request: %w", err)), true
	}
	var (
		result any
		err    error
	)
	switch request.Op {
	case OpStart:
		result, err = s.handleStart(ctx, request.Body)
	case OpInspect:
		result, err = s.handleInspect(ctx, request.Body)
	case OpSubscribe:
		result, err = s.handleSubscribe(ctx, request.Body)
	case OpApply:
		result, err = s.handleApply(ctx, request.Body)
	default:
		err = fmt.Errorf("daemon: unknown operation %q", request.Op)
	}
	if err != nil {
		return errorResponse(err), errors.Is(err, errMalformedBody)
	}
	body, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return errorResponse(fmt.Errorf("daemon: cannot encode result: %w", marshalErr)), true
	}
	return mustMarshal(wireResponse{OK: true, Body: body}), false
}

// errMalformedBody marks request bodies that fail JSON decoding; it never
// escapes to the wire as a classified sentinel, only as a connection-fatal
// plain error.
var errMalformedBody = errors.New("daemon: malformed request body")

func errorResponse(err error) []byte {
	return mustMarshal(wireResponse{Error: wireErrorFor(err)})
}

func mustMarshal(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("daemon: cannot encode wire value: %v", err))
	}
	return data
}

// handleStart admits a run. AuthContext presence is enforced again here even
// though the future client adapter also checks: defense in depth at the
// trust boundary, since any local process that can reach the transport may
// bypass our own CLI.
//
// Wire limitation by design: wire starts admit only the zero agentrun.RunRequest
// until prompt transport is defined in slice 2b, because RunRequest fields are
// deliberately unexported identity-canonical data that this package cannot
// reconstruct from arbitrary JSON.
func (s *Server) handleStart(ctx context.Context, body []byte) (any, error) {
	var request execution.StartRequest
	if err := decodeBody(body, &request); err != nil {
		return nil, err
	}
	if request.AuthContext.Principal == "" {
		return nil, execution.ErrMissingPrincipal
	}
	s.admission.Lock()
	defer s.admission.Unlock()
	handle, err := s.controller.Start(ctx, request.Request, request.Policy)
	if err != nil {
		return nil, err
	}
	return handle, nil
}

// handleInspect reconstructs durable evidence of one run concurrently with
// other readers; no admission lock is taken.
func (s *Server) handleInspect(ctx context.Context, body []byte) (any, error) {
	var request execution.InspectRequest
	if err := decodeBody(body, &request); err != nil {
		return nil, err
	}
	if request.AuthContext.Principal == "" {
		return nil, execution.ErrMissingPrincipal
	}
	return s.controller.Inspect(ctx, request.RunID)
}

// handleSubscribe pages recorded events after the exclusive cursor.
func (s *Server) handleSubscribe(ctx context.Context, body []byte) (any, error) {
	var request execution.SubscribeRequest
	if err := decodeBody(body, &request); err != nil {
		return nil, err
	}
	if request.AuthContext.Principal == "" {
		return nil, execution.ErrMissingPrincipal
	}
	return s.controller.ReadEventPage(ctx, request.RunID, request.AfterCursor, request.Limit)
}

// handleApply delivers one control action under the admission mutex. The
// idempotency identity is consumed on attempt, mirroring InProcessHost: a
// rejected action still burns its identity, and concurrent replays of one
// ActionID admit at most once.
func (s *Server) handleApply(ctx context.Context, body []byte) (any, error) {
	var request execution.ApplyRequest
	if err := decodeBody(body, &request); err != nil {
		return nil, err
	}
	if request.AuthContext.Principal == "" {
		return nil, execution.ErrMissingPrincipal
	}
	if request.ActionID == "" {
		return nil, errors.New("execution: control action identity is empty")
	}
	key := appliedKey{runID: request.RunID, actionID: request.ActionID}
	s.admission.Lock()
	defer s.admission.Unlock()
	if _, seen := s.actionIDs[key]; seen {
		return nil, execution.ErrDuplicateAction
	}
	s.actionIDs[key] = struct{}{}
	return s.controller.Apply(ctx, request.RunID, request.Action)
}

// decodeBody unmarshals one operation body. Decoding failure is marked
// fatal: a client that cannot produce decodable envelopes has no defined
// continuation on this connection.
func decodeBody(body []byte, target any) error {
	if len(body) == 0 {
		return fmt.Errorf("%w: empty body", errMalformedBody)
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("%w: %v", errMalformedBody, err)
	}
	return nil
}
