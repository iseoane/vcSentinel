package review

import (
	"fmt"
	"testing"
)

// rawFinding builds a v1 per-dimension finding for the disposition tests.
func rawFinding(file string, line int, severity, description, status string) ReviewFinding {
	return ReviewFinding{File: file, Line: Linea(line), Severity: severity, Description: description, Status: status}
}

// aggregatedFinding builds the v2 counterpart aggregation persists, which
// never carries the disposition the raw finding recorded.
func aggregatedFinding(dimension, file string, line int, severity, description string) Hallazgo {
	return Hallazgo{
		Dimension:   dimension,
		Severity:    severity,
		Description: description,
		Location:    Ubicacion{Archivo: file, LineaInicio: line},
	}
}

// statusesByKey indexes results by the full join key rather than by
// description alone. Collapsing on description would let an implementation
// that cross-associates findings sharing a description across dimensions or
// files pass unnoticed, which is exactly the mis-attribution these tests
// exist to detect.
func statusesByKey(findings []Hallazgo) map[string]string {
	statuses := make(map[string]string, len(findings))
	for _, finding := range findings {
		key := fmt.Sprintf("%s|%s|%d|%s", finding.Dimension, finding.Location.Archivo, finding.Location.LineaInicio, finding.Description)
		statuses[key] = finding.Status
	}
	return statuses
}

func key(dimension, file string, line int, description string) string {
	return fmt.Sprintf("%s|%s|%d|%s", dimension, file, line, description)
}

func TestFindingsWithDispositionsRestoresConfirmedOntoAggregate(t *testing.T) {
	// Both dimensions report the same description on the same file and line:
	// only the logic one recorded a disposition, so associating them would be
	// a cross-dimension mis-attribution.
	revision := Revision{
		Dims: []DimensionResult{
			{Dim: DimLogic, Findings: []ReviewFinding{
				rawFinding("a.go", 10, SevCritical, "nil dereference", StatusConfirmed),
			}},
			{Dim: DimSecurity, Findings: []ReviewFinding{
				rawFinding("a.go", 10, SevCritical, "nil dereference", ""),
			}},
		},
		AggregatedFindings: []Hallazgo{
			aggregatedFinding(DimLogic, "a.go", 10, SevCritical, "nil dereference"),
			aggregatedFinding(DimSecurity, "a.go", 10, SevCritical, "nil dereference"),
		},
	}
	findings := revision.FindingsWithDispositions()
	if len(findings) != 2 {
		t.Fatalf("findings = %d, want 2: %#v", len(findings), findings)
	}
	statuses := statusesByKey(findings)
	if statuses[key(DimLogic, "a.go", 10, "nil dereference")] != StatusConfirmed {
		t.Fatalf("logic disposition = %q, want %q", statuses[key(DimLogic, "a.go", 10, "nil dereference")], StatusConfirmed)
	}
	if got := statuses[key(DimSecurity, "a.go", 10, "nil dereference")]; got != "" {
		t.Fatalf("security disposition = %q, want unknown: the disposition belongs to another dimension", got)
	}
	if revision.AggregatedFindings[0].Status != "" {
		t.Fatal("the persisted aggregated finding was mutated in place")
	}
}

func TestFindingsWithDispositionsAppendsRefutedWithoutCounterpart(t *testing.T) {
	revision := Revision{
		Dims: []DimensionResult{{Dim: DimSecurity, Findings: []ReviewFinding{
			rawFinding("a.go", 10, SevCritical, "injected query", StatusRefuted),
			rawFinding("b.go", 30, SevCritical, "unchecked input", StatusConfirmed),
		}}},
		AggregatedFindings: []Hallazgo{
			aggregatedFinding(DimSecurity, "b.go", 30, SevCritical, "unchecked input"),
		},
	}
	findings := revision.FindingsWithDispositions()
	if len(findings) != 2 {
		t.Fatalf("findings = %d, want 2: %#v", len(findings), findings)
	}
	statuses := statusesByKey(findings)
	if statuses[key(DimSecurity, "a.go", 10, "injected query")] != StatusRefuted {
		t.Fatalf("refuted disposition = %#v", statuses)
	}
	if statuses[key(DimSecurity, "b.go", 30, "unchecked input")] != StatusConfirmed {
		t.Fatalf("confirmed disposition = %#v", statuses)
	}
}

