package metrics

import (
	"encoding/json"
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

func TestEvidenceAvailabilityDistinguishesUnknownPartialAndComplete(t *testing.T) {
	partialValue := 0.5
	partial := Coverage{Observed: 1, Total: 2, Value: &partialValue}
	completeValue := 1.0
	complete := Coverage{Observed: 2, Total: 2, Value: &completeValue}
	if !partial.Known() || partial.Complete() || !complete.Complete() || (Coverage{}).Known() {
		t.Fatalf("coverage availability = partial:%v/%v complete:%v unknown:%v", partial.Known(), partial.Complete(), complete.Known(), (Coverage{}).Known())
	}
	ratioValue := 0.5
	if (Ratio{Value: &ratioValue, Coverage: partial}).Known() || (Ratio{Value: &ratioValue, Coverage: complete}).Known() == false {
		t.Fatal("ratio availability did not follow coverage completeness")
	}
	measurementValue := int64(1)
	if (Measurement{Value: &measurementValue, Coverage: partial}).Known() || (Measurement{Value: &measurementValue, Coverage: complete}).Known() == false {
		t.Fatal("measurement availability did not follow coverage completeness")
	}
}

func TestCostTotalCoverageUsesRunEvidence(t *testing.T) {
	complete := CostAggregate{ObservedRuns: 2, TotalRuns: 2, CostPerConfirmed: Ratio{Coverage: Coverage{Observed: 1, Total: 2}}}
	partial := CostAggregate{ObservedRuns: 1, TotalRuns: 2, CostPerConfirmed: Ratio{Coverage: Coverage{Observed: 2, Total: 2}}}
	if !complete.TotalCoverage().Complete() || partial.TotalCoverage().Complete() {
		t.Fatalf("cost total coverage ignored run evidence: complete=%#v partial=%#v", complete.TotalCoverage(), partial.TotalCoverage())
	}
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

func TestReadStoreKeepsDistinctAnonymousRemediationEvents(t *testing.T) {
	commonDir := t.TempDir()
	at := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	events := []ops.Evento{
		{At: at, Cmd: "remediate", Exit: 0, Detail: ops.EventDetail{
			"kind": "remediation", "target": "target-a", "dimension": "logic",
		}},
		{At: at, Cmd: "remediate", Exit: 0, Detail: ops.EventDetail{
			"kind": "remediation", "target": "target-b", "dimension": "security",
		}},
	}
	var data []byte
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshal event: %v", err)
		}
		data = append(data, line...)
		data = append(data, '\n')
	}
	eventsPath := filepath.Join(commonDir, "vas-sentinel", "events.jsonl")
	if err := os.MkdirAll(filepath.Dir(eventsPath), 0755); err != nil {
		t.Fatalf("create events directory: %v", err)
	}
	if err := os.WriteFile(eventsPath, data, 0644); err != nil {
		t.Fatalf("write events: %v", err)
	}

	input, err := ReadStore(commonDir)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	got := Aggregate(input)
	if got.Remediation.Attempts != 2 {
		t.Fatalf("distinct remediation events were collapsed: %#v", got.Remediation)
	}
}

func TestReadStoreKeepsAnonymousRemediationOutcomesDistinct(t *testing.T) {
	commonDir := t.TempDir()
	at := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	events := []ops.Evento{
		{At: at, Cmd: "remediate", Exit: 0, Detail: ops.EventDetail{
			"kind": "remediation", "target": "target", "dimension": "logic", "success": true,
		}},
		{At: at, Cmd: "remediate", Exit: 1, Detail: ops.EventDetail{
			"kind": "remediation", "target": "target", "dimension": "logic", "success": false,
		}},
	}
	var data []byte
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshal event: %v", err)
		}
		data = append(data, line...)
		data = append(data, '\n')
	}
	eventsPath := filepath.Join(commonDir, "vas-sentinel", "events.jsonl")
	if err := os.MkdirAll(filepath.Dir(eventsPath), 0755); err != nil {
		t.Fatalf("create events directory: %v", err)
	}
	if err := os.WriteFile(eventsPath, data, 0644); err != nil {
		t.Fatalf("write events: %v", err)
	}

	input, err := ReadStore(commonDir)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	got := Aggregate(input)
	if got.Remediation.Attempts != 2 || got.Remediation.Succeeded != 1 || got.Remediation.Failed != 1 {
		t.Fatalf("anonymous remediation outcomes were collapsed: %#v", got.Remediation)
	}
}

