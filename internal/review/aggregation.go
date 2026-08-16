package review

import (
	"math"
	"strings"
	"unicode"
)

const defaultDescriptionSimilarityThreshold = 0.7

// aggregateFindings collapses exact fingerprints and cross-dimension reports
// that identify the same symbol in overlapping source ranges.
func aggregateFindings(findings []Hallazgo, threshold float64) []Hallazgo {
	if len(findings) == 0 {
		return nil
	}
	if threshold <= 0 || threshold > 1 {
		threshold = defaultDescriptionSimilarityThreshold
	}

	aggregated := make([]Hallazgo, 0, len(findings))
	exact := make(map[string]int, len(findings))
	for _, finding := range findings {
		if finding.Status == StatusRefuted {
			continue
		}
		finding.Fingerprint = Fingerprint(finding)
		fingerprint := finding.Fingerprint
		if index, ok := exact[fingerprint]; ok {
			aggregated[index] = mergeFindings(aggregated[index], finding)
			continue
		}

		index := -1
		for i := range aggregated {
			if areProximateFindings(aggregated[i], finding, threshold) {
				index = i
				break
			}
		}
		if index < 0 {
			finding.EvidenceSet = &FindingEvidenceSet{Values: findingEvidences(finding)}
			aggregated = append(aggregated, finding)
			exact[fingerprint] = len(aggregated) - 1
			continue
		}

		aggregated[index] = mergeFindings(aggregated[index], finding)
		exact[fingerprint] = index
	}
	for i := range aggregated {
		aggregated[i].Confidence = corroboratedConfidence(aggregated[i])
	}
	return aggregated
}

func mergeFindings(merged, finding Hallazgo) Hallazgo {
	evidences := append(findingEvidences(merged), findingEvidences(finding)...)
	if severityRank(finding.Severity) > severityRank(merged.Severity) {
		finding.EvidenceSet = &FindingEvidenceSet{Values: evidences}
		return finding
	}
	merged.EvidenceSet = &FindingEvidenceSet{Values: evidences}
	return merged
}

func findingEvidences(finding Hallazgo) []FindingEvidence {
	if finding.EvidenceSet != nil {
		return append([]FindingEvidence(nil), finding.EvidenceSet.Values...)
	}
	return []FindingEvidence{{
		Dimension:  finding.Dimension,
		Producer:   finding.Producer,
		Evidence:   finding.Evidence,
		Confidence: finding.Confidence,
	}}
}

func areProximateFindings(left, right Hallazgo, threshold float64) bool {
	if left.Location.Archivo != right.Location.Archivo || left.Location.Simbolo == "" || left.Location.Simbolo != right.Location.Simbolo {
		return false
	}
	if !sourceRangesOverlap(left.Location, right.Location) {
		return false
	}
	return descriptionSimilarity(left.Description, right.Description) > threshold
}

func sourceRangesOverlap(left, right Ubicacion) bool {
	if left.LineaInicio <= 0 || right.LineaInicio <= 0 {
		return false
	}
	leftEnd := left.LineaFin
	if leftEnd == 0 {
		leftEnd = left.LineaInicio
	}
	rightEnd := right.LineaFin
	if rightEnd == 0 {
		rightEnd = right.LineaInicio
	}
	return left.LineaInicio <= rightEnd && right.LineaInicio <= leftEnd
}

func descriptionSimilarity(left, right string) float64 {
	leftWords := descriptionWords(left)
	rightWords := descriptionWords(right)
	if len(leftWords) == 0 || len(rightWords) == 0 {
		return 0
	}
	intersection := 0
	for word := range leftWords {
		if rightWords[word] {
			intersection++
		}
	}
	return float64(intersection) / float64(len(leftWords)+len(rightWords)-intersection)
}

func descriptionWords(description string) map[string]bool {
	words := make(map[string]bool)
	for _, word := range strings.FieldsFunc(strings.ToLower(description), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	}) {
		words[word] = true
	}
	return words
}

func corroboratedConfidence(finding Hallazgo) float64 {
	byProducer := make(map[Productor]float64)
	for _, evidence := range findingEvidences(finding) {
		if evidence.Producer.Agente == "" && evidence.Producer.Binario == "" {
			continue
		}
		if evidence.Confidence > byProducer[evidence.Producer] {
			byProducer[evidence.Producer] = evidence.Confidence
		}
	}
	if len(byProducer) == 0 {
		return finding.Confidence
	}
	if len(byProducer) == 1 {
		for _, confidence := range byProducer {
			return confidence
		}
	}
	confidence := 1.0
	for _, producerConfidence := range byProducer {
		confidence *= 1 - math.Max(0, math.Min(1, producerConfidence))
	}
	return 1 - confidence
}