// A raw finding with no aggregated counterpart and no refutation was dropped
// by SupersedeDeterministicFindings, silently and without being marked.
// Appending it would resurrect a superseded finding as a live observation.
func TestFindingsWithDispositionsDropsUnmatchedNonRefutedRaw(t *testing.T) {
	revision := Revision{
		Dims: []DimensionResult{{Dim: DimLogic, Findings: []ReviewFinding{
			rawFinding("superseded.go", 10, SevCritical, "duplicated by gofmt", StatusConfirmed),
			rawFinding("b.go", 30, SevCritical, "unchecked input", ""),
			rawFinding("kept.go", 40, SevCritical, "real defect", StatusConfirmed),
		}}},
		AggregatedFindings: []Hallazgo{
			aggregatedFinding(DimLogic, "kept.go", 40, SevCritical, "real defect"),
		},
	}
	findings := revision.FindingsWithDispositions()
	if len(findings) != 1 {
		t.Fatalf("findings = %d, want 1: only the surviving aggregate, %#v", len(findings), findings)
	}
	if findings[0].Location.Archivo != "kept.go" || findings[0].Status != StatusConfirmed {
		t.Fatalf("finding = %#v, want the confirmed kept.go aggregate", findings[0])
	}
}

func TestFindingsWithDispositionsCountsAMatchedFindingOnce(t *testing.T) {
	revision := Revision{
		Dims: []DimensionResult{{Dim: DimLogic, Findings: []ReviewFinding{
			rawFinding("a.go", 10, SevCritical, "nil dereference", StatusRefuted),
		}}},
		AggregatedFindings: []Hallazgo{
			aggregatedFinding(DimLogic, "a.go", 10, SevCritical, "nil dereference"),
		},
	}
	findings := revision.FindingsWithDispositions()
	if len(findings) != 1 {
		t.Fatalf("findings = %d, want 1: %#v", len(findings), findings)
	}
	if findings[0].Status != StatusRefuted {
		t.Fatalf("status = %q, want %q", findings[0].Status, StatusRefuted)
	}
}

func TestFindingsWithDispositionsKeepsTheAggregateRecordedStatus(t *testing.T) {
	aggregated := aggregatedFinding(DimLogic, "a.go", 10, SevCritical, "nil dereference")
	aggregated.Status = StatusRefuted
	revision := Revision{
		Dims: []DimensionResult{{Dim: DimLogic, Findings: []ReviewFinding{
			rawFinding("a.go", 10, SevCritical, "nil dereference", StatusConfirmed),
		}}},
		AggregatedFindings: []Hallazgo{aggregated},
	}
	findings := revision.FindingsWithDispositions()
	if len(findings) != 1 || findings[0].Status != StatusRefuted {
		t.Fatalf("findings = %#v, want the single aggregate keeping %q", findings, StatusRefuted)
	}
}

func TestFindingsWithDispositionsIgnoresContradictoryRawStatuses(t *testing.T) {
	revision := Revision{
		Dims: []DimensionResult{{Dim: DimLogic, Findings: []ReviewFinding{
			rawFinding("a.go", 10, SevCritical, "nil dereference", StatusConfirmed),
			rawFinding("a.go", 10, SevCritical, "nil dereference", StatusAcceptedByUser),
		}}},
		AggregatedFindings: []Hallazgo{
			aggregatedFinding(DimLogic, "a.go", 10, SevCritical, "nil dereference"),
		},
	}
	findings := revision.FindingsWithDispositions()
	if len(findings) != 1 {
		t.Fatalf("findings = %d, want 1: %#v", len(findings), findings)
	}
	if findings[0].Status != "" {
		t.Fatalf("status = %q, want unknown for contradictory raw evidence", findings[0].Status)
	}
}

// The key omits Evidence and Title because the v1 shape has neither, so two
// aggregates can share it. Attributing the disposition to both would mark a
// finding nobody disposed of, which for a security finding reads as dismissed.
func TestFindingsWithDispositionsRefusesAmbiguousAttribution(t *testing.T) {
	first := aggregatedFinding(DimSecurity, "a.go", 10, SevCritical, "unchecked input")
	first.Title = "sql injection"
	second := aggregatedFinding(DimSecurity, "a.go", 10, SevCritical, "unchecked input")
	second.Title = "path traversal"
	revision := Revision{
		Dims: []DimensionResult{{Dim: DimSecurity, Findings: []ReviewFinding{
			rawFinding("a.go", 10, SevCritical, "unchecked input", StatusConfirmed),
		}}},
		AggregatedFindings: []Hallazgo{first, second},
	}
	findings := revision.FindingsWithDispositions()
	if len(findings) != 2 {
		t.Fatalf("findings = %d, want 2: %#v", len(findings), findings)
	}
	for _, finding := range findings {
		if finding.Status != "" {
			t.Fatalf("finding %q status = %q, want unknown: the key matched more than one aggregate", finding.Title, finding.Status)
		}
	}
}

