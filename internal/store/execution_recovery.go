package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
)

// Recovery classification for interrupted durable runs (ticket 10 slice 1).
// The classifier is a pure derivation over verified event streams plus the
// persisted projection snapshot: it never writes, never repairs bytes, and
// maps every stream to exactly one class. Scanning extends the R7 read-time
// reconciliation instead of duplicating it — orphaned-canceled verdicts come
// from reconcileProjection itself, and corruption carries the exact
// underlying error text so operators see the real defect.

// RecoveryClass is the evidence-based recovery verdict for one durable run.
type RecoveryClass string

const (
	// RecoveryRecoverable marks an awaiting-decision head whose recorded
	// decision evidence is intact: exactly today's resumable case that
	// Controller.Recover accepts.
	RecoveryRecoverable RecoveryClass = "recoverable"
	// RecoveryTerminalUnprojected marks a verified terminal frame whose
	// persisted state.json snapshot lags it. Repair is a deterministic
	// projection rebuild from the verified stream, never invented frames.
	RecoveryTerminalUnprojected RecoveryClass = "terminal_unprojected"
	// RecoveryCorrupt marks a hash-chain break, an invalid lifecycle
	// transition, or an unreadable partial tail. The reason carries the
	// exact underlying error text; nothing is silently repaired.
	RecoveryCorrupt RecoveryClass = "corrupt"
	// RecoveryOrphanedCanceled carries the R7 reconciled owner-death-during-
	// cancellation verdict, which stays final unless the operator retries.
	RecoveryOrphanedCanceled RecoveryClass = "orphaned_canceled"
	// RecoveryOperatorRequired marks evidence that cannot decide between
	// outcomes; the reason names the exact missing evidence.
	RecoveryOperatorRequired RecoveryClass = "operator_required"
	// RecoverySettled marks healthy terminal streams whose snapshot already
	// matches the stream head. They are not recoverable work; ScanRecoveries
	// excludes them, and the value exists only so the classifier stays total.
	RecoverySettled RecoveryClass = "settled"
)

// RecoveryEntry is one classified non-terminal run produced by a recovery
// scan. Reconciled is true only when the entry derives from the R7 restart
// reconciliation rather than the raw stream head.
type RecoveryEntry struct {
	RunID        string        `json:"run_id"`
	Class        RecoveryClass `json:"class"`
	Reason       string        `json:"reason"`
	HeadSequence uint64        `json:"head_sequence"`
	Reconciled   bool          `json:"reconciled,omitempty"`
}

// recoveryEvidence bundles everything the classifier may look at. Every field
// is optional; unknown shapes default to operator-required instead of panicking.
type recoveryEvidence struct {
	// frames are the verified hash-chained frames, including a valid final
	// escalation tail frame when one survived a crash before its newline.
	frames []EventFrame
	// tailErr reports an incomplete final JSONL record that carries no
	// trustworthy evidence.
	tailErr error
	// scanErr reports corruption found while validating complete records.
	scanErr error
	// snapshot is the persisted state.json content; nil means the file is
	// absent. snapshotErr reports an unreadable snapshot.
	snapshot    *RunProjection
	snapshotErr error
}

func recoveryEntry(runID string, class RecoveryClass, reason string, headSequence uint64, reconciled bool) RecoveryEntry {
	return RecoveryEntry{
		RunID: runID, Class: class, Reason: reason,
		HeadSequence: headSequence, Reconciled: reconciled,
	}
}

