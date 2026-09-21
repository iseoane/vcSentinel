package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
)

// Retention and purge policy for durable execution records (ticket 12,
// slice 3). Nothing in the store ever purges automatically: pruning is an
// explicit operator action driven by `sentinel runs prune --older-than`.
//
// A run is PRUNABLE only when every guard holds:
//   - its admission record exists and decodes (a partial or corrupt record
//     is never removed);
//   - its event stream scans clean, with no incomplete final tail;
//   - the honest reconciled view is terminal and NOT canceled-orphaned
//     (an orphaned-canceled stream keeps its recovery evidence until an
//     explicit recover or repair settles it);
//   - its last event predates the cutoff;
//   - none of its invocation identities is referenced as review provenance
//     by the caller-supplied reference set;
//   - its immutable metrics snapshot exists (a missing snapshot is absence
//     of evidence, never a zero: T9.5 never deletes before measurement
//     survives);
//   - it settled in a single attempt (retry multiplicity lives only in the
//     event stream, which the snapshot does not record);
//   - its snapshot agrees with its terminal outcomes (terminal success
//     beside snapshot failures keeps the stream: collecting it would flip
//     the run from success to failed);
//   - it is not the parent linkage target of any surviving run.
//
// The single exception is a crash-interrupted removal: a directory holding
// ONLY its event-lock file (no request.json, no events file) carries no
// evidence at all, so the next prune finishes what the interrupted pass
// started, whatever its age.
//
// Everything else is KEPT with a stable machine-readable reason. Prune
// removes whole execution directories only; it never rewrites the bytes of
// a surviving record.

// Stable operator-facing strings emitted by prune decisions. They are part
// of the machine-readable contract documented in docs/design/runs-cli.md; the
// doc-pin test keeps them and the documentation table in lockstep.
const (
	PruneActionPruned = "pruned"
	PruneActionKept   = "kept"
)

// Fixed retention-reason strings used verbatim in PruneDecision.Reason.
const (
	// PruneReasonPruned accompanies every successful removal.
	PruneReasonPruned = "pruned"
	// PruneReasonTerminalRecent keeps a terminal record younger than cutoff.
	PruneReasonTerminalRecent = "terminal-recent"
	// PruneReasonNonTerminal keeps any stream whose reconciled head is not
	// terminal.
	PruneReasonNonTerminal = "non-terminal"
	// PruneReasonCorruptTail keeps a stream ending in an incomplete tail.
	PruneReasonCorruptTail = "corrupt: incomplete final event tail"
	// PruneReasonIncompleteAdmission keeps any listed directory whose
	// admission record is missing or undecodable while real event bytes
	// remain present.
	PruneReasonIncompleteAdmission = "incomplete or corrupt admission record"
	// PruneReasonNoMetricsSnapshot keeps a run whose immutable metrics
	// snapshot has not been written. T9.5 collects execution detail only
	// when the measurement survives it: a missing snapshot is absence of
	// evidence, never a zero, so the stream stays until its snapshot
	// exists.
	PruneReasonNoMetricsSnapshot = "awaiting metrics snapshot"
	// PruneReasonMultiAttempt keeps a run settled in more than one attempt.
	// The snapshot records no attempt multiplicity, so the retried-runs
	// aggregate reads it only from the event stream: deleting the stream
	// would move a measurement retention promises to leave untouched.
	PruneReasonMultiAttempt = "multiple attempts: retry evidence lives only in the event stream"
	// PruneReasonContradiction keeps a run whose snapshot disagrees with
	// its terminal outcomes: terminal success beside snapshot failures.
	// Collecting it would flip the run from success to failed, so
	// agreement — not mere presence — is the collection bar.
	PruneReasonContradiction = "snapshot contradicts terminal success"
	// PruneReasonOrphanedCanceled keeps owner-death recovery evidence.
	PruneReasonOrphanedCanceled = "orphaned-canceled recovery evidence"
	// PruneReasonRemovalRemnant reports the successful removal of a
	// crash-interrupted leftover: a directory holding only its event-lock
	// file.
	PruneReasonRemovalRemnant = "prunable-remnant"
)

