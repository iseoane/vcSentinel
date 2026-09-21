package execution

import (
	"context"
	"errors"
	"fmt"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
)

// ErrRunNotRecoverable reports durable evidence that Recover must refuse.
// Recover never fabricates completion: evidence without a resumable head is
// rejected with this sentinel instead of being guessed into a live run.
var ErrRunNotRecoverable = errors.New("execution: durable run evidence cannot be recovered")

// Recover is the explicit operator entry point for resuming a run this
// process may never have owned, using durable evidence only.
//
// Durable shapes map to exactly one action:
//
//   - awaiting-decision head (projection state awaiting_decision and the last
//     event ends there): the pending decision is reconstructed and surfaced
//     through the returned Handle without launching any adapter call. The
//     caller then applies respond or abort on the reconstructed state; both
//     proceed because recovery derives the faithful physical ancestor chain,
//     so continuations keep their original lineage identity. The reconstructed
//     request payload is identity-only, so adapters that need the original
//     prompt text will fail honestly instead of guessing content.
//   - retryable terminal heads (failed, canceled, timed_out): resumed by
//     delegating to Retry, which appends a DecisionRetry event and relaunches
//     the next attempt inside the same logical job. Operators may call either
//     entry point for these states.
//   - orphaned-canceled heads (the R7 reconciled owner-death-during-
//     cancellation verdict): the missing canceled settlement is materialized
//     first — the verified escalation transitions already prove it — and the
//     recovery then delegates to Retry, so the relaunch keeps a fresh
//     invocation identity while the interrupted attempt's record stays
//     intact.
//   - succeeded or unavailable terminal outcomes: refused with
//     ErrRunNotRecoverable; those outcomes are final and nothing may resume
//     them.
//   - running head without terminal evidence (a worker died mid-attempt):
//     refused with ErrRunNotRecoverable. Fabricating the interrupted
//     attempt's outcome is forbidden and deep interruption classification
//     belongs to the R8 roadmap unit.
//   - empty stream or missing execution record: refused explicitly — either
//     ErrRunNotRecoverable or the store's own missing-execution error.
//   - corrupt or incomplete logs: the store's corruption error propagates
//     untouched.
//
// expectedRevision optionally pins the durable stream head; zero skips the
// check because revision zero never exists once a run has events. The pin
// travels into any Retry delegation so competing writers fail explicitly
// instead of racing the recovery.
//
// Recovery is idempotent-safe: recovering a run whose bookkeeping is still
// unclosed fails with ErrRunNotActive when the durable head is non-terminal,
// and the durable stream is never double-applied. As in Retry, an unclosed
// entry over a terminal durable head is stale completion bookkeeping and
// follows the evidence switch instead of refusing.
func (c *Controller) Recover(ctx context.Context, runID agentrun.Identity, expectedRevision uint64) (Handle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Handle{}, err
	}
	if c.store == nil {
		return Handle{}, ErrControllerNotReady
	}

	c.mu.Lock()
	state := c.runs[string(runID)]
	c.mu.Unlock()
	// The live-state guard cross-checks the durable stream, exactly like
	// Retry: finish() appends the terminal event before releasing the
	// bookkeeping, so an unclosed entry over an already-terminal durable
	// head is stale memory, not an active run. Only a non-terminal head
	// (running, awaiting decision) refuses here; a terminal head proceeds
	// into the switch below, where final outcomes refuse with
	// ErrRunNotRecoverable and retryable ones delegate to Retry.
	liveUnclosed := state != nil && !state.doneClosed()

	events, projection, err := c.durableEvidence(ctx, runID)
	if err != nil {
		return Handle{}, err
	}
	if refusalErr := retryLiveGuardRefusal(liveUnclosed, projection.State.TerminalClass() != agentrun.TerminalNone); refusalErr != nil {
		return Handle{}, refusalErr
	}
	// The switch re-reads evidence inside each branch on purpose: durable
	// state can change between this projection read and the branch's own
	// reconstruction, and every writer revalidates against the store's
	// revision guard, so a lost race fails explicitly instead of applying.
	if expectedRevision != 0 && expectedRevision != projection.Revision {
		return Handle{}, fmt.Errorf("%w: expected revision %d, found %d", ErrStaleRevision, expectedRevision, projection.Revision)
	}
	switch {
	case projection.State == agentrun.StateAwaitingDecision &&
		len(events) > 0 && events[len(events)-1].To == agentrun.StateAwaitingDecision:
		return c.recoverAwaitingRun(ctx, runID)
	case projection.State.Retryable():
		if finalized, metricsErr := c.store.ReadExecutionMetrics(string(runID)); metricsErr != nil {
			return Handle{}, metricsErr
		} else if finalized != nil {
			return Handle{}, ErrMetricsFinalized
		}
		return c.Retry(ctx, runID, expectedRevision)
	case c.orphanedCancellationRelaunch(runID, projection):
		if finalized, metricsErr := c.store.ReadExecutionMetrics(string(runID)); metricsErr != nil {
			return Handle{}, metricsErr
		} else if finalized != nil {
			return Handle{}, ErrMetricsFinalized
		}
		// Ticket 10 slice 3: owner-death-during-cancellation evidence reads
		// canceled through the R7 reconciliation. The explicit operator
		// recovery delegates to the same fresh-identity retry contract as
		// any other retryable cancellation; Retry materializes the
		// settlement the dying owner never wrote before relaunching.
		return c.Retry(ctx, runID, expectedRevision)
	default:
		return Handle{}, fmt.Errorf("%w: %s", ErrRunNotRecoverable, recoverRefusal(projection.State))
	}
}

