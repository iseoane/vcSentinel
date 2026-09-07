package metrics

import (
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// FU-6: human and automated provenance stay distinguishable in the
// aggregates. A human-issued refutation still counts as a refutation of
// record, but it credits no agent invocation: the producing model and agent
// keep the observation, never the refutation.
func TestAggregateFindingsDoesNotCreditHumanRefutationsToAgents(t *testing.T) {
	at := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	human := FindingObservation{
		Fingerprint: "f-human", At: at,
		Finding: review.Finding{
			Dimension: review.DimLogic, Status: review.StatusRefuted,
			Producer:         review.Producer{Agent: "agent-a", Model: "model-a"},
			RefutationActor:  review.RefutationActorHuman,
			RefutationReason: "verified safe",
		},
	}
	automated := FindingObservation{
		Fingerprint: "f-auto", At: at,
		Finding: review.Finding{
			Dimension: review.DimLogic, Status: review.StatusRefuted,
			Producer:         review.Producer{Agent: "agent-a", Model: "model-a"},
			RefutationActor:  review.RefutationActorRefuter,
			RefutationReason: "evidence mismatch",
		},
	}

	got := Aggregate(Input{Findings: []FindingObservation{human, automated}}).Findings

	if got.Refuted != 2 {
		t.Fatalf("refuted = %d, want both refutations of record", got.Refuted)
	}
	byModel := map[string]ModelAggregate{}
	for _, row := range got.ByModel {
		byModel[row.Model] = row
	}
	row, ok := byModel["model-a"]
	if !ok {
		t.Fatalf("models = %+v, want model-a observed", got.ByModel)
	}
	if row.Observed != 2 {
		t.Fatalf("model-a observed = %d, want both findings attributed as produced", row.Observed)
	}
	if row.Refuted != 1 {
		t.Fatalf("model-a refuted = %d, want only the automated refutation credited", row.Refuted)
	}
	byAgent := map[string]AgentAggregate{}
	for _, r := range got.ByAgent {
		byAgent[r.Agent] = r
	}
	if byAgent["agent-a"].Refuted != 1 {
		t.Fatalf("agent-a refuted = %d, want only the automated refutation credited", byAgent["agent-a"].Refuted)
	}
}

// FU-6 defect 3: the model and agent refutation noise basis must exclude a
// human-refuted finding entirely. The numerator already skips it, so leaving
// the finding in the Known basis presents a measured 0.00 no automated
// behaviour can ever influence: the human answer, not the producer, moves
// the rate. Excluding it leaves the rate unmeasured (coverage incomplete,
// Known() false) rather than a confident zero; the observation stays
// attributed (Observed) and the dimension keeps the full basis and the
// refutation of record.
func TestAggregateFindingsHumanRefutationLeavesTheModelNoiseBasis(t *testing.T) {
	at := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	observation := FindingObservation{
		Fingerprint: "f-human", At: at,
		Finding: review.Finding{
			Dimension:        review.DimLogic,
			Status:           review.StatusRefuted,
			Producer:         review.Producer{Agent: "agent-a", Model: "model-a"},
			RefutationActor:  review.RefutationActorHuman,
			RefutationReason: "verified safe",
		},
	}

	got := Aggregate(Input{Findings: []FindingObservation{observation}}).Findings

	if got.Refuted != 1 {
		t.Fatalf("refuted = %d, want the human refutation of record", got.Refuted)
	}
	byModel := map[string]ModelAggregate{}
	for _, row := range got.ByModel {
		byModel[row.Model] = row
	}
	row, ok := byModel["model-a"]
	if !ok {
		t.Fatalf("models = %+v, want model-a observed", got.ByModel)
	}
	if row.Observed != 1 {
		t.Fatalf("model-a observed = %d, want the production still attributed", row.Observed)
	}
	if row.RefutationRate.Coverage.Observed != 0 || row.RefutationRate.Known() {
		t.Fatalf("model-a refutation rate = %+v, want the human answer outside the noise basis", row.RefutationRate)
	}
	byAgent := map[string]AgentAggregate{}
	for _, r := range got.ByAgent {
		byAgent[r.Agent] = r
	}
	agentRow := byAgent["agent-a"]
	if agentRow.Observed != 1 {
		t.Fatalf("agent-a observed = %d, want the production still attributed", agentRow.Observed)
	}
	if agentRow.RefutationRate.Coverage.Observed != 0 || agentRow.RefutationRate.Known() {
		t.Fatalf("agent-a refutation rate = %+v, want the human answer outside the noise basis", agentRow.RefutationRate)
	}
	var dimRow *DimensionAggregate
	for i, r := range got.ByDimension {
		if r.Dimension == review.DimLogic {
			dimRow = &got.ByDimension[i]
		}
	}
	if dimRow == nil {
		t.Fatalf("dimensions = %+v, want logic observed", got.ByDimension)
	}
	if dimRow.Refuted != 1 || dimRow.RefutationRate.Denominator != 1 || dimRow.RefutationRate.Value == nil || *dimRow.RefutationRate.Value != 1 {
		t.Fatalf("logic refutation = %+v, want the dimension to keep the full basis and the refutation of record", dimRow.RefutationRate)
	}
}

// FU-6: the metrics reader observes standing human answers through the same
// domain projection as every other consumer: a refutation recorded after
// the audit still clears the finding in the aggregates.
func TestReadLedgerOverlaysStandingHumanDispositions(t *testing.T) {
	commonDir := t.TempDir()
	at := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	ledger := review.NewLedger(commonDir)
	finding := review.Finding{
		Dimension: review.DimSecurity, Severity: review.SevCritical,
		Status: review.StatusConfirmed, Description: "injected query",
		Fingerprint: "fp-standing",
		Location:    review.Location{File: "a.go", LineStart: 10},
		Producer:    review.Producer{Agent: "agent-a", Model: "model-a"},
	}
	if err := ledger.SaveRevision("abc123", "message", "bucket", "model-a", review.Revision{
		At: at, Result: "block", AggregatedFindings: []review.Finding{finding},
	}); err != nil {
		t.Fatalf("save revision: %v", err)
	}
	st := store.NewStore(commonDir)
	if err := st.AppendDisposition(&review.FindingDisposition{
		SHA: "abc123", Fingerprint: "fp-standing", Status: review.StatusRefuted,
		Reason: "verified safe", Path: "a.go", LineStart: 9, LineEnd: 11,
		Evidence: "safe call here", RangeHash: "aa",
		Actor: review.RefutationActorHuman, Source: review.DispositionSourceHuman,
		At: at.Add(time.Minute),
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	input, err := ReadStore(commonDir)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	if len(input.Findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(input.Findings))
	}
	observed := input.Findings[0].Finding
	if observed.Status != review.StatusRefuted || observed.RefutationActor != review.RefutationActorHuman {
		t.Fatalf("finding = %+v, want the standing human answer overlaid", observed)
	}
	if observed.InvocationID != "" {
		t.Fatalf("invocation = %q, human decisions cite no invocation", observed.InvocationID)
	}
	report := Aggregate(input)
	if report.Findings.Refuted != 1 || report.Findings.Effective != 0 {
		t.Fatalf("aggregate = %+v, want the human answer clearing the effective population", report.Findings)
	}
}
