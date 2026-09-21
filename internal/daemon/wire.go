package daemon

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/ISeoane-Quental/vcSentinel/internal/execution"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
)

// MaxFrameSize bounds one frame body in both directions. 8 MiB sits far
// above any legitimate Inspection or event page and small enough that a
// hostile length prefix cannot exhaust memory before the check rejects it.
const MaxFrameSize = 8 << 20

// ErrFrameTooLarge reports a frame whose body exceeds MaxFrameSize. It is a
// distinct sentinel so callers can separate a protocol-size violation from an
// ordinary transport failure. Use errors.Is to detect it.
var ErrFrameTooLarge = errors.New("daemon: frame exceeds the maximum wire size")

// WriteFrame emits one length-prefixed frame to w: a 4-byte big-endian
// uint32 body length followed by the raw UTF-8 JSON body. Bodies larger than
// MaxFrameSize are rejected before any byte is written, so an oversized send
// fails on the writer side with the same distinct sentinel a hostile prefix
// would raise on the reader side.
func WriteFrame(w io.Writer, body []byte) error {
	if len(body) > MaxFrameSize {
		return fmt.Errorf("%w: %d bytes", ErrFrameTooLarge, len(body))
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(body)))
	if _, err := w.Write(prefix[:]); err != nil {
		return err
	}
	_, err := w.Write(body)
	return err
}

