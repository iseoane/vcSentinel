package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/execution"
)

const (
	// DefaultGracePeriod bounds the cooperative shutdown window — in-flight
	// dispatch operations first, then still-executing detached runs — before
	// the sequence proceeds to explicit settlement regardless. It is the
	// zero-value budget of every server built by NewServer and
	// NewServerWithGrace.
	DefaultGracePeriod = 30 * time.Second

	// OrphanedByShutdown is the outcome reason persisted for every run still
	// active when the drain budget expired, so durable evidence states
	// honestly who authored the cancellation and why.
	OrphanedByShutdown = "orphaned by daemon shutdown"

	// pollInterval is the sleep between readiness polls inside the bounded
	// shutdown waits and their tests: short enough to observe sub-second
	// budgets, long enough not to spin.
	pollInterval = 2 * time.Millisecond
)

var (
	// ErrDaemonShuttingDown reports an operation refused because the
	// graceful shutdown sequence already began: new connections are
	// rejected before their handshake, non-lifecycle operations on existing
	// connections fail with this sentinel, and a second shutdown request is
	// deterministic instead of a duplicate drain.
	ErrDaemonShuttingDown = errors.New("daemon: server is shutting down")

	// errServerClosed reports use of a server whose resources were already
	// released, either by Close or by a completed graceful sequence.
	errServerClosed = errors.New("daemon: server is closed")
)

// ShutdownRequest opens the graceful lifecycle operation. AuthContext
// presence is enforced at the trust boundary like every other op.
type ShutdownRequest struct {
	AuthContext execution.AuthContext `json:"auth_context"`
}

// ShutdownResult answers a successful graceful shutdown with the evidence a
// stop command can surface: how many runs were explicitly orphaned after the
// drain budget expired.
type ShutdownResult struct {
	OrphanedRuns int `json:"orphaned_runs"`
}

// NewServerWithGrace binds the daemon server to controller with an explicit
// grace budget for the shutdown drain. A single extra parameter did not
// justify an options pattern; grace <= 0 normalizes to DefaultGracePeriod.
func NewServerWithGrace(controller *execution.Controller, repositoryFingerprint string, grace time.Duration) *Server {
	server := NewServer(controller, repositoryFingerprint)
	if grace <= 0 {
		grace = DefaultGracePeriod
	}
	server.grace = grace
	return server
}

// Shutdown runs the full graceful sequence programmatically — bounded drain
// of in-flight dispatch operations, explicit orphan settlement for survivors,
// then release of the listener and connections — without requiring any wire
// client to carry the OpShutdown request. It returns after Serve's listener
// has been closed, so Serve returns nil afterwards.
//
// The sequence runs at most once per server: a concurrent or repeated call
// fails with ErrDaemonShuttingDown (still draining or already answered) or
// errServerClosed (resources already released), never with a duplicate drain.
func (s *Server) Shutdown() error {
	if err := s.beginShutdown(); err != nil {
		return err
	}
	if _, err := s.runGracefulSequence(); err != nil {
		s.tryFinalize()
		return err
	}
	s.tryFinalize()
	return nil
}

// handleShutdown serves OpShutdown over the wire. The ok answer is produced
// only AFTER the graceful sequence completed, so the stop command can await
// real termination; serveConn then releases the listener and connections via
// tryFinalize, which closes the listener and makes Serve return nil.
func (s *Server) handleShutdown(ctx context.Context, body []byte) (any, error) {
	var request ShutdownRequest
	if err := decodeBody(body, &request); err != nil {
		return nil, err
	}
	if request.AuthContext.Principal == "" {
		return nil, execution.ErrMissingPrincipal
	}
	if err := s.beginShutdown(); err != nil {
		return nil, err
	}
	orphaned, err := s.runGracefulSequence()
	if err != nil {
		return nil, err
	}
	return ShutdownResult{OrphanedRuns: orphaned}, nil
}

// beginShutdown claims the one-shot right to run the graceful sequence under
// mu. Every loser gets a deterministic error naming why it lost.
func (s *Server) beginShutdown() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed:
		return errServerClosed
	case s.shuttingDown:
		return ErrDaemonShuttingDown
	}
	s.shuttingDown = true
	return nil
}

// runGracefulSequence gives executing work one bounded cooperative window
// and then settles every surviving active run as orphaned. The grace budget
// covers both phases together: in-flight dispatch operations first, then
// runs still executing on their detached workers. sequenceDone closes exactly
// once here so the ack path can finalize even when settlement itself failed:
// a shutdown that cannot complete its evidence still stops serving.
func (s *Server) runGracefulSequence() (int, error) {
	defer close(s.sequenceDone)
	deadline := time.Now().Add(s.grace)
	s.drainInflight(deadline)
	if remaining := time.Until(deadline); remaining > 0 {
		s.controller.WaitForActiveRuns(remaining)
	}
	orphaned, err := s.controller.OrphanOwnedRuns(OrphanedByShutdown)
	s.mu.Lock()
	s.orphanedRuns = len(orphaned)
	s.mu.Unlock()
	if err != nil {
		return len(orphaned), fmt.Errorf("daemon: orphaning active runs failed after settling %d: %w", len(orphaned), err)
	}
	return len(orphaned), nil
}

// drainInflight waits for every counted dispatch operation to finish until
// deadline: expiry proceeds to settlement regardless of what is still
// running. A Close during the drain cancels the base context, which unblocks
// the remaining dispatches quickly; the counter then drains naturally.
func (s *Server) drainInflight(deadline time.Time) {
	for {
		s.mu.Lock()
		remaining := s.inflightCount
		s.mu.Unlock()
		if remaining == 0 || !time.Now().Before(deadline) {
			return
		}
		time.Sleep(pollInterval)
	}
}

// isStopping reports whether the server stopped admitting work.
func (s *Server) isStopping() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shuttingDown || s.closed
}

// orphanedAtStop reports how many active runs the completed graceful sequence
// settled as orphaned. It reads zero until a sequence has actually run, so
// callers that observe it after Serve returned always see the final count of
// the shutdown that ended the serve loop.
func (s *Server) orphanedAtStop() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.orphanedRuns
}

// tryFinalize performs the ordered resource release exactly once, but only
// when the graceful sequence actually finished (sequenceDone closed): the
// answer to the stop requester must leave this process first. Closing the
// listener here is what makes Serve return nil.
func (s *Server) tryFinalize() {
	select {
	case <-s.sequenceDone:
	default:
		return
	}
	s.finalizeOnce.Do(func() {
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
