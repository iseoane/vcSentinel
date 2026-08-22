package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

func (s *runState) doneClosed() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
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
	frame := events[len(events)-1]
	job, err := agentrun.NewRecoveredLogicalJob(agentrun.Identity(frame.RunID), agentrun.Identity(frame.JobID))
	if err != nil {
		return nil, err
	}
	invocation, err := agentrun.NewRecoveredInvocation(
		agentrun.Identity(frame.RunID), agentrun.Identity(frame.JobID),
		agentrun.Identity(frame.InvocationID), agentrun.Identity(frame.LineageID),
		agentrun.Identity(frame.ParentInvocationID), countInvocationAttempts(events),
	)
	if err != nil {
		return nil, err
	}
	return &runState{
		job: job, invocation: invocation, revision: projection.Revision,
		state: projection.State, done: make(chan struct{}), recovered: true,
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

// countInvocationAttempts derives the attempt number of a stream head by
// counting invocation-identity boundaries across the durable event order.
func countInvocationAttempts(events []store.EventFrame) uint32 {
	var attempt uint32
	previous := agentrun.Identity("")
	for _, frame := range events {
		if id := agentrun.Identity(frame.InvocationID); id != previous {
			attempt++
			previous = id
		}
	}
	return attempt
}

// reconstructTerminalInvocation rebuilds the terminal head invocation from
// durable evidence. The recovered ancestor list stays intentionally
// incomplete like awaiting-decision recovery; the attempt number is derived
// from the invocation boundaries recorded in the stream.
func reconstructTerminalInvocation(events []store.EventFrame) (agentrun.LogicalJob, agentrun.InvocationEnvelope, error) {
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
		agentrun.Identity(head.ParentInvocationID), countInvocationAttempts(events),
	)
	if err != nil {
		return agentrun.LogicalJob{}, agentrun.InvocationEnvelope{}, err
	}
	return job, parent, nil
}
