package execution

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// Shutdown orphaning for repository-local daemons (ticket 14 slice 3a). When
// a daemon stops gracefully it must not leave still-active invocations as
// silent operator_required residue: after its drain budget expires, every
// survivor is settled as canceled with an explicit reason, so the next R8
// recovery scan classifies the streams cleanly with no special cases.
//
// This is the minimal extraction of the existing cancellation-settlement
// primitives, not a parallel format. Live runs reuse settleCanceledLocked —
// the same writer the abort paths use. Runs whose durable head has no live
// owner share R8's appendOrphanedCancellationSettlement implementation over
// store.AppendTerminalEvent. Nothing else about durable persistence changes.

// pollInterval is the sleep between readiness polls in bounded waits and
// their tests: short enough to observe sub-second budgets, long enough not
// to spin.
const pollInterval = 2 * time.Millisecond

// orphanableHeadStates lists the durable stream heads whose canceled
// settlement is provably safe to author from durable evidence alone. They
// mirror the transitions the controller itself already writes: abortWaiting
// (awaiting_decision -> canceled), abortRunning (running -> canceled), and
// the escalation tail (terminating -> canceled). Any other non-terminal head
// stays untouched so its classification keeps failing open to
// operator_required instead of being guessed into a fabricated settlement.
var orphanableHeadStates = map[agentrun.LifecycleState]bool{
	agentrun.StateRunning:          true,
	agentrun.StateAwaitingDecision: true,
	agentrun.StateTerminating:      true,
}

// OrphanActiveRuns settles every active run as canceled, carrying detail as
// the outcome reason. It is the daemon-shutdown counterpart of the R8
// recovery machinery: after it returns, store.ScanRecoveries classifies the
// affected streams as settled terminal evidence with no special cases.
//
// The candidate set joins two sources so no survivor class is missed: every
// run registered in this process, plus every durable execution recorded in
// the store — the latter covers cross-process residue left behind by a
// crashed predecessor that this process never owned.
//
// Settlement rules, per candidate:
//
//   - Live and not yet completing: the detached worker context is canceled
//     cooperatively and the run settles through the same locked writer the
//     abort paths use. A late adapter result can no longer author a
//     different outcome, exactly as with Apply(abort).
//   - Live but inside a bounded escalation: the escalation goroutine owns
//     the settlement; the run is skipped rather than double-settled.
//   - Already terminal durably: skipped, nothing to orphan.
//   - Non-terminal durable head without live state (cross-process residue):
//     settled from verified stream evidence when the head is decidable;
//     undecidable shapes fail explicitly and stay operator-owned.
//
// detail must be non-empty: an orphaned settlement without its honesty would
// be indistinguishable from an ordinary abort. The returned identities list
// exactly the runs this call settled. Per-candidate failures never stop the
// sweep: every remaining candidate is still attempted, and the aggregated
// error names each failed run alongside how many settlements landed, so one
// undecidable stream cannot strand later survivors unsettled.
func (c *Controller) OrphanActiveRuns(detail string) ([]agentrun.Identity, error) {
	return c.orphanRuns(detail, true)
}

// OrphanOwnedRuns settles only runs this controller admitted. A graceful
// daemon shutdown must not take ownership of a review supervised by a separate
// in-process controller; a later recovery scan handles genuine residue.
func (c *Controller) OrphanOwnedRuns(detail string) ([]agentrun.Identity, error) {
	return c.orphanRuns(detail, false)
}

func (c *Controller) orphanRuns(detail string, includeDurableResidue bool) ([]agentrun.Identity, error) {
	if c.store == nil {
		return nil, ErrControllerNotReady
	}
	if strings.TrimSpace(detail) == "" {
		return nil, errors.New("execution: orphan settlement requires a non-empty reason")
	}

	c.mu.Lock()
	candidates := make(map[string]struct{}, len(c.runs))
	for id := range c.runs {
		candidates[id] = struct{}{}
	}
	c.mu.Unlock()

	if includeDurableResidue {
		durableIDs, err := c.store.ListExecutionIDs()
		if err != nil {
			return nil, err
		}
		for _, id := range durableIDs {
			candidates[id] = struct{}{}
		}
	}

	orphaned := make([]agentrun.Identity, 0, len(candidates))
	var failures []error
	for id := range candidates {
		wasOrphaned, orphanErr := c.orphanRun(agentrun.Identity(id), detail)
		if orphanErr != nil {
			failures = append(failures, fmt.Errorf("%s: %w", id, orphanErr))
			continue
		}
		if wasOrphaned {
			orphaned = append(orphaned, agentrun.Identity(id))
		}
	}
	if len(failures) > 0 {
		// errors.Join keeps every failed run's sentinel identity reachable
		// through errors.Is while naming each failed run in the message.
		return orphaned, fmt.Errorf("execution: orphan settlement failed for %d run(s) after settling %d: %w",
			len(failures), len(orphaned), errors.Join(failures...))
	}
	return orphaned, nil
}

