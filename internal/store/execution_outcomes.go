package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
)

var ErrAttemptOutcomeCorrupt = errors.New("store: corrupt attempt outcome")

// AttemptOutcome is the admitted operational result of one physical adapter
// invocation. Raw adapter output is deliberately not persisted; OutputHash
// binds any output returned to the caller to this invocation. The transcript
// and identity fields are additive provenance: every one is omitempty, so
// records written before raw-transcript threading keep their exact legacy
// bytes and old readers ignore the new keys.
type AttemptOutcome struct {
	RunID        string                `json:"run_id"`
	JobID        string                `json:"job_id"`
	InvocationID string                `json:"invocation_id"`
	LineageID    string                `json:"lineage_id"`
	Class        agentrun.OutcomeClass `json:"class"`
	Error        string                `json:"error,omitempty"`
	OutputHash   string                `json:"output_hash,omitempty"`
	At           time.Time             `json:"at"`
	// TranscriptSHA256 and TranscriptSize are tamper evidence over the exact
	// bytes of <execution-dir>/transcripts/<InvocationID>.json (see
	// VerifyTranscript). They are recorded only when the sidecar write
	// succeeded; absence means no transcript exists for this invocation.
	TranscriptSHA256 string `json:"transcript_sha256,omitempty"`
	TranscriptSize   int64  `json:"transcript_size,omitempty"`
	// Agent, Model, Effort, and StopReason are the observed effective
	// identity of the responder, reported by the adapter only after a
	// successful request. They are never fabricated: fields the provider did
	// not expose stay empty.
	Agent      string `json:"agent,omitempty"`
	Model      string `json:"model,omitempty"`
	Effort     string `json:"effort,omitempty"`
	StopReason string `json:"stop_reason,omitempty"`
}

// InvocationResponse binds an explicit control response to the child
// invocation that will consume it. The answer itself stays at the adapter
// seam; only its content hash is durable evidence.
type InvocationResponse struct {
	RunID              string    `json:"run_id"`
	ParentInvocationID string    `json:"parent_invocation_id"`
	InvocationID       string    `json:"invocation_id"`
	LineageID          string    `json:"lineage_id"`
	ResponseHash       string    `json:"response_hash"`
	At                 time.Time `json:"at"`
}

// SaveAttemptOutcome durably records an invocation result before the caller
// observes completion. Repeating the same record is idempotent; changing it
// is rejected as an immutable conflict.
func (s *Store) SaveAttemptOutcome(outcome AttemptOutcome) error {
	if err := validateAttemptOutcome(outcome); err != nil {
		return err
	}
	directory, err := s.executionDir(outcome.RunID)
	if err != nil {
		return err
	}
	if err := ensureExecutionExists(directory); err != nil {
		return err
	}
	data, err := marshalRecord(outcome)
	if err != nil {
		return err
	}
	path := filepath.Join(directory, "outcomes", outcome.InvocationID+".json")
	return withExecutionLock(directory, func() error {
		return writeImmutableRecord(path, data)
	})
}

