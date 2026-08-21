package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
)

var (
	// ErrRevisionConflict means that another writer advanced the event stream.
	ErrRevisionConflict = errors.New("store: execution revision conflict")
	// ErrEventCorrupt means that an event log failed an integrity invariant.
	ErrEventCorrupt = errors.New("store: corrupt execution event log")
	// ErrIncompleteEventTail means that the final JSONL record is incomplete.
	ErrIncompleteEventTail = errors.New("store: incomplete final execution event")
	ErrProjectionCorrupt   = errors.New("store: corrupt execution projection")

	// Compatibility names keep the error vocabulary explicit at call sites.
	ErrExecutionRevisionConflict = ErrRevisionConflict
	ErrCorruptEventLog           = ErrEventCorrupt
	ErrRecoverableEventTail      = ErrIncompleteEventTail
)

// RevisionConflictError reports the revision observed while a write was
// waiting for the event-stream lock.
type RevisionConflictError struct {
	Expected uint64
	Actual   uint64
}

func (e RevisionConflictError) Error() string {
	return fmt.Sprintf("store: expected execution revision %d, found %d", e.Expected, e.Actual)
}

func (e RevisionConflictError) Unwrap() error { return ErrRevisionConflict }

// EventCorruptionError identifies the invariant that made a log unreadable.
type EventCorruptionError struct {
	RunID    string
	Sequence uint64
	Reason   string
}

func (e EventCorruptionError) Error() string {
	return fmt.Sprintf("store: corrupt execution event log for %s at sequence %d: %s", e.RunID, e.Sequence, e.Reason)
}

func (e EventCorruptionError) Unwrap() error { return ErrEventCorrupt }

// IncompleteEventTailError identifies a recoverable final JSONL tail.
type IncompleteEventTailError struct{ RunID string }

func (e IncompleteEventTailError) Error() string {
	return fmt.Sprintf("store: incomplete final execution event for %s", e.RunID)
}

func (e IncompleteEventTailError) Unwrap() error { return ErrIncompleteEventTail }

// EventFrame is the durable representation of an agentrun.NormalizedEvent.
// Sequence and revision advance together; hashes make the append-only stream
// self-validating without depending on a database.
type EventFrame struct {
	Sequence        uint64                  `json:"sequence"`
	Revision        uint64                  `json:"revision"`
	At              time.Time               `json:"at"`
	RunID           string                  `json:"run_id"`
	JobID           string                  `json:"job_id"`
	InvocationID    string                  `json:"invocation_id"`
	LineageID       string                  `json:"lineage_id"`
	From            agentrun.LifecycleState `json:"from"`
	To              agentrun.LifecycleState `json:"to"`
	Decision        agentrun.Decision       `json:"decision"`
	Terminal        agentrun.TerminalClass  `json:"terminal"`
	PredecessorHash string                  `json:"predecessor_hash,omitempty"`
	ContentHash     string                  `json:"content_hash"`
}

// RunEvent is an expressive alias for callers that prefer the domain name.
type RunEvent = EventFrame

func (e EventFrame) PreviousHash() string { return e.PredecessorHash }
func (e EventFrame) Hash() string         { return e.ContentHash }

// EventReceipt confirms the durable sequence, chain position, and projected
// state committed by AppendEvent.
type EventReceipt struct {
	RunID           string                  `json:"run_id"`
	Sequence        uint64                  `json:"sequence"`
	Revision        uint64                  `json:"revision"`
	PredecessorHash string                  `json:"predecessor_hash,omitempty"`
	ContentHash     string                  `json:"content_hash"`
	State           agentrun.LifecycleState `json:"state"`
	At              time.Time               `json:"at"`
}

type Receipt = EventReceipt

// EventPage is a validated page of the event stream. The next cursor is the
// last returned revision, not an array offset, so callers can resume safely.
type EventPage struct {
	Events       []EventFrame `json:"events"`
	NextSequence uint64       `json:"next_sequence,omitempty"`
	NextRevision uint64       `json:"next_revision,omitempty"`
	HasMore      bool         `json:"has_more"`
}

// RunProjection is the atomically replaced state projection derived from the
// event stream.
type RunProjection struct {
	RunID         string                  `json:"run_id"`
	Sequence      uint64                  `json:"sequence"`
	Revision      uint64                  `json:"revision"`
	State         agentrun.LifecycleState `json:"state"`
	Terminal      agentrun.TerminalClass  `json:"terminal"`
	LastEventHash string                  `json:"last_event_hash,omitempty"`
	UpdatedAt     time.Time               `json:"updated_at,omitempty"`
}

