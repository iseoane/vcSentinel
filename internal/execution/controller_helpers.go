package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
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
