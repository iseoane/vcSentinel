package daemon

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
)

// Server serves the six repository-host operations over framed connections
// against one execution.Controller. It is transport-agnostic: Serve accepts
// any listener produced by Listen.
//
// Serialization contract: Start, Apply, Recover, and Retry run under one
// admission mutex so two state-mutating control decisions can never
// interleave; Inspect and Subscribe serve concurrently because they only
// read durable state. Apply additionally owns replay detection of
// idempotency identities (mirroring InProcessHost), so a replayed ActionID
// fails with ErrDuplicateAction even under concurrency.
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

	// grace bounds how long the graceful shutdown drain waits for in-flight
	// dispatch operations before proceeding to explicit orphan settlement.
	// Zero normalizes to DefaultGracePeriod (see NewServerWithGrace).
	grace time.Duration

	// Lifecycle coordination for the graceful shutdown path. shuttingDown
	// flips at most once under mu and refuses newly admitted work; inflight
	// counts running dispatch operations and is mutated only while holding
	// mu (beginDispatch/endDispatch), so the bounded drain observes a
	// consistent counter; sequenceDone closes when the graceful sequence
	// finishes, regardless of whether orphan settlement itself succeeded;
	// and finalizeOnce releases the listener and every connection exactly
	// once, strictly after the shutdown answer left this process.
	shuttingDown  bool
	inflightCount int
	sequenceDone  chan struct{}
	finalizeOnce  sync.Once

	// orphanedRuns records how many active runs the completed graceful
	// sequence settled as orphaned. It is written exactly once under mu at
	// sequence end so the lifecycle runner can report the count after Serve
	// returns, regardless of whether the shutdown arrived programmatically
	// or over the wire (where the count also travels inside ShutdownResult).
	orphanedRuns int

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
		controller:   controller,
		repository:   repositoryFingerprint,
		actionIDs:    make(map[appliedKey]struct{}),
		grace:        DefaultGracePeriod,
		sequenceDone: make(chan struct{}),
		ctx:          ctx,
		cancel:       cancel,
		conns:        make(map[net.Conn]struct{}),
	}
}

// Serve accepts connections on l until Close is called, a graceful shutdown
// completes, or l fails. Every accepted connection is served on its own
// goroutine: it performs one handshake and then a request loop until the
// peer disconnects or an error frame aborts it. A tcp listener's bearer
// token, when present, becomes mandatory in every handshake on that
// listener.
//
// Once the graceful sequence finishes, the listener is closed and Serve
// returns nil; connections accepted after the sequence began are rejected
// before their handshake.
func (s *Server) Serve(l net.Listener) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errServerClosed
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

// Close is the immediate resource-release path: it stops accepting, cancels
// the server base context (which cancels every per-request dispatch context),
// and force-closes every open connection. It performs NO drain of in-flight
// dispatch operations and persists NO orphan settlements, so runs still
// active at this point remain exactly as they are — detached run workers
// survive by design (Controller.Start spawns them from context.Background())
// and their classification belongs to the R8 recovery machinery on restart.
//
// Relationship to the graceful path: Shutdown — reachable programmatically or
// through the authenticated OpShutdown wire operation — runs the bounded
// drain plus explicit orphan settlement and then releases the same resources
// exactly once via tryFinalize. Close stays safe to call before, during, or
// after that sequence: it never resurrects anything, and a later finalize is
// reduced to idempotent best-effort closes on already-released resources.
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
//
// Dispatch accounting: every request except OpShutdown is counted in the
// in-flight WaitGroup so the graceful drain can wait for it; the shutdown op
// is exempt because it must never wait for itself. Once the graceful
// sequence began, newly counted work is refused and new connections are
// rejected before their handshake.
func (s *Server) serveConn(conn net.Conn, requiredToken string) {
	defer s.untrackConn(conn)
	defer func() { _ = conn.Close() }()

	connCtx, cancelConn := context.WithCancel(s.ctx)
	defer cancelConn()

	s.mu.Lock()
	refused := s.shuttingDown || s.closed
	s.mu.Unlock()
	if refused {
		return
	}

	if !s.handshake(conn, requiredToken) {
		return
	}
	for {
		frame, err := ReadFrame(conn)
		if err != nil {
			return
		}
		if op, ok := probeOp(frame); ok && op == OpShutdown {
			response, fatal := s.dispatch(connCtx, frame)
			writeErr := WriteFrame(conn, response)
			// The answer — success or classified failure — has left this
			// process at this point (or the peer is gone); only now may the
			// listener and connections be released.
			s.tryFinalize()
			if writeErr != nil || fatal {
				return
			}
			continue
		}
		tracked := s.beginDispatch()
		response, fatal := s.dispatch(connCtx, frame)
		if tracked {
			s.endDispatch()
		}
		if err := WriteFrame(conn, response); err != nil {
			return
		}
		if fatal {
			return
		}
	}
}

