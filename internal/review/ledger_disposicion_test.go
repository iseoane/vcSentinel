package review

import "testing"

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

func statusesByDescription(findings []Hallazgo) map[string]string {
	statuses := make(map[string]string, len(findings))
	for _, finding := range findings {
		statuses[finding.Description] = finding.Status
	}
	return statuses
}

func TestFindingsWithDispositionsRestoresConfirmedOntoAggregate(t *testing.T) {
	revision := Revision{
		Dims: []DimensionResult{{Dim: DimLogic, Findings: []ReviewFinding{
			rawFinding("a.go", 10, SevCritical, "nil dereference", StatusConfirmed),
			rawFinding("b.go", 20, SevWarning, "unclear name", ""),
		}}},
		AggregatedFindings: []Hallazgo{
			aggregatedFinding(DimLogic, "a.go", 10, SevCritical, "nil dereference"),
			aggregatedFinding(DimLogic, "b.go", 20, SevWarning, "unclear name"),
		},
	}
	findings := revision.FindingsWithDispositions()
	if len(findings) != 2 {
		t.Fatalf("findings = %d, want 2: %#v", len(findings), findings)
	}
	statuses := statusesByDescription(findings)
	if statuses["nil dereference"] != StatusConfirmed {
		t.Fatalf("confirmed disposition = %q, want %q", statuses["nil dereference"], StatusConfirmed)
	}
	if statuses["unclear name"] != "" {
		t.Fatalf("finding without a recorded disposition = %q, want unknown", statuses["unclear name"])
	}
	if revision.AggregatedFindings[0].Status != "" {
		t.Fatal("the persisted aggregated finding was mutated in place")
	}
}

func TestFindingsWithDispositionsAppendsRefutedWithoutCounterpart(t *testing.T) {
	revision := Revision{
		Dims: []DimensionResult{{Dim: DimSecurity, Findings: []ReviewFinding{
			rawFinding("a.go", 10, SevCritical, "injected query", StatusRefuted),
			rawFinding("a.go", 30, SevCritical, "unchecked input", StatusConfirmed),
		}}},
		AggregatedFindings: []Hallazgo{
			aggregatedFinding(DimSecurity, "a.go", 30, SevCritical, "unchecked input"),
		},
	}
	findings := revision.FindingsWithDispositions()
	if len(findings) != 2 {
		t.Fatalf("findings = %d, want 2: %#v", len(findings), findings)
	}
	statuses := statusesByDescription(findings)
	if statuses["injected query"] != StatusRefuted {
		t.Fatalf("refuted disposition = %q, want %q", statuses["injected query"], StatusRefuted)
	}
	if statuses["unchecked input"] != StatusConfirmed {
		t.Fatalf("confirmed disposition = %q, want %q", statuses["unchecked input"], StatusConfirmed)
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

func TestFindingsWithDispositionsFallsBackToRawDimsWithTheirStatus(t *testing.T) {
	revision := Revision{
		Dims: []DimensionResult{{Dim: DimTests, Findings: []ReviewFinding{
			rawFinding("a_test.go", 5, SevCritical, "missing coverage", StatusConfirmed),
			rawFinding("b_test.go", 7, SevWarning, "weak assertion", ""),
		}}},
	}
	findings := revision.FindingsWithDispositions()
	if len(findings) != 2 {
		t.Fatalf("findings = %d, want 2: %#v", len(findings), findings)
	}
	statuses := statusesByDescription(findings)
	if statuses["missing coverage"] != StatusConfirmed || statuses["weak assertion"] != "" {
		t.Fatalf("fallback statuses = %#v", statuses)
	}
	if findings[0].Dimension != DimTests {
		t.Fatalf("dimension = %q, want %q", findings[0].Dimension, DimTests)
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
