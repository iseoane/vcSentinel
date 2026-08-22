package execution

import (
	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// abortRunning settles a running attempt as canceled at abort time, authored
// by the controller instead of by whatever the adapter later reports. This
// closes the JD-A1 gap: a late adapter result can no longer overwrite the
// intended settlement, because finish() finds the run already done and
// returns without appending anything. The worker context is canceled first so
// the provider call starts unwinding immediately; the durable evidence event
// (running -> canceled, decision abort, plus its attempt outcome) is appended
// exactly once per aborted attempt under the state lock.
//
// The settled state stays registered so a repeated Apply(ActionAbort) is
// idempotent: it returns accepted without appending any event.
func (c *Controller) abortRunning(state *runState, runID agentrun.Identity) (ApplyResult, error) {
	if state.cancel != nil {
		state.cancel()
	}
	return c.settleCanceled(state, runID, agentrun.StateRunning, "aborted while running")
}

// abortWaiting settles an awaiting_decision run as canceled. The adapter is
// not executing here, so there is no worker context to cancel; only the
// durable settlement remains.
func (c *Controller) abortWaiting(state *runState, runID agentrun.Identity) (ApplyResult, error) {
	return c.settleCanceled(state, runID, agentrun.StateAwaitingDecision, "aborted while awaiting a response")
}

// settleCanceled appends the terminal cancellation evidence for one aborted
// attempt from the given source lifecycle state and completes the run as
// canceled. The completion carries no output and no error: waiting callers
// observe the deterministic canceled settlement regardless of what the
// adapter eventually returns.
func (c *Controller) settleCanceled(state *runState, runID agentrun.Identity, from agentrun.LifecycleState, detail string) (ApplyResult, error) {
	at := c.now().UTC()
	outcome := store.AttemptOutcome{
		RunID: string(runID), JobID: string(state.job.ID()), InvocationID: string(state.invocation.InvocationID()),
		LineageID: string(state.invocation.LineageIdentity()), Class: agentrun.OutcomeCancellation,
		Error: detail, At: at,
	}
	event, eventErr := agentrun.NewNormalizedEvent(state.invocation, from, agentrun.StateCanceled, agentrun.DecisionAbort, at)
	if eventErr != nil {
		c.completeLocked(state, state.invocation, from, agentrun.OutcomeCancellation, AdapterResult{}, eventErr.Error(), eventErr, true)
		return ApplyResult{}, eventErr
	}
	receipt, persistenceErr := c.store.AppendTerminalEvent(string(runID), event, state.revision, outcome)
	if persistenceErr != nil {
		if receipt.Revision > state.revision {
			state.revision = receipt.Revision
			state.state = receipt.State
		}
		c.completeLocked(state, state.invocation, state.state, agentrun.OutcomeCancellation, AdapterResult{}, persistenceErr.Error(), persistenceErr, true)
		return ApplyResult{}, persistenceErr
	}
	state.revision = receipt.Revision
	state.state = receipt.State
	state.running = false
	c.completeLocked(state, state.invocation, agentrun.StateCanceled, agentrun.OutcomeCancellation, AdapterResult{}, outcome.Error, nil, true)
	return ApplyResult{RunID: runID, InvocationID: state.invocation.InvocationID(), Accepted: true}, nil
}
