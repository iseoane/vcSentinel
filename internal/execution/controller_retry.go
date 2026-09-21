package execution

import (
	"context"
	"fmt"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
)

// retryLiveGuardRefusal resolves the live-state guard of Retry against the
// durable stream. finish() appends the terminal event BEFORE completeLocked
// releases the worker bookkeeping (the done close plus the runs-map delete),
// so between those steps live memory still claims the run is active while the
// durable head is already terminal. During that window persisted truth
// outranks the stale bookkeeping: the caller falls through to the shared
// terminal classification instead of being refused here. Only an unclosed
// bookkeeping entry over a non-terminal durable head — a genuinely running or
// awaiting-decision run — refuses with ErrRunNotActive.
func retryLiveGuardRefusal(liveUnclosed, headTerminal bool) error {
	if liveUnclosed && !headTerminal {
		return ErrRunNotActive
	}
	return nil
}

// Retry relaunches a terminally failed, canceled, or timed-out run as the
// next attempt inside its original logical job and run identity. The run
// state is reconstructed from durable evidence when this process never owned
// it, so a fresh controller can retry a run started elsewhere. An
// orphaned-canceled stream (the R7 owner-death-during-cancellation verdict)
// is accepted through the same contract: its reconciled canceled settlement
// is materialized first, then the relaunch proceeds exactly as for any other
// retryable cancellation. Active runs fail with ErrRunNotActive; succeeded
// and unavailable outcomes stay final and fail with ErrRunNotRetryable.
//
// The live-state guard no longer trusts this process's bookkeeping alone.
// Because finish() persists the terminal event before releasing the
// bookkeeping, an unclosed entry is cross-checked against the durable
// stream: a terminal head follows the shared terminal classification
// (ErrRunNotRetryable for final outcomes, a legitimate relaunch for
// retryable ones), and only a non-terminal head yields ErrRunNotActive.
// Retrying an already-retried current state fails explicitly instead of
// double-applying. Every relaunch derives its attempt from
// NewRetryInvocation, so the resumed work always carries a fresh invocation
// identity while earlier attempts keep their durable records.
//
// expectedRevision optionally pins the durable stream head; zero skips the
// check because revision zero never exists once a run has events.
func (c *Controller) Retry(ctx context.Context, runID agentrun.Identity, expectedRevision uint64) (Handle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Handle{}, err
	}
	if c.store == nil || c.adapter == nil {
		return Handle{}, ErrControllerNotReady
	}

	c.mu.Lock()
	state := c.runs[string(runID)]
	c.mu.Unlock()
	liveUnclosed := state != nil && !state.doneClosed()
	events, projection, err := c.durableEvidence(ctx, runID)
	if err != nil {
		return Handle{}, err
	}
	if refusalErr := retryLiveGuardRefusal(liveUnclosed, projection.State.TerminalClass() != agentrun.TerminalNone); refusalErr != nil {
		return Handle{}, refusalErr
	}
	if expectedRevision != 0 && expectedRevision != projection.Revision {
		return Handle{}, fmt.Errorf("%w: expected revision %d, found %d", ErrStaleRevision, expectedRevision, projection.Revision)
	}
	if finalized, metricsErr := c.store.ReadExecutionMetrics(string(runID)); metricsErr != nil {
		return Handle{}, metricsErr
	} else if finalized != nil {
		return Handle{}, ErrMetricsFinalized
	}
	// The relaunch append guards against the head at append time; an
	// orphaned-canceled settlement appended below moves that head by one, so
	// the guard travels with it instead of staying on the read revision.
	appendGuard := projection.Revision
	from := projection.State
	if !projection.State.Retryable() {
		if !c.orphanedCancellationRelaunch(runID, projection) {
			if projection.State.TerminalClass() == agentrun.TerminalNone {
				return Handle{}, ErrRunNotActive
			}
			return Handle{}, ErrRunNotRetryable
		}
		settlementReceipt, settleErr := c.appendOrphanedCancellationSettlement(events, projection, reconciledOwnerDeathReason)
		if settleErr != nil {
			return Handle{}, settleErr
		}
		// The verified escalation evidence reads canceled through the R7
		// reconciliation, so the honest origin of the relaunch event is the
		// reconciled state, not the raw escalation head.
		appendGuard = settlementReceipt.Revision
		from = agentrun.StateCanceled
	}

	var (
		parent agentrun.InvocationEnvelope
		job    agentrun.LogicalJob
	)
	if state != nil {
		parent = state.invocation
		job = state.job
	} else {
		job, parent, err = recoverHeadEnvelope(events)
		if err != nil {
			return Handle{}, err
		}
	}
	retryInvocation, err := agentrun.NewRetryInvocation(parent)
	if err != nil {
		return Handle{}, err
	}
	event, err := agentrun.NewNormalizedEvent(retryInvocation, from, agentrun.StateRunning, agentrun.DecisionRetry, c.now().UTC())
	if err != nil {
		return Handle{}, err
	}
	receipt, err := c.store.AppendEvent(string(runID), event, appendGuard)
	if err != nil {
		return Handle{}, err
	}

	next := &runState{
		job: job, invocation: retryInvocation,
		revision: receipt.Revision, state: agentrun.StateRunning,
		done: make(chan struct{}),
	}
	workerContext, cancel := context.WithCancel(context.Background())
	next.cancel = cancel
	c.mu.Lock()
	// A relaunch that fell through the live-state guard may replace its OWN
	// stale bookkeeping entry: the finishing worker already authored the
	// terminal evidence, cannot write again, and completeLocked skips the
	// map delete on pointer inequality. Any other unclosed entry means a
	// newer live run owns this identity and the retry must refuse. The
	// store's revision guard keeps competing retries from both appending,
	// so only the successful appender ever reaches this install.
	if existing := c.runs[string(runID)]; existing != nil && !existing.doneClosed() && existing != state {
		c.mu.Unlock()
		cancel()
		return Handle{}, ErrRunNotActive
	}
	c.runs[string(runID)] = next
	c.mu.Unlock()
	go c.execute(next, workerContext, retryInvocation, "")
	return Handle{RunID: runID, JobID: retryInvocation.JobID(), InvocationID: retryInvocation.InvocationID(), state: next}, nil
}