// Format-string retention reasons; the verb supplies dynamic evidence after
// the stable prefix quoted in docs/design/runs-cli.md.
const (
	PruneReasonUnreadableFmt       = "unreadable: %v"
	PruneReasonCorruptFmt          = "corrupt: %v"
	PruneReasonProvenanceFmt       = "review provenance references invocation %s"
	PruneReasonParentOfSurvivorFmt = "parent of surviving run %s"
	PruneReasonRemovalFailedFmt    = "removal failed: %v"
)

// PruneDecision records what the prune pass decided for one execution
// record. Reason is a stable, operator-facing string: "pruned" for removals,
// otherwise why the record was retained.
type PruneDecision struct {
	RunID  string `json:"run_id"`
	Action string `json:"action"`
	Reason string `json:"reason"`
}

// PruneReport is the complete result of one prune pass. Decisions is an
// empty array, never null, when there is nothing to examine.
type PruneReport struct {
	Cutoff    time.Time       `json:"cutoff"`
	Examined  int             `json:"examined"`
	Pruned    int             `json:"pruned"`
	Kept      int             `json:"kept"`
	Decisions []PruneDecision `json:"decisions"`
}

// pruneInLockRefusal marks a removal refused by the re-verification inside
// the cross-process event lock (the classify→delete TOCTOU window): the
// record changed provenance between classification and deletion. Reason
// carries the final stable keep reason for the operator report, so the
// refusal surfaces exactly like any classification-time retention guard.
type pruneInLockRefusal struct {
	reason string
}

func (e pruneInLockRefusal) Error() string { return e.reason }

// pruneRaceWindowHook, when non-nil, runs inside PruneExecutions after
// classification and before any removal. Production code never sets it;
// tests use it to persist new provenance state inside the classify→delete
// window and prove the locked section refuses what classification missed.
var pruneRaceWindowHook func(s *Store)

// prunableRun is the intermediate classification of one examined run.
type prunableRun struct {
	runID       string
	parentRunID string
	prunable    bool
	// remnant marks a crash-interrupted removal leftover: the removal
	// path skips stream re-verification because there is no stream left
	// to verify.
	remnant bool
	reason  string
}

// PruneExecutions removes ONLY terminal, non-referenced executions whose
// last event predates cutoff, refusing every other record with an explicit
// reason. referencedInvocations carries the invocation identities that
// review evidence still cites (ledger fichas and persisted findings); the
// caller builds it so this package stays free of review-package policy.
// The returned report lists one decision per examined run; a global listing
// failure returns the error with an empty report.
func (s *Store) PruneExecutions(cutoff time.Time, referencedInvocations map[string]bool) (PruneReport, error) {
	report := PruneReport{Cutoff: cutoff, Decisions: []PruneDecision{}}
	ids, err := s.ListExecutionIDs()
	if err != nil {
		return report, err
	}
	report.Examined = len(ids)

	classified := make([]prunableRun, 0, len(ids))
	for _, id := range ids {
		run := s.classifyForPrune(id, cutoff, referencedInvocations)
		classified = append(classified, run)
	}

	// Parent protection: any parent linkage target of a SURVIVING run is
	// kept, even when the parent itself would be prunable on its own.
	for _, run := range classified {
		if run.prunable || run.parentRunID == "" {
			continue
		}
		for index := range classified {
			other := &classified[index]
			if other.runID == run.parentRunID && other.prunable {
				other.prunable = false
				other.reason = fmt.Sprintf(PruneReasonParentOfSurvivorFmt, run.runID)
			}
		}
	}

	// Test seam: persist state inside the classify→delete window.
	if pruneRaceWindowHook != nil {
		pruneRaceWindowHook(s)
	}

	for _, run := range classified {
		if !run.prunable {
			report.Kept++
			report.Decisions = append(report.Decisions, PruneDecision{
				RunID: run.runID, Action: PruneActionKept, Reason: run.reason,
			})
			continue
		}
		var removalErr error
		if run.remnant {
			removalErr = s.removeExecutionRemnant(run.runID)
		} else {
			removalErr = s.removeExecutionDirectory(run.runID, referencedInvocations)
		}
		if removalErr != nil {
			report.Kept++
			reason := fmt.Sprintf(PruneReasonRemovalFailedFmt, removalErr)
			var refusal pruneInLockRefusal
			if errors.As(removalErr, &refusal) {
				reason = refusal.reason
			}
			report.Decisions = append(report.Decisions, PruneDecision{
				RunID: run.runID, Action: PruneActionKept, Reason: reason,
			})
			continue
		}
		report.Pruned++
		reason := PruneReasonPruned
		if run.remnant {
			reason = PruneReasonRemovalRemnant
		}
		report.Decisions = append(report.Decisions, PruneDecision{
			RunID: run.runID, Action: PruneActionPruned, Reason: reason,
		})
	}
	return report, nil
}