// probeOp cheaply decodes only the operation name of a framed request. A
// frame that cannot be decoded at all is not exempted from dispatch
// accounting: dispatch still answers it with the connection-fatal malformed
// error.
func probeOp(frame []byte) (string, bool) {
	var probe struct {
		Op string `json:"op"`
	}
	if err := json.Unmarshal(frame, &probe); err != nil {
		return "", false
	}
	return probe.Op, true
}

// beginDispatch counts one incoming request as in-flight. It returns false
// — without counting anything — when the server already stopped admitting
// work, so the caller skips the paired endDispatch.
func (s *Server) beginDispatch() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shuttingDown || s.closed {
		return false
	}
	s.inflightCount++
	return true
}

// endDispatch releases one counted dispatch operation.
func (s *Server) endDispatch() {
	s.mu.Lock()
	s.inflightCount--
	s.mu.Unlock()
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
	// Once the graceful sequence began, only the lifecycle op itself is
	// still answered; everything else fails deterministically so callers
	// observe the shutdown instead of hanging on a draining server.
	if request.Op != OpShutdown && s.isStopping() {
		return errorResponse(ErrDaemonShuttingDown), false
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
	case OpRecover:
		result, err = s.handleRecover(ctx, request.Body)
	case OpRetry:
		result, err = s.handleRetry(ctx, request.Body)
	case OpShutdown:
		result, err = s.handleShutdown(ctx, request.Body)
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
// though the client adapter also checks: defense in depth at the trust
// boundary, since any local process that can reach the transport may bypass
// our own CLI. The admission payload invariant is validated through the same
// shared execution.ValidateStartRequest helper InProcessHost.Start uses, so
// the two admission paths cannot drift, and the explicit Candidate/Prompt
// form resolves into the canonical agentrun.RunRequest server-side via
// execution.ResolveAdmissionRequest before delegation.
func (s *Server) handleStart(ctx context.Context, body []byte) (any, error) {
	var request execution.StartRequest
	if err := decodeBody(body, &request); err != nil {
		return nil, err
	}
	if request.AuthContext.Principal == "" {
		return nil, execution.ErrMissingPrincipal
	}
	if err := execution.ValidateStartRequest(request); err != nil {
		return nil, err
	}
	s.admission.Lock()
	defer s.admission.Unlock()
	handle, err := s.controller.Start(ctx, execution.ResolveAdmissionRequest(request), request.Policy)
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

// handleRecover resumes one run through explicit operator recovery under the
// admission mutex. Recovery mutates run state — it may append an
// orphaned-cancellation settlement or relaunch a retryable head — so it can
// never interleave with Start, Apply, or Retry. AuthContext presence is
// enforced again here like every op: defense in depth at the trust boundary,
// since any local process that can reach the transport may bypass our own
// CLI. The envelope fields map positionally onto the controller seam:
// RunID names the run and ExpectedRevision optionally pins its durable
// stream head (zero skips the check, mirroring Controller.Recover).
func (s *Server) handleRecover(ctx context.Context, body []byte) (any, error) {
	var request execution.RecoverRequest
	if err := decodeBody(body, &request); err != nil {
		return nil, err
	}
	if request.AuthContext.Principal == "" {
		return nil, execution.ErrMissingPrincipal
	}
	s.admission.Lock()
	defer s.admission.Unlock()
	return s.controller.Recover(ctx, request.RunID, request.ExpectedRevision)
}

// handleRetry relaunches one retryable run under the admission mutex, for
// the same state-mutating reason as Recover: the relaunch appends a retry
// decision event and registers a live run. AuthContext presence is enforced
// again here like every op. The envelope fields map positionally onto the
// controller seam: RunID names the run and ExpectedRevision optionally pins
// its durable stream head (zero skips the check, mirroring Controller.Retry).
func (s *Server) handleRetry(ctx context.Context, body []byte) (any, error) {
	var request execution.RetryRequest
	if err := decodeBody(body, &request); err != nil {
		return nil, err
	}
	if request.AuthContext.Principal == "" {
		return nil, execution.ErrMissingPrincipal
	}
	s.admission.Lock()
	defer s.admission.Unlock()
	return s.controller.Retry(ctx, request.RunID, request.ExpectedRevision)
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
