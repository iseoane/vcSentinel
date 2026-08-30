package metrics

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

func aggregateExecutions(observations []ExecutionObservation, suppliedStages []StageObservation, confirmedFindings int64) (ExecutionAggregate, []CostAggregate, []StageAggregate) {
	groups := make(map[string][]ExecutionObservation)
	for _, observation := range observations {
		key := strings.TrimSpace(observation.LogicalRunID)
		if key == "" {
			key = strings.TrimSpace(observation.RunID)
		}
		if key == "" && observation.Metrics != nil {
			key = strings.TrimSpace(observation.Metrics.RunID)
		}
		if key == "" {
			key = "anonymous:" + executionSortKey(observation)
		}
		groups[key] = append(groups[key], observation)
	}
	groupKeys := make([]string, 0, len(groups))
	for key := range groups {
		groupKeys = append(groupKeys, key)
	}
	sort.Strings(groupKeys)
	result := ExecutionAggregate{}
	costs := make(map[string]*costCounter)
	stages := make(map[string][]int64)
	var reuseObservedRuns, identityObservedRuns, identityKnownRuns int64
	for _, key := range groupKeys {
		group := groups[key]
		result.LogicalRuns++
		outcomes := uniqueOutcomes(group)
		if len(outcomes) > 1 {
			result.RetriedRuns++
		}
		metricsSnapshot := selectMetrics(group)
		class, terminal := finalOutcome(outcomes)
		switch {
		case terminal && class == agentrun.OutcomeSuccess:
			result.SuccessfulRuns++
		case terminal:
			result.FailedRuns++
		case metricsSnapshot != nil:
			if len(metricsSnapshot.Failures) > 0 {
				result.FailedRuns++
			} else {
				result.SuccessfulRuns++
			}
		}
		for _, outcome := range outcomes {
			if outcome.Class.IsTerminal() && outcome.Class != agentrun.OutcomeSuccess {
				result.Failures = appendFailure(result.Failures, string(outcome.Class))
			}
		}
		if metricsSnapshot != nil {
			for _, failure := range metricsSnapshot.Failures {
				if failure.Class != "" {
					result.Failures = appendFailure(result.Failures, string(failure.Class))
				}
			}
		}
		if metricsSnapshot == nil {
			continue
		}
		result.MeasuredRuns++
		result.Duration.Total++
		result.InputTokens.Total++
		result.OutputTokens.Total++
		result.TotalTokens.Total++
		result.CachedInputTokens.Total++
		result.ReasoningTokens.Total++
		if metricsSnapshot.Timing != nil {
			if metricsSnapshot.Timing.TotalDurationNanos != nil {
				result.Duration.Observed++
				addMeasurementValue(&result.Duration, int64(*metricsSnapshot.Timing.TotalDurationNanos))
			}
			for _, timing := range metricsSnapshot.Timing.ByCapability {
				if timing.CapabilityID != "" && timing.DurationNanos >= 0 {
					stages[timing.CapabilityID] = append(stages[timing.CapabilityID], int64(timing.DurationNanos))
				}
			}
		}
		if metricsSnapshot.Usage != nil {
			if metricsSnapshot.Usage.TotalTokens != nil {
				result.TotalTokens.Observed++
				addMeasurementValue(&result.TotalTokens, *metricsSnapshot.Usage.TotalTokens)
			}
			if metricsSnapshot.Usage.CachedInputTokens != nil {
				result.CachedInputTokens.Observed++
				addMeasurementValue(&result.CachedInputTokens, *metricsSnapshot.Usage.CachedInputTokens)
			}
			if metricsSnapshot.Usage.ReasoningTokens != nil {
				result.ReasoningTokens.Observed++
				addMeasurementValue(&result.ReasoningTokens, *metricsSnapshot.Usage.ReasoningTokens)
			}
		}
		if metricsSnapshot.Usage != nil {
			if metricsSnapshot.Usage.InputTokens != nil {
				result.InputTokens.Observed++
				addMeasurementValue(&result.InputTokens, *metricsSnapshot.Usage.InputTokens)
			}
			if metricsSnapshot.Usage.OutputTokens != nil {
				result.OutputTokens.Observed++
				addMeasurementValue(&result.OutputTokens, *metricsSnapshot.Usage.OutputTokens)
			}
		}
		if metricsSnapshot.Cost != nil && metricsSnapshot.Cost.Currency != "" {
			currency := metricsSnapshot.Cost.Currency
			counter := costs[currency]
			if counter == nil {
				counter = &costCounter{}
				costs[currency] = counter
			}
			counter.TotalMicros += metricsSnapshot.Cost.AmountMicros
			counter.ObservedRuns++
		}
		if metricsSnapshot.Reuse != nil {
			reuseObservedRuns++
			for _, capability := range uniqueStrings(metricsSnapshot.Reuse.ReusedCapabilityIDs) {
				if capability != "" {
					result.Reuse.Reused++
				}
			}
			for _, capability := range uniqueStrings(metricsSnapshot.Reuse.RecomputedCapabilityIDs) {
				if capability != "" {
					result.Reuse.Recomputed++
				}
			}
		}
		identityObservedRuns++
		for _, identity := range metricsSnapshot.Identities {
			if identity.Agent != "" || identity.Model != "" || identity.Effort != "" {
				identityKnownRuns++
				break
			}
		}
		if metricsSnapshot.Scope != nil {
			switch metricsSnapshot.Scope.Kind {
			case store.ScopeFull:
				result.Scope.Full++
			case store.ScopeAffected:
				result.Scope.Affected++
			default:
				result.Scope.Unknown++
			}
		} else {
			result.Scope.Unknown++
		}
	}
	result.SuccessRate = ratio(result.SuccessfulRuns, result.SuccessfulRuns+result.FailedRuns, result.SuccessfulRuns+result.FailedRuns, result.LogicalRuns)
	result.Reuse.Rate = ratio(result.Reuse.Reused, result.Reuse.Reused+result.Reuse.Recomputed, reuseObservedRuns, result.MeasuredRuns)
	result.Duration.Coverage = coverage(result.Duration.Observed, result.Duration.Total)
	result.InputTokens.Coverage = coverage(result.InputTokens.Observed, result.InputTokens.Total)
	result.OutputTokens.Coverage = coverage(result.OutputTokens.Observed, result.OutputTokens.Total)
	result.CostCoverage = costCoverage(costs, result.MeasuredRuns)
	result.IdentityCoverage = coverage(identityKnownRuns, identityObservedRuns)
	result.TotalTokens.Coverage = coverage(result.TotalTokens.Observed, result.TotalTokens.Total)
	result.CachedInputTokens.Coverage = coverage(result.CachedInputTokens.Observed, result.CachedInputTokens.Total)
	result.ReasoningTokens.Coverage = coverage(result.ReasoningTokens.Observed, result.ReasoningTokens.Total)
	result.Scope.Coverage = coverage(result.Scope.Full+result.Scope.Affected, result.MeasuredRuns)
	sort.Slice(result.Failures, func(i, j int) bool { return result.Failures[i].Class < result.Failures[j].Class })

	for _, sample := range suppliedStages {
		if strings.TrimSpace(sample.Stage) != "" && sample.DurationNanos >= 0 {
			stages[sample.Stage] = append(stages[sample.Stage], sample.DurationNanos)
		}
	}
	stageRows := make([]StageAggregate, 0, len(stages))
	stageNames := make([]string, 0, len(stages))
	for name := range stages {
		stageNames = append(stageNames, name)
	}
	sort.Strings(stageNames)
	for _, name := range stageNames {
		values := append([]int64(nil), stages[name]...)
		sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
		stageRows = append(stageRows, StageAggregate{
			Stage: name, Samples: int64(len(values)),
			P50Nanos: percentile(values, 0.50), P95Nanos: percentile(values, 0.95),
			Coverage: coverage(int64(len(values)), int64(len(values))),
		})
	}

	costRows := make([]CostAggregate, 0, len(costs))
	currencies := make([]string, 0, len(costs))
	for currency := range costs {
		currencies = append(currencies, currency)
	}
	sort.Strings(currencies)
	for _, currency := range currencies {
		counter := costs[currency]
		costRows = append(costRows, CostAggregate{
			Currency: currency, TotalMicros: counter.TotalMicros,
			ObservedRuns: counter.ObservedRuns, TotalRuns: result.MeasuredRuns,
			CostPerConfirmed: ratio(counter.TotalMicros, confirmedFindings, counter.ObservedRuns, result.MeasuredRuns),
		})
	}
	return result, costRows, stageRows
}