type StateProjection = RunProjection

type eventContent struct {
	At           time.Time               `json:"at"`
	RunID        string                  `json:"run_id"`
	JobID        string                  `json:"job_id"`
	InvocationID string                  `json:"invocation_id"`
	LineageID    string                  `json:"lineage_id"`
	From         agentrun.LifecycleState `json:"from"`
	To           agentrun.LifecycleState `json:"to"`
	Decision     agentrun.Decision       `json:"decision"`
	Terminal     agentrun.TerminalClass  `json:"terminal"`
}

type scannedEvents struct {
	frames []EventFrame
	tail   *eventTail
}

type eventTail struct {
	offset int64
	frame  *EventFrame
}

// AppendEvent appends one normalized R1 event if expectedRevision still names
// the current stream head. The lock is a cross-process atomic directory lock.
func (s *Store) AppendEvent(runID string, event agentrun.NormalizedEvent, expectedRevision uint64) (EventReceipt, error) {
	directory, err := s.executionDir(runID)
	if err != nil {
		return EventReceipt{}, err
	}
	if err := ensureExecutionExists(directory); err != nil {
		return EventReceipt{}, err
	}
	if string(event.RunID()) != runID {
		return EventReceipt{}, fmt.Errorf("store: event belongs to run %q, not %q", event.RunID(), runID)
	}

	var receipt EventReceipt
	err = withExecutionLock(directory, func() error {
		log, err := scanEventLog(runID, filepath.Join(directory, "events.jsonl"))
		if err != nil {
			return err
		}
		if log.tail != nil {
			return IncompleteEventTailError{RunID: runID}
		}
		actual := uint64(len(log.frames))
		if actual != expectedRevision {
			return RevisionConflictError{Expected: expectedRevision, Actual: actual}
		}

		previousHash := ""
		if len(log.frames) > 0 {
			previousHash = log.frames[len(log.frames)-1].ContentHash
			if event.From() != log.frames[len(log.frames)-1].To {
				return EventCorruptionError{RunID: runID, Sequence: actual + 1, Reason: "event source state does not match the stream head"}
			}
		}
		frame := frameForEvent(event, actual+1, actual+1, previousHash)
		path := filepath.Join(directory, "events.jsonl")
		if err := appendEventFrame(path, frame); err != nil {
			return err
		}

		frames := append(append([]EventFrame(nil), log.frames...), frame)
		projection := projectionFor(runID, frames)
		if err := writeProjection(filepath.Join(directory, "state.json"), projection); err != nil {
			return err
		}
		receipt = EventReceipt{
			RunID: runID, Sequence: frame.Sequence, Revision: frame.Revision,
			PredecessorHash: frame.PredecessorHash, ContentHash: frame.ContentHash,
			State: projection.State, At: frame.At,
		}
		return writeReceipt(filepath.Join(directory, "receipts"), receipt)
	})
	return receipt, err
}

// AppendRunEvent is a named synonym for integrations that distinguish run
// events from the legacy operations log.
func (s *Store) AppendRunEvent(runID string, event agentrun.NormalizedEvent, expectedRevision uint64) (EventReceipt, error) {
	return s.AppendEvent(runID, event, expectedRevision)
}

// ReadEvents validates the complete stream before returning the requested
// page. afterRevision is an exclusive cursor.
func (s *Store) ReadEvents(runID string, afterRevision uint64, limit int) (EventPage, error) {
	if limit <= 0 {
		return EventPage{}, errors.New("store: event page limit must be positive")
	}
	directory, err := s.executionDir(runID)
	if err != nil {
		return EventPage{}, err
	}
	log, err := scanEventLog(runID, filepath.Join(directory, "events.jsonl"))
	if err != nil {
		return EventPage{}, err
	}
	if log.tail != nil {
		return EventPage{}, IncompleteEventTailError{RunID: runID}
	}
	start := len(log.frames)
	if afterRevision < uint64(len(log.frames)) {
		start = int(afterRevision)
	}
	if afterRevision > uint64(len(log.frames)) {
		start = len(log.frames)
	}
	end := start + limit
	if end > len(log.frames) {
		end = len(log.frames)
	}
	page := EventPage{Events: append([]EventFrame(nil), log.frames[start:end]...)}
	page.HasMore = end < len(log.frames)
	if page.HasMore {
		page.NextSequence = uint64(end)
		page.NextRevision = uint64(end)
	}
	return page, nil
}