// orphanedCancellationRelaunch reports whether the verified stream carries
// ticket 08's owner-death-during-cancellation fingerprint: escalation
// transitions recorded without any canceled settlement frame. Only a raw
// non-terminal head can carry it; the R7 reconciled verdict then reads as
// canceled, which an explicit operator retry may relaunch.
func (c *Controller) orphanedCancellationRelaunch(runID agentrun.Identity, projection *store.RunProjection) bool {
	if projection.Terminal != agentrun.TerminalNone {
		return false
	}
	reconciled, err := c.store.ReadReconciledProjection(string(runID))
	return err == nil && reconciled.OrphanedCancellation
}

// reconciledOwnerDeathReason is the outcome reason the R8 recovery machinery
// persists when it materializes the reconciled canceled settlement for an
// owner-death-during-cancellation stream.
const reconciledOwnerDeathReason = "owner death during cancellation was reconciled at explicit operator recovery"

// appendOrphanedCancellationSettlement records the durable canceled
// settlement whose absence defines an orphaned-canceled stream: the verified
// escalation transitions already prove the cancellation, so this materializes
// the R7 reconciled verdict instead of guessing an outcome. It must be called
// only after orphanedCancellationRelaunch accepted the shape, and it guards
// its append with the caller-validated read revision so a competing recovery
// fails explicitly instead of double-appending. detail is persisted verbatim
// as the outcome reason; the daemon-shutdown orphaning path shares this one
// implementation with its own honesty text.
//
// Streams whose escalation tail crashed before its newline never reach this
// point: their unreadable tail fails closed as corruption and remains an
// operator repair decision, not a silent rewrite.
func (c *Controller) appendOrphanedCancellationSettlement(events []store.EventFrame, projection *store.RunProjection, detail string) (store.EventReceipt, error) {
	if len(events) == 0 {
		// Fail closed on the cross-process race where the reconciled
		// verdict was read but the evidence frames vanished before this
		// settlement append; indexing the head here would panic instead
		// of refusing explicitly.
		return store.EventReceipt{}, fmt.Errorf("%w: orphaned cancellation evidence vanished between reads", ErrRunNotRecoverable)
	}
	head := events[len(events)-1]
	job, parent, err := recoverHeadEnvelope(events)
	if err != nil {
		return store.EventReceipt{}, err
	}
	at := c.now().UTC()
	outcome := store.AttemptOutcome{
		RunID: string(head.RunID), JobID: string(job.ID()),
		InvocationID: string(parent.InvocationID()), LineageID: string(parent.LineageIdentity()),
		Class: agentrun.OutcomeCancellation,
		Error: detail, At: at,
	}
	event, eventErr := agentrun.NewNormalizedEvent(parent, head.To, agentrun.StateCanceled, agentrun.DecisionAbort, at)
	if eventErr != nil {
		return store.EventReceipt{}, eventErr
	}
	return c.store.AppendTerminalEvent(head.RunID, event, projection.Revision, outcome)
}

// recoverRefusal explains why a projection state offers no resumable head.
func recoverRefusal(state agentrun.LifecycleState) string {
	switch state {
	case agentrun.StateSucceeded, agentrun.StateUnavailable:
		return string(state) + " outcome is final"
	case agentrun.StateRunning:
		return "running head needs interrupted-run classification"
	default:
		return "no admitted lifecycle evidence to reconstruct"
	}
}

// recoverAwaitingRun registers the reconstructed awaiting state so subsequent
// Apply(respond|abort) calls operate on it. The registration rechecks the
// live-run map under the lock, mirroring the Apply and Retry race rules.
func (c *Controller) recoverAwaitingRun(ctx context.Context, runID agentrun.Identity) (Handle, error) {
	state, err := c.reconstructAwaitingState(ctx, runID)
	if err != nil {
		return Handle{}, err
	}
	if state == nil {
		return Handle{}, fmt.Errorf("%w: awaiting evidence vanished between reads", ErrRunNotRecoverable)
	}
	jobID := state.job.ID()
	invocationID := state.invocation.InvocationID()
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing := c.runs[string(runID)]; existing != nil && !existing.doneClosed() {
		return Handle{}, ErrRunNotActive
	}
	c.runs[string(runID)] = state
	return Handle{RunID: runID, JobID: jobID, InvocationID: invocationID, state: state}, nil
}