// ReadAttemptOutcomes returns every admitted invocation outcome in completion
// order. A missing outcomes directory is the valid state for a run that has
// not reached an adapter boundary yet.
func (s *Store) ReadAttemptOutcomes(runID string) ([]AttemptOutcome, error) {
	directory, err := s.executionDir(runID)
	if err != nil {
		return nil, err
	}
	if err := ensureExecutionExists(directory); err != nil {
		return nil, err
	}
	// Ticket 14 diagnosis: the legacy sidecar records are read BEFORE the
	// event log on purpose. AppendTerminalEvent appends the terminal event
	// first and writes outcomes/<invocation>.json second, both inside one
	// locked section; a lock-free reader that scanned the log first and the
	// sidecars second could observe the fresh sidecar with a stale log and
	// misreport a mid-persistence write as "outcome has no terminal event"
	// (the flaky TestRunsRetryRelaunchesFailedRunInsideSameIdentity root
	// cause). Reading sidecars first closes that window: observing a
	// sidecar proves its terminal append already completed, so the later
	// log scan must include it, while missing sidecars yield the honest
	// pre-settlement view. Genuine incomplete persistence (a sidecar whose
	// terminal event never arrives) still reports exactly the same error.
	legacy, legacyErr := readAttemptOutcomes(filepath.Join(directory, "outcomes"), runID)
	log, scanErr := scanEventLog(runID, filepath.Join(directory, "events.jsonl"))
	if scanErr != nil {
		return nil, scanErr
	}
	if log.tail != nil {
		return nil, IncompleteEventTailError{RunID: runID}
	}
	terminalFrames := terminalEventFrames(log.frames)
	if len(log.frames) == 0 {
		// A staged outcome recorded before any lifecycle event (the public
		// SaveAttemptOutcome contract) stays readable: an empty stream with
		// sidecars is admitted evidence in waiting, not corruption.
		if legacyErr != nil {
			return nil, legacyErr
		}
		return legacy, nil
	}
	// Event-authoritative runs win over the legacy surface entirely: when
	// every terminal frame carries its embedded outcome, a missing or even
	// corrupt sidecar must not fail the read.
	if terminalFramesHaveEmbeddedEvidence(terminalFrames) {
		return outcomesFromFrames(terminalFrames, nil), nil
	}
	if legacyErr != nil {
		if len(terminalFrames) > 0 {
			return nil, terminalPersistenceError(runID, terminalFrames[0].InvocationID, "outcome", legacyErr)
		}
		return nil, legacyErr
	}
	if len(terminalFrames) == 0 {
		if len(legacy) > 0 {
			return nil, terminalPersistenceError(runID, legacy[0].InvocationID, "outcome", errors.New("outcome has no terminal event"))
		}
		return []AttemptOutcome{}, nil
	}
	return outcomesFromFrames(terminalFrames, legacy), nil
}

// SaveInvocationResponse durably binds a response to a child invocation
// before that invocation is started.
func (s *Store) SaveInvocationResponse(response InvocationResponse) error {
	if err := validateInvocationResponse(response); err != nil {
		return err
	}
	directory, err := s.executionDir(response.RunID)
	if err != nil {
		return err
	}
	if err := ensureExecutionExists(directory); err != nil {
		return err
	}
	data, err := marshalRecord(response)
	if err != nil {
		return err
	}
	path := filepath.Join(directory, "responses", response.InvocationID+".json")
	return withExecutionLock(directory, func() error {
		return writeImmutableRecord(path, data)
	})
}

// ReadInvocationResponses returns the durable control-response lineage for a
// run, sorted by admission time.
func (s *Store) ReadInvocationResponses(runID string) ([]InvocationResponse, error) {
	directory, err := s.executionDir(runID)
	if err != nil {
		return nil, err
	}
	if err := ensureExecutionExists(directory); err != nil {
		return nil, err
	}
	return readInvocationResponses(filepath.Join(directory, "responses"), runID)
}

func validateAttemptOutcome(outcome AttemptOutcome) error {
	if !validRunID(outcome.RunID) || outcome.JobID == "" || !validRunID(outcome.InvocationID) || outcome.LineageID == "" {
		return fmt.Errorf("%w: incomplete invocation identity", ErrAttemptOutcomeCorrupt)
	}
	if !knownOutcomeClass(outcome.Class) {
		return fmt.Errorf("%w: unknown outcome class %q", ErrAttemptOutcomeCorrupt, outcome.Class)
	}
	if outcome.At.IsZero() {
		return fmt.Errorf("%w: outcome timestamp is empty", ErrAttemptOutcomeCorrupt)
	}
	return nil
}

func validateInvocationResponse(response InvocationResponse) error {
	if !validRunID(response.RunID) || !validRunID(response.ParentInvocationID) ||
		!validRunID(response.InvocationID) || response.LineageID == "" || response.ResponseHash == "" {
		return fmt.Errorf("%w: incomplete response identity", ErrAttemptOutcomeCorrupt)
	}
	if response.At.IsZero() {
		return fmt.Errorf("%w: response timestamp is empty", ErrAttemptOutcomeCorrupt)
	}
	return nil
}

func knownOutcomeClass(class agentrun.OutcomeClass) bool {
	switch class {
	case agentrun.OutcomeSuccess, agentrun.OutcomeFailure, agentrun.OutcomeUnavailable,
		agentrun.OutcomeTimeout, agentrun.OutcomeCancellation, agentrun.OutcomeProcessError,
		agentrun.OutcomeAwaitingDecision:
		return true
	default:
		return false
	}
}

