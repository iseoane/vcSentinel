package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// HandshakeError reports a rejected or malformed opening handshake. Code is
// one of the protocol diagnostic codes (codeRevisionMismatch,
// codeRepositoryMismatch, codeUnauthorized) when the server classified the
// rejection and empty when it did not; Message carries the server's
// diagnosable answer verbatim. Handshake codes are protocol diagnostics, not
// registry sentinels, so this error never unwraps into errors.Is vocabulary.
type HandshakeError struct {
	Code    string
	Message string
}

// Error implements the error interface with the decoded message.
func (e *HandshakeError) Error() string { return e.Message }

// RemoteHost implements execution.RepositoryHost over ONE persistent framed
// connection to a repository-local daemon. It exists for one-shot CLI
// semantics: a command resolves the endpoint, dials, performs its handful of
// operations, and closes.
//
// Connection discipline: every method serializes through one mutex that
// gives strict request/response affinity — a frame is written and its answer
// read under the same lock — so concurrent callers can never interleave
// frames on the shared connection. Cancellation is checked before each call;
// a canceled context best-effort kills the whole connection, because a CLI
// caller never issues another request after abandoning one. A failure mid
// exchange also kills the connection: framing alignment cannot be trusted
// after an unexpected byte stream. Once the connection is dead — killed or
// closed — every later call fails with the deterministic closed-connection
// error below.
type RemoteHost struct {
	endpoint Endpoint

	mu     sync.Mutex
	conn   net.Conn
	closed bool
}

var (
	_ execution.RepositoryHost = (*RemoteHost)(nil)
	_ io.Closer                = (*RemoteHost)(nil)
)

// errConnClosed reports any use of a RemoteHost whose connection already
// died mid-exchange or was closed. It is a package sentinel so post-mortem
// callers can match the failure deterministically with errors.Is.
var errConnClosed = errors.New("connection is closed")

// DialRemoteHost connects to ep, performs the protocol handshake (protocol
// revision, repository fingerprint, and the bearer token when the transport
// is tcp), and returns a ready RemoteHost. Any guard rejection surfaces as a
// deterministic *HandshakeError carrying the server's classification;
// transport failures surface wrapped with their cause. On every failure path
// the half-open connection is closed before returning.
func DialRemoteHost(ep Endpoint, repositoryFingerprint string) (*RemoteHost, error) {
	conn, err := Dial(ep)
	if err != nil {
		return nil, fmt.Errorf("daemon: cannot reach endpoint %s://%s: %w", ep.Network, ep.Address, err)
	}
	token := ""
	if ep.Network == "tcp" {
		token, err = ReadBearerToken(ep)
		if err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("daemon: cannot read the bearer token of the tcp endpoint: %w", err)
		}
	}
	handshake, err := json.Marshal(handshakeRequest{
		ProtocolRevision: ProtocolRevision,
		Repository:       repositoryFingerprint,
		Token:            token,
	})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("daemon: cannot encode the handshake: %w", err)
	}
	if err := WriteFrame(conn, handshake); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("daemon: cannot send the handshake: %w", err)
	}
	raw, err := ReadFrame(conn)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("daemon: cannot read the handshake response: %w", err)
	}
	var response handshakeResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("daemon: malformed handshake response: %w", err)
	}
	if !response.OK {
		_ = conn.Close()
		rejected := &HandshakeError{Message: "daemon: handshake rejected without a diagnosable answer"}
		if response.Error != nil {
			rejected.Code = response.Error.Code
			rejected.Message = response.Error.Message
		}
		return nil, rejected
	}
	return &RemoteHost{endpoint: ep, conn: conn}, nil
}

// Start admits a run remotely and returns its handle. The envelope travels
// unchanged, including the explicit Candidate/Prompt admission form; the
// daemon validates it with the same shared helper the in-process host uses
// and resolves the canonical request server-side.
func (h *RemoteHost) Start(ctx context.Context, request execution.StartRequest) (execution.Handle, error) {
	var handle execution.Handle
	return handle, h.call(ctx, OpStart, request, &handle)
}

