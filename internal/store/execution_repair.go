package store

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Recovery repair for terminal-but-unprojected runs (ticket 10 slice 2).
// The slice-1 classifier proved that one crash window — appending a verified
// terminal frame and dying before replacing state.json — leaves an honest,
// append-only stream with a lagging snapshot. Repair closes exactly that
// window and nothing else: it replays the fully verified event stream through
// the same deterministic projection machinery production writers use
// (projectionFor + marshalRecord) and rewrites only the snapshot through the
// same atomic temp+fsync+rename path under the cross-process execution lock.
// Event bytes are never touched here; every other class is refused with its
// exact reason instead of being "fixed".

// ErrRecoveryNotRepairable means repair refused a run whose recovery class is
// not terminal-but-unprojected.
var ErrRecoveryNotRepairable = errors.New("store: projection repair applies only to terminal-but-unprojected streams")

// RecoveryNotRepairableError carries the refusing verdict verbatim so an
// operator sees the same reason a read-only scan would print for the run.
type RecoveryNotRepairableError struct {
	RunID  string
	Class  RecoveryClass
	Reason string
}

func (e RecoveryNotRepairableError) Error() string {
	return fmt.Sprintf("store: cannot repair run %s classified %s: %s", e.RunID, e.Class, e.Reason)
}

func (e RecoveryNotRepairableError) Unwrap() error { return ErrRecoveryNotRepairable }

// RecoveryRepairResult reports one repair attempt. Rewritten is false when
// the replay already matched the persisted snapshot byte for byte, which is
// the deterministic no-op proof for healthy stores.
type RecoveryRepairResult struct {
	RunID       string        `json:"run_id"`
	ClassBefore RecoveryClass `json:"class_before"`
	ClassAfter  RecoveryClass `json:"class_after"`
	Rewritten   bool          `json:"rewritten"`
}

// ReplayProjection serializes the deterministic projection of already
// verified frames into exactly the bytes writeProjection persists. It is the
// single source of truth shared by the repair writer and the determinism
// tests: replaying a healthy stream must reproduce the on-disk state.json
// byte for byte.
func ReplayProjection(runID string, frames []EventFrame) ([]byte, error) {
	return marshalRecord(projectionFor(runID, frames))
}

// RepairTerminalUnprojected rebuilds state.json from the verified event
// stream when — and only when — the classifier proves the lagging-snapshot
// window: a valid terminal frame whose persisted snapshot lags or is
// unreadable. Corrupt, orphaned-canceled, recoverable, operator-required,
// and settled runs are refused with a RecoveryNotRepairableError carrying
// their exact verdict; nothing is written on any refusal path.
func RepairTerminalUnprojected(s *Store, runID string) (RecoveryRepairResult, error) {
	if s == nil {
		return RecoveryRepairResult{}, errors.New("store: projection repair requires a store")
	}
	directory, err := s.executionDir(runID)
	if err != nil {
		return RecoveryRepairResult{}, err
	}
	if err := ensureExecutionExists(directory); err != nil {
		return RecoveryRepairResult{}, err
	}
	var result RecoveryRepairResult
	lockErr := withExecutionLock(directory, func() error {
		var repairErr error
		result, repairErr = repairProjectionLocked(directory, runID)
		return repairErr
	})
	if lockErr != nil {
		return RecoveryRepairResult{}, lockErr
	}
	return result, nil
}

// repairProjectionLocked runs under the execution lock so the classification,
// the replay, and the snapshot replacement observe one consistent stream.
func repairProjectionLocked(directory, runID string) (RecoveryRepairResult, error) {
	before := classifyRecovery(runID, readRecoveryEvidence(runID, directory))
	if before.Class != RecoveryTerminalUnprojected {
		return RecoveryRepairResult{}, RecoveryNotRepairableError{RunID: runID, Class: before.Class, Reason: before.Reason}
	}
	log, err := scanEventLog(runID, filepath.Join(directory, "events.jsonl"))
	if err != nil {
		return RecoveryRepairResult{}, err
	}
	if log.tail != nil {
		return RecoveryRepairResult{}, IncompleteEventTailError{RunID: runID}
	}
	data, err := ReplayProjection(runID, log.frames)
	if err != nil {
		return RecoveryRepairResult{}, err
	}
	path := filepath.Join(directory, "state.json")
	current, readErr := os.ReadFile(path)
	rewritten := false
	switch {
	case errors.Is(readErr, os.ErrNotExist):
		rewritten = true
		if err := atomicWrite(path, data); err != nil {
			return RecoveryRepairResult{}, err
		}
	case readErr != nil:
		return RecoveryRepairResult{}, readErr
	case !bytes.Equal(current, data):
		rewritten = true
		if err := atomicWrite(path, data); err != nil {
			return RecoveryRepairResult{}, err
		}
	}
	after := classifyRecovery(runID, readRecoveryEvidence(runID, directory))
	if after.Class != RecoverySettled {
		return RecoveryRepairResult{}, fmt.Errorf("store: rebuilt projection for %s still classifies as %s: %s", runID, after.Class, after.Reason)
	}
	return RecoveryRepairResult{
		RunID: runID, ClassBefore: RecoveryTerminalUnprojected,
		ClassAfter: RecoverySettled, Rewritten: rewritten,
	}, nil
}
