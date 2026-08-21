// Package execution owns one durable supervisor for one agentrun logical job.
package execution

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

var (
	ErrControllerNotReady = errors.New("execution: controller is not ready")
	ErrRunAlreadyExists   = errors.New("execution: run already has durable lifecycle events")
	ErrRunNotActive       = errors.New("execution: run is not active in this controller")
	ErrUnsupportedAction  = errors.New("execution: unsupported control action")
	ErrDecisionNotPending = errors.New("execution: run is not awaiting a response")
)

// Adapter is the provider-neutral execution seam. The controller supplies the
// immutable job and invocation; response is empty for the first attempt and is
// populated only for an explicit continuation.
type Adapter interface {
	Execute(context.Context, agentrun.LogicalJob, agentrun.InvocationEnvelope, string) (AdapterResult, error)
}

// AdapterResult is untrusted provider output. The controller admits only its
// hash to the store and returns the raw value through Completion after binding
// it to the run and invocation identities.
type AdapterResult struct {
	Output           string
	AwaitingDecision bool
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
	return AdapterError{Class: class, Err: err}
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

	mu   sync.Mutex
	runs map[string]*runState
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
}

func NewController(s *store.Store, adapter Adapter) *Controller {
	return NewControllerWithClock(s, adapter, time.Now)
}

func NewControllerWithClock(s *store.Store, adapter Adapter, now func() time.Time) *Controller {
	if now == nil {
		now = time.Now
	}
	return &Controller{store: s, adapter: adapter, now: now, runs: make(map[string]*runState)}
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
	responses, err := c.store.ReadInvocationResponses(string(runID))
	if err != nil {
		return Inspection{}, err
	}
	events := make([]store.EventFrame, 0)
	var cursor uint64
	for {
		if err := ctx.Err(); err != nil {
			return Inspection{}, err
		}
		page, err := c.store.ReadEvents(string(runID), cursor, 128)
		if err != nil {
			return Inspection{}, err
		}
		events = append(events, page.Events...)
		if !page.HasMore {
			break
		}
		cursor = page.NextRevision
	}
	projection, err := c.store.ReadProjection(string(runID))
	if err != nil {
		return Inspection{}, err
	}
	return Inspection{Projection: *projection, Events: events, Outcomes: outcomes, Responses: responses}, nil
}

// Apply accepts the initial control actions. Abort is cooperative and
// response creates a child invocation linked to the waiting invocation.
func (c *Controller) Apply(ctx context.Context, runID agentrun.Identity, action ControlAction) (ApplyResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ApplyResult{}, err
	}
	c.mu.Lock()
	state := c.runs[string(runID)]
	c.mu.Unlock()
	if state == nil {
		return ApplyResult{}, ErrRunNotActive
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.doneClosed() {
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
		state.cancel()
		return ApplyResult{RunID: runID, InvocationID: state.invocation.InvocationID(), Accepted: true}, nil
	case ActionRespond:
		return c.respond(state, runID, action.Response)
	default:
		return ApplyResult{}, fmt.Errorf("%w: %q", ErrUnsupportedAction, action.Kind)
	}
}

func (c *Controller) execute(state *runState, ctx context.Context, invocation agentrun.InvocationEnvelope, response string) {
	result, adapterErr := c.adapter.Execute(ctx, state.job, invocation, response)
	c.finish(state, invocation, result, adapterErr)
}

func (c *Controller) finish(state *runState, invocation agentrun.InvocationEnvelope, result AdapterResult, adapterErr error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.doneClosed() {
		return
	}
	class := classify(adapterErr)
	if adapterErr == nil && result.AwaitingDecision {
		class = agentrun.OutcomeAwaitingDecision
	}
	outputHash := hashIfPresent(result.Output)
	if adapterErr == nil && result.AwaitingDecision {
		receipt, eventErr := c.appendTransitionLocked(state, invocation, agentrun.StateRunning, agentrun.StateAwaitingDecision, agentrun.DecisionNone)
		if eventErr != nil {
			c.completeLocked(state, invocation, agentrun.StateRunning, class, result, eventErr.Error(), eventErr)
			return
		}
		state.revision = receipt.Revision
		state.state = agentrun.StateAwaitingDecision
		state.running = false
		state.cancel = nil
		return
	}

	outcome := store.AttemptOutcome{
		RunID: string(invocation.RunID()), JobID: string(invocation.JobID()),
		InvocationID: string(invocation.InvocationID()), LineageID: string(invocation.LineageIdentity()),
		Class: class, Error: errorText(adapterErr), OutputHash: outputHash, At: c.now().UTC(),
	}
	outcomeErr := c.store.SaveAttemptOutcome(outcome)
	target := terminalState(class)
	decision := terminalDecision(class)
	receipt, eventErr := c.appendTransitionLocked(state, invocation, state.state, target, decision)
	if eventErr != nil {
		c.completeLocked(state, invocation, state.state, class, result, joinText(adapterErr, outcomeErr, eventErr), errors.Join(outcomeErr, eventErr))
		return
	}
	state.revision = receipt.Revision
	state.state = target
	state.running = false
	state.cancel = nil
	c.completeLocked(state, invocation, target, class, result, joinText(adapterErr, outcomeErr), errors.Join(outcomeErr, eventErr))
}

