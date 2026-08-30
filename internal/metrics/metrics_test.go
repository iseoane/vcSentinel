package metrics

import (
	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/ops"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestAggregateSyntheticStore(t *testing.T) {
	at := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	input := Input{
		Findings: []FindingObservation{
			{Fingerprint: "f-confirmed", At: at, Finding: review.Hallazgo{
				Fingerprint: "f-confirmed", Dimension: review.DimLogic, Status: review.StatusConfirmed,
				Producer: review.Productor{Agente: "agent-a", Modelo: "model-a"},
			}},
			{Fingerprint: "f-confirmed", At: at.Add(-time.Minute), Finding: review.Hallazgo{
				Fingerprint: "f-confirmed", Dimension: review.DimLogic, Status: review.StatusConfirmed,
				Producer: review.Productor{Agente: "agent-a", Modelo: "model-a"},
			}}, // logical retry: deduplicated by fingerprint
			{Fingerprint: "f-refuted", At: at, Finding: review.Hallazgo{
				Fingerprint: "f-refuted", Dimension: review.DimSecurity, Status: review.StatusRefuted,
				Producer: review.Productor{Agente: "agent-b", Modelo: "model-b"},
			}},
			{Fingerprint: "f-override", At: at, Finding: review.Hallazgo{
				Fingerprint: "f-override", Dimension: review.DimLogic, Status: review.StatusAcceptedByUser,
				Producer: review.Productor{Agente: "agent-b", Modelo: "model-b"},
			}},
			{Fingerprint: "f-reopened", At: at, Finding: review.Hallazgo{
				Fingerprint: "f-reopened", Dimension: review.DimSecurity, Status: review.StatusReopened,
				Producer: review.Productor{Agente: "agent-a", Modelo: "model-a"},
			}},
			{Fingerprint: "f-superseded", At: at, Superseded: true, Finding: review.Hallazgo{
				Fingerprint: "f-superseded", Dimension: review.DimLogic, Status: review.StatusConfirmed,
				Producer: review.Productor{Agente: "agent-a", Modelo: "model-a"},
			}},
		},
		Decisions: []store.Decision{{
			Fingerprint: "f-override", Decision: "accepted_by_user", Actor: "user", At: at,
		}},
		Remediations: []RemediationObservation{
			{Target: "f-confirmed", LogicalID: "remediation-1", Dimension: review.DimLogic, Success: true},
			{Target: "f-reopened", LogicalID: "remediation-2", Dimension: review.DimSecurity, Success: false},
			{Target: "f-reopened", LogicalID: "remediation-2", Dimension: review.DimSecurity, Success: false}, // retry once
		},
		Executions: []ExecutionObservation{
			{
				RunID: "run-a", Outcomes: []store.AttemptOutcome{
					{RunID: "run-a", InvocationID: "attempt-a1", Class: agentrun.OutcomeTimeout, At: at},
					{RunID: "run-a", InvocationID: "attempt-a2", Class: agentrun.OutcomeSuccess, At: at.Add(time.Second)},
				},
				Metrics: executionMetrics("run-a", 100, 10, 80, 100, 50, &store.ExecutionCost{AmountMicros: 1200, Currency: "USD", Provenance: store.CostProvenance{Source: store.CostSourceEstimate}}, []string{"cap-b"}, []string{"cap-a"}),
			},
			{
				RunID: "run-b", Outcomes: []store.AttemptOutcome{{RunID: "run-b", InvocationID: "attempt-b1", Class: agentrun.OutcomeFailure, At: at}},
				Metrics: executionMetrics("run-b", 200, 30, 30, 200, 100, &store.ExecutionCost{AmountMicros: 900, Currency: "EUR", Provenance: store.CostProvenance{Source: store.CostSourceEstimate}}, nil, nil),
			},
			{RunID: "run-historical", Outcomes: []store.AttemptOutcome{{RunID: "run-historical", InvocationID: "attempt-old", Class: agentrun.OutcomeSuccess, At: at}}},
			{RunID: "run-missing-cost", Outcomes: []store.AttemptOutcome{{RunID: "run-missing-cost", InvocationID: "attempt-missing", Class: agentrun.OutcomeSuccess, At: at}}, Metrics: executionMetrics("run-missing-cost", 0, 0, 0, 0, 0, nil, nil, nil)},
		},
	}

	got := Aggregate(input)

	if got.Findings.Effective != 3 {
		t.Fatalf("effective findings = %d, want 3", got.Findings.Effective)
	}
	if got.Findings.Observed != 4 {
		t.Fatalf("observed findings = %d, want 4", got.Findings.Observed)
	}
	if got.Findings.Confirmed != 2 || got.Findings.Refuted != 1 || got.Findings.Overrides != 1 || got.Findings.Reopened != 1 {
		t.Fatalf("finding lifecycle totals = confirmed:%d refuted:%d overrides:%d reopened:%d", got.Findings.Confirmed, got.Findings.Refuted, got.Findings.Overrides, got.Findings.Reopened)
	}
	assertRatio(t, "confirmation rate", got.Findings.ConfirmationRate, 2, 3, 3, 3, 0.6667)
	assertRatio(t, "refutation rate", got.Findings.RefutationRate, 1, 4, 4, 4, 0.25)
	assertRatio(t, "override rate", got.Findings.OverrideRate, 1, 3, 3, 3, 0.3333)

	if got.Findings.ByDimension[0].Dimension != review.DimLogic || got.Findings.ByDimension[1].Dimension != review.DimSecurity {
		t.Fatalf("dimensions are not stable: %#v", got.Findings.ByDimension)
	}
	logic := got.Findings.ByDimension[0]
	if logic.Findings != 2 || logic.Confirmed != 1 || logic.Overrides != 1 {
		t.Fatalf("logic aggregate = %#v", logic)
	}
	security := got.Findings.ByDimension[1]
	if security.Findings != 1 || security.Refuted != 1 || security.Reopened != 1 {
		t.Fatalf("security aggregate = %#v", security)
	}

	if len(got.Findings.ByModel) != 2 || got.Findings.ByModel[0].Model != "model-a" || got.Findings.ByModel[1].Model != "model-b" {
		t.Fatalf("models are not stable: %#v", got.Findings.ByModel)
	}
	assertRatio(t, "model-b refutation rate", got.Findings.ByModel[1].RefutationRate, 1, 2, 2, 2, 0.5)

	if got.Remediation.Attempts != 2 || got.Remediation.Succeeded != 1 || got.Remediation.Failed != 1 {
		t.Fatalf("remediation = %#v", got.Remediation)
	}
	assertRatio(t, "remediation success rate", got.Remediation.SuccessRate, 1, 2, 2, 2, 0.5)

	if got.Executions.LogicalRuns != 4 || got.Executions.MeasuredRuns != 3 || got.Executions.RetriedRuns != 1 || got.Executions.SuccessfulRuns != 3 || got.Executions.FailedRuns != 1 {
		t.Fatalf("execution lifecycle = %#v", got.Executions)
	}
	assertCoverage(t, "cost coverage", got.Executions.CostCoverage, 2, 3, 0.6667)
	if got.Executions.Duration.Value == nil || *got.Executions.Duration.Value != 300 || got.Executions.Duration.Observed != 3 {
		t.Fatalf("duration measurement = %#v", got.Executions.Duration)
	}
	if got.Executions.InputTokens.Value == nil || *got.Executions.InputTokens.Value != 300 {
		t.Fatalf("input token measurement = %#v", got.Executions.InputTokens)
	}
	if got.Executions.Reuse.Reused != 1 || got.Executions.Reuse.Recomputed != 1 {
		t.Fatalf("reuse = %#v", got.Executions.Reuse)
	}

	if len(got.Costs) != 2 || got.Costs[0].Currency != "EUR" || got.Costs[1].Currency != "USD" {
		t.Fatalf("cost currencies are not stable/separate: %#v", got.Costs)
	}
	if got.Costs[0].TotalMicros != 900 || got.Costs[1].TotalMicros != 1200 {
		t.Fatalf("cost totals = %#v", got.Costs)
	}

	if len(got.Stages) != 2 || got.Stages[0].Stage != "a" || got.Stages[1].Stage != "z" {
		t.Fatalf("stages are not stable: %#v", got.Stages)
	}
	if got.Stages[0].P50Nanos != 10 || got.Stages[0].P95Nanos != 30 {
		t.Fatalf("stage a percentile = %#v", got.Stages[0])
	}
}

