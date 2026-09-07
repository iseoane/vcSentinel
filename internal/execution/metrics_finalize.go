package execution

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// ErrMetricsFinalized marks a run whose immutable metrics snapshot has already
// frozen its retry/recovery lifecycle.
var ErrMetricsFinalized = errors.New("execution: metrics already finalized")

// ErrMetricsNotFinal marks a run whose current durable head is not an
// intrinsically final outcome. Awaiting and retryable terminal states must
// remain unfrozen so a later response, retry, or recovery can advance them.
var ErrMetricsNotFinal = errors.New("execution: metrics require a non-retryable terminal state")

// FinalizeMetrics folds the durable observations for one run exactly once.
// Ordinary callers may finalize only an intrinsically final durable head:
// success or unavailable. Awaiting and retryable terminal states remain
// unfrozen so a later response, retry, or recovery can advance them.
// A repeated identical finalization is idempotent; a different snapshot
// remains an immutable conflict.
func (c *Controller) FinalizeMetrics(ctx context.Context, runID agentrun.Identity, semanticFailures []store.ExecutionFailure) (store.ExecutionMetrics, error) {
	return c.finalizeMetrics(ctx, runID, semanticFailures, false)
}

// FinalizeMetricsForDisposition persists metrics for a semantic owner's final
// disposition of one physical invocation. Unlike FinalizeMetrics, it accepts
// retryable terminal states because the owner has explicitly chosen not to
// use Controller.Retry for this physical run. The resulting snapshot freezes
// that run and Controller.Retry will reject any later relaunch.
func (c *Controller) FinalizeMetricsForDisposition(ctx context.Context, runID agentrun.Identity, semanticFailures []store.ExecutionFailure) (store.ExecutionMetrics, error) {
	return c.finalizeMetrics(ctx, runID, semanticFailures, true)
}

func (c *Controller) finalizeMetrics(ctx context.Context, runID agentrun.Identity, semanticFailures []store.ExecutionFailure, explicitDisposition bool) (store.ExecutionMetrics, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return store.ExecutionMetrics{}, err
	}
	projection, err := c.store.ReadDerivedProjection(string(runID))
	if err != nil {
		return store.ExecutionMetrics{}, err
	}
	if projection == nil || projection.State.TerminalClass() == agentrun.TerminalNone ||
		(!explicitDisposition && projection.State.Retryable()) {
		state := agentrun.LifecycleState("")
		if projection != nil {
			state = projection.State
		}
		return store.ExecutionMetrics{}, fmt.Errorf("%w: state %q", ErrMetricsNotFinal, state)
	}
	outcomes, err := c.store.ReadAttemptOutcomes(string(runID))
	if err != nil {
		return store.ExecutionMetrics{}, err
	}
	metrics := foldExecutionMetrics(string(runID), outcomes, semanticFailures)
	if existing, readErr := c.store.ReadExecutionMetrics(string(runID)); readErr != nil {
		return store.ExecutionMetrics{}, readErr
	} else if existing != nil {
		if reflect.DeepEqual(*existing, metrics) {
			return *existing, nil
		}
		return store.ExecutionMetrics{}, store.ErrImmutableConflict
	}
	if err := c.store.SaveExecutionMetricsForRevision(metrics, projection.Revision); err != nil {
		if errors.Is(err, store.ErrImmutableConflict) {
			if existing, readErr := c.store.ReadExecutionMetrics(string(runID)); readErr == nil && existing != nil && reflect.DeepEqual(*existing, metrics) {
				return *existing, nil
			}
		}
		return store.ExecutionMetrics{}, err
	}
	return metrics, nil
}

