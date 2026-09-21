// Package execution owns one durable supervisor for one agentrun logical job.
package execution

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vcSentinel/internal/process"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
)

var (
	ErrControllerNotReady = errors.New("execution: controller is not ready")
	ErrRunAlreadyExists   = errors.New("execution: run already has durable lifecycle events")
	ErrRunNotActive       = errors.New("execution: run is not active in this controller")
	ErrUnsupportedAction  = errors.New("execution: unsupported control action")
	ErrDecisionNotPending = errors.New("execution: run is not awaiting a response")
	// ErrRunNotRetryable reports a terminal outcome that stays final: success
	// and unavailable evidence cannot be relaunched by a retry decision.
	ErrRunNotRetryable = errors.New("execution: terminal run outcome cannot be retried")
	// ErrStaleRevision reports that the durable stream head moved past the
	// revision the caller expected when applying a state-changing action.
	ErrStaleRevision = errors.New("execution: stale execution revision")
)

// Adapter is the provider-neutral execution seam. The controller supplies the
// immutable job and invocation; response is empty for the first attempt and is
// populated only for an explicit continuation.
type Adapter interface {
	Execute(context.Context, agentrun.LogicalJob, agentrun.InvocationEnvelope, string) (AdapterResult, error)
}

// AdapterUsage contains only token fields whose provider payload was
// recognized. Pointer fields preserve an observed zero from an absent value.
type AdapterUsage struct {
	InputTokens           *int64
	OutputTokens          *int64
	TotalTokens           *int64
	CachedInputTokens     *int64
	CacheWriteInputTokens *int64
	ReasoningTokens       *int64
}

// AdapterObservation is provider-neutral evidence from one physical
// invocation. RequestedModel and RequestedEffort are kept distinct from
// Model and Effort: configured declarations are never promoted to observed
// provider evidence when the wire omits them.
type AdapterObservation struct {
	Agent           string
	Model           string
	RequestedModel  string
	Effort          string
	RequestedEffort string
	StopReason      string
	Enforcement     string
	Usage           *AdapterUsage
	DurationNanos   *time.Duration
	// Turns is the observed model-turn count, when the provider reports one
	// (OpenCode's --format json step_finish-per-turn stream). Nil means the
	// provider exposes no comparable count — never a bare zero, which would
	// be indistinguishable from a real observed-zero measurement.
	Turns *int
}

// AdapterResult is untrusted provider output. The controller admits only its
// hash to the store and returns the raw value through Completion after binding
// it to the run and invocation identities. Observation carries the normalized
// wire evidence alongside partial output, including when Execute returns an
// error.
type AdapterResult struct {
	Output           string
	AwaitingDecision bool
	Observation      *AdapterObservation
}

// AdapterError lets an adapter classify an operational failure without
// deciding any semantic review result.
type AdapterError struct {
	Class agentrun.OutcomeClass
	Err   error
}

func (e AdapterError) Error() string {
	if e.Err == nil {
		return string(e.Class)
	}
	return e.Err.Error()
}

func (e AdapterError) Unwrap() error                  { return e.Err }
func (e AdapterError) Outcome() agentrun.OutcomeClass { return e.Class }

func NewAdapterError(class agentrun.OutcomeClass, err error) error {
	return &AdapterError{Class: class, Err: err}
}

type Action string

const (
	ActionAbort   Action = "abort"
	ActionRespond Action = "respond"
)

type ControlAction struct {
	Kind     Action
	Response string
}

type ApplyResult struct {
	RunID        agentrun.Identity
	InvocationID agentrun.Identity
	Accepted     bool
}

// Handle identifies an admitted run and lets a caller wait without owning the
// worker context. Cancelling Wait only detaches observation.
type Handle struct {
	RunID        agentrun.Identity
	JobID        agentrun.Identity
	InvocationID agentrun.Identity
	state        *runState
}