// WaitForActiveRuns bounds how long a stopping owner waits for executing
// work to finish on its own. It blocks until no run in this process is still
// executing (or inside a bounded cancellation escalation) or budget expires,
// whichever comes first, and reports how many such runs remain. Quiescent
// runs — notably awaiting-decision heads with no adapter attached — are not
// work in progress and never extend the wait.
func (c *Controller) WaitForActiveRuns(budget time.Duration) int {
	deadline := time.Now().Add(budget)
	for {
		c.mu.Lock()
		snapshot := make([]*runState, 0, len(c.runs))
		for _, state := range c.runs {
			snapshot = append(snapshot, state)
		}
		c.mu.Unlock()
		busy := 0
		for _, state := range snapshot {
			state.mu.Lock()
			executing := !state.doneClosed() && (state.running || state.aborting)
			state.mu.Unlock()
			if executing {
				busy++
			}
		}
		if busy == 0 || !time.Now().Before(deadline) {
			return busy
		}
		time.Sleep(pollInterval)
	}
}

// OrphanRun is the per-run operator entry to the same settlement the shutdown
// sweep performs: it retires ONE run whose durable head is non-terminal and
// whose owner is gone. OrphanActiveRuns sweeps; this decides about one.
//
// It exists because the recovery classifier is read-only by contract and
// refuses to guess an outcome, so a running head with no owner is classified
// operator_required and stays there forever: `runs prune` skips non-terminal
// records, and Apply(abort) fails with ErrRunNotActive. The missing input was
// never evidence, it was the operator's decision, and detail is where that
// decision is recorded. Nothing is invented: the canceled frame is authored
// from the verified stream by appendOrphanedCancellationSettlement, pinned to
// the observed revision, and an undecidable head still fails explicitly.
//
// detail must be non-empty for the same reason the sweep requires it: an
// orphaned settlement without its reason is indistinguishable from an ordinary
// abort. It reports whether this call authored the settlement.
func (c *Controller) OrphanRun(runID agentrun.Identity, detail string) (bool, error) {
	if c.store == nil {
		return false, ErrControllerNotReady
	}
	if strings.TrimSpace(detail) == "" {
		return false, errors.New("execution: orphan settlement requires a non-empty reason")
	}
	return c.orphanRun(runID, detail)
}

// orphanRun settles one candidate survivor. It reports whether a durable
// canceled settlement was authored by this call.
func (c *Controller) orphanRun(runID agentrun.Identity, detail string) (bool, error) {
	c.mu.Lock()
	state := c.runs[string(runID)]
	c.mu.Unlock()

	if state != nil {
		state.mu.Lock()
		switch {
		case state.aborting:
			// The escalation goroutine owns this settlement; touching the
			// stream here would race it, and the loser's ErrStaleRevision
			// must never surface as an orphaning failure.
			state.mu.Unlock()
			return false, nil
		case !state.doneClosed():
			// Cooperative stop first: the blocked adapter observes context
			// cancellation and exits; finish() later finds the run already
			// settled and drops whatever it returns, mirroring Apply(abort).
			if state.cancel != nil {
				state.cancel()
			}
			_, err := c.settleCanceledLocked(state, runID, state.state, detail)
			state.mu.Unlock()
			if err != nil {
				return false, err
			}
			return true, nil
		}
		state.mu.Unlock()
		// The worker already finished without a terminal projection: fall
		// through to the durable verdict.
	}
	return c.orphanDurableHead(runID, detail)
}

// orphanDurableHead settles a non-terminal stream head purely from verified
// durable evidence. It delegates the append to R8's
// appendOrphanedCancellationSettlement — the same lineage reconstruction,
// canceled terminal event, and revision-pinned store.AppendTerminalEvent the
// retry path uses — so a competing writer fails explicitly with
// ErrStaleRevision instead of double-appending, and a vanished-evidence race
// fails closed instead of indexing an empty frame slice.
func (c *Controller) orphanDurableHead(runID agentrun.Identity, detail string) (bool, error) {
	events, projection, err := c.durableEvidence(context.Background(), runID)
	if err != nil {
		if errors.Is(err, store.ErrExecutionNotFound) {
			return false, nil
		}
		return false, err
	}
	if projection.Terminal != agentrun.TerminalNone {
		return false, nil
	}
	if len(events) == 0 {
		return false, fmt.Errorf("%w: durable stream records no evidence frames; nothing decidable to orphan", ErrRunNotRecoverable)
	}
	if !orphanableHeadStates[events[len(events)-1].To] {
		return false, fmt.Errorf("%w: %s head cannot be orphaned automatically; the outcome stays operator-owned",
			ErrRunNotRecoverable, events[len(events)-1].To)
	}
	if _, err := c.appendOrphanedCancellationSettlement(events, projection, detail); err != nil {
		return false, err
	}
	return true, nil
}
