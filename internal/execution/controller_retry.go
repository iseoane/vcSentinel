package execution

import (
	"context"
	"fmt"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
)

// Retry relaunches a terminally failed, canceled, or timed-out run as the
// next attempt inside its original logical job and run identity. The run
// state is reconstructed from durable evidence when this process never owned
// it, so a fresh controller can retry a run started elsewhere. Active runs
// fail with ErrRunNotActive; succeeded and unavailable outcomes stay final
// and fail with ErrRunNotRetryable. Retrying an already-retried current
// state fails explicitly instead of double-applying.
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
	if state != nil && !state.doneClosed() {
		return Handle{}, ErrRunNotActive
	}
	events, projection, err := c.durableEvidence(ctx, runID)
	if err != nil {
		return Handle{}, err
	}
	if expectedRevision != 0 && expectedRevision != projection.Revision {
		return Handle{}, fmt.Errorf("%w: expected revision %d, found %d", ErrStaleRevision, expectedRevision, projection.Revision)
	}
	if !projection.State.Retryable() {
		if projection.State.TerminalClass() == agentrun.TerminalNone {
			return Handle{}, ErrRunNotActive
		}
		return Handle{}, ErrRunNotRetryable
	}

	var (
		parent agentrun.InvocationEnvelope
		job    agentrun.LogicalJob
	)
	if state != nil {
		parent = state.invocation
		job = state.job
	} else {
		job, parent, err = reconstructTerminalInvocation(events)
		if err != nil {
			return Handle{}, err
		}
	}
	retryInvocation, err := agentrun.NewRetryInvocation(parent)
	if err != nil {
		return Handle{}, err
	}
	event, err := agentrun.NewNormalizedEvent(retryInvocation, projection.State, agentrun.StateRunning, agentrun.DecisionRetry, c.now().UTC())
	if err != nil {
		return Handle{}, err
	}
	receipt, err := c.store.AppendEvent(string(runID), event, projection.Revision)
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
	if existing := c.runs[string(runID)]; existing != nil && !existing.doneClosed() {
		c.mu.Unlock()
		cancel()
		return Handle{}, ErrRunNotActive
	}
	c.runs[string(runID)] = next
	c.mu.Unlock()
	go c.execute(next, workerContext, retryInvocation, "")
	return Handle{RunID: runID, JobID: retryInvocation.JobID(), InvocationID: retryInvocation.InvocationID(), state: next}, nil
}