// Wait waits for a terminal controller result. An adapter failure is returned
// in Completion, not as the Go error; the Go error is reserved for controller
// or durable-store failures.
func (h Handle) Wait(ctx context.Context) (Completion, error) {
	if h.state == nil {
		return Completion{}, ErrRunNotActive
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-h.state.done:
		h.state.mu.Lock()
		defer h.state.mu.Unlock()
		return h.state.completion, h.state.completionErr
	case <-ctx.Done():
		return Completion{}, ctx.Err()
	}
}

type Completion struct {
	RunID        agentrun.Identity
	JobID        agentrun.Identity
	InvocationID agentrun.Identity
	State        agentrun.LifecycleState
	Outcome      agentrun.OutcomeClass
	Output       string
	OutputHash   string
	Error        string
}

type Inspection struct {
	Projection store.RunProjection
	Events     []store.EventFrame
	Outcomes   []store.AttemptOutcome
	Responses  []store.InvocationResponse
}

type Controller struct {
	store   *store.Store
	adapter Adapter
	now     func() time.Time
	// escalation is the bounded cancellation escalation policy of ticket 08
	// slice 2. The zero value normalizes to enabled with default budgets;
	// disabling it keeps cooperative cancellation and orphan detection while
	// never issuing a kill signal beyond the direct child.
	escalation EscalationPolicy

	mu   sync.Mutex
	runs map[string]*runState
}

// EscalationPolicy configures bounded escalation after cooperative
// cancellation. Zero durations select the defaults; Disabled is the only way
// to turn escalation off and stays a construction-time rollback seam wired
// from review.cancellation_escalation (ticket 08 slice 3).
type EscalationPolicy struct {
	// Disabled restricts every kill to the DIRECT CHILD: cancellation and
	// deadlines reach it through the exec kill switch (CommandContext's
	// built-in mechanism, kept active), while no code path — neither
	// controller escalation nor the adapter containment watchdog — ever
	// signals the whole tree. Aborts settle promptly as canceled; when a tree
	// was owned the settlement records the pid plus an explicit statement
	// that descendant accounting was unavailable, never a reaped or orphaned
	// claim beyond that. No terminating or terminated frames are appended in
	// this mode.
	Disabled bool
	// Grace is the cooperative window before whole-tree termination.
	Grace time.Duration
	// FinalBudget bounds exit confirmation after termination; expiry settles
	// the attempt as orphaned instead of reaped.
	FinalBudget time.Duration
}

// Normalized returns the policy with every zero field replaced by its default.
func (p EscalationPolicy) Normalized() EscalationPolicy {
	if p.Grace <= 0 {
		p.Grace = process.DefaultGrace
	}
	if p.FinalBudget <= 0 {
		p.FinalBudget = process.DefaultFinalBudget
	}
	return p
}

type runState struct {
	mu sync.Mutex

	job           agentrun.LogicalJob
	invocation    agentrun.InvocationEnvelope
	revision      uint64
	state         agentrun.LifecycleState
	cancel        context.CancelFunc
	running       bool
	done          chan struct{}
	completion    Completion
	completionErr error
	// attemptStarted uses the monotonic component supplied by time.Now. It is
	// never reconstructed from persisted wall-clock timestamps.
	attemptStarted time.Time
	// aborting marks an in-flight bounded escalation between the canceled
	// worker context and the terminal settlement. While it is set, finish()
	// drops late adapter results and repeated aborts stay idempotent.
	aborting bool
}

func NewController(s *store.Store, adapter Adapter) *Controller {
	return NewControllerWithClock(s, adapter, time.Now)
}

// Backing exposes the durable store this controller was built over. Callers
// that receive an already-constructed controller (the repository-local daemon
// entry point, for example) use it to run store-level machinery such as boot
// reconciliation against exactly the same storage the controller serves,
// instead of threading a second, independently constructed store that could
// drift away from the controller's own.
func (c *Controller) Backing() *store.Store { return c.store }

func NewControllerWithClock(s *store.Store, adapter Adapter, now func() time.Time) *Controller {
	return NewControllerWithClockAndEscalation(s, adapter, now, EscalationPolicy{})
}