// CauseGroup groups distinct findings whose descriptions point to the same
// underlying symptom, without merging or discarding any of them. Cause is a
// representative description for the group (see dominantCause), not a
// verified root cause. Unlike aggregateFindings/mergeFindings, this is a
// non-destructive view: every finding in Effects survives intact in
// resultado.Findings, this only groups references to them.
type CauseGroup struct {
	Cause   string
	Effects []Hallazgo
}

// correlateFindingsByCause groups findings whose descriptions describe the
// same underlying symptom (e.g. the same broken test) regardless of where
// each one was reported. It is deliberately location-agnostic, unlike
// areProximateFindings: that is what lets it catch correlated effects across
// different files and symbols that aggregateFindings's proximity check can
// never merge. Grouping is transitive: findings form a group whenever they
// are connected through a chain of pairwise-similar descriptions (connected
// components over descriptionSimilarity, computed with union-find), not only
// when each one is directly similar to a single anchor finding. Groups with a
// single member are dropped.
func correlateFindingsByCause(findings []Hallazgo, threshold float64) []CauseGroup {
	if len(findings) == 0 {
		return nil
	}
	if threshold <= 0 || threshold > 1 {
		threshold = defaultDescriptionSimilarityThreshold
	}

	parent := make([]int, len(findings))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	union := func(i, j int) {
		ri, rj := find(i), find(j)
		if ri != rj {
			parent[rj] = ri
		}
	}

	for i := range findings {
		for j := i + 1; j < len(findings); j++ {
			if descriptionSimilarity(findings[i].Description, findings[j].Description) > threshold {
				union(i, j)
			}
		}
	}

	var rootOrder []int
	membersByRoot := make(map[int][]Hallazgo, len(findings))
	for i := range findings {
		root := find(i)
		if _, seen := membersByRoot[root]; !seen {
			rootOrder = append(rootOrder, root)
		}
		membersByRoot[root] = append(membersByRoot[root], findings[i])
	}

	var groups []CauseGroup
	for _, root := range rootOrder {
		group := membersByRoot[root]
		if len(group) > 1 {
			groups = append(groups, CauseGroup{Cause: dominantCause(group), Effects: group})
		}
	}
	return groups
}

// dominantCause returns the Description of the group's medoid: the member
// with the highest total description similarity to the rest of the group.
// Picking by raw Confidence instead would let a transitive-chain endpoint
// label the whole group even when it shares nothing with the opposite
// endpoint. Ties are broken by highest Confidence, then by first
// encountered. Requires a non-empty group.
func dominantCause(group []Hallazgo) string {
	scoreOf := func(i int) float64 {
		score := 0.0
		for j := range group {
			if i != j {
				score += descriptionSimilarity(group[i].Description, group[j].Description)
			}
		}
		return score
	}

	best := 0
	bestScore := scoreOf(0)
	terms := len(group) - 1
	for i := 1; i < len(group); i++ {
		score := scoreOf(i)
		// Only the strict branch ever raises bestScore, so it always holds
		// the true running maximum: also raising it from the tie branch
		// would let it drift toward whichever member the tie-break last
		// picked instead. See scoresTie for why terms is passed through.
		tied := scoresTie(score, bestScore, terms)
		if score > bestScore && !tied {
			bestScore = score
			best = i
		} else if tied && group[i].Confidence > group[best].Confidence {
			best = i
		}
	}
	return group[best].Description
}

// scoresTie reports whether a and b, each the sum of terms values, are
// within floating-point summation noise of each other. Different members
// sum the same underlying similarities in a different order (each skips its
// own index), and float addition is not associative, so a mathematically
// equal total can come out a few ULPs apart; naive summation error grows
// roughly linearly with terms, so the tolerance scales with it too — a fixed
// tolerance would either be too tight for large groups (masking a real tie
// as a difference) or too loose for long descriptions (masking a real
// difference as a tie). descriptionSimilarity sums are rational numbers
// whose denominators are bounded by description word-set sizes, so for the
// group sizes and description lengths real findings actually produce, two
// mathematically distinct sums stay separated by far more than this noise
// floor. terms is clamped to at least 1 so a caller passing 0 or a negative
// value (which should not happen, but this stays correct either way) never
// collapses or inverts the tolerance.
func scoresTie(a, b float64, terms int) bool {
	if a == b {
		return true
	}
	if terms < 1 {
		terms = 1
	}
	const ulpsPerTerm = 4
	scale := math.Max(math.Abs(a), math.Abs(b))
	ulp := math.Nextafter(scale, math.Inf(1)) - scale
	return math.Abs(a-b) <= ulp*float64(terms)*ulpsPerTerm
}

func severityRank(severity string) int {
	switch severity {
	case SevCritical:
		return 3
	case SevWarning:
		return 2
	case SevAdvisory:
		return 1
	default:
		return 0
	}
}