// classifyForPrune applies the retention guards to one run. The returned
// prunableRun is either a removal candidate (prunable set) or kept with its
// stable reason.
func (s *Store) classifyForPrune(runID string, cutoff time.Time, referencedInvocations map[string]bool) prunableRun {
	// parentOfSurvivor carries this run's own parent linkage out of every
	// classification branch: the guard protects the parent of any SURVIVING
	// run, so the linkage must survive even when this run itself is kept.
	var survivorLink string
	kept := func(reason string) prunableRun {
		return prunableRun{runID: runID, parentRunID: survivorLink, prunable: false, reason: reason}
	}
	directory, err := s.executionDir(runID)
	if err != nil {
		return kept(fmt.Sprintf(PruneReasonUnreadableFmt, err))
	}
	request, requestErr := s.ReadExecutionRequest(runID)
	if requestErr != nil {
		// A directory holding only its event-lock file is the leftover of
		// a crash-interrupted removal (removeExecutionDirectory deletes
		// children first and the lock plus directory last). It carries no
		// admission record and no event bytes, so there is nothing to
		// preserve; finishing the removal on the next prune is the repair.
		if isRemovalRemnant(directory) {
			return prunableRun{runID: runID, prunable: true, remnant: true}
		}
		// A missing or undecodable request record with real content left
		// is exactly the partial-write shape we refuse to touch.
		return kept(PruneReasonIncompleteAdmission)
	}
	survivorLink = request.ParentRunID
	log, scanErr := scanEventLog(runID, filepath.Join(directory, "events.jsonl"))
	if scanErr != nil {
		var corrupt EventCorruptionError
		if errors.As(scanErr, &corrupt) {
			return kept(fmt.Sprintf(PruneReasonCorruptFmt, scanErr))
		}
		return kept(fmt.Sprintf(PruneReasonUnreadableFmt, scanErr))
	}
	if log.tail != nil {
		return kept(PruneReasonCorruptTail)
	}
	projection := reconcileProjection(runID, log.frames)
	if projection.OrphanedCancellation {
		return kept(PruneReasonOrphanedCanceled)
	}
	if projection.Terminal == agentrun.TerminalNone {
		return kept(PruneReasonNonTerminal)
	}
	if projection.UpdatedAt.After(cutoff) {
		return kept(PruneReasonTerminalRecent)
	}
	for _, frame := range log.frames {
		if referencedInvocations[frame.InvocationID] {
			return kept(fmt.Sprintf(PruneReasonProvenanceFmt, frame.InvocationID))
		}
	}
	// T9.5 collection guards: pruning must be invisible to measurement. A
	// missing snapshot keeps the execution — absence is unknown, never
	// zero — and a run settled in more than one attempt keeps its stream.
	// The attempt count comes from ReadAttemptOutcomes, the same reconciled
	// source the metrics aggregator groups by: the snapshot records no
	// attempt multiplicity, so the retried-runs aggregate reads it only
	// from these bytes. A snapshot or outcome set that cannot be read
	// keeps the run too: an undecidable measurement never authorizes a
	// deletion.
	snapshot, snapshotErr := s.ReadExecutionMetrics(runID)
	if snapshotErr != nil {
		return kept(fmt.Sprintf(PruneReasonUnreadableFmt, snapshotErr))
	}
	if snapshot == nil {
		return kept(PruneReasonNoMetricsSnapshot)
	}
	outcomes, outcomesErr := s.ReadAttemptOutcomes(runID)
	if outcomesErr != nil {
		return kept(fmt.Sprintf(PruneReasonUnreadableFmt, outcomesErr))
	}
	if attemptCount(outcomes) > 1 {
		return kept(PruneReasonMultiAttempt)
	}
	if snapshotContradictsStream(outcomes, snapshot) {
		return kept(PruneReasonContradiction)
	}
	return prunableRun{runID: runID, parentRunID: survivorLink, prunable: true}
}