func TestOverrideRateExcludesRefutedOverrides(t *testing.T) {
	got := Aggregate(Input{
		Findings: []FindingObservation{{
			Fingerprint: "finding",
			Finding:     review.Hallazgo{Fingerprint: "finding", Status: review.StatusRefuted},
		}},
		Decisions: []store.Decision{{Fingerprint: "finding", Decision: "accepted_by_user"}},
	})
	if got.Findings.OverrideRate.Numerator > got.Findings.OverrideRate.Denominator {
		t.Fatalf("override ratio exceeds one: %#v", got.Findings.OverrideRate)
	}
	if got.Findings.Overrides != 0 {
		t.Fatalf("refuted finding counted as an effective override: %#v", got.Findings)
	}
}

func TestFindingOverrideFingerprintTrimsWhitespace(t *testing.T) {
	got := Aggregate(Input{
		Findings: []FindingObservation{{
			Fingerprint: "finding",
			Finding:     review.Hallazgo{Fingerprint: "finding", Status: review.StatusConfirmed},
		}},
		Decisions: []store.Decision{{Fingerprint: " finding ", Decision: "accepted_by_user"}},
	})
	if got.Findings.Overrides != 1 {
		t.Fatalf("trimmed decision fingerprint was not matched: %#v", got.Findings)
	}
}

func TestAnonymousRemediationKeysAreInjectiveAndNamespaced(t *testing.T) {
	at := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	anonymousA := RemediationObservation{Target: "a:b", Dimension: "c", At: at, Success: true}
	anonymousB := RemediationObservation{Target: "a", Dimension: "b:c", At: at, Success: true}
	collidingExplicitID := "anonymous:a:b:c:" + at.Format(time.RFC3339Nano) + ":true"
	got := Aggregate(Input{Remediations: []RemediationObservation{
		anonymousA,
		anonymousB,
		{LogicalID: collidingExplicitID, Target: "explicit", At: at, Success: false},
	}})
	if got.Remediation.Attempts != 3 {
		t.Fatalf("anonymous remediation identities collided: %#v", got.Remediation)
	}
}

func TestMetricSnapshotsMergeComplementaryEvidenceRegardlessOfOrder(t *testing.T) {
	firstDuration := time.Duration(10)
	secondDuration := time.Duration(20)
	inputTokens := int64(5)
	first := &store.ExecutionMetrics{
		Version: store.ExecutionMetricsSchemaVersion,
		RunID:   "run",
		Timing:  &store.ExecutionTiming{TotalDurationNanos: &firstDuration},
	}
	second := &store.ExecutionMetrics{
		Version: store.ExecutionMetricsSchemaVersion,
		RunID:   "run",
		Timing:  &store.ExecutionTiming{TotalDurationNanos: &secondDuration},
		Usage:   &store.ExecutionTokenUsage{InputTokens: &inputTokens},
		Cost:    &store.ExecutionCost{AmountMicros: 12, Currency: "USD"},
	}
	input := Input{Executions: []ExecutionObservation{
		{RunID: "run", Metrics: first},
		{RunID: "run", Metrics: second},
	}}
	want := Aggregate(input)
	input.Executions[0], input.Executions[1] = input.Executions[1], input.Executions[0]
	got := Aggregate(input)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("metric snapshot order changed aggregate:\n got: %#v\nwant: %#v", got, want)
	}
	if got.Executions.Duration.Value == nil || *got.Executions.Duration.Value != 20 {
		t.Fatalf("merged duration = %#v", got.Executions.Duration)
	}
	if got.Executions.InputTokens.Observed != 1 || len(got.Costs) != 1 {
		t.Fatalf("complementary metrics were discarded: executions=%#v costs=%#v", got.Executions, got.Costs)
	}
}