// classifyRecovery derives exactly one recovery verdict from the given
// evidence. It is total: every input shape returns a class, and shapes the
// evidence cannot decide default to operator-required with the missing
// evidence named.
func classifyRecovery(runID string, evidence recoveryEvidence) RecoveryEntry {
	switch {
	case evidence.scanErr != nil:
		return recoveryEntry(runID, RecoveryCorrupt, evidence.scanErr.Error(), 0, false)
	case evidence.tailErr != nil:
		return recoveryEntry(runID, RecoveryCorrupt, evidence.tailErr.Error(), uint64(len(evidence.frames)), false)
	case len(evidence.frames) == 0:
		return recoveryEntry(runID, RecoveryOperatorRequired,
			"stream records no events: missing admission evidence for every recovery outcome", 0, false)
	}
	reconciled := reconcileProjection(runID, evidence.frames)
	head := evidence.frames[len(evidence.frames)-1]
	if reconciled.OrphanedCancellation {
		return recoveryEntry(runID, RecoveryOrphanedCanceled,
			"owner death during cancellation: escalation transitions are recorded without any canceled settlement frame",
			head.Sequence, true)
	}
	if head.Terminal != agentrun.TerminalNone {
		switch {
		case evidence.snapshotErr != nil:
			return recoveryEntry(runID, RecoveryTerminalUnprojected,
				fmt.Sprintf("verified %s terminal frame exists but the persisted snapshot is unreadable (%s); rebuild can repair the snapshot",
					head.To, evidence.snapshotErr.Error()), head.Sequence, false)
		case evidence.snapshot == nil:
			return recoveryEntry(runID, RecoveryTerminalUnprojected,
				fmt.Sprintf("verified %s terminal frame exists but no persisted snapshot does; rebuild can write it from the stream",
					head.To), head.Sequence, false)
		case evidence.snapshot.Sequence < head.Sequence || evidence.snapshot.Revision < head.Revision:
			return recoveryEntry(runID, RecoveryTerminalUnprojected,
				fmt.Sprintf("verified %s terminal frame exists but the persisted snapshot lags it (snapshot revision %d, stream head revision %d)",
					head.To, evidence.snapshot.Revision, head.Revision), head.Sequence, false)
		default:
			return recoveryEntry(runID, RecoverySettled,
				"terminal stream already projected; no recovery work applies", head.Sequence, false)
		}
	}
	if head.To == agentrun.StateAwaitingDecision && reconciled.State == agentrun.StateAwaitingDecision {
		return recoveryEntry(runID, RecoveryRecoverable,
			"awaiting-decision head with intact decision evidence: resume reconstructs the pending decision without launching an adapter call",
			head.Sequence, false)
	}
	// Operator-required fallback: the reason names the exact evidence whose
	// absence blocks every automatic verdict, per stream shape, instead of a
	// generic refusal an operator would have to decode.
	var reason string
	switch head.To {
	case agentrun.StateRunning:
		reason = "head running without a terminal frame, an awaiting-decision head, or cancellation escalation transitions: outcome unknown"
	case agentrun.StateCreated, agentrun.StateQueued, agentrun.StateAdmitted:
		reason = fmt.Sprintf("head %s never reached running: outcome unknown because no execution progress, terminal frame, or cancellation transition is recorded", head.To)
	default:
		reason = fmt.Sprintf("non-terminal %s head cannot decide recovery: missing a terminal frame, an awaiting-decision head, or cancellation escalation transitions", head.To)
	}
	return recoveryEntry(runID, RecoveryOperatorRequired, reason, head.Sequence, false)
}

// readRecoveryEvidence gathers the scan inputs for one run. It only reads:
// no lock is taken and no byte is written anywhere on this path.
func readRecoveryEvidence(runID, directory string) recoveryEvidence {
	var evidence recoveryEvidence
	log, err := scanEventLog(runID, filepath.Join(directory, "events.jsonl"))
	if err != nil {
		evidence.scanErr = err
		evidence.frames = []EventFrame{}
		return evidence
	}
	evidence.frames = append([]EventFrame(nil), log.frames...)
	if log.tail != nil {
		// Mirror ReadReconciledProjection: a fully valid escalation tail
		// frame proves owner death mid-cancellation; every other incomplete
		// tail keeps failing closed with its exact defect.
		if log.tail.frame == nil || !escalationTransition(*log.tail.frame) {
			evidence.tailErr = IncompleteEventTailError{RunID: runID}
			return evidence
		}
		evidence.frames = append(evidence.frames, *log.tail.frame)
	}
	data, err := os.ReadFile(filepath.Join(directory, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return evidence
	}
	if err != nil {
		evidence.snapshotErr = err
		return evidence
	}
	var snapshot RunProjection
	if err := json.Unmarshal(data, &snapshot); err != nil {
		evidence.snapshotErr = fmt.Errorf("%w: %v", ErrProjectionCorrupt, err)
		return evidence
	}
	evidence.snapshot = &snapshot
	return evidence
}

// ScanRecoveries lists one recovery entry for every non-terminal run in the
// store, classified purely from verified durable evidence. It is READ-ONLY:
// no file is created, replaced, locked, or appended on this path. Runs whose
// projections are already terminal are excluded entirely; a stray directory
// without an execution record is ignored like in ListExecutionIDs.
func ScanRecoveries(s *Store) ([]RecoveryEntry, error) {
	if s == nil {
		return nil, errors.New("store: recovery scan requires a store")
	}
	ids, err := s.ListExecutionIDs()
	if err != nil {
		return nil, err
	}
	entries := make([]RecoveryEntry, 0, len(ids))
	for _, id := range ids {
		entry, found, err := scanRecovery(s, id)
		if err != nil {
			return nil, err
		}
		if found && entry.Class != RecoverySettled {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

// scanRecovery classifies one run. found stays false for stray directories
// without an admission record, which are not recoverable work.
func scanRecovery(s *Store, runID string) (RecoveryEntry, bool, error) {
	directory, err := s.executionDir(runID)
	if err != nil {
		return RecoveryEntry{}, false, err
	}
	if err := ensureExecutionExists(directory); err != nil {
		if errors.Is(err, ErrExecutionNotFound) {
			return RecoveryEntry{}, false, nil
		}
		return RecoveryEntry{}, false, err
	}
	return classifyRecovery(runID, readRecoveryEvidence(runID, directory)), true, nil
}