func TestAggregateIsDeterministicRegardlessOfInputOrder(t *testing.T) {
	input := Input{
		Findings: []FindingObservation{
			{Fingerprint: "b", Finding: review.Hallazgo{Dimension: review.DimSecurity, Status: review.StatusRefuted, Producer: review.Productor{Modelo: "m2"}}},
			{Fingerprint: "a", Finding: review.Hallazgo{Dimension: review.DimLogic, Status: review.StatusConfirmed, Producer: review.Productor{Modelo: "m1"}}},
		},
		Remediations: []RemediationObservation{{LogicalID: "r2", Success: false}, {LogicalID: "r1", Success: true}},
		Executions: []ExecutionObservation{
			{RunID: "b", Outcomes: []store.AttemptOutcome{{RunID: "b", InvocationID: "b1", Class: agentrun.OutcomeFailure}}},
			{RunID: "a", Outcomes: []store.AttemptOutcome{{RunID: "a", InvocationID: "a1", Class: agentrun.OutcomeSuccess}}},
		},
	}
	want := Aggregate(input)
	input.Findings[0], input.Findings[1] = input.Findings[1], input.Findings[0]
	input.Remediations[0], input.Remediations[1] = input.Remediations[1], input.Remediations[0]
	input.Executions[0], input.Executions[1] = input.Executions[1], input.Executions[0]
	if got := Aggregate(input); !reflect.DeepEqual(got, want) {
		t.Fatalf("aggregate changes with input order:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestZeroDenominatorRatioIsUnavailable(t *testing.T) {
	got := Aggregate(Input{})
	for name, ratio := range map[string]Ratio{
		"confirmation": got.Findings.ConfirmationRate,
		"refutation":   got.Findings.RefutationRate,
		"override":     got.Findings.OverrideRate,
		"remediation":  got.Remediation.SuccessRate,
		"reuse":        got.Executions.Reuse.Rate,
	} {
		if ratio.Denominator != 0 || ratio.Value != nil {
			t.Errorf("%s ratio = %#v, want unavailable zero denominator", name, ratio)
		}
	}
	if !math.IsNaN(ratioValue(got.Findings.ConfirmationRate)) && !math.IsInf(ratioValue(got.Findings.ConfirmationRate), 0) {
		return
	}
	t.Fatal("zero-denominator ratio exposed NaN or infinity")
}

func assertRatio(t *testing.T, name string, got Ratio, numerator, denominator, coverageObserved, coverageTotal int64, value float64) {
	t.Helper()
	if got.Numerator != numerator || got.Denominator != denominator {
		t.Fatalf("%s = %#v, want numerator=%d denominator=%d", name, got, numerator, denominator)
	}
	assertCoverage(t, name+" coverage", got.Coverage, coverageObserved, coverageTotal, float64(coverageObserved)/float64(coverageTotal))
	if got.Value == nil || *got.Value != value {
		t.Fatalf("%s value = %#v, want %.4f", name, got.Value, value)
	}
}

func assertCoverage(t *testing.T, name string, got Coverage, observed, total int64, value float64) {
	t.Helper()
	if got.Observed != observed || got.Total != total || got.Value == nil || *got.Value != value {
		t.Fatalf("%s = %#v, want observed=%d total=%d value=%.4f", name, got, observed, total, value)
	}
}

func ratioValue(r Ratio) float64 {
	if r.Value == nil {
		return 0
	}
	return *r.Value
}

func TestReadStoreReadsLedgerEventsAndRetainedMetrics(t *testing.T) {
	commonDir := t.TempDir()
	at := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	ledger := review.NuevoLedger(commonDir)
	finding := review.Hallazgo{
		Fingerprint: "stored-finding",
		Dimension:   review.DimLogic,
		Status:      review.StatusConfirmed,
		Producer:    review.Productor{Agente: "agent-a", Modelo: "model-a"},
	}
	if err := ledger.GuardarRevision("abc123", "message", "bucket", "model-a", review.Revision{
		At: at, AggregatedFindings: []review.Hallazgo{finding},
	}); err != nil {
		t.Fatalf("save ledger revision: %v", err)
	}

	request := agentrun.NewRunRequest(agentrun.Candidate("candidate"), agentrun.Prompt("prompt"), nil)
	job := agentrun.NewLogicalJob(request)
	st := store.NuevoStore(commonDir)
	if err := st.CreateRun(job, store.RunPolicy{ID: "policy"}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	duration := time.Duration(42)
	if err := st.SaveExecutionMetrics(store.ExecutionMetrics{
		Version: store.ExecutionMetricsSchemaVersion,
		RunID:   string(job.RunID()),
		Timing:  &store.ExecutionTiming{TotalDurationNanos: &duration},
	}); err != nil {
		t.Fatalf("save metrics: %v", err)
	}
	if err := ops.RegistrarEvento(commonDir, "remediation", 0, nil, ops.EventDetail{
		"kind": "remediation", "logical_id": "remediation-1",
		"stage": "repair", "duration_ns": float64(42),
	}, ""); err != nil {
		t.Fatalf("save event: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(commonDir, "vas-sentinel", "executions", "v1", string(job.RunID()))); err != nil {
		t.Fatalf("prune execution detail: %v", err)
	}

	input, err := ReadStore(commonDir)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	if len(input.Findings) != 1 || input.Findings[0].Fingerprint != "stored-finding" {
		t.Fatalf("findings = %#v", input.Findings)
	}
	if len(input.Executions) != 1 || input.Executions[0].Metrics == nil {
		t.Fatalf("retained executions = %#v", input.Executions)
	}
	if len(input.Events) != 1 || len(input.Stages) != 1 {
		t.Fatalf("events/stages = %d/%d", len(input.Events), len(input.Stages))
	}
	if len(input.Remediations) != 1 || input.Remediations[0].LogicalID != "remediation-1" {
		t.Fatalf("remediations = %#v", input.Remediations)
	}
}

func executionMetrics(runID string, total, stageA, stageZ, input, output int64, cost *store.ExecutionCost, reused, recomputed []string) *store.ExecutionMetrics {
	totalDuration := time.Duration(total)
	usageInput, usageOutput := input, output
	return &store.ExecutionMetrics{
		Version: store.ExecutionMetricsSchemaVersion,
		RunID:   runID,
		Timing: &store.ExecutionTiming{
			TotalDurationNanos: &totalDuration,
			ByCapability: []store.CapabilityTiming{
				{CapabilityID: "z", DurationNanos: time.Duration(stageZ)},
				{CapabilityID: "a", DurationNanos: time.Duration(stageA)},
			},
		},
		Usage: &store.ExecutionTokenUsage{InputTokens: &usageInput, OutputTokens: &usageOutput, Source: store.ObservationSourceAdapter},
		Cost:  cost,
		Reuse: &store.ExecutionReuse{ReusedCapabilityIDs: reused, RecomputedCapabilityIDs: recomputed},
	}
}