// NewControllerWithClockAndEscalation builds a controller whose aborts run
// bounded escalation against owned process trees per the given policy.
func NewControllerWithClockAndEscalation(s *store.Store, adapter Adapter, now func() time.Time, policy EscalationPolicy) *Controller {
	if now == nil {
		now = time.Now
	}
	return &Controller{store: s, adapter: adapter, now: now, escalation: policy.Normalized(), runs: make(map[string]*runState)}
}

// Start admits identities and the pre-execution lifecycle before launching a
// detached per-run worker. The caller context is used only for admission; it
// never becomes the worker cancellation context.
func (c *Controller) Start(ctx context.Context, request agentrun.RunRequest, policy store.RunPolicy) (Handle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Handle{}, err
	}
	if c.store == nil || c.adapter == nil {
		return Handle{}, ErrControllerNotReady
	}
	if policy.ID == "" {
		return Handle{}, errors.New("execution: policy identity is empty")
	}

	job := agentrun.NewLogicalJob(request)
	invocation, err := agentrun.NewRootInvocation(job, 1, agentrun.DecisionStart)
	if err != nil {
		return Handle{}, err
	}
	if err := c.store.CreateRun(job, policy); err != nil {
		return Handle{}, err
	}
	page, err := c.store.ReadEvents(string(job.RunID()), 0, 1)
	if err != nil {
		return Handle{}, err
	}
	if len(page.Events) > 0 {
		return Handle{}, ErrRunAlreadyExists
	}

	state := &runState{
		job: job, invocation: invocation, state: agentrun.StateCreated,
		done: make(chan struct{}),
	}
	for _, transition := range []struct {
		from, to agentrun.LifecycleState
		decision agentrun.Decision
	}{
		{agentrun.StateCreated, agentrun.StateQueued, agentrun.DecisionStart},
		{agentrun.StateQueued, agentrun.StateAdmitted, agentrun.DecisionStart},
		{agentrun.StateAdmitted, agentrun.StateRunning, agentrun.DecisionStart},
	} {
		if err := c.appendTransition(state, invocation, transition.from, transition.to, transition.decision); err != nil {
			return Handle{}, err
		}
	}

	workerContext, cancel := context.WithCancel(context.Background())
	state.cancel = cancel
	state.running = true
	c.mu.Lock()
	if _, exists := c.runs[string(job.RunID())]; exists {
		c.mu.Unlock()
		cancel()
		return Handle{}, ErrRunAlreadyExists
	}
	c.runs[string(job.RunID())] = state
	c.mu.Unlock()

	go c.execute(state, workerContext, invocation, "")
	return Handle{RunID: job.RunID(), JobID: job.ID(), InvocationID: invocation.InvocationID(), state: state}, nil
}

// Inspect reconstructs a run exclusively from durable state, so a fresh
// controller process can inspect a completed run without the original worker.
func (c *Controller) Inspect(ctx context.Context, runID agentrun.Identity) (Inspection, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Inspection{}, err
	}
	outcomes, err := c.store.ReadAttemptOutcomes(string(runID))
	if err != nil {
		return Inspection{}, err
	}
	events, projection, err := c.durableEvidence(ctx, runID)
	if err != nil {
		return Inspection{}, err
	}
	return Inspection{Projection: *projection, Events: events, Outcomes: outcomes, Responses: responsesFromEvents(events)}, nil
}

