package review

import "testing"

func TestAggregateFindingsCollapsesExactFingerprints(t *testing.T) {
	first := Hallazgo{
		Dimension:  DimLogic,
		Severity:   SevWarning,
		Confidence: 0.6,
		Title:      "unchecked error",
		Evidence:   "if err != nil { return }",
		Location:   Ubicacion{Archivo: "config.go", LineaInicio: 12, Simbolo: "parseConfig"},
		Producer:   Productor{Agente: "logic-reviewer"},
	}
	first.Fingerprint = Fingerprint(first)
	second := first
	second.Severity = SevCritical
	second.Producer = Productor{Agente: "retry-reviewer"}

	aggregated := aggregateFindings([]Hallazgo{first, second}, defaultDescriptionSimilarityThreshold)
	if len(aggregated) != 1 {
		t.Fatalf("aggregated findings = %d, expected 1", len(aggregated))
	}
	if got := aggregated[0]; got.Severity != SevCritical || got.EvidenceSet == nil || len(got.EvidenceSet.Values) != 2 {
		t.Errorf("aggregated finding = %#v, expected CRITICAL severity and 2 evidences", got)
	}
}