type costCounter struct {
	TotalMicros  int64
	ObservedRuns int64
}

func addMeasurementValue(measurement *Measurement, value int64) {
	if measurement.Value == nil {
		measurement.Value = new(int64)
	}
	*measurement.Value += value
}

func costCoverage(costs map[string]*costCounter, total int64) Coverage {
	var observed int64
	for _, counter := range costs {
		observed += counter.ObservedRuns
	}
	if observed > total {
		observed = total
	}
	return coverage(observed, total)
}

func selectMetrics(group []ExecutionObservation) *store.ExecutionMetrics {
	var selected *store.ExecutionMetrics
	selectedKey := ""
	for _, observation := range group {
		if observation.Metrics == nil {
			continue
		}
		key := metricSortKey(*observation.Metrics)
		if selected == nil || key > selectedKey {
			copy := *observation.Metrics
			selected = &copy
			selectedKey = key
		}
	}
	return selected
}
func executionSortKey(observation ExecutionObservation) string {
	if observation.Metrics != nil {
		return metricSortKey(*observation.Metrics)
	}
	parts := make([]string, 0, len(observation.Outcomes))
	for _, outcome := range observation.Outcomes {
		parts = append(parts, fmt.Sprintf("%s|%s|%s|%s|%s", outcome.RunID, outcome.At.UTC().Format(time.RFC3339Nano), outcome.Class, outcome.Error, outcome.OutputHash))
	}
	sort.Strings(parts)
	return strings.Join(parts, "\x00")
}