// Apply accepts the initial control actions. Abort is cooperative and
// controller-authored: applying it to a running attempt cancels the worker
// context and appends the terminal canceled settlement itself, so a late
// adapter result can no longer author a different outcome for that attempt.
// Repeated aborts against an already canceled settlement are idempotent.
// Response creates a child invocation linked to the waiting invocation.
func (c *Controller) Apply(ctx context.Context, runID agentrun.Identity, action ControlAction) (ApplyResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ApplyResult{}, err
	}
	if c.store == nil {
		return ApplyResult{}, ErrControllerNotReady
	}
	if action.Kind != ActionAbort && action.Kind != ActionRespond {
		return ApplyResult{}, fmt.Errorf("%w: %q", ErrUnsupportedAction, action.Kind)
	}
	c.mu.Lock()
	state := c.runs[string(runID)]
	c.mu.Unlock()
	if state == nil {
		var err error
		state, err = c.reconstructAwaitingState(ctx, runID)
		if err != nil {
			return ApplyResult{}, err
		}
		if state == nil {
			return ApplyResult{}, ErrRunNotActive
		}
		c.mu.Lock()
		if existing := c.runs[string(runID)]; existing != nil {
			state = existing
		} else {
			c.runs[string(runID)] = state
		}
		c.mu.Unlock()
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.doneClosed() {
		// A settled canceled run answers a repeated abort idempotently: the
		// evidence event was already appended exactly once, so re-asking
		// neither duplicates it nor fails.
		if action.Kind == ActionAbort && state.settledCanceled() {
			return ApplyResult{RunID: runID, InvocationID: state.completion.InvocationID, Accepted: true}, nil
		}
		return ApplyResult{}, ErrRunNotActive
	}
	if state.aborting {
		// Bounded escalation is in flight between cancellation and its
		// terminal settlement. A repeated abort stays idempotent: accepted,
		// without appending any duplicate evidence; other actions cannot
		// interleave with an in-flight settlement.
		if action.Kind == ActionAbort {
			return ApplyResult{RunID: runID, InvocationID: state.invocation.InvocationID(), Accepted: true}, nil
		}
		return ApplyResult{}, ErrRunNotActive
	}
	switch action.Kind {
	case ActionAbort:
		if state.state == agentrun.StateAwaitingDecision {
			return c.abortWaiting(state, runID)
		}
		if !state.running {
			return ApplyResult{}, ErrRunNotActive
		}
		return c.abortRunning(state, runID)
	case ActionRespond:
		return c.respond(state, runID, action.Response)
	}
	return ApplyResult{}, fmt.Errorf("%w: %q", ErrUnsupportedAction, action.Kind)
}

// ReadEventPage returns durably recorded events of a run strictly after
// afterCursor, honoring limit; limit <= 0 falls back to the standard page
// size.
func (c *Controller) ReadEventPage(ctx context.Context, runID agentrun.Identity, afterCursor uint64, limit int) (store.EventPage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return store.EventPage{}, err
	}
	if c.store == nil {
		return store.EventPage{}, ErrControllerNotReady
	}
	if limit <= 0 {
		limit = eventPageSize
	}
	return c.store.ReadEvents(string(runID), afterCursor, limit)
}

