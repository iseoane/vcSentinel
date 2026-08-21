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
	page, err := c.store.ReadEvents(string(runID), projection.Revision-1, 1)
	if err != nil {
		return nil, err
	}
	if len(page.Events) != 1 || page.Events[0].To != agentrun.StateAwaitingDecision {
		return nil, nil
	}
	frame := page.Events[0]
	job, err := agentrun.NewRecoveredLogicalJob(agentrun.Identity(frame.RunID), agentrun.Identity(frame.JobID))
	if err != nil {
		return nil, err
	}
	invocation, err := agentrun.NewRecoveredInvocation(
		agentrun.Identity(frame.RunID), agentrun.Identity(frame.JobID),
		agentrun.Identity(frame.InvocationID), agentrun.Identity(frame.LineageID),
		agentrun.Identity(frame.ParentInvocationID),
	)
	if err != nil {
		return nil, err
	}
	return &runState{
		job: job, invocation: invocation, revision: projection.Revision,
		state: projection.State, done: make(chan struct{}), recovered: true,
	}, nil
}