func (c *Controller) respond(state *runState, runID agentrun.Identity, response string) (ApplyResult, error) {
	if state.state != agentrun.StateAwaitingDecision {
		return ApplyResult{}, ErrDecisionNotPending
	}
	if strings.TrimSpace(response) == "" {
		return ApplyResult{}, errors.New("execution: response is empty")
	}
	child, err := agentrun.NewChildInvocation(state.invocation, state.invocation.Attempt()+1, agentrun.DecisionRespond)
	if err != nil {
		return ApplyResult{}, err
	}
	record := store.InvocationResponse{
		RunID: string(runID), ParentInvocationID: string(state.invocation.InvocationID()),
		InvocationID: string(child.InvocationID()), LineageID: string(child.LineageIdentity()),
		ResponseHash: hashText(response), At: c.now().UTC(),
	}
	if err := c.store.SaveInvocationResponse(record); err != nil {
		return ApplyResult{}, err
	}
	receipt, err := c.appendTransitionLocked(state, child, agentrun.StateAwaitingDecision, agentrun.StateRunning, agentrun.DecisionRespond)
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

func (c *Controller) abortWaiting(state *runState, runID agentrun.Identity) (ApplyResult, error) {
	outcome := store.AttemptOutcome{
		RunID: string(runID), JobID: string(state.job.ID()), InvocationID: string(state.invocation.InvocationID()),
		LineageID: string(state.invocation.LineageIdentity()), Class: agentrun.OutcomeCancellation,
		Error: "aborted while awaiting a response", At: c.now().UTC(),
	}
	outcomeErr := c.store.SaveAttemptOutcome(outcome)
	receipt, eventErr := c.appendTransitionLocked(state, state.invocation, agentrun.StateAwaitingDecision, agentrun.StateCanceled, agentrun.DecisionAbort)
	if eventErr != nil {
		c.completeLocked(state, state.invocation, agentrun.StateAwaitingDecision, agentrun.OutcomeCancellation, AdapterResult{}, joinText(outcomeErr, eventErr), errors.Join(outcomeErr, eventErr))
		return ApplyResult{}, errors.Join(outcomeErr, eventErr)
	}
	state.revision = receipt.Revision
	state.state = agentrun.StateCanceled
	state.running = false
	c.completeLocked(state, state.invocation, agentrun.StateCanceled, agentrun.OutcomeCancellation, AdapterResult{}, joinText(outcomeErr), outcomeErr)
	return ApplyResult{RunID: runID, InvocationID: state.invocation.InvocationID(), Accepted: true}, outcomeErr
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

func (c *Controller) appendTransitionLocked(state *runState, invocation agentrun.InvocationEnvelope, from, to agentrun.LifecycleState, decision agentrun.Decision) (store.EventReceipt, error) {
	event, err := agentrun.NewNormalizedEvent(invocation, from, to, decision, c.now())
	if err != nil {
		return store.EventReceipt{}, err
	}
	return c.store.AppendEvent(string(invocation.RunID()), event, state.revision)
}

func (c *Controller) completeLocked(state *runState, invocation agentrun.InvocationEnvelope, lifecycle agentrun.LifecycleState, class agentrun.OutcomeClass, result AdapterResult, detail string, completionErr error) {
	state.completion = Completion{
		RunID: invocation.RunID(), JobID: invocation.JobID(), InvocationID: invocation.InvocationID(),
		State: lifecycle, Outcome: class, Output: result.Output, OutputHash: hashIfPresent(result.Output), Error: detail,
	}
	state.completionErr = completionErr
	state.running = false
	if state.cancel != nil {
		state.cancel()
		state.cancel = nil
	}
	close(state.done)
	c.mu.Lock()
	if c.runs[string(invocation.RunID())] == state {
		delete(c.runs, string(invocation.RunID()))
	}
	c.mu.Unlock()
}
