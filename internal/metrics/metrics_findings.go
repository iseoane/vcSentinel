package metrics

import (
	"sort"
	"strconv"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

func aggregateFindings(observations []FindingObservation, decisions []store.Decision) FindingsAggregate {
	byFingerprint := make(map[string]FindingObservation, len(observations))
	for _, observation := range observations {
		if observation.Superseded || strings.EqualFold(strings.TrimSpace(observation.Finding.Status), "superseded") {
			continue
		}
		key := findingFingerprint(observation)
		if current, exists := byFingerprint[key]; !exists || findingObservationAfter(observation, current) {
			byFingerprint[key] = observation
		}
	}

	overrides := make(map[string]struct{})
	for _, decision := range decisions {
		fingerprint := strings.TrimSpace(decision.Fingerprint)
		if fingerprint != "" && isUserOverride(decision.Decision) {
			overrides[fingerprint] = struct{}{}
		}
	}

	keys := make([]string, 0, len(byFingerprint))
	for key := range byFingerprint {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := FindingsAggregate{}
	// known counts the observations whose Status attribute is present at all,
	// effectiveKnown the same within the non-refuted population, and
	// overrideObservable the non-refuted observations whose override attribute
	// could be resolved. Each is the coverage basis of the rate it feeds; none
	// may be replaced by that rate's denominator.
	var known, effectiveKnown, overrideObservable int64
	dimensions := make(map[string]*findingCounter)
	models := make(map[string]*findingCounter)
	agents := make(map[string]*findingCounter)
	for _, key := range keys {
		observation := byFingerprint[key]
		finding := observation.Finding
		status := strings.ToLower(strings.TrimSpace(finding.Status))
		dimension := displayDimension(finding.Dimension)
		model := displayIdentity(finding.Producer.Modelo)
		agent := displayIdentity(finding.Producer.Agente)
		override := status != review.StatusRefuted && (status == review.StatusAcceptedByUser || hasFingerprintOverride(overrides, key, finding))
		knownStatus := status != ""

		result.Observed++
		if knownStatus {
			known++
			if status == review.StatusRefuted {
				result.Refuted++
			}
			if status == review.StatusConfirmed || status == review.StatusFixed || status == review.StatusReopened {
				result.Confirmed++
			}
		}
		if override {
			result.Overrides++
		}
		// A recorded reopen is the only answer that resolves the attribute
		// today, so the two counters move together here. They are separate
		// because a producer that records "examined, not reopened" increments
		// ReopenResolved alone, which is what lets a fully answered population
		// report a measured zero instead of no evidence.
		if status == review.StatusReopened {
			result.Reopened++
			result.ReopenResolved++
		}

		dim := counterFor(dimensions, dimension)
		modelCounter := counterFor(models, model)
		agentCounter := counterFor(agents, agent)
		for _, counter := range []*findingCounter{dim, modelCounter, agentCounter} {
			counter.Observed++
			if !knownStatus {
				continue
			}
			counter.Known++
			if status == review.StatusRefuted {
				counter.Refuted++
			}
			if status == review.StatusConfirmed || status == review.StatusFixed || status == review.StatusReopened {
				counter.Confirmed++
			}
		}
		if override {
			dim.Overrides++
		}
		if status == review.StatusReopened {
			dim.Reopened++
			dim.ReopenResolved++
		}

		if status != review.StatusRefuted {
			result.Effective++
			overrideObservable++
			dim.Effective++
			dim.OverrideObservable++
			if knownStatus {
				effectiveKnown++
				dim.EffectiveKnown++
			}
		}
	}

	// Confirmation and refutation both read finding.Status, so their coverage is
	// the population that carries a status at all. Override reads store.Decision
	// records instead: metrics_reader.go propagates a LeerDecisiones failure
	// rather than returning an empty set, so the decisions ledger is either
	// complete or the whole report fails. Its attribute is therefore genuinely
	// observable for every member of the effective population, and
	// overrideObservable records that per observation rather than assuming it.
	result.ConfirmationRate = ratio(result.Confirmed, result.Effective, effectiveKnown, result.Effective)
	result.RefutationRate = ratio(result.Refuted, result.Observed, known, result.Observed)
	result.OverrideRate = ratio(result.Overrides, result.Effective, overrideObservable, result.Effective)
	result.ByDimension = make([]DimensionAggregate, 0, len(dimensions))
	for name, counter := range dimensions {
		active := counter.Effective
		row := DimensionAggregate{
			Dimension: name, Findings: active, Observed: counter.Observed,
			Confirmed: counter.Confirmed, Refuted: counter.Refuted,
			Overrides: counter.Overrides, Reopened: counter.Reopened, ReopenResolved: counter.ReopenResolved,
			ConfirmationRate: ratio(counter.Confirmed, active, counter.EffectiveKnown, active),
			RefutationRate:   ratio(counter.Refuted, counter.Observed, counter.Known, counter.Observed),
			OverrideRate:     ratio(counter.Overrides, active, counter.OverrideObservable, active),
		}
		result.ByDimension = append(result.ByDimension, row)
	}
	sort.Slice(result.ByDimension, func(i, j int) bool {
		return orderingKey(result.ByDimension[i].Dimension) < orderingKey(result.ByDimension[j].Dimension)
	})

	result.ByModel = make([]ModelAggregate, 0, len(models))
	for name, counter := range models {
		result.ByModel = append(result.ByModel, ModelAggregate{
			Model: name, Observed: counter.Observed, Confirmed: counter.Confirmed,
			Refuted:        counter.Refuted,
			RefutationRate: ratio(counter.Refuted, counter.Observed, counter.Known, counter.Observed),
		})
	}
	sort.Slice(result.ByModel, func(i, j int) bool {
		return orderingKey(result.ByModel[i].Model) < orderingKey(result.ByModel[j].Model)
	})
	result.ByAgent = make([]AgentAggregate, 0, len(agents))
	for name, counter := range agents {
		result.ByAgent = append(result.ByAgent, AgentAggregate{
			Agent: name, Observed: counter.Observed, Confirmed: counter.Confirmed,
			Refuted:        counter.Refuted,
			RefutationRate: ratio(counter.Refuted, counter.Observed, counter.Known, counter.Observed),
		})
	}
	sort.Slice(result.ByAgent, func(i, j int) bool {
		return orderingKey(result.ByAgent[i].Agent) < orderingKey(result.ByAgent[j].Agent)
	})
	return result
}

type findingCounter struct {
	Observed           int64
	Known              int64
	Effective          int64
	EffectiveKnown     int64
	OverrideObservable int64
	Confirmed          int64
	Refuted            int64
	Overrides          int64
	Reopened           int64
	ReopenResolved     int64
}

func counterFor(counters map[string]*findingCounter, key string) *findingCounter {
	counter := counters[key]
	if counter == nil {
		counter = &findingCounter{}
		counters[key] = counter
	}
	return counter
}
func findingFingerprint(observation FindingObservation) string {
	if value := strings.TrimSpace(observation.Fingerprint); value != "" {
		return value
	}
	if value := strings.TrimSpace(observation.Finding.Fingerprint); value != "" {
		return value
	}
	return strings.TrimSpace(review.Fingerprint(observation.Finding))
}

func findingObservationAfter(candidate, current FindingObservation) bool {
	if !candidate.At.Equal(current.At) {
		return candidate.At.After(current.At)
	}
	if candidate.Revision != current.Revision {
		return candidate.Revision > current.Revision
	}
	candidateOrigin := originRank(candidate.Origin)
	currentOrigin := originRank(current.Origin)
	if candidateOrigin != currentOrigin {
		return candidateOrigin > currentOrigin
	}
	// Recorded evidence beats its absence. The same finding can reach the
	// reader through the aggregated set, which carries no lifecycle status,
	// and through the raw per-dimension set that recorded one (FU-7); on a
	// genuine tie the observation that answers the attribute must win. Stated
	// here rather than left to findingSortKey, which happens to compare
	// Status as a string and would resolve it only by accident.
	candidateDisposed := strings.TrimSpace(candidate.Finding.Status) != ""
	currentDisposed := strings.TrimSpace(current.Finding.Status) != ""
	if candidateDisposed != currentDisposed {
		return candidateDisposed
	}
	return findingSortKey(candidate) > findingSortKey(current)
}

func findingSortKey(observation FindingObservation) string {
	finding := observation.Finding
	return strings.Join([]string{observation.Commit, strconv.Itoa(observation.Revision), observation.Origin, finding.Dimension, finding.Status, finding.Producer.Modelo, finding.Producer.Agente, finding.Description, finding.Title}, "\x00")
}

func originRank(origin string) int {
	if strings.EqualFold(origin, "store") {
		return 2
	}
	if strings.EqualFold(origin, "ledger") {
		return 1
	}
	return 0
}

func hasFingerprintOverride(overrides map[string]struct{}, key string, finding review.Hallazgo) bool {
	if _, ok := overrides[strings.TrimSpace(key)]; ok {
		return true
	}
	if fingerprint := strings.TrimSpace(finding.Fingerprint); fingerprint != "" {
		_, ok := overrides[fingerprint]
		return ok
	}
	return false
}

func isUserOverride(decision string) bool {
	switch strings.ToLower(strings.TrimSpace(decision)) {
	case "accept", "accepted", "accepted_by_user", "override", "overridden", "reject", "rejected", "rejected_by_user":
		return true
	default:
		return false
	}
}

func displayDimension(value string) string {
	if strings.TrimSpace(value) == "" {
		return unknownLabel
	}
	return value
}

func displayIdentity(value string) string {
	if strings.TrimSpace(value) == "" {
		return unknownLabel
	}
	return value
}

func orderingKey(value string) string {
	if value == unknownLabel {
		return "\xff" + value
	}
	return value
}