// Inspect reconstructs durable evidence of one run remotely.
func (h *RemoteHost) Inspect(ctx context.Context, request execution.InspectRequest) (execution.Inspection, error) {
	var inspection execution.Inspection
	return inspection, h.call(ctx, OpInspect, request, &inspection)
}

// Subscribe pages recorded events after the exclusive cursor, remotely.
func (h *RemoteHost) Subscribe(ctx context.Context, request execution.SubscribeRequest) (store.EventPage, error) {
	var page store.EventPage
	return page, h.call(ctx, OpSubscribe, request, &page)
}

// Apply delivers one control action remotely. Sentinel identity survives the
// wire: failures decode into *RemoteError whose Unwrap resolves the same
// package sentinel the server-side failure carried, so errors.Is parity
// holds (for example ErrDuplicateAction on a replayed ActionID).
func (h *RemoteHost) Apply(ctx context.Context, request execution.ApplyRequest) (execution.ApplyResult, error) {
	var result execution.ApplyResult
	return result, h.call(ctx, OpApply, request, &result)
}

// Close closes the underlying connection. It is idempotent: closing twice is
// a no-op, and Close on a host whose connection already died mid-exchange
// also reports success, because markConnDead already tore the dead socket
// down when the failure was observed.
func (h *RemoteHost) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.conn == nil {
		h.closed = true
		return nil
	}
	err := h.conn.Close()
	h.conn = nil
	h.closed = true
	return err
}

// markConnDead tears down a connection that can no longer be trusted. The
// caller MUST already hold h.mu: this helper deliberately never locks, so it
// stays safe on the failure paths inside call — invoking Close there while
// the deferred unlock still holds the mutex would deadlock a non-reentrant
// sync.Mutex and wedge the host forever.
func (h *RemoteHost) markConnDead() {
	h.closed = true
	if h.conn != nil {
		_ = h.conn.Close() // best-effort; the socket may already be gone.
		h.conn = nil
	}
}

// call performs exactly one framed operation under the connection lock:
// marshal, write, read, decode. Every failure after the connection entered
// the exchange — and every observed cancellation — marks the connection dead
// best-effort, honoring the one-shot CLI contract. The top-of-call guard
// then fails every post-mortem call with the deterministic closed-connection
// error instead of silently reusing an untrustworthy socket.
func (h *RemoteHost) call(ctx context.Context, op string, envelope any, result any) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.conn == nil {
		return fmt.Errorf("daemon: %w", errConnClosed)
	}
	if err := ctx.Err(); err != nil {
		h.markConnDead()
		return err
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("daemon: cannot encode the %s envelope: %w", op, err)
	}
	frame, err := json.Marshal(wireRequest{Op: op, Body: body})
	if err != nil {
		return fmt.Errorf("daemon: cannot encode the %s request frame: %w", op, err)
	}

	if err := WriteFrame(h.conn, frame); err != nil {
		h.markConnDead()
		return fmt.Errorf("daemon: cannot send the %s request: %w", op, err)
	}
	raw, err := ReadFrame(h.conn)
	if err != nil {
		h.markConnDead()
		return fmt.Errorf("daemon: cannot read the %s response: %w", op, err)
	}
	if err := ctx.Err(); err != nil {
		h.markConnDead()
		return err
	}
	var response wireResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		h.markConnDead()
		return fmt.Errorf("daemon: malformed %s response frame: %w", op, err)
	}
	if !response.OK {
		if response.Error == nil {
			h.markConnDead()
			return fmt.Errorf("daemon: the %s operation failed without a classified error", op)
		}
		return &RemoteError{Code: response.Error.Code, Message: response.Error.Message}
	}
	if len(response.Body) == 0 {
		h.markConnDead()
		return fmt.Errorf("daemon: the %s response carries no body", op)
	}
	if err := json.Unmarshal(response.Body, result); err != nil {
		h.markConnDead()
		return fmt.Errorf("daemon: cannot decode the %s result: %w", op, err)
	}
	return nil
}
