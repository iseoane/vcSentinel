package execution

import (
	"context"
	"errors"
	"fmt"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
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
// Recovery is idempotent-safe: recovering a run that is already live or
// already recovered in this controller fails with ErrRunNotActive, and the
// durable stream is never double-applied.
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
	if state != nil && !state.doneClosed() {
		return Handle{}, ErrRunNotActive
	}

	events, projection, err := c.durableEvidence(ctx, runID)
	if err != nil {
		return Handle{}, err
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
		return c.Retry(ctx, runID, expectedRevision)
	default:
		return Handle{}, fmt.Errorf("%w: %s", ErrRunNotRecoverable, recoverRefusal(projection.State))
	}
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
