package metrics

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

func aggregateExecutions(observations []ExecutionObservation, suppliedStages []StageObservation, confirmedFindings int64) (ExecutionAggregate, []CostAggregate, []StageAggregate) {
	ordered := append([]ExecutionObservation(nil), observations...)
	sort.Slice(ordered, func(i, j int) bool {
		return executionObservationSortKey(ordered[i]) < executionObservationSortKey(ordered[j])
	})
	groups := make(map[string][]ExecutionObservation)
	anonymousCounts := make(map[string]int)
	for _, observation := range ordered {
		key := strings.TrimSpace(observation.LogicalRunID)
		if key == "" {
			key = strings.TrimSpace(observation.RunID)
		}
		if key == "" && observation.Metrics != nil {
			key = strings.TrimSpace(observation.Metrics.RunID)
		}
		if key == "" {
			outcomeIDs := make([]string, 0, len(observation.Outcomes))
			for _, outcome := range observation.Outcomes {
				if value := strings.TrimSpace(outcome.RunID); value != "" {
					outcomeIDs = append(outcomeIDs, value)
				}
			}
			sort.Strings(outcomeIDs)
			if len(outcomeIDs) > 0 {
				key = outcomeIDs[0]
			}
		}
		if key == "" {
			base := "anonymous-execution:" + executionObservationSortKey(observation)
			occurrence := anonymousCounts[base]
			anonymousCounts[base] = occurrence + 1
			key = fmt.Sprintf("%s:%020d", base, occurrence)
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
	stageRuns := make(map[string]map[string]struct{})
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
		case metricsSnapshot != nil && len(metricsSnapshot.Failures) > 0:
			result.FailedRuns++
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
			if metricsSnapshot.Timing.TotalDurationNanos != nil && *metricsSnapshot.Timing.TotalDurationNanos >= 0 {
				result.Duration.Observed++
				addMeasurementValue(&result.Duration, int64(*metricsSnapshot.Timing.TotalDurationNanos))
			}
			for _, timing := range metricsSnapshot.Timing.ByCapability {
				if timing.CapabilityID != "" && timing.DurationNanos >= 0 {
					stages[timing.CapabilityID] = append(stages[timing.CapabilityID], int64(timing.DurationNanos))
					if stageRuns[timing.CapabilityID] == nil {
						stageRuns[timing.CapabilityID] = make(map[string]struct{})
					}
					stageRuns[timing.CapabilityID][key] = struct{}{}
				}
			}
		}
		if metricsSnapshot.Usage != nil {
			if metricsSnapshot.Usage.TotalTokens != nil && *metricsSnapshot.Usage.TotalTokens >= 0 {
				result.TotalTokens.Observed++
				addMeasurementValue(&result.TotalTokens, *metricsSnapshot.Usage.TotalTokens)
			}
			if metricsSnapshot.Usage.CachedInputTokens != nil && *metricsSnapshot.Usage.CachedInputTokens >= 0 {
				result.CachedInputTokens.Observed++
				addMeasurementValue(&result.CachedInputTokens, *metricsSnapshot.Usage.CachedInputTokens)
			}
			if metricsSnapshot.Usage.ReasoningTokens != nil && *metricsSnapshot.Usage.ReasoningTokens >= 0 {
				result.ReasoningTokens.Observed++
				addMeasurementValue(&result.ReasoningTokens, *metricsSnapshot.Usage.ReasoningTokens)
			}
		}
		if metricsSnapshot.Usage != nil {
			if metricsSnapshot.Usage.InputTokens != nil && *metricsSnapshot.Usage.InputTokens >= 0 {
				result.InputTokens.Observed++
				addMeasurementValue(&result.InputTokens, *metricsSnapshot.Usage.InputTokens)
			}
			if metricsSnapshot.Usage.OutputTokens != nil && *metricsSnapshot.Usage.OutputTokens >= 0 {
				result.OutputTokens.Observed++
				addMeasurementValue(&result.OutputTokens, *metricsSnapshot.Usage.OutputTokens)
			}
		}
		if metricsSnapshot.Cost != nil && metricsSnapshot.Cost.Currency != "" && metricsSnapshot.Cost.AmountMicros >= 0 {
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
		if strings.TrimSpace(sample.Stage) == "" || sample.DurationNanos < 0 {
			continue
		}
		stages[sample.Stage] = append(stages[sample.Stage], sample.DurationNanos)
		runKey := strings.TrimSpace(sample.LogicalRunID)
		if runKey == "" {
			runKey = fmt.Sprintf("supplied-stage:%s:%d", sample.Stage, sample.DurationNanos)
		}
		if stageRuns[sample.Stage] == nil {
			stageRuns[sample.Stage] = make(map[string]struct{})
		}
		stageRuns[sample.Stage][runKey] = struct{}{}
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
		observedRuns := int64(len(stageRuns[name]))
		stageRows = append(stageRows, StageAggregate{
			Stage: name, Samples: int64(len(values)),
			P50Nanos: percentile(values, 0.50), P95Nanos: percentile(values, 0.95),
			Coverage: coverage(observedRuns, result.MeasuredRuns),
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
	snapshots := make([]store.ExecutionMetrics, 0, len(group))
	for _, observation := range group {
		if observation.Metrics != nil {
			snapshots = append(snapshots, canonicalMetrics(*observation.Metrics))
		}
	}
	if len(snapshots) == 0 {
		return nil
	}
	sort.Slice(snapshots, func(i, j int) bool { return metricSortKey(snapshots[i]) < metricSortKey(snapshots[j]) })
	merged := snapshots[0]
	for _, snapshot := range snapshots[1:] {
		merged = mergeMetrics(merged, snapshot)
	}
	return &merged
}

func executionObservationSortKey(observation ExecutionObservation) string {
	outcomes := make([]string, 0, len(observation.Outcomes))
	for _, outcome := range observation.Outcomes {
		outcomes = append(outcomes, outcomeSortKey(outcome))
	}
	sort.Strings(outcomes)
	return executionSortKey(observation) + "\x00" + strings.Join(outcomes, "\x00")
}

func executionSortKey(observation ExecutionObservation) string {
	if observation.Metrics != nil {
		return metricSortKey(*observation.Metrics)
	}
	parts := make([]string, 0, len(observation.Outcomes))
	for _, outcome := range observation.Outcomes {
		parts = append(parts, outcomeSortKey(outcome))
	}
	sort.Strings(parts)
	return strings.Join(parts, "\x00")
}

func metricSortKey(metrics store.ExecutionMetrics) string {
	data, _ := json.Marshal(canonicalMetrics(metrics))
	return string(data)
}

func canonicalMetrics(metrics store.ExecutionMetrics) store.ExecutionMetrics {
	result := metrics
	result.Identities = append([]store.ObservedExecutionIdentity(nil), metrics.Identities...)
	sort.Slice(result.Identities, func(i, j int) bool {
		return executionIdentitySortKey(result.Identities[i]) < executionIdentitySortKey(result.Identities[j])
	})
	if metrics.Timing != nil {
		timing := *metrics.Timing
		timing.ByCapability = append([]store.CapabilityTiming(nil), metrics.Timing.ByCapability...)
		sort.Slice(timing.ByCapability, func(i, j int) bool {
			left, right := timing.ByCapability[i], timing.ByCapability[j]
			if left.CapabilityID != right.CapabilityID {
				return left.CapabilityID < right.CapabilityID
			}
			return left.DurationNanos < right.DurationNanos
		})
		timing.ByAgent = append([]store.AgentTiming(nil), metrics.Timing.ByAgent...)
		sort.Slice(timing.ByAgent, func(i, j int) bool {
			left, right := timing.ByAgent[i], timing.ByAgent[j]
			leftKey, rightKey := executionIdentitySortKey(left.Identity), executionIdentitySortKey(right.Identity)
			if leftKey != rightKey {
				return leftKey < rightKey
			}
			return left.DurationNanos < right.DurationNanos
		})
		result.Timing = &timing
	}
	if metrics.Usage != nil {
		usage := *metrics.Usage
		result.Usage = &usage
	}
	if metrics.Cost != nil {
		cost := *metrics.Cost
		result.Cost = &cost
	}
	if metrics.Scope != nil {
		scope := *metrics.Scope
		if metrics.Scope.Savings != nil {
			savings := *metrics.Scope.Savings
			if metrics.Scope.Savings.Cost != nil {
				cost := *metrics.Scope.Savings.Cost
				savings.Cost = &cost
			}
			scope.Savings = &savings
		}
		result.Scope = &scope
	}
	if metrics.Reuse != nil {
		reuse := *metrics.Reuse
		reuse.ReusedCapabilityIDs = append([]string(nil), metrics.Reuse.ReusedCapabilityIDs...)
		reuse.RecomputedCapabilityIDs = append([]string(nil), metrics.Reuse.RecomputedCapabilityIDs...)
		sort.Strings(reuse.ReusedCapabilityIDs)
		sort.Strings(reuse.RecomputedCapabilityIDs)
		result.Reuse = &reuse
	}
	result.Failures = append([]store.ExecutionFailure(nil), metrics.Failures...)
	sort.Slice(result.Failures, func(i, j int) bool {
		left, right := result.Failures[i], result.Failures[j]
		leftKey := fmt.Sprintf("%s\x00%s\x00%s", left.InvocationID, left.Class, left.Detail)
		rightKey := fmt.Sprintf("%s\x00%s\x00%s", right.InvocationID, right.Class, right.Detail)
		return leftKey < rightKey
	})
	return result
}

func executionIdentitySortKey(identity store.ObservedExecutionIdentity) string {
	data, _ := json.Marshal(identity)
	return string(data)
}

func mergeMetrics(left, right store.ExecutionMetrics) store.ExecutionMetrics {
	result := left
	if right.Version > result.Version {
		result.Version = right.Version
	}
	if result.RunID == "" || right.RunID > result.RunID {
		result.RunID = right.RunID
	}
	result.Identities = mergeIdentities(result.Identities, right.Identities)
	result.Timing = mergeTiming(result.Timing, right.Timing)
	result.Usage = mergeUsage(result.Usage, right.Usage)
	result.Cost = mergeCost(result.Cost, right.Cost)
	result.Scope = mergeScope(result.Scope, right.Scope)
	result.Reuse = mergeReuse(result.Reuse, right.Reuse)
	result.Failures = mergeFailures(result.Failures, right.Failures)
	return canonicalMetrics(result)
}

func mergeIdentities(left, right []store.ObservedExecutionIdentity) []store.ObservedExecutionIdentity {
	byKey := make(map[string]store.ObservedExecutionIdentity, len(left)+len(right))
	for _, identity := range append(append([]store.ObservedExecutionIdentity(nil), left...), right...) {
		key := executionIdentitySortKey(identity)
		if current, exists := byKey[key]; !exists || key > executionIdentitySortKey(current) {
			byKey[key] = identity
		}
	}
	result := make([]store.ObservedExecutionIdentity, 0, len(byKey))
	for _, identity := range byKey {
		result = append(result, identity)
	}
	sort.Slice(result, func(i, j int) bool {
		return executionIdentitySortKey(result[i]) < executionIdentitySortKey(result[j])
	})
	return result
}

func mergeTiming(left, right *store.ExecutionTiming) *store.ExecutionTiming {
	if left == nil {
		if right == nil {
			return nil
		}
		copy := *right
		return &copy
	}
	if right == nil {
		copy := *left
		return &copy
	}
	result := *left
	if result.TotalDurationNanos == nil || (right.TotalDurationNanos != nil && *right.TotalDurationNanos > *result.TotalDurationNanos) {
		if right.TotalDurationNanos != nil {
			value := *right.TotalDurationNanos
			result.TotalDurationNanos = &value
		}
	}
	result.ByCapability = mergeCapabilityTimings(left.ByCapability, right.ByCapability)
	result.ByAgent = mergeAgentTimings(left.ByAgent, right.ByAgent)
	return &result
}
func mergeCapabilityTimings(left, right []store.CapabilityTiming) []store.CapabilityTiming {
	type key struct {
		capability string
		duration   int64
	}
	byKey := make(map[key]store.CapabilityTiming, len(left)+len(right))
	for _, timing := range append(append([]store.CapabilityTiming(nil), left...), right...) {
		byKey[key{capability: timing.CapabilityID, duration: int64(timing.DurationNanos)}] = timing
	}
	result := make([]store.CapabilityTiming, 0, len(byKey))
	for _, timing := range byKey {
		result = append(result, timing)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CapabilityID != result[j].CapabilityID {
			return result[i].CapabilityID < result[j].CapabilityID
		}
		return result[i].DurationNanos < result[j].DurationNanos
	})
	return result
}

func mergeAgentTimings(left, right []store.AgentTiming) []store.AgentTiming {
	type key struct {
		identity string
		duration int64
	}
	byKey := make(map[key]store.AgentTiming, len(left)+len(right))
	for _, timing := range append(append([]store.AgentTiming(nil), left...), right...) {
		byKey[key{identity: executionIdentitySortKey(timing.Identity), duration: int64(timing.DurationNanos)}] = timing
	}
	result := make([]store.AgentTiming, 0, len(byKey))
	for _, timing := range byKey {
		result = append(result, timing)
	}
	sort.Slice(result, func(i, j int) bool {
		leftKey, rightKey := executionIdentitySortKey(result[i].Identity), executionIdentitySortKey(result[j].Identity)
		if leftKey != rightKey {
			return leftKey < rightKey
		}
		return result[i].DurationNanos < result[j].DurationNanos
	})
	return result
}

func mergeUsage(left, right *store.ExecutionTokenUsage) *store.ExecutionTokenUsage {
	if left == nil {
		if right == nil {
			return nil
		}
		copy := *right
		return &copy
	}
	if right == nil {
		copy := *left
		return &copy
	}
	result := *left
	result.InputTokens = maxIntPointer(left.InputTokens, right.InputTokens)
	result.OutputTokens = maxIntPointer(left.OutputTokens, right.OutputTokens)
	result.TotalTokens = maxIntPointer(left.TotalTokens, right.TotalTokens)
	result.CachedInputTokens = maxIntPointer(left.CachedInputTokens, right.CachedInputTokens)
	result.ReasoningTokens = maxIntPointer(left.ReasoningTokens, right.ReasoningTokens)
	if right.Source > result.Source {
		result.Source = right.Source
	}
	return &result
}

func maxIntPointer(left, right *int64) *int64 {
	if left == nil {
		if right == nil {
			return nil
		}
		value := *right
		return &value
	}
	if right == nil || *left >= *right {
		value := *left
		return &value
	}
	value := *right
	return &value
}

func mergeCost(left, right *store.ExecutionCost) *store.ExecutionCost {
	if left == nil {
		if right == nil {
			return nil
		}
		copy := *right
		return &copy
	}
	if right == nil {
		copy := *left
		return &copy
	}
	if costAfter(right, left) {
		copy := *right
		return &copy
	}
	copy := *left
	return &copy
}

func costAfter(candidate, current *store.ExecutionCost) bool {
	if candidate.Currency != current.Currency {
		return candidate.Currency > current.Currency
	}
	if candidate.AmountMicros != current.AmountMicros {
		return candidate.AmountMicros > current.AmountMicros
	}
	candidateKey := fmt.Sprintf("%s\x00%s", candidate.Provenance.Source, candidate.Provenance.Reference)
	currentKey := fmt.Sprintf("%s\x00%s", current.Provenance.Source, current.Provenance.Reference)
	return candidateKey > currentKey
}

func mergeScope(left, right *store.ExecutionScope) *store.ExecutionScope {
	if left == nil {
		if right == nil {
			return nil
		}
		copy := *right
		return &copy
	}
	if right == nil {
		copy := *left
		return &copy
	}
	result := *left
	if result.Kind == "" || right.Kind > result.Kind {
		result.Kind = right.Kind
	}
	if result.Savings == nil && right.Savings != nil {
		savings := *right.Savings
		result.Savings = &savings
	}
	return &result
}

func mergeReuse(left, right *store.ExecutionReuse) *store.ExecutionReuse {
	if left == nil {
		if right == nil {
			return nil
		}
		copy := *right
		return &copy
	}
	if right == nil {
		copy := *left
		return &copy
	}
	result := &store.ExecutionReuse{
		ReusedCapabilityIDs:     uniqueStrings(append(append([]string(nil), left.ReusedCapabilityIDs...), right.ReusedCapabilityIDs...)),
		RecomputedCapabilityIDs: uniqueStrings(append(append([]string(nil), left.RecomputedCapabilityIDs...), right.RecomputedCapabilityIDs...)),
	}
	sort.Strings(result.ReusedCapabilityIDs)
	sort.Strings(result.RecomputedCapabilityIDs)
	return result
}

func mergeFailures(left, right []store.ExecutionFailure) []store.ExecutionFailure {
	byKey := make(map[string]store.ExecutionFailure, len(left)+len(right))
	for _, failure := range append(append([]store.ExecutionFailure(nil), left...), right...) {
		key := fmt.Sprintf("%s\x00%s\x00%s", failure.InvocationID, failure.Class, failure.Detail)
		byKey[key] = failure
	}
	result := make([]store.ExecutionFailure, 0, len(byKey))
	for _, failure := range byKey {
		result = append(result, failure)
	}
	sort.Slice(result, func(i, j int) bool {
		leftKey := fmt.Sprintf("%s\x00%s\x00%s", result[i].InvocationID, result[i].Class, result[i].Detail)
		rightKey := fmt.Sprintf("%s\x00%s\x00%s", result[j].InvocationID, result[j].Class, result[j].Detail)
		return leftKey < rightKey
	})
	return result
}
func canonicalOutcome(outcome store.AttemptOutcome) store.AttemptOutcome {
	outcome.At = outcome.At.UTC()
	return outcome
}

func outcomeSortKey(outcome store.AttemptOutcome) string {
	data, _ := json.Marshal(canonicalOutcome(outcome))
	return string(data)
}

func outcomeIdentityKey(outcome store.AttemptOutcome) string {
	if invocationID := strings.TrimSpace(outcome.InvocationID); invocationID != "" {
		return "invocation:" + invocationID
	}
	return "anonymous:" + outcomeSortKey(outcome)
}

func uniqueOutcomes(group []ExecutionObservation) []store.AttemptOutcome {
	seen := make(map[string]store.AttemptOutcome)
	for _, observation := range group {
		for _, outcome := range observation.Outcomes {
			key := outcomeIdentityKey(outcome)
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
		if !outcomes[i].At.Equal(outcomes[j].At) {
			return outcomes[i].At.Before(outcomes[j].At)
		}
		return outcomeSortKey(outcomes[i]) < outcomeSortKey(outcomes[j])
	})
	return outcomes
}

func finalOutcome(outcomes []store.AttemptOutcome) (agentrun.OutcomeClass, bool) {
	ordered := append([]store.AttemptOutcome(nil), outcomes...)
	sort.Slice(ordered, func(i, j int) bool {
		if !ordered[i].At.Equal(ordered[j].At) {
			return ordered[i].At.Before(ordered[j].At)
		}
		return outcomeSortKey(ordered[i]) < outcomeSortKey(ordered[j])
	})
	for i := len(ordered) - 1; i >= 0; i-- {
		if ordered[i].Class.IsTerminal() {
			return ordered[i].Class, true
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
	return outcomeSortKey(candidate) > outcomeSortKey(current)
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
