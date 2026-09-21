package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
)

func (s *runState) doneClosed() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// settledCanceled reports whether this state already carries the durable,
// controller-authored cancellation settlement with no infrastructure error,
// which makes a repeated abort idempotent instead of an invalid-state error.
func (s *runState) settledCanceled() bool {
	return s.completionErr == nil && s.completion.State == agentrun.StateCanceled
}

func classify(err error) agentrun.OutcomeClass {
	if err == nil {
		return agentrun.OutcomeSuccess
	}
	var classified interface{ Outcome() agentrun.OutcomeClass }
	if errors.As(err, &classified) && knownClass(classified.Outcome()) {
		return classified.Outcome()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return agentrun.OutcomeTimeout
	}
	if errors.Is(err, context.Canceled) {
		return agentrun.OutcomeCancellation
	}
	return agentrun.OutcomeFailure
}

func knownClass(class agentrun.OutcomeClass) bool {
	switch class {
	case agentrun.OutcomeFailure, agentrun.OutcomeUnavailable, agentrun.OutcomeTimeout,
		agentrun.OutcomeCancellation, agentrun.OutcomeProcessError:
		return true
	default:
		return false
	}
}

// outcomeFromTerminalState is the inverse of terminalState: it translates the
// terminal state another writer already left on the stream into the
// equivalent outcome class, reconciling a lost settlement race without
// inventing a class other than the durably recorded one.
func outcomeFromTerminalState(state agentrun.LifecycleState) (agentrun.OutcomeClass, bool) {
	switch state {
	case agentrun.StateSucceeded:
		return agentrun.OutcomeSuccess, true
	case agentrun.StateUnavailable:
		return agentrun.OutcomeUnavailable, true
	case agentrun.StateTimedOut:
		return agentrun.OutcomeTimeout, true
	case agentrun.StateCanceled:
		return agentrun.OutcomeCancellation, true
	case agentrun.StateFailed:
		return agentrun.OutcomeFailure, true
	default:
		return "", false
	}
}

func terminalState(class agentrun.OutcomeClass) agentrun.LifecycleState {
	switch class {
	case agentrun.OutcomeSuccess:
		return agentrun.StateSucceeded
	case agentrun.OutcomeUnavailable:
		return agentrun.StateUnavailable
	case agentrun.OutcomeTimeout:
		return agentrun.StateTimedOut
	case agentrun.OutcomeCancellation:
		return agentrun.StateCanceled
	default:
		return agentrun.StateFailed
	}
}

func terminalDecision(class agentrun.OutcomeClass) agentrun.Decision {
	switch class {
	case agentrun.OutcomeSuccess:
		return agentrun.DecisionComplete
	case agentrun.OutcomeCancellation:
		return agentrun.DecisionAbort
	default:
		return agentrun.DecisionNone
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func joinText(values ...error) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if value != nil {
			parts = append(parts, value.Error())
		}
	}
	return strings.Join(parts, "; ")
}

// HashAdapterOutput is the single hashing authority for AdapterResult
// outputs: it derives the exact digest the controller persists into
// AttemptOutcome.OutputHash. Evidence admission and every future consumer
// must call this helper instead of reimplementing the algorithm, so the
// returned-output binding can never drift from the durable record.
func HashAdapterOutput(output string) string {
	return hashIfPresent(output)
}

func hashIfPresent(value string) string {
	if value == "" {
		return ""
	}
	return hashText(value)
}