// snapshotContradictsStream reports whether the snapshot disagrees with the
// terminal outcomes about the run's verdict: terminal success beside
// snapshot failures. The aggregator follows live outcomes while the stream
// survives and the snapshot once it is gone, so collecting here would flip
// the run from success to failed. The review engine's corrective-retry
// shape produces exactly this evidence (semantic failure recorded, attempt
// ultimately succeeding), which is why presence alone cannot authorize
// collection.
func snapshotContradictsStream(outcomes []AttemptOutcome, snapshot *ExecutionMetrics) bool {
	if snapshot == nil || len(snapshot.Failures) == 0 {
		return false
	}
	for _, outcome := range outcomes {
		if outcome.Class.IsTerminal() && outcome.Class == agentrun.OutcomeSuccess {
			return true
		}
	}
	return false
}

// attemptCount counts distinct settled attempts in reconciled outcomes by
// invocation identity: the same per-invocation population the metrics
// aggregator deduplicates into logical runs. An empty identity never
// collapses into another attempt: an unattributable outcome keeps the run,
// because assuming single-attempt would delete retry evidence on a guess.
func attemptCount(outcomes []AttemptOutcome) int {
	seen := map[string]bool{}
	for _, outcome := range outcomes {
		if outcome.InvocationID == "" {
			return 2
		}
		seen[outcome.InvocationID] = true
	}
	return len(seen)
}

// isRemovalRemnant reports whether a directory holds ONLY its cross-process
// event-lock file. An empty directory deliberately does not qualify: it may
// be an interrupted CreateRun admission rather than an interrupted removal,
// and without a lock file there is no signal that a removal ever started.
func isRemovalRemnant(directory string) bool {
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) == 0 {
		return false
	}
	for _, entry := range entries {
		if entry.Name() != ".events.lock" {
			return false
		}
	}
	return true
}