// The same ambiguity must not let a refuted sibling reappear as a separate
// observation: it is already represented, ambiguously, among those aggregates.
func TestFindingsWithDispositionsDoesNotAppendRefutedUnderAmbiguity(t *testing.T) {
	first := aggregatedFinding(DimSecurity, "a.go", 10, SevCritical, "unchecked input")
	first.Title = "sql injection"
	second := aggregatedFinding(DimSecurity, "a.go", 10, SevCritical, "unchecked input")
	second.Title = "path traversal"
	revision := Revision{
		Dims: []DimensionResult{{Dim: DimSecurity, Findings: []ReviewFinding{
			rawFinding("a.go", 10, SevCritical, "unchecked input", StatusRefuted),
		}}},
		AggregatedFindings: []Hallazgo{first, second},
	}
	findings := revision.FindingsWithDispositions()
	if len(findings) != 2 {
		t.Fatalf("findings = %d, want 2: the refuted raw must not be observed a third time, %#v", len(findings), findings)
	}
	for _, finding := range findings {
		if finding.Status != "" {
			t.Fatalf("finding %q status = %q, want unknown", finding.Title, finding.Status)
		}
	}
}

func TestFindingsWithDispositionsNormalizesRecordedStatus(t *testing.T) {
	revision := Revision{
		Dims: []DimensionResult{{Dim: DimLogic, Findings: []ReviewFinding{
			rawFinding("a.go", 10, SevCritical, "nil dereference", "  CONFIRMED  "),
			rawFinding("b.go", 20, SevWarning, "unclear name", "   "),
		}}},
		AggregatedFindings: []Hallazgo{
			aggregatedFinding(DimLogic, "a.go", 10, SevCritical, "nil dereference"),
			aggregatedFinding(DimLogic, "b.go", 20, SevWarning, "unclear name"),
		},
	}
	statuses := statusesByKey(revision.FindingsWithDispositions())
	if got := statuses[key(DimLogic, "a.go", 10, "nil dereference")]; got != StatusConfirmed {
		t.Fatalf("status = %q, want the canonical %q", got, StatusConfirmed)
	}
	if got := statuses[key(DimLogic, "b.go", 20, "unclear name")]; got != "" {
		t.Fatalf("whitespace-only status = %q, want unknown", got)
	}
}

func TestFindingsWithDispositionsNormalizesTheAggregateOwnStatus(t *testing.T) {
	aggregated := aggregatedFinding(DimLogic, "a.go", 10, SevCritical, "nil dereference")
	aggregated.Status = "   "
	revision := Revision{
		Dims: []DimensionResult{{Dim: DimLogic, Findings: []ReviewFinding{
			rawFinding("a.go", 10, SevCritical, "nil dereference", StatusConfirmed),
		}}},
		AggregatedFindings: []Hallazgo{aggregated},
	}
	findings := revision.FindingsWithDispositions()
	if len(findings) != 1 || findings[0].Status != StatusConfirmed {
		t.Fatalf("findings = %#v, want a whitespace-only status treated as absent", findings)
	}
}

func TestFindingsWithDispositionsFallsBackToRawDimsWithTheirStatus(t *testing.T) {
	revision := Revision{
		Dims: []DimensionResult{{Dim: DimTests, Findings: []ReviewFinding{
			rawFinding("a_test.go", 5, SevCritical, "missing coverage", " Confirmed "),
			rawFinding("b_test.go", 7, SevWarning, "weak assertion", ""),
		}}},
	}
	findings := revision.FindingsWithDispositions()
	if len(findings) != 2 {
		t.Fatalf("findings = %d, want 2: %#v", len(findings), findings)
	}
	statuses := statusesByKey(findings)
	if statuses[key(DimTests, "a_test.go", 5, "missing coverage")] != StatusConfirmed {
		t.Fatalf("fallback statuses = %#v", statuses)
	}
	if statuses[key(DimTests, "b_test.go", 7, "weak assertion")] != "" {
		t.Fatalf("fallback statuses = %#v", statuses)
	}
}

func TestHallazgosEfectivosStillIgnoresDispositions(t *testing.T) {
	revision := Revision{
		Dims: []DimensionResult{{Dim: DimLogic, Findings: []ReviewFinding{
			rawFinding("a.go", 10, SevCritical, "nil dereference", StatusConfirmed),
		}}},
		AggregatedFindings: []Hallazgo{
			aggregatedFinding(DimLogic, "a.go", 10, SevCritical, "nil dereference"),
		},
	}
	findings := revision.HallazgosEfectivos()
	if len(findings) != 1 || findings[0].Status != "" {
		t.Fatalf("HallazgosEfectivos = %#v, want the gate selection unchanged", findings)
	}
}

func TestNormalizeStatus(t *testing.T) {
	cases := map[string]string{
		"  CONFIRMED  ": StatusConfirmed,
		"Refuted":       StatusRefuted,
		"   ":           "",
		"":              "",
		StatusConfirmed: StatusConfirmed,
	}
	for input, want := range cases {
		if got := NormalizeStatus(input); got != want {
			t.Fatalf("NormalizeStatus(%q) = %q, want %q", input, got, want)
		}
	}
}
