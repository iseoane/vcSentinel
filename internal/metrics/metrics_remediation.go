package metrics

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

func aggregateRemediations(observations []RemediationObservation) RemediationAggregate {
	selected := make(map[string]RemediationObservation, len(observations))
	for _, observation := range observations {
		key := strings.TrimSpace(observation.LogicalID)
		if key == "" {
			key = fmt.Sprintf("anonymous:%s:%s:%s:%t", observation.Target, observation.Dimension, observation.At.UTC().Format(time.RFC3339Nano), observation.Success)
		}
		if current, exists := selected[key]; !exists || remediationAfter(observation, current) {
			selected[key] = observation
		}
	}
	result := RemediationAggregate{}
	byDimension := make(map[string]*remediationCounter)
	keys := make([]string, 0, len(selected))
	for key := range selected {
		keys = append(keys, key)
	}
	sort.Strings(keys)
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
