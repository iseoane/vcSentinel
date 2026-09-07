package review

import (
	"math"
	"strings"
	"unicode"
)

const defaultDescriptionSimilarityThreshold = 0.7

// aggregateFindings collapses exact fingerprints and cross-dimension reports
// that identify the same symbol in overlapping source ranges.
func aggregateFindings(findings []Finding, threshold float64) []Finding {
	if len(findings) == 0 {
		return nil
	}
	if threshold <= 0 || threshold > 1 {
		threshold = defaultDescriptionSimilarityThreshold
	}

	aggregated := make([]Finding, 0, len(findings))
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

func mergeFindings(merged, finding Finding) Finding {
	evidences := append(findingEvidences(merged), findingEvidences(finding)...)
	if severityRank(finding.Severity) > severityRank(merged.Severity) {
		finding.EvidenceSet = &FindingEvidenceSet{Values: evidences}
		return finding
	}
	merged.EvidenceSet = &FindingEvidenceSet{Values: evidences}
	return merged
}

func findingEvidences(finding Finding) []FindingEvidence {
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

func areProximateFindings(left, right Finding, threshold float64) bool {
	if left.Location.File != right.Location.File || left.Location.Simbolo == "" || left.Location.Simbolo != right.Location.Simbolo {
		return false
	}
	if !sourceRangesOverlap(left.Location, right.Location) {
		return false
	}
	return descriptionSimilarity(left.Description, right.Description) > threshold
}

func sourceRangesOverlap(left, right Location) bool {
	if left.LineStart <= 0 || right.LineStart <= 0 {
		return false
	}
	leftEnd := left.LineEnd
	if leftEnd == 0 {
		leftEnd = left.LineStart
	}
	rightEnd := right.LineEnd
	if rightEnd == 0 {
		rightEnd = right.LineStart
	}
	return left.LineStart <= rightEnd && right.LineStart <= leftEnd
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

func corroboratedConfidence(finding Finding) float64 {
	byProducer := make(map[Producer]float64)
	for _, evidence := range findingEvidences(finding) {
		if evidence.Producer.Agent == "" && evidence.Producer.Binary == "" {
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
// result.Findings, this only groups references to them.
type CauseGroup struct {
	Cause   string
	Effects []Finding
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
func correlateFindingsByCause(findings []Finding, threshold float64) []CauseGroup {
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
	membersByRoot := make(map[int][]Finding, len(findings))
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
func dominantCause(group []Finding) string {
	scores := make([]float64, len(group))
	confidences := make([]float64, len(group))
	for i := range group {
		confidences[i] = group[i].Confidence
		for j := range group {
			if i != j {
				scores[i] += descriptionSimilarity(group[i].Description, group[j].Description)
			}
		}
	}
	return group[selectDominant(scores, confidences, len(group)-1)].Description
}

// selectDominant returns the index of the highest-scoring member, breaking
// ties by highest confidence, then by first encountered. terms is the
// number of values each score sums (see scoresTie). bestScore always holds
// the true maximum score seen so far, even across a tie-break switch: the
// tie branch also raises it whenever the tied candidate's own score is
// higher (never lower), keeping later comparisons anchored to the real
// maximum instead of drifting toward whichever member the tie-break last
// picked. Requires len(scores) >= 1 and len(confidences) >= len(scores).
func selectDominant(scores, confidences []float64, terms int) int {
	best := 0
	bestScore := scores[0]
	for i := 1; i < len(scores); i++ {
		tied := scoresTie(scores[i], bestScore, terms)
		if scores[i] > bestScore && !tied {
			bestScore = scores[i]
			best = i
		} else if tied {
			if scores[i] > bestScore {
				bestScore = scores[i]
			}
			if confidences[i] > confidences[best] {
				best = i
			}
		}
	}
	return best
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