// ReadFrame reads exactly one length-prefixed frame from r and returns its
// raw body. A clean end of stream surfaces as io.EOF from the length-prefix
// read; a prefix that announces more than MaxFrameSize bytes fails with
// ErrFrameTooLarge before any body byte is allocated, and a truncated body
// surfaces as io.ErrUnexpectedEOF. The reader is never left having consumed
// a partial body silently: framing errors always abort the connection.
func ReadFrame(r io.Reader) ([]byte, error) {
	var prefix [4]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(prefix[:])
	if size > MaxFrameSize {
		return nil, fmt.Errorf("%w: %d bytes", ErrFrameTooLarge, size)
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

// The six repository-host operations plus the lifecycle operation. The set
// is closed on purpose: adding an op is a protocol change and must keep the
// server dispatch exhaustive.
const (
	OpStart     = "start"
	OpInspect   = "inspect"
	OpSubscribe = "subscribe"
	OpApply     = "apply"
	// OpRecover and OpRetry carry the two relaunch operations. Like Start
	// and Apply they mutate run state, so the server dispatch routes them
	// under the same admission mutex. They were added WITHOUT bumping
	// protocol_revision deliberately: old peers that do not know them
	// receive a deterministic unknown-operation wire error instead of a
	// handshake rejection, which is accepted additive evolution.
	OpRecover = "recover"
	OpRetry   = "retry"
	// OpShutdown triggers the graceful server shutdown: bounded drain of
	// in-flight dispatch operations, explicit orphan settlement for
	// still-active runs, then an ok answer only after that sequence
	// completed. It is handshake-gated like every other op. This op was
	// added WITHOUT bumping protocol_revision deliberately: old peers that
	// do not know it receive a deterministic unknown-operation wire error
	// instead of a handshake rejection, which is accepted additive evolution.
	OpShutdown = "shutdown"
)

// handshakeRequest opens every connection. ProtocolRevision must equal
// ProtocolRevision and Repository must equal the server's repository
// fingerprint; Token carries the bearer token of the tcp transport only and
// stays empty for unix sockets, where filesystem permissions already scope
// access.
type handshakeRequest struct {
	ProtocolRevision int    `json:"protocol_revision"`
	Repository       string `json:"repository"`
	Token            string `json:"token,omitempty"`
}

// handshakeResponse answers the opening handshake. ok=false always carries a
// non-nil error and the server closes the connection right after sending it.
type handshakeResponse struct {
	OK    bool       `json:"ok"`
	Error *wireError `json:"error,omitempty"`
}

// wireRequest is one framed operation call. Body holds the marshaled
// execution request envelope for that op, unchanged.
type wireRequest struct {
	Op   string          `json:"op"`
	Body json.RawMessage `json:"body,omitempty"`
}

// wireResponse answers one operation call. Exactly one of body and error is
// non-empty: ok=true carries the marshaled result envelope, ok=false carries
// the classified wireError.
type wireResponse struct {
	OK    bool            `json:"ok"`
	Body  json.RawMessage `json:"body,omitempty"`
	Error *wireError      `json:"error,omitempty"`
}

// wireError is the wire form of a failed operation. Code is one of the
// canonical registry codes when the failure maps to a known sentinel and
// empty when it does not; Message always carries err.Error().
type wireError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Handshake failure codes. They are protocol diagnostics, not part of the
// sentinel registry: they never cross back into errors.Is vocabulary.
const (
	codeRevisionMismatch   = "protocol_revision_mismatch"
	codeRepositoryMismatch = "repository_mismatch"
	codeUnauthorized       = "unauthorized"
)

// wireSentinels is the canonical code registry. It exists so that
// errors.Is SURVIVES the wire: the server classifies a controller failure
// into its code, the client decodes the code into a *RemoteError whose
// Unwrap resolves the same sentinel, so remote identity equals local
// identity for every listed error. Unknown failures travel with code "" and
// degrade to their message only.
//
// The daemon package already depends on execution for dispatch; importing
// store here rides the same edge and keeps the registry in one table.
var wireSentinels = []struct {
	code     string
	sentinel error
}{
	{"execution.controller_not_ready", execution.ErrControllerNotReady},
	{"execution.run_already_exists", execution.ErrRunAlreadyExists},
	{"execution.run_not_active", execution.ErrRunNotActive},
	{"execution.unsupported_action", execution.ErrUnsupportedAction},
	{"execution.decision_not_pending", execution.ErrDecisionNotPending},
	{"execution.stale_revision", execution.ErrStaleRevision},
	{"execution.missing_principal", execution.ErrMissingPrincipal},
	{"execution.duplicate_action", execution.ErrDuplicateAction},
	{"execution.run_not_retryable", execution.ErrRunNotRetryable},
	{"execution.run_not_recoverable", execution.ErrRunNotRecoverable},
	{"store.execution_not_found", store.ErrExecutionNotFound},
	{"daemon.daemon_owned", ErrDaemonOwned},
	{"daemon.shutting_down", ErrDaemonShuttingDown},
}

// errorCodeFor resolves err against the registry through errors.Is, so
// wrapped controller failures classify as their sentinel. Unlisted errors
// yield the empty code by design.
func errorCodeFor(err error) string {
	if err == nil {
		return ""
	}
	for _, entry := range wireSentinels {
		if errors.Is(err, entry.sentinel) {
			return entry.code
		}
	}
	return ""
}

// sentinelForCode is the reverse lookup used by RemoteError.Unwrap.
func sentinelForCode(code string) error {
	for _, entry := range wireSentinels {
		if entry.code == code {
			return entry.sentinel
		}
	}
	return nil
}

// RemoteError is the client-side decoding of a failed wire operation. Its
// Unwrap resolves the canonical code back to the original package sentinel,
// so errors.Is(remoteErr, execution.ErrRunNotActive) holds true remotely
// exactly when the server-side failure satisfied it locally. An unknown or
// empty code unwraps to nil and the message is all that survives.
type RemoteError struct {
	Code    string
	Message string
}

// Error implements the error interface with the decoded message.
func (e *RemoteError) Error() string {
	return e.Message
}

// Unwrap resolves the canonical registry code to its sentinel, mirroring how
// the server classified the original failure. Codes outside the registry
// resolve to nil, which simply reports no wrapped identity.
func (e *RemoteError) Unwrap() error {
	return sentinelForCode(e.Code)
}

// wireErrorFor classifies any server-side failure onto the wire. Known
// sentinels travel with their canonical code so the client recovers
// errors.Is identity; everything else travels with an empty code and its
// full message.
func wireErrorFor(err error) *wireError {
	return &wireError{Code: errorCodeFor(err), Message: err.Error()}
}
