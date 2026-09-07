package review

import (
	"fmt"
	"testing"
)

// rawFinding builds a v1 per-dimension finding for the disposition tests.
func sampleFinding(file string, line int, severity, description, status string) ReviewFinding {
	return ReviewFinding{File: file, Line: Line(line), Severity: severity, Description: description, Status: status}
}

// aggregatedFinding builds the v2 counterpart aggregation persists, which
// never carries the disposition the raw finding recorded.
func aggregatedFinding(dimension, file string, line int, severity, description string) Finding {
	return Finding{
		Dimension:   dimension,
		Severity:    severity,
		Description: description,
		Location:    Location{File: file, LineStart: line},
	}
}

// statusesByKey indexes results by the full join key rather than by
// description alone. Collapsing on description would let an implementation
// that cross-associates findings sharing a description across dimensions or
// files pass unnoticed, which is exactly the mis-attribution these tests
// exist to detect.
func statusesByKey(findings []Finding) map[string]string {
	statuses := make(map[string]string, len(findings))
	for _, finding := range findings {
		key := fmt.Sprintf("%s|%s|%d|%s", finding.Dimension, finding.Location.File, finding.Location.LineStart, finding.Description)
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
				sampleFinding("a.go", 10, SevCritical, "nil dereference", StatusConfirmed),
			}},
			{Dim: DimSecurity, Findings: []ReviewFinding{
				sampleFinding("a.go", 10, SevCritical, "nil dereference", ""),
			}},
		},
		AggregatedFindings: []Finding{
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
			sampleFinding("a.go", 10, SevCritical, "injected query", StatusRefuted),
			sampleFinding("b.go", 30, SevCritical, "unchecked input", StatusConfirmed),
		}}},
		AggregatedFindings: []Finding{
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
			sampleFinding("superseded.go", 10, SevCritical, "duplicated by gofmt", StatusConfirmed),
			sampleFinding("b.go", 30, SevCritical, "unchecked input", ""),
			sampleFinding("kept.go", 40, SevCritical, "real defect", StatusConfirmed),
		}}},
		AggregatedFindings: []Finding{
			aggregatedFinding(DimLogic, "kept.go", 40, SevCritical, "real defect"),
		},
	}
	findings := revision.FindingsWithDispositions()
	if len(findings) != 1 {
		t.Fatalf("findings = %d, want 1: only the surviving aggregate, %#v", len(findings), findings)
	}
	if findings[0].Location.File != "kept.go" || findings[0].Status != StatusConfirmed {
		t.Fatalf("finding = %#v, want the confirmed kept.go aggregate", findings[0])
	}
}

func TestFindingsWithDispositionsCountsAMatchedFindingOnce(t *testing.T) {
	revision := Revision{
		Dims: []DimensionResult{{Dim: DimLogic, Findings: []ReviewFinding{
			sampleFinding("a.go", 10, SevCritical, "nil dereference", StatusRefuted),
		}}},
		AggregatedFindings: []Finding{
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
			sampleFinding("a.go", 10, SevCritical, "nil dereference", StatusConfirmed),
		}}},
		AggregatedFindings: []Finding{aggregated},
	}
	findings := revision.FindingsWithDispositions()
	if len(findings) != 1 || findings[0].Status != StatusRefuted {
		t.Fatalf("findings = %#v, want the single aggregate keeping %q", findings, StatusRefuted)
	}
}

func TestFindingsWithDispositionsIgnoresContradictoryRawStatuses(t *testing.T) {
	revision := Revision{
		Dims: []DimensionResult{{Dim: DimLogic, Findings: []ReviewFinding{
			sampleFinding("a.go", 10, SevCritical, "nil dereference", StatusConfirmed),
			sampleFinding("a.go", 10, SevCritical, "nil dereference", StatusAcceptedByUser),
		}}},
		AggregatedFindings: []Finding{
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
			sampleFinding("a.go", 10, SevCritical, "unchecked input", StatusConfirmed),
		}}},
		AggregatedFindings: []Finding{first, second},
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
			sampleFinding("a.go", 10, SevCritical, "unchecked input", StatusRefuted),
		}}},
		AggregatedFindings: []Finding{first, second},
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
			sampleFinding("a.go", 10, SevCritical, "nil dereference", "  CONFIRMED  "),
			sampleFinding("b.go", 20, SevWarning, "unclear name", "   "),
		}}},
		AggregatedFindings: []Finding{
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
			sampleFinding("a.go", 10, SevCritical, "nil dereference", StatusConfirmed),
		}}},
		AggregatedFindings: []Finding{aggregated},
	}
	findings := revision.FindingsWithDispositions()
	if len(findings) != 1 || findings[0].Status != StatusConfirmed {
		t.Fatalf("findings = %#v, want a whitespace-only status treated as absent", findings)
	}
}

