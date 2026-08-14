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
		if finding.Fingerprint == "" {
			finding.Fingerprint = Fingerprint(finding)
		}
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
	if severityRank(finding.Severity) > severityRank(merged.Severity) {
		merged.Severity = finding.Severity
	}
	merged.EvidenceSet = &FindingEvidenceSet{Values: append(findingEvidences(merged), findingEvidences(finding)...)}
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
	if len(byProducer) < 2 {
		return finding.Confidence
	}
	confidence := 1.0
	for _, producerConfidence := range byProducer {
		confidence *= 1 - math.Max(0, math.Min(1, producerConfidence))
	}
	return 1 - confidence
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