func TestFinalOutcomeTieIsIndependentOfInputOrder(t *testing.T) {
	at := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	failure := store.AttemptOutcome{RunID: "run", At: at, Class: agentrun.OutcomeFailure}
	timeout := store.AttemptOutcome{RunID: "run", At: at, Class: agentrun.OutcomeTimeout}
	first, firstOK := finalOutcome([]store.AttemptOutcome{failure, timeout})
	second, secondOK := finalOutcome([]store.AttemptOutcome{timeout, failure})
	if !firstOK || !secondOK || first != second {
		t.Fatalf("equal-time terminal outcomes depend on order: %q/%q", first, second)
	}
}

func TestStageCoverageUsesMeasuredRunPopulation(t *testing.T) {
	firstDuration := time.Duration(10)
	first := &store.ExecutionMetrics{
		Version: store.ExecutionMetricsSchemaVersion,
		RunID:   "run-a",
		Timing: &store.ExecutionTiming{
			TotalDurationNanos: &firstDuration,
			ByCapability:       []store.CapabilityTiming{{CapabilityID: "stage", DurationNanos: 10}},
		},
	}
	second := &store.ExecutionMetrics{Version: store.ExecutionMetricsSchemaVersion, RunID: "run-b"}
	got := Aggregate(Input{Executions: []ExecutionObservation{
		{RunID: "run-a", Metrics: first},
		{RunID: "run-b", Metrics: second},
	}})
	if len(got.Stages) != 1 {
		t.Fatalf("stages = %#v", got.Stages)
	}
	assertCoverage(t, "stage coverage", got.Stages[0].Coverage, 1, 2, 0.5)
}

func TestMetricsWithoutTerminalOutcomeRemainUnclassified(t *testing.T) {
	got := Aggregate(Input{Executions: []ExecutionObservation{{
		RunID: "run",
		Metrics: &store.ExecutionMetrics{
			Version: store.ExecutionMetricsSchemaVersion,
			RunID:   "run",
		},
	}}})
	if got.Executions.SuccessfulRuns != 0 || got.Executions.FailedRuns != 0 {
		t.Fatalf("nonterminal metrics classified as terminal: %#v", got.Executions)
	}
}

func TestNegativeMetricValuesAreUnavailable(t *testing.T) {
	duration := time.Duration(-1)
	inputTokens := int64(-2)
	outputTokens := int64(-3)
	got := Aggregate(Input{Executions: []ExecutionObservation{{
		RunID: "run",
		Metrics: &store.ExecutionMetrics{
			Version: store.ExecutionMetricsSchemaVersion,
			RunID:   "run",
			Timing:  &store.ExecutionTiming{TotalDurationNanos: &duration},
			Usage:   &store.ExecutionTokenUsage{InputTokens: &inputTokens, OutputTokens: &outputTokens},
			Cost:    &store.ExecutionCost{AmountMicros: -4, Currency: "USD"},
		},
	}}})
	if got.Executions.Duration.Observed != 0 || got.Executions.InputTokens.Observed != 0 || got.Executions.OutputTokens.Observed != 0 || len(got.Costs) != 0 {
		t.Fatalf("negative metrics were aggregated: executions=%#v costs=%#v", got.Executions, got.Costs)
	}
}