func (c *Controller) execute(state *runState, ctx context.Context, invocation agentrun.InvocationEnvelope, response string) {
	// time.Now carries a monotonic component. Persist only the resulting
	// duration, never a wall-clock-derived interval.
	started := time.Now()
	state.mu.Lock()
	state.attemptStarted = started
	state.mu.Unlock()
	// Stamp the escalation policy onto the worker context so the adapter-side
	// containment watchdog shares one budget AND one kill scope with
	// controller-authored escalation: in Disabled mode nothing downstream of
	// this context may signal beyond the direct child.
	result, adapterErr := c.adapter.Execute(
		process.WithContainmentPolicy(ctx, c.escalation.Grace, !c.escalation.Disabled),
		state.job, invocation, response)
	duration := time.Since(started)
	if result.Observation == nil {
		result.Observation = &AdapterObservation{}
	}
	result.Observation.DurationNanos = &duration
	c.finish(state, invocation, result, adapterErr)
}
func (c *Controller) finish(state *runState, invocation agentrun.InvocationEnvelope, result AdapterResult, adapterErr error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.doneClosed() || state.aborting {
		// A settled run keeps its controller-authored terminal evidence. An
		// in-flight escalation owns the settlement too: whatever the adapter
		// returns after the tree was killed is dropped here.
		return
	}
	class := classify(adapterErr)
	if adapterErr == nil && result.AwaitingDecision {
		class = agentrun.OutcomeAwaitingDecision
	}
	outputHash := hashIfPresent(result.Output)
	if adapterErr == nil && result.AwaitingDecision {
		observation := storeObservation(result.Observation)
		receipt, eventErr := c.appendObservationLocked(state, invocation, observation)
		if eventErr != nil {
			c.completeLocked(state, invocation, agentrun.StateRunning, class, result, eventErr.Error(), eventErr, true)
			return
		}
		state.revision = receipt.Revision
		state.state = agentrun.StateAwaitingDecision
		state.running = false
		state.cancel = nil
		state.attemptStarted = time.Time{}
		return
	}

	outcome := store.AttemptOutcome{
		RunID: string(invocation.RunID()), JobID: string(invocation.JobID()),
		InvocationID: string(invocation.InvocationID()), LineageID: string(invocation.LineageIdentity()),
		Class: class, Error: errorText(adapterErr), OutputHash: outputHash,
	}
	applyObservation(&outcome, result.Observation)
	target := terminalState(class)
	decision := terminalDecision(class)
	at := c.now().UTC()
	outcome.At = at
	capturedTranscript := false
	if adapterErr == nil && !result.AwaitingDecision {
		if reporter, ok := c.adapter.(TranscriptReporter); ok {
			capturedTranscript = c.captureTranscript(&outcome, result, reporter)
		}
	}
	event, eventErr := agentrun.NewNormalizedEvent(invocation, state.state, target, decision, at)
	if eventErr != nil {
		c.completeLocked(state, invocation, state.state, class, result, joinText(adapterErr, eventErr), eventErr, true)
		return
	}
	receipt, persistenceErr := c.store.AppendTerminalEvent(string(invocation.RunID()), event, state.revision, outcome)
	if persistenceErr != nil {
		if capturedTranscript {
			if err := c.store.RemoveTranscript(outcome.RunID, outcome.InvocationID); err != nil {
				fmt.Fprintf(os.Stderr, "execution: transcript sidecar cleanup for invocation %s failed: %v\n", outcome.InvocationID, err)
			}
		}
		if receipt.Revision > state.revision {
			state.revision = receipt.Revision
			state.state = receipt.State
		}
		// Sheet 18: losing the terminal-append race is NOT an infrastructure
		// failure. Another writer settled the run first and its event is the
		// authority — already written and immutable — which means the run is
		// SETTLED, not that this execution failed. Reporting it as an error
		// threw away work already paid for: the provider had answered, the
		// output existed, and the user only saw "expected execution revision N,
		// found M".
		//
		// Only reconcile when the durable head is ALREADY terminal. A conflict
		// against a non-terminal head is a genuinely concurrent write and stays
		// an error. Our own result is discarded just like on an abort: the
		// reported class is the one recorded durably, never ours.
		if errors.Is(persistenceErr, store.ErrRevisionConflict) {
			if reconciled, class, ok := c.reconcileLostTerminalRace(invocation.RunID()); ok {
				state.revision = reconciled.Revision
				state.state = reconciled.State
				c.completeLocked(state, invocation, reconciled.State, class, AdapterResult{}, "settled by another writer while this attempt was running: "+string(reconciled.Terminal), nil, false)
				return
			}
		}
		c.completeLocked(state, invocation, state.state, class, result, joinText(adapterErr, persistenceErr), persistenceErr, true)
		return
	}
	state.revision = receipt.Revision
	state.state = receipt.State
	state.running = false
	state.cancel = nil
	c.completeLocked(state, invocation, target, class, result, errorText(adapterErr), nil, false)
}

// reconcileLostTerminalRace reads the durable state after losing the
// terminal append. It returns the projection and its class only when the
// head is already terminal, the only situation in which the run is truly
// settled by another writer.
func (c *Controller) reconcileLostTerminalRace(runID agentrun.Identity) (store.RunProjection, agentrun.OutcomeClass, bool) {
	derived, err := c.store.ReadDerivedProjection(string(runID))
	if err != nil || derived == nil {
		return store.RunProjection{}, "", false
	}
	projection := *derived
	if projection.Terminal == agentrun.TerminalNone {
		return store.RunProjection{}, "", false
	}
	class, ok := outcomeFromTerminalState(projection.State)
	if !ok {
		return store.RunProjection{}, "", false
	}
	return projection, class, true
}

