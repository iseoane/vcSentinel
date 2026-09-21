package metrics

import (
	"testing"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/review"
)

// FU-7: the dispositions the review engine records on the raw per-dimension
// findings must reach the metrics reader. Aggregation persists the merged v2
// findings with an empty status and drops the refuted ones altogether, so a
// reader that only consults the aggregated set reports zero coverage over a
// ledger that holds real evidence.

func dispositionRawFinding(file string, line int, description, status string) review.ReviewFinding {
	return review.ReviewFinding{
		File: file, Line: review.Line(line), Severity: review.SevCritical,
		Description: description, Status: status,
	}
}

func dispositionAggregate(dimension, file string, line int, description, fingerprint string) review.Finding {
	return review.Finding{
		Fingerprint: fingerprint, Dimension: dimension, Severity: review.SevCritical,
		Description: description,
		Location:    review.Location{File: file, LineStart: line},
	}
}

func saveDispositionRevision(t *testing.T, commonDir, sha string, revision review.Revision) {
	t.Helper()
	if err := review.NewLedger(commonDir).SaveRevision(sha, "message", "bucket", "model-a", revision); err != nil {
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
		AggregatedFindings: []review.Finding{
			dispositionAggregate(review.DimLogic, "a.go", 10, "nil dereference", "fp-confirmed"),
			dispositionAggregate(review.DimLogic, "b.go", 20, "unbounded loop", "fp-unknown"),
			// Deterministic findings are appended to the aggregated set and
			// have no raw counterpart, so a reader that consulted only the
			// raw dims would never observe this one. It is what makes the two
			// persisted shapes distinguishable in this fixture.
			dispositionAggregate(review.DimLogic, "c.go", 30, "gofmt drift", "fp-deterministic"),
		},
	})

	result := readDispositionFindings(t, commonDir)
	if result.Observed != 3 || result.Confirmed != 1 || result.Refuted != 0 {
		t.Fatalf("observed=%d confirmed=%d refuted=%d, want 3/1/0", result.Observed, result.Confirmed, result.Refuted)
	}
	if result.ConfirmationRate.Coverage.Observed != 1 || result.ConfirmationRate.Coverage.Total != 3 {
		t.Fatalf("confirmation coverage = %#v, want 1/3", result.ConfirmationRate.Coverage)
	}
	if len(result.ByDimension) != 1 || result.ByDimension[0].Confirmed != 1 || result.ByDimension[0].Observed != 3 {
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
		AggregatedFindings: []review.Finding{
			dispositionAggregate(review.DimSecurity, "b.go", 30, "unchecked input", "fp-confirmed"),
			dispositionAggregate(review.DimSecurity, "c.go", 40, "weak cipher", "fp-deterministic"),
		},
	})

	result := readDispositionFindings(t, commonDir)
	if result.Observed != 3 || result.Confirmed != 1 || result.Refuted != 1 {
		t.Fatalf("observed=%d confirmed=%d refuted=%d, want 3/1/1", result.Observed, result.Confirmed, result.Refuted)
	}
	if result.Effective != 2 {
		t.Fatalf("effective = %d, want 2: a refuted finding is observed but not effective", result.Effective)
	}
	if result.RefutationRate.Coverage.Observed != 2 || result.RefutationRate.Coverage.Total != 3 {
		t.Fatalf("refutation coverage = %#v, want 2/3", result.RefutationRate.Coverage)
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
		AggregatedFindings: []review.Finding{
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
			// Deliberately not the canonical token: the guard must interpret
			// the status, not compare its bytes.
			dispositionRawFinding("a.go", 10, "injected query", "  REFUTED  "),
			dispositionRawFinding("b.go", 30, "unchecked input", review.StatusConfirmed),
		}}},
		AggregatedFindings: []review.Finding{
			dispositionAggregate(review.DimLogic, "b.go", 30, "unchecked input", "fp-confirmed"),
		},
	})

	input, err := ReadStore(commonDir)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	// Identity, not cardinality: asserting only a count passes just as well
	// when the refuted finding is remediated and the confirmed one omitted.
	if len(input.Remediations) != 1 {
		t.Fatalf("remediations = %#v, want exactly the confirmed finding", input.Remediations)
	}
	remediation := input.Remediations[0]
	if remediation.Target != "fp-confirmed" || !remediation.Success || remediation.Dimension != review.DimLogic {
		t.Fatalf("remediation = %#v, want the confirmed fp-confirmed finding of the fixed revision", remediation)
	}
	if remediation.LogicalID != "fixed:fp-confirmed:" {
		t.Fatalf("remediation logical id = %q, want the fixed identity of fp-confirmed", remediation.LogicalID)
	}
}