// removeExecutionRemnant finishes a crash-interrupted removal: it deletes a
// directory holding nothing but its event-lock file. The locked section
// clears every child except the lock file itself — guarding against a
// concurrent pass recreating content between classification and deletion —
// then the lock plus directory are removed after releasing, because
// Windows refuses to delete paths under an open handle.
//
// Ticket 14 prune admission window: if real content exists under the lock —
// an admission landed between classification and this critical section,
// which CreateRun's own event-lock section makes a serialized possibility —
// the cleanup refuses instead of wiping it. The stable keep reason is the
// incomplete-admission guard: the next prune classifies the record from its
// actual evidence like any other. Pure lock-only remnants still self-heal.
func (s *Store) removeExecutionRemnant(runID string) error {
	return s.withExecutionLifecycleLock(runID, func() error {
		directory, err := s.executionDir(runID)
		if err != nil {
			return err
		}
		lockPath := filepath.Join(directory, ".events.lock")
		err = withExecutionLock(directory, func() error {
			entries, readErr := os.ReadDir(directory)
			if readErr != nil {
				return readErr
			}
			for _, entry := range entries {
				if entry.Name() == ".events.lock" {
					continue
				}
				return pruneInLockRefusal{reason: PruneReasonIncompleteAdmission}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if err := os.Remove(lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return os.Remove(directory)
	})
}

// removeExecutionDirectory deletes one whole execution record under its
// cross-process event lock. The locked section re-verifies the stream, the
// provenance guards, and the T9.5 attempt guard (ticket 13 hardening pool,
// JD-R10 W1): a reference persisted between classification and lock, a
// child run that appeared naming this record as parent, or a retry attempt
// that settled inside the classify→delete window refuses the deletion with
// a typed refusal instead of destroying evidence. It then removes every
// child except the lock file itself; the lock file and the directory are
// removed after releasing the lock, because Windows refuses to rename or
// delete paths under an open handle. A failure anywhere leaves whatever was
// not yet deleted in place and reports the error.
func (s *Store) removeExecutionDirectory(runID string, referencedInvocations map[string]bool) error {
	return s.withExecutionLifecycleLock(runID, func() error {
		directory, err := s.executionDir(runID)
		if err != nil {
			return err
		}
		// Never recreate a record another pass already removed.
		if _, statErr := os.Stat(directory); errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("store: execution record vanished before removal: %s", runID)
		}
		lockPath := filepath.Join(directory, ".events.lock")
		err = withExecutionLock(directory, func() error {
			log, scanErr := scanEventLog(runID, filepath.Join(directory, "events.jsonl"))
			if scanErr != nil {
				return scanErr
			}
			if log.tail != nil {
				return IncompleteEventTailError{RunID: runID}
			}
			projection := reconcileProjection(runID, log.frames)
			if projection.Terminal == agentrun.TerminalNone {
				return fmt.Errorf("store: run %s left its terminal state before pruning", runID)
			}
			for _, frame := range log.frames {
				if referencedInvocations[frame.InvocationID] {
					return pruneInLockRefusal{
						reason: fmt.Sprintf(PruneReasonProvenanceFmt, frame.InvocationID),
					}
				}
			}
			if child := s.findLateChildUnderLock(runID); child != "" {
				return pruneInLockRefusal{
					reason: fmt.Sprintf(PruneReasonParentOfSurvivorFmt, child),
				}
			}
			// The classify→delete window can also complete a retry: a run
			// classified as single-attempt may settle a second attempt after
			// classification, and the terminal re-check above goes green again
			// once it does. Re-derive the attempt count from the same
			// reconciled outcomes the classifier used and refuse on growth.
			// The snapshot needs no re-check: snapshots are write-once
			// immutable with no deletion path, so classify-time presence is
			// stable across this window, while attempts can still appear.
			lockedOutcomes, lockedOutcomesErr := s.ReadAttemptOutcomes(runID)
			if lockedOutcomesErr != nil {
				return pruneInLockRefusal{
					reason: fmt.Sprintf(PruneReasonUnreadableFmt, lockedOutcomesErr),
				}
			}
			if attemptCount(lockedOutcomes) > 1 {
				return pruneInLockRefusal{reason: PruneReasonMultiAttempt}
			}
			entries, readErr := os.ReadDir(directory)
			if readErr != nil {
				return readErr
			}
			for _, entry := range entries {
				if entry.Name() == ".events.lock" {
					continue
				}
				if err := os.RemoveAll(filepath.Join(directory, entry.Name())); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if err := os.Remove(lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return os.Remove(directory)
	})
}

// findLateChildUnderLock re-scans the execution listing under the event lock
// and returns the identity of a run whose persisted request names runID as
// ParentRunID, or "" when none does. This closes the second half of the
// classify→delete window: a gate child admitted after classification must
// keep its root alive even though the classification pass never saw it.
// Sibling requests that fail to decode are skipped: an unreadable request
// cannot name a parent (classification keeps such records on its own
// incomplete-admission guard), and failing the scan here would deadlock
// pruning behind an unrelated remnant.
func (s *Store) findLateChildUnderLock(runID string) string {
	ids, err := s.ListExecutionIDs()
	if err != nil {
		return ""
	}
	for _, id := range ids {
		if id == runID {
			continue
		}
		request, readErr := s.ReadExecutionRequest(id)
		if readErr != nil || request.ParentRunID != runID {
			continue
		}
		return id
	}
	return ""
}