func TestReadStoreIgnoresSymlinkedEvidence(t *testing.T) {
	commonDir := t.TempDir()
	outsideDir := t.TempDir()
	metricData, err := json.Marshal(store.ExecutionMetrics{
		Version: store.ExecutionMetricsSchemaVersion,
		RunID:   "linked-run",
	})
	if err != nil {
		t.Fatal(err)
	}
	metricTarget := filepath.Join(outsideDir, "metric.json")
	if err := os.WriteFile(metricTarget, metricData, 0600); err != nil {
		t.Fatal(err)
	}
	metricsDir := filepath.Join(commonDir, "vas-sentinel", "metrics", "v1")
	if err := os.MkdirAll(metricsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(metricTarget, filepath.Join(metricsDir, "linked-run.json")); err != nil {
		t.Fatal(err)
	}
	findingData, err := json.Marshal(review.Hallazgo{Fingerprint: "linked-finding", Status: review.StatusConfirmed})
	if err != nil {
		t.Fatal(err)
	}
	findingTarget := filepath.Join(outsideDir, "finding.json")
	if err := os.WriteFile(findingTarget, findingData, 0600); err != nil {
		t.Fatal(err)
	}
	findingsDir := filepath.Join(commonDir, "vas-sentinel", "findings")
	if err := os.MkdirAll(findingsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(findingTarget, filepath.Join(findingsDir, "linked-finding.json")); err != nil {
		t.Fatal(err)
	}
	input, err := ReadStore(commonDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(input.Executions) != 0 || len(input.Findings) != 0 {
		t.Fatalf("symlinked evidence was followed: %#v", input)
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

func TestMergeAgentTimingsKeepsDistinctIdentitiesRegardlessOfOrder(t *testing.T) {
	first := store.AgentTiming{
		Identity: store.ObservedExecutionIdentity{
			InvocationID: "worker",
			Agent:        "\x00agent\x00",
			Source:       store.ObservationSourceAdapter,
		},
		DurationNanos: 10,
	}
	second := store.AgentTiming{
		Identity: store.ObservedExecutionIdentity{
			InvocationID: "worker\x00\x00agent",
			Source:       store.ObservationSourceAdapter,
		},
		DurationNanos: 10,
	}
	forward := mergeAgentTimings([]store.AgentTiming{first}, []store.AgentTiming{second})
	reverse := mergeAgentTimings([]store.AgentTiming{second}, []store.AgentTiming{first})
	if len(forward) != 2 || len(reverse) != 2 {
		t.Fatalf("distinct agent timing identities were collapsed: forward=%#v reverse=%#v", forward, reverse)
	}
	if !reflect.DeepEqual(forward, reverse) {
		t.Fatalf("agent timing merge depends on input order: forward=%#v reverse=%#v", forward, reverse)
	}
	seen := make(map[store.ObservedExecutionIdentity]bool, len(forward))
	for _, timing := range forward {
		seen[timing.Identity] = true
	}
	if !seen[first.Identity] || !seen[second.Identity] {
		t.Fatalf("merged agent timings lost an identity: %#v", forward)
	}
}

func TestMetricSnapshotsDoNotDuplicateTimingEvidence(t *testing.T) {
	metrics := func() *store.ExecutionMetrics {
		duration := time.Duration(10)
		return &store.ExecutionMetrics{
			Version: store.ExecutionMetricsSchemaVersion,
			RunID:   "run",
			Timing: &store.ExecutionTiming{
				ByCapability: []store.CapabilityTiming{{CapabilityID: "stage", DurationNanos: duration}},
			},
		}
	}
	got := Aggregate(Input{Executions: []ExecutionObservation{
		{RunID: "run", Metrics: metrics()},
		{RunID: "run", Metrics: metrics()},
	}})
	if len(got.Stages) != 1 || got.Stages[0].Samples != 1 {
		t.Fatalf("duplicate metric snapshots inflated stage samples: %#v", got.Stages)
	}
}
func TestStageFromEventCarriesLogicalRunID(t *testing.T) {
	stage, ok := stageFromEvent(ops.Evento{Detail: ops.EventDetail{
		"stage": "stage", "duration_ns": float64(10), "run_id": "run-a",
	}})
	if !ok {
		t.Fatal("stage event was not recognized")
	}
	if stage.LogicalRunID != "run-a" {
		t.Fatalf("stage logical run id = %q, want run-a", stage.LogicalRunID)
	}
}

func TestOutcomeIdentityNormalizesEquivalentTimeOffsets(t *testing.T) {
	utc := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	offset := utc.In(time.FixedZone("offset", 2*60*60))
	outcome := func(at time.Time) store.AttemptOutcome {
		return store.AttemptOutcome{RunID: "run", At: at, Class: agentrun.OutcomeFailure}
	}
	got := uniqueOutcomes([]ExecutionObservation{{
		RunID: "run", Outcomes: []store.AttemptOutcome{outcome(utc)},
	}, {
		RunID: "run", Outcomes: []store.AttemptOutcome{outcome(offset)},
	}})
	if len(got) != 1 {
		t.Fatalf("equivalent outcome timestamps were not deduplicated: %#v", got)
	}
}

func TestMergeCostUsesNumericAmountOrdering(t *testing.T) {
	left := &store.ExecutionCost{AmountMicros: 99, Currency: "USD"}
	right := &store.ExecutionCost{AmountMicros: 100, Currency: "USD"}
	got := mergeCost(left, right)
	if got == nil || got.AmountMicros != 100 {
		t.Fatalf("mergeCost chose %v, want numeric maximum 100", got)
	}
}

func TestStageFromEventSkipsBlankRunAlias(t *testing.T) {
	stage, ok := stageFromEvent(ops.Evento{Detail: ops.EventDetail{
		"stage": "stage", "duration_ns": float64(10),
		"logical_run_id": "   ", "run_id": "run-b",
	}})
	if !ok {
		t.Fatal("stage event was not recognized")
	}
	if stage.LogicalRunID != "run-b" {
		t.Fatalf("stage logical run id = %q, want run-b", stage.LogicalRunID)
	}
}

// findingWithStatus builds one observation whose only variable attributes are
// the ones every disposition-coverage assertion depends on.
func findingWithStatus(fingerprint, dimension, model, agent, status string) FindingObservation {
	return FindingObservation{Fingerprint: fingerprint, Finding: review.Hallazgo{
		Fingerprint: fingerprint, Dimension: dimension, Status: status,
		Producer: review.Productor{Agente: agent, Modelo: model},
	}}
}

// mixedStatusInput carries two findings per dimension, model and agent, of
// which exactly one has a known status. Every disposition coverage must
// therefore land strictly between zero and one.
func mixedStatusInput() Input {
	return Input{Findings: []FindingObservation{
		findingWithStatus("a", review.DimLogic, "m1", "a1", review.StatusConfirmed),
		findingWithStatus("b", review.DimLogic, "m1", "a1", ""),
		findingWithStatus("c", review.DimSecurity, "m2", "a2", review.StatusRefuted),
		findingWithStatus("d", review.DimSecurity, "m2", "a2", ""),
	}}
}

func assertRatioCoverage(t *testing.T, name string, got Ratio, numerator, denominator, observed, total int64) {
	t.Helper()
	if got.Numerator != numerator || got.Denominator != denominator {
		t.Errorf("%s = %d/%d, want %d/%d", name, got.Numerator, got.Denominator, numerator, denominator)
	}
	if got.Coverage.Observed != observed || got.Coverage.Total != total {
		t.Errorf("%s coverage = %d/%d, want %d/%d", name, got.Coverage.Observed, got.Coverage.Total, observed, total)
	}
}

func TestDispositionCoverageIsDerivedFromKnownStatuses(t *testing.T) {
	got := Aggregate(mixedStatusInput()).Findings

	assertRatioCoverage(t, "global refutation", got.RefutationRate, 1, 4, 2, 4)
	assertRatioCoverage(t, "global confirmation", got.ConfirmationRate, 1, 3, 1, 3)

	logic, security := got.ByDimension[0], got.ByDimension[1]
	if logic.Dimension != review.DimLogic || security.Dimension != review.DimSecurity {
		t.Fatalf("dimension order = %q, %q", logic.Dimension, security.Dimension)
	}
	assertRatioCoverage(t, "logic refutation", logic.RefutationRate, 0, 2, 1, 2)
	assertRatioCoverage(t, "logic confirmation", logic.ConfirmationRate, 1, 2, 1, 2)
	assertRatioCoverage(t, "security refutation", security.RefutationRate, 1, 2, 1, 2)
	assertRatioCoverage(t, "security confirmation", security.ConfirmationRate, 0, 1, 0, 1)

	assertRatioCoverage(t, "m1 refutation", got.ByModel[0].RefutationRate, 0, 2, 1, 2)
	assertRatioCoverage(t, "m2 refutation", got.ByModel[1].RefutationRate, 1, 2, 1, 2)
	assertRatioCoverage(t, "a1 refutation", got.ByAgent[0].RefutationRate, 0, 2, 1, 2)
	assertRatioCoverage(t, "a2 refutation", got.ByAgent[1].RefutationRate, 1, 2, 1, 2)

	for name, ratio := range map[string]Ratio{
		"global refutation": got.RefutationRate, "logic refutation": logic.RefutationRate,
		"m1 refutation": got.ByModel[0].RefutationRate, "a1 refutation": got.ByAgent[0].RefutationRate,
	} {
		if ratio.Known() {
			t.Errorf("%s = %#v, want unavailable under partial coverage", name, ratio)
		}
	}
}

func TestDispositionCoverageIsZeroWhenNoStatusIsKnown(t *testing.T) {
	got := Aggregate(Input{Findings: []FindingObservation{
		findingWithStatus("a", review.DimLogic, "m1", "a1", ""),
		findingWithStatus("b", review.DimLogic, "m1", "a1", ""),
	}}).Findings

	cases := map[string]Ratio{
		"global refutation":   got.RefutationRate,
		"global confirmation": got.ConfirmationRate,
		"logic refutation":    got.ByDimension[0].RefutationRate,
		"logic confirmation":  got.ByDimension[0].ConfirmationRate,
		"model refutation":    got.ByModel[0].RefutationRate,
		"agent refutation":    got.ByAgent[0].RefutationRate,
	}
	for name, ratio := range cases {
		if ratio.Coverage.Observed != 0 || ratio.Coverage.Total != 2 {
			t.Errorf("%s coverage = %d/%d, want 0/2", name, ratio.Coverage.Observed, ratio.Coverage.Total)
		}
		if ratio.Known() {
			t.Errorf("%s = %#v, want unavailable rather than a measured zero", name, ratio)
		}
	}
}

func TestGlobalAndDimensionRefutationCoverageCannotDisagree(t *testing.T) {
	for _, status := range []string{"", review.StatusConfirmed} {
		got := Aggregate(Input{Findings: []FindingObservation{
			findingWithStatus("a", review.DimLogic, "m1", "a1", status),
			findingWithStatus("b", review.DimLogic, "m1", "a1", status),
		}}).Findings
		if len(got.ByDimension) != 1 || len(got.ByModel) != 1 || len(got.ByAgent) != 1 {
			t.Fatalf("single-dimension store produced %d dimensions", len(got.ByDimension))
		}
		for name, group := range map[string]Coverage{
			"dimension": got.ByDimension[0].RefutationRate.Coverage,
			"model":     got.ByModel[0].RefutationRate.Coverage,
			"agent":     got.ByAgent[0].RefutationRate.Coverage,
		} {
			if !reflect.DeepEqual(group, got.RefutationRate.Coverage) {
				t.Errorf("status %q: %s refutation coverage = %#v, global = %#v", status, name, group, got.RefutationRate.Coverage)
			}
		}
	}
}

func TestOverrideCoverageSpansTheWholeEffectivePopulation(t *testing.T) {
	got := Aggregate(mixedStatusInput()).Findings
	assertRatioCoverage(t, "global override", got.OverrideRate, 0, 3, 3, 3)
	assertRatioCoverage(t, "logic override", got.ByDimension[0].OverrideRate, 0, 2, 2, 2)
	assertRatioCoverage(t, "security override", got.ByDimension[1].OverrideRate, 0, 1, 1, 1)
	if !got.OverrideRate.Known() || !got.ByDimension[1].OverrideRate.Known() {
		t.Fatalf("override rate is unavailable despite a complete decisions ledger: %#v", got.OverrideRate)
	}
}

func TestReopenCoverageIsUnknownWithoutAProductionWriter(t *testing.T) {
	got := Aggregate(Input{Findings: []FindingObservation{
		findingWithStatus("a", review.DimLogic, "m1", "a1", review.StatusReopened),
		findingWithStatus("b", review.DimLogic, "m1", "a1", review.StatusConfirmed),
	}}).Findings

	for name, cov := range map[string]Coverage{
		"global":    got.ReopenCoverage(),
		"dimension": got.ByDimension[0].ReopenCoverage(),
	} {
		if cov.Observed != 0 || cov.Total != 2 || cov.Complete() {
			t.Errorf("%s reopen coverage = %#v, want 0/2 and incomplete", name, cov)
		}
	}
}
