package metrics

import (
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

// FU-7: the dispositions the review engine records on the raw per-dimension
// findings must reach the metrics reader. Aggregation persists the merged v2
// findings with an empty status and drops the refuted ones altogether, so a
// reader that only consults the aggregated set reports zero coverage over a
// ledger that holds real evidence.

func dispositionRawFinding(file string, line int, description, status string) review.ReviewFinding {
	return review.ReviewFinding{
		File: file, Line: review.Linea(line), Severity: review.SevCritical,
		Description: description, Status: status,
	}
}

func dispositionAggregate(dimension, file string, line int, description, fingerprint string) review.Hallazgo {
	return review.Hallazgo{
		Fingerprint: fingerprint, Dimension: dimension, Severity: review.SevCritical,
		Description: description,
		Location:    review.Ubicacion{Archivo: file, LineaInicio: line},
	}
}

func saveDispositionRevision(t *testing.T, commonDir, sha string, revision review.Revision) {
	t.Helper()
	if err := review.NuevoLedger(commonDir).GuardarRevision(sha, "message", "bucket", "model-a", revision); err != nil {
		t.Fatalf("save ledger revision: %v", err)
	}
}

func readDispositionFindings(t *testing.T, commonDir string) FindingsAggregate {
	t.Helper()
	input, err := ReadStore(commonDir)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	return aggregateFindings(input.Findings, input.Decisions)
}

func TestReadLedgerSurfacesConfirmedDispositionOntoAggregatedFindings(t *testing.T) {
	commonDir := t.TempDir()
	at := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	saveDispositionRevision(t, commonDir, "aaa111", review.Revision{
		At: at,
		Dims: []review.DimensionResult{{Dim: review.DimLogic, Findings: []review.ReviewFinding{
			dispositionRawFinding("a.go", 10, "nil dereference", review.StatusConfirmed),
			dispositionRawFinding("b.go", 20, "unbounded loop", ""),
		}}},
		AggregatedFindings: []review.Hallazgo{
			dispositionAggregate(review.DimLogic, "a.go", 10, "nil dereference", "fp-confirmed"),
			dispositionAggregate(review.DimLogic, "b.go", 20, "unbounded loop", "fp-unknown"),
		},
	})

	result := readDispositionFindings(t, commonDir)
	if result.Observed != 2 || result.Confirmed != 1 || result.Refuted != 0 {
		t.Fatalf("observed=%d confirmed=%d refuted=%d, want 2/1/0", result.Observed, result.Confirmed, result.Refuted)
	}
	if result.ConfirmationRate.Coverage.Observed != 1 || result.ConfirmationRate.Coverage.Total != 2 {
		t.Fatalf("confirmation coverage = %#v, want 1/2", result.ConfirmationRate.Coverage)
	}
	if len(result.ByDimension) != 1 || result.ByDimension[0].Confirmed != 1 {
		t.Fatalf("by dimension = %#v, want the confirmation attributed to its dimension", result.ByDimension)
	}
}

func TestReadLedgerSurfacesRefutedDispositionAggregationDropped(t *testing.T) {
	commonDir := t.TempDir()
	at := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	saveDispositionRevision(t, commonDir, "bbb222", review.Revision{
		At: at,
		Dims: []review.DimensionResult{{Dim: review.DimSecurity, Findings: []review.ReviewFinding{
			dispositionRawFinding("a.go", 10, "injected query", review.StatusRefuted),
			dispositionRawFinding("b.go", 30, "unchecked input", review.StatusConfirmed),
		}}},
		AggregatedFindings: []review.Hallazgo{
			dispositionAggregate(review.DimSecurity, "b.go", 30, "unchecked input", "fp-confirmed"),
		},
	})

	result := readDispositionFindings(t, commonDir)
	if result.Observed != 2 || result.Confirmed != 1 || result.Refuted != 1 {
		t.Fatalf("observed=%d confirmed=%d refuted=%d, want 2/1/1", result.Observed, result.Confirmed, result.Refuted)
	}
	if result.Effective != 1 {
		t.Fatalf("effective = %d, want 1: a refuted finding is observed but not effective", result.Effective)
	}
	if result.RefutationRate.Coverage.Observed != 2 || result.RefutationRate.Coverage.Total != 2 {
		t.Fatalf("refutation coverage = %#v, want 2/2", result.RefutationRate.Coverage)
	}
}

func TestReadLedgerCountsAFindingReachableThroughBothPathsOnce(t *testing.T) {
	commonDir := t.TempDir()
	at := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	saveDispositionRevision(t, commonDir, "ccc333", review.Revision{
		At: at,
		Dims: []review.DimensionResult{{Dim: review.DimLogic, Findings: []review.ReviewFinding{
			dispositionRawFinding("a.go", 10, "nil dereference", review.StatusRefuted),
		}}},
		AggregatedFindings: []review.Hallazgo{
			dispositionAggregate(review.DimLogic, "a.go", 10, "nil dereference", "fp-single"),
		},
	})

	result := readDispositionFindings(t, commonDir)
	if result.Observed != 1 || result.Refuted != 1 {
		t.Fatalf("observed=%d refuted=%d, want 1/1: the same finding must not be observed twice", result.Observed, result.Refuted)
	}
}

func TestReadLedgerKeepsRawDimsFallbackWithItsDispositions(t *testing.T) {
	commonDir := t.TempDir()
	at := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	saveDispositionRevision(t, commonDir, "ddd444", review.Revision{
		At: at,
		Dims: []review.DimensionResult{{Dim: review.DimTests, Findings: []review.ReviewFinding{
			dispositionRawFinding("a_test.go", 5, "missing coverage", review.StatusConfirmed),
		}}, {Dim: review.DimStyle, Findings: []review.ReviewFinding{
			dispositionRawFinding("b.go", 7, "inconsistent naming", ""),
		}}},
	})

	result := readDispositionFindings(t, commonDir)
	if result.Observed != 2 || result.Confirmed != 1 {
		t.Fatalf("observed=%d confirmed=%d, want 2/1", result.Observed, result.Confirmed)
	}
	if result.ConfirmationRate.Coverage.Observed != 1 || result.ConfirmationRate.Coverage.Total != 2 {
		t.Fatalf("confirmation coverage = %#v, want 1/2: the undisposed finding stays unknown", result.ConfirmationRate.Coverage)
	}
}

func TestReadLedgerDoesNotRemediateARefutedFindingInAFixedRevision(t *testing.T) {
	commonDir := t.TempDir()
	at := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	saveDispositionRevision(t, commonDir, "eee555", review.Revision{
		At: at, Fixed: true,
		Dims: []review.DimensionResult{{Dim: review.DimLogic, Findings: []review.ReviewFinding{
			dispositionRawFinding("a.go", 10, "injected query", review.StatusRefuted),
			dispositionRawFinding("b.go", 30, "unchecked input", review.StatusConfirmed),
		}}},
		AggregatedFindings: []review.Hallazgo{
			dispositionAggregate(review.DimLogic, "b.go", 30, "unchecked input", "fp-confirmed"),
		},
	})

	input, err := ReadStore(commonDir)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	if len(input.Remediations) != 1 {
		t.Fatalf("remediations = %#v, want only the non-refuted finding of the fixed revision", input.Remediations)
	}
}

func TestFindingObservationPrefersTheObservationCarryingAStatus(t *testing.T) {
	at := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	base := FindingObservation{
		Fingerprint: "fp", Commit: "aaa111", Revision: 0, At: at, Origin: "ledger",
		Finding: review.Hallazgo{Dimension: review.DimLogic, Description: "nil dereference"},
	}
	disposed := base
	disposed.Finding.Status = review.StatusRefuted
	if !findingObservationAfter(disposed, base) {
		t.Fatal("an observation carrying a disposition must win a tie against one without it")
	}
	if findingObservationAfter(base, disposed) {
		t.Fatal("an observation without a disposition must lose a tie against one carrying it")
	}
	result := aggregateFindings([]FindingObservation{base, disposed}, nil)
	if result.Observed != 1 || result.Refuted != 1 {
		t.Fatalf("observed=%d refuted=%d, want 1/1", result.Observed, result.Refuted)
	}
}
