package execution

import (
	"fmt"
	"os"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vcSentinel/internal/process"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
)

// TreeProvider is the optional structural contract of adapters that own a
// live provider process tree. It is discovered like every other optional
// reviewer extension: adapters that do not implement it keep the prompt
// settle-immediately behavior of slice 1.
type TreeProvider interface {
	// OwnedTree returns the currently owned tree, or nil when the adapter is
	// not executing an owned child.
	OwnedTree() *process.Tree
}

// ownedTree discovers the adapter's live process tree, if any.
func (c *Controller) ownedTree() *process.Tree {
	if c.adapter == nil {
		return nil
	}
	if provider, ok := c.adapter.(TreeProvider); ok {
		return provider.OwnedTree()
	}
	return nil
}

// abortRunning settles a running attempt as canceled at abort time, authored
// by the controller instead of by whatever the adapter later reports. This
// closes the JD-A1 gap: a late adapter result can no longer overwrite the
// intended settlement, because finish() finds the run already done and
// returns without appending anything.
//
// Ticket 08 slice 2 adds bounded escalation in between: when escalation is
// enabled and the adapter owns a live process tree, cancellation first
// cooperates for the grace budget, then hard-terminates the whole tree
// (termination-attempted), confirms exit (reaped) or honestly declares it
// orphaned, and only then appends the same authoritative canceled settlement
// with that evidence trail behind it.
//
// With escalation disabled, cancellation reaches only the direct child
// through the exec kill switch; no whole-tree signal is ever issued by any
// code path. The settlement stays plain when no tree is owned, and carries an
// explicit descendant-accounting caveat (never a reaped/orphaned claim) when
// a tree was owned but its descendants were left unowned.
func (c *Controller) abortRunning(state *runState, runID agentrun.Identity) (ApplyResult, error) {
	tree := c.ownedTree()
	if state.cancel != nil {
		state.cancel()
	}
	policy := c.escalation
	if policy.Disabled {
		if tree == nil || tree.Pid() <= 0 {
			// No owned tree: nothing beyond the direct child ever existed to
			// account for; slice-1 plain settlement is exact.
			return c.settleCanceled(state, runID, agentrun.StateRunning, "aborted while running")
		}
		detail := fmt.Sprintf("aborted while running; direct child (pid %d) killed by context cancellation through the exec kill switch; descendants beyond the direct child were never signaled and remain unconfirmed (orphan accounting unavailable: whole-tree termination disabled)", tree.Pid())
		return c.settleCanceled(state, runID, agentrun.StateRunning, detail)
	}
	if tree == nil || tree.Pid() <= 0 {
		return c.settleCanceled(state, runID, agentrun.StateRunning, "aborted while running")
	}
	state.aborting = true
	go c.escalateAndSettle(state, runID, tree, policy)
	return ApplyResult{RunID: runID, InvocationID: state.invocation.InvocationID(), Accepted: true}, nil
}

// escalateAndSettle runs the bounded escalation state machine against the
// owned tree and authors the terminal settlement once the outcome is known.
// It runs on its own goroutine; every durable append takes the state lock,
// so double aborts during escalation stay idempotent through the aborting
// flag and the store's post-terminal rejection stays a second safety net.
func (c *Controller) escalateAndSettle(state *runState, runID agentrun.Identity, tree *process.Tree, policy EscalationPolicy) {
	outcome := process.RunEscalation(process.Escalator{
		Exit:        tree.Exited(),
		Grace:       policy.Grace,
		FinalBudget: policy.FinalBudget,
		Terminate:   func() error { return process.Terminate(tree) },
		Hooks: process.EscalationHooks{
			OnTerminateAttempted: func(at time.Time) {
				c.appendEscalationEvent(state, agentrun.StateRunning, agentrun.StateTerminating)
			},
			OnReaped: func(at time.Time) {
				c.appendEscalationEvent(state, agentrun.StateTerminating, agentrun.StateTerminated)
			},
		},
	})

	state.mu.Lock()
	defer state.mu.Unlock()
	switch outcome {
	case process.OutcomeCooperative:
		c.settleCanceledLocked(state, runID, agentrun.StateRunning, "aborted while running")
	case process.OutcomeReaped:
		detail := fmt.Sprintf("aborted while running; escalated termination reaped process tree (pid %d)", tree.Pid())
		c.settleCanceledLocked(state, runID, agentrun.StateTerminated, detail)
	default:
		detail := fmt.Sprintf("aborted while running; process tree orphaned after termination attempt (pid %d unconfirmed)", tree.Pid())
		c.settleCanceledLocked(state, runID, agentrun.StateTerminating, detail)
	}
	tree.Release()
}