func foldExecutionMetrics(runID string, outcomes []store.AttemptOutcome, semanticFailures []store.ExecutionFailure) store.ExecutionMetrics {
	metrics := store.ExecutionMetrics{Version: store.ExecutionMetricsSchemaVersion, RunID: runID}
	observed := make([]store.AttemptObservation, 0, len(outcomes))
	allCompletedObserved := true
	allCompletedAgentTiming := true
	completed := 0
	byAgent := make([]store.AgentTiming, 0)
	agentIndex := make(map[string]int)
	for _, outcome := range outcomes {
		if outcome.Class.IsTerminal() && outcome.Class != agentrun.OutcomeSuccess {
			metrics.Failures = append(metrics.Failures, store.ExecutionFailure{
				InvocationID: outcome.InvocationID,
				Class:        store.FailureClass(outcome.Class),
				Detail:       outcome.Error,
			})
		}
		if !outcome.Class.IsTerminal() {
			continue
		}
		completed++
		if outcome.Observation == nil {
			allCompletedObserved = false
			allCompletedAgentTiming = false
			continue
		}
		observed = append(observed, *outcome.Observation)
		// Identity reads outcome.Observation only, never the flattened
		// outcome Agent/Model/Effort beside it. Provenance differs per
		// adapter kind (FU-9): a CLI adapter's transcript annotation carries
		// its resolved configuration (agentadapter.CLIAdapter.EffectiveAgent
		// reports the construction config), so those flattened values are
		// declarations, not evidence. An acpx adapter reports wire-only
		// identity, which already lands in Observation when the provider
		// announces it — the flattened copy adds nothing. Reading the
		// flattened fields would launder declarations into observations.
		identity := store.ObservedExecutionIdentity{
			InvocationID:    outcome.InvocationID,
			Agent:           outcome.Observation.Agent,
			Model:           outcome.Observation.Model,
			RequestedModel:  outcome.Observation.RequestedModel,
			Effort:          outcome.Observation.Effort,
			RequestedEffort: outcome.Observation.RequestedEffort,
			Source:          store.ObservationSourceAdapter,
		}
		if identity.Agent != "" || identity.Model != "" || identity.RequestedModel != "" ||
			identity.Effort != "" || identity.RequestedEffort != "" {
			metrics.Identities = append(metrics.Identities, identity)
		}
		if outcome.Observation.DurationNanos == nil || outcome.Observation.Agent == "" {
			allCompletedAgentTiming = false
			continue
		}
		if index, ok := agentIndex[outcome.Observation.Agent]; ok {
			byAgent[index].DurationNanos += *outcome.Observation.DurationNanos
		} else {
			agentIndex[outcome.Observation.Agent] = len(byAgent)
			byAgent = append(byAgent, store.AgentTiming{
				Identity: store.ObservedExecutionIdentity{
					Agent:  outcome.Observation.Agent,
					Source: store.ObservationSourceAdapter,
				},
				DurationNanos: *outcome.Observation.DurationNanos,
			})
		}
	}
	if completed > 0 && allCompletedObserved {
		metrics.Timing = foldTiming(observed)
		metrics.Usage = foldUsage(observed)
		if allCompletedAgentTiming {
			metrics.Timing.ByAgent = byAgent
		}
	}
	metrics.Failures = appendUniqueFailures(metrics.Failures, semanticFailures)
	return metrics
}

func foldTiming(observations []store.AttemptObservation) *store.ExecutionTiming {
	var total time.Duration
	for _, observation := range observations {
		if observation.DurationNanos == nil {
			return nil
		}
		total += *observation.DurationNanos
	}
	return &store.ExecutionTiming{TotalDurationNanos: &total}
}

func foldUsage(observations []store.AttemptObservation) *store.ExecutionTokenUsage {
	allInput, allOutput, allTotal, allCached, allReasoning := true, true, true, true, true
	var input, output, total, cached, reasoning int64
	for _, observation := range observations {
		if observation.Usage == nil {
			allInput, allOutput, allTotal, allCached, allReasoning = false, false, false, false, false
			continue
		}
		if observation.Usage.InputTokens == nil {
			allInput = false
		} else {
			input += *observation.Usage.InputTokens
		}
		if observation.Usage.OutputTokens == nil {
			allOutput = false
		} else {
			output += *observation.Usage.OutputTokens
		}
		if observation.Usage.TotalTokens == nil {
			allTotal = false
		} else {
			total += *observation.Usage.TotalTokens
		}
		if observation.Usage.CachedInputTokens == nil {
			allCached = false
		} else {
			cached += *observation.Usage.CachedInputTokens
		}
		if observation.Usage.ReasoningTokens == nil {
			allReasoning = false
		} else {
			reasoning += *observation.Usage.ReasoningTokens
		}
	}
	usage := &store.ExecutionTokenUsage{Source: store.ObservationSourceAdapter}
	if allInput {
		usage.InputTokens = &input
	}
	if allOutput {
		usage.OutputTokens = &output
	}
	if allTotal {
		usage.TotalTokens = &total
	}
	if allCached {
		usage.CachedInputTokens = &cached
	}
	if allReasoning {
		usage.ReasoningTokens = &reasoning
	}
	if usage.InputTokens == nil && usage.OutputTokens == nil && usage.TotalTokens == nil && usage.CachedInputTokens == nil && usage.ReasoningTokens == nil {
		return nil
	}
	return usage
}

func appendUniqueFailures(existing, additions []store.ExecutionFailure) []store.ExecutionFailure {
	seen := make(map[string]struct{}, len(existing)+len(additions))
	for _, failure := range existing {
		seen[failure.InvocationID+"\x00"+string(failure.Class)+"\x00"+failure.Detail] = struct{}{}
	}
	for _, failure := range additions {
		key := failure.InvocationID + "\x00" + string(failure.Class) + "\x00" + failure.Detail
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		existing = append(existing, failure)
	}
	return existing
}
