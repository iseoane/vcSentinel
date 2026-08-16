package review

import (
	"math"
	"testing"
)

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

func TestAggregateFindingsRecomputesFingerprintFromCanonicalFields(t *testing.T) {
	first := Hallazgo{
		Dimension: DimLogic, Title: "unchecked error", Evidence: "if err != nil { return }",
		Location:    Ubicacion{Archivo: "config.go", LineaInicio: 12, Simbolo: "parseConfig"},
		Fingerprint: "untrusted-first",
	}
	second := first
	second.Fingerprint = "untrusted-second"

	aggregated := aggregateFindings([]Hallazgo{first, second}, defaultDescriptionSimilarityThreshold)
	if len(aggregated) != 1 {
		t.Fatalf("aggregated findings = %d, expected 1", len(aggregated))
	}
	if got, want := aggregated[0].Fingerprint, Fingerprint(first); got != want {
		t.Errorf("fingerprint = %q, expected canonical %q", got, want)
	}
}

func TestAggregateFindingsUsesHighestSeverityAsCanonicalFinding(t *testing.T) {
	first := Hallazgo{
		Dimension: DimLogic, Severity: SevWarning, Confidence: 0.6,
		Description: "ignored parse error permits invalid configuration", Evidence: "return nil",
		Location: Ubicacion{Archivo: "config.go", LineaInicio: 12, LineaFin: 16, Simbolo: "parseConfig"},
		Producer: Productor{Agente: "logic-reviewer"},
	}
	highest := Hallazgo{
		Dimension: DimDesign, Severity: SevCritical, Confidence: 0.8,
		Description: "invalid configuration is permitted after ignored parse error", Evidence: "return without handling the parse error",
		Location: Ubicacion{Archivo: "config.go", LineaInicio: 14, LineaFin: 18, Simbolo: "parseConfig"},
		Producer: Productor{Agente: "design-reviewer"},
	}

	aggregated := aggregateFindings([]Hallazgo{first, highest}, 0.3)
	if len(aggregated) != 1 {
		t.Fatalf("aggregated findings = %d, expected 1", len(aggregated))
	}
	got := aggregated[0]
	if got.Severity != highest.Severity || got.Description != highest.Description || got.Location != highest.Location || got.Evidence != highest.Evidence {
		t.Errorf("canonical finding = %#v, expected highest severity finding %#v", got, highest)
	}
	if got.EvidenceSet == nil || len(got.EvidenceSet.Values) != 2 {
		t.Errorf("evidence set = %#v, expected both reports", got.EvidenceSet)
	}
}

func TestAggregateFindingsExcludesRefutedFindings(t *testing.T) {
	refuted := Hallazgo{Status: StatusRefuted, Fingerprint: "ignored"}
	if aggregated := aggregateFindings([]Hallazgo{refuted}, defaultDescriptionSimilarityThreshold); len(aggregated) != 0 {
		t.Errorf("aggregated findings = %#v, expected refuted finding to be excluded", aggregated)
	}
}

func TestAreProximateFindingsRejectsDistinctLocationsAndBoundarySimilarity(t *testing.T) {
	base := Hallazgo{
		Description: "ignored parse error", Location: Ubicacion{Archivo: "config.go", LineaInicio: 12, LineaFin: 16, Simbolo: "parseConfig"},
	}
	for _, tc := range []struct {
		name      string
		other     Hallazgo
		threshold float64
	}{
		{"different file", Hallazgo{Description: base.Description, Location: Ubicacion{Archivo: "other.go", LineaInicio: 12, LineaFin: 16, Simbolo: "parseConfig"}}, 0.5},
		{"different symbol", Hallazgo{Description: base.Description, Location: Ubicacion{Archivo: "config.go", LineaInicio: 12, LineaFin: 16, Simbolo: "other"}}, 0.5},
		{"non-overlapping range", Hallazgo{Description: base.Description, Location: Ubicacion{Archivo: "config.go", LineaInicio: 17, LineaFin: 20, Simbolo: "parseConfig"}}, 0.5},
		{"similarity at threshold", Hallazgo{Description: "ignored validation error", Location: base.Location}, 0.5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if areProximateFindings(base, tc.other, tc.threshold) {
				t.Errorf("findings must not be proximate")
			}
		})
	}
}