func (c *Controller) respond(state *runState, runID agentrun.Identity, response string) (ApplyResult, error) {
	if state.state != agentrun.StateAwaitingDecision {
		return ApplyResult{}, ErrDecisionNotPending
	}
	if c.adapter == nil {
		return ApplyResult{}, ErrControllerNotReady
	}
	if strings.TrimSpace(response) == "" {
		return ApplyResult{}, errors.New("execution: response is empty")
	}
	child, err := agentrun.NewChildInvocation(state.invocation, state.invocation.Attempt()+1, agentrun.DecisionRespond)
	if err != nil {
		return ApplyResult{}, err
	}
	event, err := agentrun.NewResponseEvent(child, agentrun.StateAwaitingDecision, agentrun.StateRunning, hashText(response), c.now())
	if err != nil {
		return ApplyResult{}, err
	}
	receipt, err := c.appendEventLocked(state, event)
	if err != nil {
		return ApplyResult{}, err
	}
	workerContext, cancel := context.WithCancel(context.Background())
	state.invocation = child
	state.revision = receipt.Revision
	state.state = agentrun.StateRunning
	state.cancel = cancel
	state.running = true
	go c.execute(state, workerContext, child, response)
	return ApplyResult{RunID: runID, InvocationID: child.InvocationID(), Accepted: true}, nil
}

func (c *Controller) appendTransition(state *runState, invocation agentrun.InvocationEnvelope, from, to agentrun.LifecycleState, decision agentrun.Decision) error {
	event, err := agentrun.NewNormalizedEvent(invocation, from, to, decision, c.now())
	if err != nil {
		return err
	}
	receipt, err := c.store.AppendEvent(string(invocation.RunID()), event, state.revision)
	if err != nil {
		return err
	}
	state.revision = receipt.Revision
	state.state = to
	return nil
}

func (c *Controller) appendEventLocked(state *runState, event agentrun.NormalizedEvent) (store.EventReceipt, error) {
	return c.store.AppendEvent(string(event.RunID()), event, state.revision)
}

func (c *Controller) appendTransitionLocked(state *runState, invocation agentrun.InvocationEnvelope, from, to agentrun.LifecycleState, decision agentrun.Decision) (store.EventReceipt, error) {
	event, err := agentrun.NewNormalizedEvent(invocation, from, to, decision, c.now())
	if err != nil {
		return store.EventReceipt{}, err
	}
	return c.appendEventLocked(state, event)
}
func (c *Controller) appendObservationLocked(state *runState, invocation agentrun.InvocationEnvelope, observation *store.AttemptObservation) (store.EventReceipt, error) {
	event, err := agentrun.NewNormalizedEvent(invocation, state.state, agentrun.StateAwaitingDecision, agentrun.DecisionNone, c.now())
	if err != nil {
		return store.EventReceipt{}, err
	}
	return c.store.AppendAttemptObservation(string(invocation.RunID()), event, state.revision, observation)
}

func (c *Controller) completeLocked(state *runState, invocation agentrun.InvocationEnvelope, lifecycle agentrun.LifecycleState, class agentrun.OutcomeClass, result AdapterResult, detail string, completionErr error, retain bool) {
	state.completion = Completion{
		RunID: invocation.RunID(), JobID: invocation.JobID(), InvocationID: invocation.InvocationID(),
		State: lifecycle, Outcome: class, Output: result.Output, OutputHash: hashIfPresent(result.Output), Error: detail,
	}
	state.completionErr = completionErr
	state.running = false
	state.attemptStarted = time.Time{}
	if state.cancel != nil {
		state.cancel()
		state.cancel = nil
	}
	close(state.done)
	if !retain {
		c.mu.Lock()
		if c.runs[string(invocation.RunID())] == state {
			delete(c.runs, string(invocation.RunID()))
		}
		c.mu.Unlock()
	}
}