func TestFindingObservationPrefersTheObservationCarryingAStatus(t *testing.T) {
	at := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	base := FindingObservation{
		Fingerprint: "fp", Commit: "aaa111", Revision: 0, At: at, Origin: "ledger",
		Finding: review.Finding{Dimension: review.DimLogic, Description: "nil dereference"},
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

func TestReadLedgerInterpretsANonCanonicalRefutedStatus(t *testing.T) {
	commonDir := t.TempDir()
	at := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	saveDispositionRevision(t, commonDir, "fff666", review.Revision{
		At: at,
		Dims: []review.DimensionResult{{Dim: review.DimSecurity, Findings: []review.ReviewFinding{
			dispositionRawFinding("a.go", 10, "injected query", " Refuted "),
			dispositionRawFinding("b.go", 30, "unchecked input", " CONFIRMED "),
		}}},
		AggregatedFindings: []review.Finding{
			dispositionAggregate(review.DimSecurity, "b.go", 30, "unchecked input", "fp-confirmed"),
		},
	})

	result := readDispositionFindings(t, commonDir)
	if result.Observed != 2 || result.Confirmed != 1 || result.Refuted != 1 {
		t.Fatalf("observed=%d confirmed=%d refuted=%d, want 2/1/1", result.Observed, result.Confirmed, result.Refuted)
	}
	if result.Effective != 1 {
		t.Fatalf("effective = %d, want 1: the padded refutation must still leave the effective population", result.Effective)
	}
}

// The aggregated finding's own status is persisted as the producer wrote it
// and is never rewritten by the review side, so it reaches the reader
// uncanonicalised. Every reader-side interpretation must go through
// review.NormalizeStatus or the two halves disagree about one record: this
// finding leaves the effective population as refuted while still being
// counted as remediated by its fixed revision.
func TestReadLedgerInterpretsAnAggregateOwnNonCanonicalStatus(t *testing.T) {
	commonDir := t.TempDir()
	at := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	refuted := dispositionAggregate(review.DimLogic, "a.go", 10, "injected query", "fp-refuted")
	refuted.Status = "  REFUTED  "
	saveDispositionRevision(t, commonDir, "ggg777", review.Revision{
		At: at, Fixed: true,
		Dims:               []review.DimensionResult{{Dim: review.DimLogic}},
		AggregatedFindings: []review.Finding{refuted},
	})

	input, err := ReadStore(commonDir)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	if len(input.Remediations) != 0 {
		t.Fatalf("remediations = %#v, want none: a refuted finding was never a defect to remediate", input.Remediations)
	}
	result := aggregateFindings(input.Findings, input.Decisions)
	if result.Observed != 1 || result.Refuted != 1 || result.Effective != 0 {
		t.Fatalf("observed=%d refuted=%d effective=%d, want 1/1/0", result.Observed, result.Refuted, result.Effective)
	}
	// The observation must carry the canonical value, not merely be counted
	// correctly by a reader that normalizes at every comparison. Asserting
	// only the counts lets the ledger return a padded status indefinitely.
	if len(input.Findings) != 1 || input.Findings[0].Finding.Status != review.StatusRefuted {
		t.Fatalf("observed status = %#v, want the canonical %q", input.Findings, review.StatusRefuted)
	}
}

// The Fixed and Reopened remediation branches read a status the same way the
// refuted guard does. Nothing covered them, so a regression to a direct
// comparison there would pass the rest of this file.
func TestReadLedgerEmitsFixedAndReopenedRemediations(t *testing.T) {
	commonDir := t.TempDir()
	at := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	fixed := dispositionAggregate(review.DimLogic, "a.go", 10, "nil dereference", "fp-fixed")
	fixed.Status = "  FIXED  "
	reopened := dispositionAggregate(review.DimLogic, "b.go", 20, "unbounded loop", "fp-reopened")
	reopened.Status = " Reopened "
	saveDispositionRevision(t, commonDir, "hhh888", review.Revision{
		At:                 at,
		Dims:               []review.DimensionResult{{Dim: review.DimLogic}},
		AggregatedFindings: []review.Finding{fixed, reopened},
	})

	input, err := ReadStore(commonDir)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	byTarget := make(map[string]RemediationObservation, len(input.Remediations))
	for _, remediation := range input.Remediations {
		byTarget[remediation.Target] = remediation
	}
	if len(input.Remediations) != 2 {
		t.Fatalf("remediations = %#v, want one fixed and one reopened", input.Remediations)
	}
	if got := byTarget["fp-fixed"]; got.LogicalID != "fixed:fp-fixed:" || !got.Success || got.Dimension != review.DimLogic {
		t.Fatalf("fixed remediation = %#v", got)
	}
	reopenedObservation := byTarget["fp-reopened"]
	if reopenedObservation.Success || reopenedObservation.LogicalID == "" {
		t.Fatalf("reopened remediation = %#v, want an unsuccessful reopen observation", reopenedObservation)
	}
	result := aggregateFindings(input.Findings, input.Decisions)
	if result.Confirmed != 2 || result.Reopened != 1 || result.ReopenResolved != 1 {
		t.Fatalf("confirmed=%d reopened=%d resolved=%d, want 2/1/1", result.Confirmed, result.Reopened, result.ReopenResolved)
	}
}