func hashText(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func responsesFromEvents(events []store.EventFrame) []store.InvocationResponse {
	responses := make([]store.InvocationResponse, 0)
	for _, event := range events {
		if event.ResponseHash == "" {
			continue
		}
		responses = append(responses, store.InvocationResponse{
			RunID: string(event.RunID), ParentInvocationID: event.ParentInvocationID,
			InvocationID: event.InvocationID, LineageID: event.LineageID,
			ResponseHash: event.ResponseHash, At: event.At,
		})
	}
	return responses
}

func (c *Controller) reconstructAwaitingState(ctx context.Context, runID agentrun.Identity) (*runState, error) {
	projection, err := c.store.ReadDerivedProjection(string(runID))
	if err != nil {
		return nil, err
	}
	if projection.State != agentrun.StateAwaitingDecision {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	events, err := c.readAllEvents(ctx, runID)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 || events[len(events)-1].To != agentrun.StateAwaitingDecision {
		return nil, nil
	}
	job, invocation, err := recoverHeadEnvelope(events)
	if err != nil {
		return nil, err
	}
	return &runState{
		job: job, invocation: invocation, revision: projection.Revision,
		state: projection.State, done: make(chan struct{}),
	}, nil
}

const eventPageSize = 128

func (c *Controller) readAllEvents(ctx context.Context, runID agentrun.Identity) ([]store.EventFrame, error) {
	events := make([]store.EventFrame, 0)
	var cursor uint64
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := c.store.ReadEvents(string(runID), cursor, eventPageSize)
		if err != nil {
			return nil, err
		}
		events = append(events, page.Events...)
		if !page.HasMore {
			return events, nil
		}
		cursor = page.NextRevision
	}
}

// durableEvidence gathers the validated event stream and the projection
// derived from it. Both come from durable state, so control decisions never
// depend on this process having started the run.
func (c *Controller) durableEvidence(ctx context.Context, runID agentrun.Identity) ([]store.EventFrame, *store.RunProjection, error) {
	projection, err := c.store.ReadDerivedProjection(string(runID))
	if err != nil {
		return nil, nil, err
	}
	events, err := c.readAllEvents(ctx, runID)
	if err != nil {
		return nil, nil, err
	}
	return events, projection, nil
}

// physicalInvocationChain lists each distinct invocation identity in durable
// event order; consecutive frames of one invocation collapse to one entry.
// Events of one physical invocation occupy a contiguous block and each child
// block directly follows its parent, so first-appearance order is the
// physical chain.
func physicalInvocationChain(events []store.EventFrame) []agentrun.Identity {
	chain := make([]agentrun.Identity, 0, len(events))
	var previous agentrun.Identity
	for _, frame := range events {
		if id := agentrun.Identity(frame.InvocationID); id != previous {
			chain = append(chain, id)
			previous = id
		}
	}
	return chain
}

// countInvocationAttempts derives the attempt number of a stream head from
// the invocation boundaries recorded in the durable event order.
func countInvocationAttempts(events []store.EventFrame) uint32 {
	return uint32(len(physicalInvocationChain(events)))
}

// invocationAncestry derives the complete physical ancestor chain of the
// stream-head invocation. The result starts at the logical job identity and
// stops at the head's parent, matching the ancestor list a live process
// would hold; the chain itself includes the stream head.
func invocationAncestry(events []store.EventFrame) []agentrun.Identity {
	if len(events) == 0 {
		return nil
	}
	chain := physicalInvocationChain(events)
	ancestors := make([]agentrun.Identity, 0, len(chain))
	ancestors = append(ancestors, agentrun.Identity(events[0].JobID))
	return append(ancestors, chain[:len(chain)-1]...)
}

// recoverHeadEnvelope rebuilds the terminal or awaiting stream-head job and
// invocation from durable evidence alone. NewRecoveredInvocation validates
// that the derived ancestry terminates at the recorded parent, so divergent
// durable evidence is refused instead of silently fabricating lineage.
func recoverHeadEnvelope(events []store.EventFrame) (agentrun.LogicalJob, agentrun.InvocationEnvelope, error) {
	if len(events) == 0 {
		return agentrun.LogicalJob{}, agentrun.InvocationEnvelope{}, ErrRunNotActive
	}
	head := events[len(events)-1]
	job, err := agentrun.NewRecoveredLogicalJob(agentrun.Identity(head.RunID), agentrun.Identity(head.JobID))
	if err != nil {
		return agentrun.LogicalJob{}, agentrun.InvocationEnvelope{}, err
	}
	parent, err := agentrun.NewRecoveredInvocation(
		agentrun.Identity(head.RunID), agentrun.Identity(head.JobID),
		agentrun.Identity(head.InvocationID), agentrun.Identity(head.LineageID),
		agentrun.Identity(head.ParentInvocationID), invocationAncestry(events),
		countInvocationAttempts(events),
	)
	if err != nil {
		return agentrun.LogicalJob{}, agentrun.InvocationEnvelope{}, err
	}
	return job, parent, nil
}