func TestCorrelateFindingsByCauseGroupsDistinctLocationsSharingRootCause(t *testing.T) {
	findings := []Hallazgo{
		{Description: "session cache race condition breaks TestUserLogin", Confidence: 0.5, Location: Ubicacion{Archivo: "session.go", Simbolo: "acquireSession"}},
		{Description: "TestUserLogin breaks because of session cache race condition", Confidence: 0.9, Location: Ubicacion{Archivo: "cache.go", Simbolo: "cacheGet"}},
		{Description: "race condition in session cache breaks TestUserLogin intermittently", Confidence: 0.4, Location: Ubicacion{Archivo: "login_test.go", Simbolo: "TestUserLogin"}},
		{Description: "TestUserLogin intermittently fails from session cache race condition", Confidence: 0.6, Location: Ubicacion{Archivo: "runner.go", Simbolo: "runSuite"}},
		{Description: "the session cache race condition is why TestUserLogin breaks", Confidence: 0.3, Location: Ubicacion{Archivo: "harness.go", Simbolo: "setupHarness"}},
	}

	groups := correlateFindingsByCause(findings, 0.4)
	if len(groups) != 1 {
		t.Fatalf("groups = %d, expected 1: %#v", len(groups), groups)
	}
	group := groups[0]
	if len(group.Effects) != 5 {
		t.Fatalf("effects = %d, expected 5: %#v", len(group.Effects), group.Effects)
	}
	if group.Cause != findings[1].Description {
		t.Errorf("cause = %q, expected highest-confidence description %q", group.Cause, findings[1].Description)
	}
}

func TestCorrelateFindingsByCauseDropsUnrelatedFindings(t *testing.T) {
	for _, tc := range []struct {
		name      string
		findings  []Hallazgo
		threshold float64
	}{
		{
			name: "unrelated descriptions",
			findings: []Hallazgo{
				{Description: "unchecked error permits invalid configuration", Confidence: 0.5, Location: Ubicacion{Archivo: "config.go", Simbolo: "parseConfig"}},
				{Description: "SQL injection via unsanitized user input in login handler", Confidence: 0.6, Location: Ubicacion{Archivo: "auth.go", Simbolo: "handleLogin"}},
				{Description: "goroutine leak in background worker pool", Confidence: 0.7, Location: Ubicacion{Archivo: "worker.go", Simbolo: "startPool"}},
			},
			threshold: 0.3,
		},
		{
			name: "similarity at threshold",
			findings: []Hallazgo{
				{Description: "ignored parse error", Confidence: 0.5, Location: Ubicacion{Archivo: "config.go", Simbolo: "parseConfig"}},
				{Description: "ignored validation error", Confidence: 0.6, Location: Ubicacion{Archivo: "validate.go", Simbolo: "validate"}},
			},
			threshold: 0.5,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if groups := correlateFindingsByCause(tc.findings, tc.threshold); len(groups) != 0 {
				t.Errorf("groups = %#v, expected no correlated causes", groups)
			}
		})
	}
}

func TestCorroboratedConfidenceUsesIndependentProducersAndRepeatedMaximum(t *testing.T) {
	for _, tc := range []struct {
		name     string
		finding  Hallazgo
		expected float64
	}{
		{
			name: "independent producers combine confidence",
			finding: Hallazgo{EvidenceSet: &FindingEvidenceSet{Values: []FindingEvidence{
				{Producer: Productor{Agente: "one"}, Confidence: 0.6},
				{Producer: Productor{Agente: "two"}, Confidence: 0.7},
			}}},
			expected: 0.88,
		},
		{
			name: "repeated producer uses maximum confidence",
			finding: Hallazgo{EvidenceSet: &FindingEvidenceSet{Values: []FindingEvidence{
				{Producer: Productor{Agente: "one"}, Confidence: 0.6},
				{Producer: Productor{Agente: "one"}, Confidence: 0.7},
			}}},
			expected: 0.7,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := corroboratedConfidence(tc.finding); math.Abs(got-tc.expected) > 1e-9 {
				t.Errorf("confidence = %v, expected %v", got, tc.expected)
			}
		})
	}
}
