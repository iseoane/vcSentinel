package store

import (
	"fmt"
	"time"
)

// AttemptObservation is the normalized evidence captured for one physical
// provider invocation. RequestedModel and RequestedEffort are deliberately
// distinct from Model and Effort; configured identity is never substituted for
// provider-reported values. The same value is embedded in the event and
// outcome records so event-history reads remain authoritative without rewriting
// older streams.
type AttemptObservation struct {
	Agent           string               `json:"agent,omitempty"`
	Model           string               `json:"model,omitempty"`
	RequestedModel  string               `json:"requested_model,omitempty"`
	Effort          string               `json:"effort,omitempty"`
	RequestedEffort string               `json:"requested_effort,omitempty"`
	StopReason      string               `json:"stop_reason,omitempty"`
	Enforcement     string               `json:"enforcement,omitempty"`
	Usage           *ExecutionTokenUsage `json:"usage,omitempty"`
	DurationNanos   *time.Duration       `json:"duration_ns,omitempty"`
	// Turns is the observed model-turn count from a provider whose wire
	// format exposes one (OpenCode's --format json step_finish-per-turn
	// stream, bound to its own configured Steps agent budget). Nil means the
	// provider reports no turn count at all — never a bare zero, which would
	// be indistinguishable from a real observed-zero measurement and would
	// corrupt a later turn-budget recalibration.
	Turns *int `json:"turns,omitempty"`
}

func cloneAttemptObservation(observation *AttemptObservation) *AttemptObservation {
	if observation == nil {
		return nil
	}
	clone := *observation
	if observation.DurationNanos != nil {
		value := *observation.DurationNanos
		clone.DurationNanos = &value
	}
	if observation.Turns != nil {
		value := *observation.Turns
		clone.Turns = &value
	}
	if observation.Usage != nil {
		usage := *observation.Usage
		for source, target := range map[**int64]**int64{
			&observation.Usage.InputTokens:       &usage.InputTokens,
			&observation.Usage.OutputTokens:      &usage.OutputTokens,
			&observation.Usage.TotalTokens:       &usage.TotalTokens,
			&observation.Usage.CachedInputTokens: &usage.CachedInputTokens,
			&observation.Usage.ReasoningTokens:   &usage.ReasoningTokens,
		} {
			if *source != nil {
				value := **source
				*target = &value
			}
		}
		clone.Usage = &usage
	}
	return &clone
}

func validateAttemptObservation(observation *AttemptObservation) error {
	if observation == nil {
		return nil
	}
	if observation.DurationNanos != nil && *observation.DurationNanos < 0 {
		return fmt.Errorf("store: negative attempt duration")
	}
	if observation.Turns != nil && *observation.Turns < 0 {
		return fmt.Errorf("store: negative attempt turn count")
	}
	if observation.Usage != nil {
		if err := validateExecutionTokenUsage(*observation.Usage); err != nil {
			return err
		}
	}
	return nil
}
func cloneDuration(value *time.Duration) *time.Duration {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