func metricSortKey(metrics store.ExecutionMetrics) string {
	reuseCount := 0
	if metrics.Reuse != nil {
		reuseCount = len(metrics.Reuse.ReusedCapabilityIDs)
	}
	capabilityCount := 0
	if metrics.Timing != nil {
		capabilityCount = len(metrics.Timing.ByCapability)
	}
	return fmt.Sprintf("%020d|%s|%d|%d|%d", len(metrics.Identities), metrics.RunID, len(metrics.Failures), capabilityCount, reuseCount)
}

func uniqueOutcomes(group []ExecutionObservation) []store.AttemptOutcome {
	seen := make(map[string]store.AttemptOutcome)
	for _, observation := range group {
		for _, outcome := range observation.Outcomes {
			key := outcome.InvocationID
			if key == "" {
				key = fmt.Sprintf("%s|%s|%s|%s|%s", outcome.RunID, outcome.At.UTC().Format(time.RFC3339Nano), outcome.Class, outcome.Error, outcome.OutputHash)
			}
			if current, exists := seen[key]; !exists || outcomeAfter(outcome, current) {
				seen[key] = outcome
			}
		}
	}
	outcomes := make([]store.AttemptOutcome, 0, len(seen))
	for _, outcome := range seen {
		outcomes = append(outcomes, outcome)
	}
	sort.Slice(outcomes, func(i, j int) bool {
		if outcomes[i].At.Equal(outcomes[j].At) {
			return outcomes[i].InvocationID < outcomes[j].InvocationID
		}
		return outcomes[i].At.Before(outcomes[j].At)
	})
	return outcomes
}

func finalOutcome(outcomes []store.AttemptOutcome) (agentrun.OutcomeClass, bool) {
	for i := len(outcomes) - 1; i >= 0; i-- {
		if outcomes[i].Class.IsTerminal() {
			return outcomes[i].Class, true
		}
	}
	return "", false
}

func appendFailure(failures []FailureAggregate, class string) []FailureAggregate {
	for i := range failures {
		if failures[i].Class == class {
			failures[i].Count++
			return failures
		}
	}
	return append(failures, FailureAggregate{Class: class, Count: 1})
}

func percentile(values []int64, p float64) int64 {
	if len(values) == 0 {
		return 0
	}
	rank := int(math.Ceil(p * float64(len(values))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(values) {
		rank = len(values)
	}
	return values[rank-1]
}

func ratio(numerator, denominator, coverageObserved, coverageTotal int64) Ratio {
	result := Ratio{Numerator: numerator, Denominator: denominator, Coverage: coverage(coverageObserved, coverageTotal)}
	if denominator > 0 {
		value := round(float64(numerator) / float64(denominator))
		result.Value = &value
	}
	return result
}

func coverage(observed, total int64) Coverage {
	if observed < 0 {
		observed = 0
	}
	if total < 0 {
		total = 0
	}
	if observed > total {
		observed = total
	}
	result := Coverage{Observed: observed, Total: total}
	if total > 0 {
		value := round(float64(observed) / float64(total))
		result.Value = &value
	}
	return result
}

func round(value float64) float64 {
	return math.Round(value*10000) / 10000
}

func outcomeAfter(candidate, current store.AttemptOutcome) bool {
	if !candidate.At.Equal(current.At) {
		return candidate.At.After(current.At)
	}
	return string(candidate.Class) > string(current.Class)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