// appendEscalationEvent appends one non-terminal escalation evidence frame
// under the state lock. A persistence failure must never block containment:
// the error surfaces on stderr and the settlement still carries whatever
// evidence made it to disk.
func (c *Controller) appendEscalationEvent(state *runState, from, to agentrun.LifecycleState) {
	state.mu.Lock()
	defer state.mu.Unlock()
	receipt, err := c.appendTransitionLocked(state, state.invocation, from, to, agentrun.DecisionNone)
	if err != nil {
		fmt.Fprintf(os.Stderr, "execution: escalation evidence event %s->%s not appended: %v\n", from, to, err)
		return
	}
	state.revision = receipt.Revision
	state.state = to
}

// abortWaiting settles an awaiting_decision run as canceled. The adapter is
// not executing here, so there is no worker context or process tree; only
// the durable settlement remains.
func (c *Controller) abortWaiting(state *runState, runID agentrun.Identity) (ApplyResult, error) {
	return c.settleCanceled(state, runID, agentrun.StateAwaitingDecision, "aborted while awaiting a response")
}

// settleCanceled appends the terminal cancellation evidence for one aborted
// attempt from the given source lifecycle state and completes the run as
// canceled. The caller must already hold the state lock (Apply does).
// The completion carries no output and no error: waiting callers observe the
// deterministic canceled settlement regardless of what the adapter
// eventually returns.
func (c *Controller) settleCanceled(state *runState, runID agentrun.Identity, from agentrun.LifecycleState, detail string) (ApplyResult, error) {
	return c.settleCanceledLocked(state, runID, from, detail)
}

// settleCanceledLocked is settleCanceled for callers already holding the
// state lock. The orphan case reuses the existing canceled terminal class
// and carries its honesty in the outcome reason: adding a new terminal kind
// would break old-stream validation, so the choice here is reuse-plus-detail
// by design.
func (c *Controller) settleCanceledLocked(state *runState, runID agentrun.Identity, from agentrun.LifecycleState, detail string) (ApplyResult, error) {
	at := c.now().UTC()
	var observation *store.AttemptObservation
	if (from == agentrun.StateRunning || from == agentrun.StateTerminating) && !state.attemptStarted.IsZero() {
		// attemptStarted is captured with time.Now's monotonic component. Pair
		// it only with a real monotonic end; injected wall-clock timestamps
		// cannot be subtracted from it safely.
		duration := time.Since(state.attemptStarted)
		if duration < 0 {
			duration = 0
		}
		observation = &store.AttemptObservation{DurationNanos: &duration}
	}
	var durationNanos *time.Duration
	if observation != nil {
		durationNanos = cloneDuration(observation.DurationNanos)
	}
	outcome := store.AttemptOutcome{
		RunID: string(runID), JobID: string(state.job.ID()), InvocationID: string(state.invocation.InvocationID()),
		LineageID: string(state.invocation.LineageIdentity()), Class: agentrun.OutcomeCancellation,
		Error: detail, At: at, Observation: observation, DurationNanos: durationNanos,
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
	state.aborting = false
	c.completeLocked(state, state.invocation, agentrun.StateCanceled, agentrun.OutcomeCancellation, AdapterResult{}, outcome.Error, nil, true)
	return ApplyResult{RunID: runID, InvocationID: state.invocation.InvocationID(), Accepted: true}, nil
}