func (s *Store) ReadEventPage(runID string, afterRevision uint64, limit int) (EventPage, error) {
	return s.ReadEvents(runID, afterRevision, limit)
}

// ReadProjection reads the current projection. A missing projection is the
// valid initial state and is returned without creating a file.
func (s *Store) ReadProjection(runID string) (*RunProjection, error) {
	directory, err := s.executionDir(runID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(directory, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return &RunProjection{RunID: runID, State: agentrun.StateCreated}, nil
	}
	if err != nil {
		return nil, err
	}
	var projection RunProjection
	if err := json.Unmarshal(data, &projection); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProjectionCorrupt, err)
	}
	if projection.RunID != runID || !knownLifecycleState(projection.State) {
		return nil, fmt.Errorf("%w: projection identity or state is invalid", ErrProjectionCorrupt)
	}
	return &projection, nil
}

func (s *Store) ReadStateProjection(runID string) (*RunProjection, error) {
	return s.ReadProjection(runID)
}

// RebuildProjection validates and rewrites state.json from the event log.
func (s *Store) RebuildProjection(runID string) error {
	directory, err := s.executionDir(runID)
	if err != nil {
		return err
	}
	if err := ensureExecutionExists(directory); err != nil {
		return err
	}
	return withExecutionLock(directory, func() error {
		log, err := scanEventLog(runID, filepath.Join(directory, "events.jsonl"))
		if err != nil {
			return err
		}
		if log.tail != nil {
			return IncompleteEventTailError{RunID: runID}
		}
		return writeProjection(filepath.Join(directory, "state.json"), projectionFor(runID, log.frames))
	})
}

// RebuildStateProjection provides the rebuilt value for callers that need it.
func (s *Store) RebuildStateProjection(runID string) (*RunProjection, error) {
	if err := s.RebuildProjection(runID); err != nil {
		return nil, err
	}
	return s.ReadProjection(runID)
}

