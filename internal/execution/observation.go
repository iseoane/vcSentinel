package execution

import (
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

func storeObservation(observation *AdapterObservation) *store.AttemptObservation {
	if observation == nil {
		return nil
	}
	mapped := &store.AttemptObservation{
		Agent:           observation.Agent,
		Model:           observation.Model,
		RequestedModel:  observation.RequestedModel,
		Effort:          observation.Effort,
		RequestedEffort: observation.RequestedEffort,
		StopReason:      observation.StopReason,
		Enforcement:     observation.Enforcement,
		DurationNanos:   cloneDuration(observation.DurationNanos),
	}
	if observation.Usage != nil {
		mapped.Usage = &store.ExecutionTokenUsage{
			InputTokens:       cloneInt64(observation.Usage.InputTokens),
			OutputTokens:      cloneInt64(observation.Usage.OutputTokens),
			TotalTokens:       cloneInt64(observation.Usage.TotalTokens),
			CachedInputTokens: cloneInt64(observation.Usage.CachedInputTokens),
			ReasoningTokens:   cloneInt64(observation.Usage.ReasoningTokens),
			Source:            store.ObservationSourceAdapter,
		}
	}
	return mapped
}

func applyObservation(outcome *store.AttemptOutcome, observation *AdapterObservation) {
	mapped := storeObservation(observation)
	if mapped == nil {
		return
	}
	outcome.Agent = mapped.Agent
	outcome.Model = mapped.Model
	outcome.RequestedModel = mapped.RequestedModel
	outcome.Effort = mapped.Effort
	outcome.RequestedEffort = mapped.RequestedEffort
	outcome.StopReason = mapped.StopReason
	outcome.Enforcement = mapped.Enforcement
	outcome.Observation = mapped
	outcome.DurationNanos = cloneDuration(mapped.DurationNanos)
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneDuration(value *time.Duration) *time.Duration {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