func terminalEventFrames(frames []EventFrame) []EventFrame {
	terminal := make([]EventFrame, 0)
	for _, frame := range frames {
		if frame.To.TerminalClass() != agentrun.TerminalNone {
			terminal = append(terminal, frame)
		}
	}
	return terminal
}

func terminalFramesHaveEmbeddedEvidence(frames []EventFrame) bool {
	if len(frames) == 0 {
		return false
	}
	for _, frame := range frames {
		if frame.OutcomeClass == "" {
			return false
		}
	}
	return true
}

func outcomesFromFrames(frames []EventFrame, legacy []AttemptOutcome) []AttemptOutcome {
	legacyByInvocation := make(map[string]AttemptOutcome, len(legacy))
	for _, outcome := range legacy {
		legacyByInvocation[outcome.InvocationID] = outcome
	}
	outcomes := make([]AttemptOutcome, 0, len(frames))
	for _, frame := range frames {
		class := frame.OutcomeClass
		if class == "" {
			class = outcomeClassForLifecycle(frame.To)
		}
		outcome := AttemptOutcome{
			RunID: frame.RunID, JobID: frame.JobID, InvocationID: frame.InvocationID,
			LineageID: frame.LineageID, Class: class, Error: frame.OutcomeError,
			OutputHash: frame.OutputHash, At: frame.At,
		}
		if frame.OutcomeClass == "" {
			if legacyOutcome, ok := legacyByInvocation[frame.InvocationID]; ok {
				outcome = legacyOutcome
			}
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes
}

func readAttemptOutcomes(directory, runID string) ([]AttemptOutcome, error) {
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return []AttemptOutcome{}, nil
	}
	if err != nil {
		return nil, err
	}
	outcomes := make([]AttemptOutcome, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tmp-") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if info.IsDir() || info.Mode()&os.ModeSymlink != 0 || filepath.Ext(entry.Name()) != ".json" {
			return nil, fmt.Errorf("%w: invalid outcome file %s", ErrAttemptOutcomeCorrupt, entry.Name())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var outcome AttemptOutcome
		if err := json.Unmarshal(data, &outcome); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrAttemptOutcomeCorrupt, entry.Name(), err)
		}
		if outcome.RunID != runID || strings.TrimSuffix(entry.Name(), ".json") != outcome.InvocationID {
			return nil, fmt.Errorf("%w: outcome identity does not match %s", ErrAttemptOutcomeCorrupt, entry.Name())
		}
		if err := validateAttemptOutcome(outcome); err != nil {
			return nil, err
		}
		outcomes = append(outcomes, outcome)
	}
	sort.Slice(outcomes, func(i, j int) bool {
		if outcomes[i].At.Equal(outcomes[j].At) {
			return outcomes[i].InvocationID < outcomes[j].InvocationID
		}
		return outcomes[i].At.Before(outcomes[j].At)
	})
	return outcomes, nil
}

func readInvocationResponses(directory, runID string) ([]InvocationResponse, error) {
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return []InvocationResponse{}, nil
	}
	if err != nil {
		return nil, err
	}
	responses := make([]InvocationResponse, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tmp-") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if info.IsDir() || info.Mode()&os.ModeSymlink != 0 || filepath.Ext(entry.Name()) != ".json" {
			return nil, fmt.Errorf("%w: invalid response file %s", ErrAttemptOutcomeCorrupt, entry.Name())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var response InvocationResponse
		if err := json.Unmarshal(data, &response); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrAttemptOutcomeCorrupt, entry.Name(), err)
		}
		if response.RunID != runID || strings.TrimSuffix(entry.Name(), ".json") != response.InvocationID {
			return nil, fmt.Errorf("%w: response identity does not match %s", ErrAttemptOutcomeCorrupt, entry.Name())
		}
		if err := validateInvocationResponse(response); err != nil {
			return nil, err
		}
		responses = append(responses, response)
	}
	sort.Slice(responses, func(i, j int) bool {
		if responses[i].At.Equal(responses[j].At) {
			return responses[i].InvocationID < responses[j].InvocationID
		}
		return responses[i].At.Before(responses[j].At)
	})
	return responses, nil
}