// RecoverEventTail repairs only an incomplete final JSONL record. It refuses
// every complete-record corruption, including a bad hash or transition.
func (s *Store) RecoverEventTail(runID string) error {
	directory, err := s.executionDir(runID)
	if err != nil {
		return err
	}
	if err := ensureExecutionExists(directory); err != nil {
		return err
	}
	return withExecutionLock(directory, func() error {
		path := filepath.Join(directory, "events.jsonl")
		log, err := scanEventLog(runID, path)
		if err != nil {
			return err
		}
		if log.tail == nil {
			return nil
		}
		file, err := os.OpenFile(path, os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		if log.tail.frame != nil {
			_, err = file.Write([]byte{'\n'})
		} else {
			err = file.Truncate(log.tail.offset)
		}
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		frames := append([]EventFrame(nil), log.frames...)
		if log.tail.frame != nil {
			frames = append(frames, *log.tail.frame)
		}
		return writeProjection(filepath.Join(directory, "state.json"), projectionFor(runID, frames))
	})
}

func (s *Store) RecoverIncompleteEventTail(runID string) error {
	return s.RecoverEventTail(runID)
}

func frameForEvent(event agentrun.NormalizedEvent, sequence, revision uint64, predecessorHash string) EventFrame {
	frame := EventFrame{
		Sequence: sequence, Revision: revision, At: event.At().UTC(),
		RunID: string(event.RunID()), JobID: string(event.JobID()),
		InvocationID: string(event.InvocationID()), LineageID: string(event.LineageIdentity()),
		From: event.From(), To: event.To(), Decision: event.Decision(),
		Terminal: event.TerminalClass(), PredecessorHash: predecessorHash,
	}
	frame.ContentHash = hashEventContent(frame.content())
	return frame
}

func (e EventFrame) content() eventContent {
	return eventContent{
		At: e.At.UTC(), RunID: e.RunID, JobID: e.JobID,
		InvocationID: e.InvocationID, LineageID: e.LineageID,
		From: e.From, To: e.To, Decision: e.Decision, Terminal: e.Terminal,
	}
}

func hashEventContent(content eventContent) string {
	data, _ := json.Marshal(content)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func appendEventFrame(path string, frame EventFrame) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func scanEventLog(runID, path string) (scannedEvents, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return scannedEvents{}, nil
	}
	if err != nil {
		return scannedEvents{}, err
	}
	parts := bytes.Split(data, []byte{'\n'})
	frames := make([]EventFrame, 0, len(parts))
	var offset int64
	for index, part := range parts {
		last := index == len(parts)-1
		if last && len(part) == 0 {
			break
		}
		if len(part) == 0 {
			return scannedEvents{}, corruption(runID, uint64(len(frames)+1), "empty event line")
		}
		var frame EventFrame
		if err := json.Unmarshal(part, &frame); err != nil {
			if last && incompleteJSON(err) {
				return scannedEvents{frames: frames, tail: &eventTail{offset: offset}}, nil
			}
			return scannedEvents{}, corruption(runID, uint64(len(frames)+1), err.Error())
		}
		if err := validateFrame(runID, frame, frames); err != nil {
			return scannedEvents{}, err
		}
		if last {
			return scannedEvents{frames: frames, tail: &eventTail{offset: offset, frame: &frame}}, nil
		}
		frames = append(frames, frame)
		offset += int64(len(part) + 1)
	}
	return scannedEvents{frames: frames}, nil
}

func validateFrame(runID string, frame EventFrame, previous []EventFrame) error {
	expected := uint64(len(previous) + 1)
	if frame.Sequence != expected {
		return corruption(runID, expected, "sequence is not contiguous")
	}
	if frame.Revision != expected {
		return corruption(runID, expected, "revision is not contiguous")
	}
	if frame.RunID != runID {
		return corruption(runID, expected, "frame run identity does not match its path")
	}
	if frame.JobID == "" || frame.InvocationID == "" || frame.LineageID == "" {
		return corruption(runID, expected, "frame identity is incomplete")
	}
	if err := agentrun.Transition(frame.From, frame.To); err != nil {
		return corruption(runID, expected, "invalid lifecycle transition")
	}
	if frame.Terminal != frame.To.TerminalClass() {
		return corruption(runID, expected, "terminal class does not match lifecycle state")
	}
	if expected == 1 {
		if frame.PredecessorHash != "" || frame.From != agentrun.StateCreated {
			return corruption(runID, expected, "first frame has an invalid predecessor or source state")
		}
	} else {
		previousFrame := previous[len(previous)-1]
		if frame.PredecessorHash != previousFrame.ContentHash {
			return corruption(runID, expected, "predecessor hash does not match the stream head")
		}
		if frame.From != previousFrame.To {
			return corruption(runID, expected, "lifecycle source does not match the stream head")
		}
	}
	if frame.ContentHash == "" || frame.ContentHash != hashEventContent(frame.content()) {
		return corruption(runID, expected, "content hash mismatch")
	}
	return nil
}

func corruption(runID string, sequence uint64, reason string) error {
	return EventCorruptionError{RunID: runID, Sequence: sequence, Reason: reason}
}

func incompleteJSON(err error) bool {
	return errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(err.Error(), "unexpected end of JSON input")
}

func projectionFor(runID string, frames []EventFrame) RunProjection {
	projection := RunProjection{RunID: runID, State: agentrun.StateCreated}
	if len(frames) == 0 {
		return projection
	}
	last := frames[len(frames)-1]
	projection.Sequence = last.Sequence
	projection.Revision = last.Revision
	projection.State = last.To
	projection.Terminal = last.Terminal
	projection.LastEventHash = last.ContentHash
	projection.UpdatedAt = last.At
	return projection
}

func writeProjection(path string, projection RunProjection) error {
	data, err := marshalRecord(projection)
	if err != nil {
		return err
	}
	return atomicWrite(path, data)
}

func writeReceipt(directory string, receipt EventReceipt) error {
	data, err := marshalRecord(receipt)
	if err != nil {
		return err
	}
	path := filepath.Join(directory, fmt.Sprintf("%020d.json", receipt.Sequence))
	return atomicWrite(path, data)
}

func ensureExecutionExists(directory string) error {
	info, err := os.Stat(filepath.Join(directory, "request.json"))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("store: execution does not exist: %s", filepath.Base(directory))
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("store: execution request record is a directory: %s", directory)
	}
	return nil
}

func knownLifecycleState(state agentrun.LifecycleState) bool {
	switch state {
	case agentrun.StateCreated, agentrun.StateQueued, agentrun.StateAdmitted,
		agentrun.StateRunning, agentrun.StateAwaitingDecision, agentrun.StateSucceeded,
		agentrun.StateFailed, agentrun.StateCanceled, agentrun.StateTimedOut,
		agentrun.StateUnavailable:
		return true
	default:
		return false
	}
}

const (
	eventLockWait = 15 * time.Second
)

func withExecutionLock(directory string, action func() error) error {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	lock, err := acquireExecutionLock(filepath.Join(directory, ".events.lock"), eventLockWait)
	if err != nil {
		return err
	}
	defer lock.Close()
	return action()
}