func TestFindingsWithDispositionsFallsBackToRawDimsWithTheirStatus(t *testing.T) {
	revision := Revision{
		Dims: []DimensionResult{{Dim: DimTests, Findings: []ReviewFinding{
			sampleFinding("a_test.go", 5, SevCritical, "missing coverage", " Confirmed "),
			sampleFinding("b_test.go", 7, SevWarning, "weak assertion", ""),
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

func TestEffectiveFindingsStillIgnoresDispositions(t *testing.T) {
	revision := Revision{
		Dims: []DimensionResult{{Dim: DimLogic, Findings: []ReviewFinding{
			sampleFinding("a.go", 10, SevCritical, "nil dereference", StatusConfirmed),
		}}},
		AggregatedFindings: []Finding{
			aggregatedFinding(DimLogic, "a.go", 10, SevCritical, "nil dereference"),
		},
	}
	findings := revision.EffectiveFindings()
	if len(findings) != 1 || findings[0].Status != "" {
		t.Fatalf("EffectiveFindings = %#v, want the gate selection unchanged", findings)
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

// The aggregate's own status must be returned canonical, not merely tested
// for presence in canonical form. Normalizing only the check and then
// discarding the value leaves one observation API returning two status
// representations: raw-path findings canonical, aggregate-path findings as
// persisted. A consumer comparing against StatusRefuted then fails to
// recognise a padded refutation.
func TestFindingsWithDispositionsCanonicalisesTheAggregateOwnStatus(t *testing.T) {
	aggregated := aggregatedFinding(DimLogic, "a.go", 10, SevCritical, "nil dereference")
	aggregated.Status = "  REFUTED  "
	revision := Revision{
		Dims:               []DimensionResult{{Dim: DimLogic}},
		AggregatedFindings: []Finding{aggregated},
	}
	findings := revision.FindingsWithDispositions()
	if len(findings) != 1 || findings[0].Status != StatusRefuted {
		t.Fatalf("findings = %#v, want the aggregate status canonicalised to %q", findings, StatusRefuted)
	}
	if revision.AggregatedFindings[0].Status != "  REFUTED  " {
		t.Fatal("the persisted aggregated finding was mutated in place")
	}
}

// Every status this method returns is canonical regardless of which persisted
// shape it came from, so a consumer never has to normalize again.
func TestFindingsWithDispositionsReturnsOnlyCanonicalStatuses(t *testing.T) {
	adopted := aggregatedFinding(DimLogic, "adopted.go", 10, SevCritical, "adopted")
	own := aggregatedFinding(DimLogic, "own.go", 20, SevCritical, "own")
	own.Status = " Accepted_By_User "
	revision := Revision{
		Dims: []DimensionResult{{Dim: DimLogic, Findings: []ReviewFinding{
			sampleFinding("adopted.go", 10, SevCritical, "adopted", " CONFIRMED "),
			sampleFinding("appended.go", 30, SevCritical, "appended", " Refuted "),
		}}},
		AggregatedFindings: []Finding{adopted, own},
	}
	statuses := statusesByKey(revision.FindingsWithDispositions())
	want := map[string]string{
		key(DimLogic, "adopted.go", 10, "adopted"):   StatusConfirmed,
		key(DimLogic, "own.go", 20, "own"):           StatusAcceptedByUser,
		key(DimLogic, "appended.go", 30, "appended"): StatusRefuted,
	}
	for k, expected := range want {
		if statuses[k] != expected {
			t.Fatalf("status[%s] = %q, want %q (all: %#v)", k, statuses[k], expected, statuses)
		}
	}
}

// A padded or differently cased refutation with no counterpart must still be
// recognised as a refutation and appended. Comparing h.Status raw against
// StatusRefuted would silently drop it.
func TestFindingsWithDispositionsAppendsANonCanonicalRefutation(t *testing.T) {
	revision := Revision{
		Dims: []DimensionResult{{Dim: DimSecurity, Findings: []ReviewFinding{
			sampleFinding("a.go", 10, SevCritical, "injected query", "  REFUTED  "),
		}}},
		AggregatedFindings: []Finding{
			aggregatedFinding(DimSecurity, "b.go", 30, SevCritical, "unchecked input"),
		},
	}
	findings := revision.FindingsWithDispositions()
	if len(findings) != 2 {
		t.Fatalf("findings = %d, want 2: the padded refutation must still be appended, %#v", len(findings), findings)
	}
	statuses := statusesByKey(findings)
	if statuses[key(DimSecurity, "a.go", 10, "injected query")] != StatusRefuted {
		t.Fatalf("statuses = %#v, want the padded refutation appended as %q", statuses, StatusRefuted)
	}
}

// Contradiction is detected after normalization, not before: two spellings of
// one disposition are one disposition, not two contradictory ones.
func TestFindingsWithDispositionsNormalizesBeforeDetectingContradiction(t *testing.T) {
	revision := Revision{
		Dims: []DimensionResult{{Dim: DimLogic, Findings: []ReviewFinding{
			sampleFinding("a.go", 10, SevCritical, "nil dereference", " confirmed "),
			sampleFinding("a.go", 10, SevCritical, "nil dereference", "CONFIRMED"),
		}}},
		AggregatedFindings: []Finding{
			aggregatedFinding(DimLogic, "a.go", 10, SevCritical, "nil dereference"),
		},
	}
	findings := revision.FindingsWithDispositions()
	if len(findings) != 1 || findings[0].Status != StatusConfirmed {
		t.Fatalf("findings = %#v, want one %q: two spellings are one disposition", findings, StatusConfirmed)
	}
}
