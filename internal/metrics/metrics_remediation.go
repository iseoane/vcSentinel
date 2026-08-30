package metrics

import (
	"sort"
	"strings"
	"time"
)

type remediationKey struct {
	LogicalID string
	Anonymous bool
	Target    string
	Dimension string
	At        time.Time
	Success   bool
}

func remediationIdentity(observation RemediationObservation) remediationKey {
	key := remediationKey{LogicalID: strings.TrimSpace(observation.LogicalID)}
	if key.LogicalID != "" {
		return key
	}
	key.Anonymous = true
	key.Target = observation.Target
	key.Dimension = observation.Dimension
	key.At = observation.At.UTC()
	key.Success = observation.Success
	return key
}
func remediationKeyLess(left, right remediationKey) bool {
	if left.Anonymous != right.Anonymous {
		return !left.Anonymous
	}
	if left.LogicalID != right.LogicalID {
		return left.LogicalID < right.LogicalID
	}
	if left.Target != right.Target {
		return left.Target < right.Target
	}
	if left.Dimension != right.Dimension {
		return left.Dimension < right.Dimension
	}
	if !left.At.Equal(right.At) {
		return left.At.Before(right.At)
	}
	return !left.Success && right.Success
}

func aggregateRemediations(observations []RemediationObservation) RemediationAggregate {
	selected := make(map[remediationKey]RemediationObservation, len(observations))
	for _, observation := range observations {
		key := remediationIdentity(observation)
		if current, exists := selected[key]; !exists || remediationAfter(observation, current) {
			selected[key] = observation
		}
	}
	result := RemediationAggregate{}
	byDimension := make(map[string]*remediationCounter)
	keys := make([]remediationKey, 0, len(selected))
	for key := range selected {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return remediationKeyLess(keys[i], keys[j]) })
	for _, key := range keys {
		observation := selected[key]
		result.Attempts++
		name := displayDimension(observation.Dimension)
		counter := remediationCounterFor(byDimension, name)
		counter.Attempts++
		if observation.Success {
			result.Succeeded++
			counter.Succeeded++
		} else {
			result.Failed++
			counter.Failed++
		}
	}
	result.SuccessRate = ratio(result.Succeeded, result.Attempts, result.Attempts, result.Attempts)
	for name, counter := range byDimension {
		result.ByDimension = append(result.ByDimension, RemediationDimensionAggregate{
			Dimension: name, Attempts: counter.Attempts, Succeeded: counter.Succeeded, Failed: counter.Failed,
			SuccessRate: ratio(counter.Succeeded, counter.Attempts, counter.Attempts, counter.Attempts),
		})
	}
	sort.Slice(result.ByDimension, func(i, j int) bool {
		return orderingKey(result.ByDimension[i].Dimension) < orderingKey(result.ByDimension[j].Dimension)
	})
	return result
}

type remediationCounter struct {
	Attempts, Succeeded, Failed int64
}

func remediationCounterFor(counters map[string]*remediationCounter, key string) *remediationCounter {
	counter := counters[key]
	if counter == nil {
		counter = &remediationCounter{}
		counters[key] = counter
	}
	return counter
}
func remediationAfter(candidate, current RemediationObservation) bool {
	if !candidate.At.Equal(current.At) {
		return candidate.At.After(current.At)
	}
	if candidate.Success != current.Success {
		return candidate.Success
	}
	return strings.Join([]string{candidate.Target, candidate.Dimension}, "\x00") > strings.Join([]string{current.Target, current.Dimension}, "\x00")
}
