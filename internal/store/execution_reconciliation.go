package store

import (
	"path/filepath"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
)

// Restart reconciliation for owner death during cancellation (ticket 08
// slice 3). A run left non-terminal whose stream already recorded the whole
// tree being terminated — but never received its canceled settlement frame —
// is the durable fingerprint of an owner that died while its own
// cancellation was still settling. The honest restart view of such a run is
// canceled-orphaned: no fabricated completion, no silent resume, no rewrite
// of the append-only stream. Plain running heads without any escalation
// transition stay untouched: their recovery classification belongs to R8.

// reconcileProjection derives the honest read-time view of a run from its
// validated frames. An authoritative terminal settlement always wins; a
// non-terminal head over escalation transitions (terminating/terminated)
// classifies as canceled-orphaned; anything else projects exactly as the raw
// stream does. The derivation never mutates or appends persisted bytes.
func reconcileProjection(runID string, frames []EventFrame) RunProjection {
	projection := projectionFor(runID, frames)
	if projection.Terminal != agentrun.TerminalNone {
		return projection
	}
	lastTerminalIdx := -1
	for i, frame := range frames {
		if frame.Terminal != agentrun.TerminalNone {
			lastTerminalIdx = i
		}
	}
	start := lastTerminalIdx + 1
	for _, frame := range frames[start:] {
		if frame.To == agentrun.StateTerminating || frame.To == agentrun.StateTerminated {
			projection.State = agentrun.StateCanceled
			projection.Terminal = agentrun.TerminalCancellation
			projection.OrphanedCancellation = true
			break
		}
	}
	return projection
}

// escalationTransition reports whether one frame records a bounded-escalation
// transition of ticket 08 slice 2: the termination attempt against the owned
// process tree, or its confirmed reaping.
func escalationTransition(frame EventFrame) bool {
	return frame.To == agentrun.StateTerminating || frame.To == agentrun.StateTerminated
}

// ReadReconciledProjection reads the current projection with restart
// reconciliation applied on top of the validated event stream. It behaves
// exactly like ReadDerivedProjection except when the stream proves owner
// death mid-cancellation:
//
//   - A complete non-terminal stream whose frames show terminating or
//     terminated transitions without any terminal frame derives the
//     canceled-orphaned view (OrphanedCancellation set).
//   - A truncated final record that is itself a fully valid hash-chained
//     escalation frame — the writer crashed after flushing the frame bytes
//     but before its newline — derives the same honest view from it.
//   - Every other incomplete final tail keeps failing closed with
//     IncompleteEventTailError; repairing those bytes remains `sentinel runs
//     recover`'s job.
//
// The underlying files are never written by this call: old readers keep
// seeing the raw stream, new readers see the honest classification.
func (s *Store) ReadReconciledProjection(runID string) (*RunProjection, error) {
	directory, err := s.executionDir(runID)
	if err != nil {
		return nil, err
	}
	if err := ensureExecutionExists(directory); err != nil {
		return nil, err
	}
	log, err := scanEventLog(runID, filepath.Join(directory, "events.jsonl"))
	if err != nil {
		return nil, err
	}
	frames := log.frames
	if log.tail != nil {
		if log.tail.frame == nil || !escalationTransition(*log.tail.frame) {
			return nil, IncompleteEventTailError{RunID: runID}
		}
		frames = append(append([]EventFrame(nil), log.frames...), *log.tail.frame)
	}
	reconciled := reconcileProjection(runID, frames)
	return &reconciled, nil
}
