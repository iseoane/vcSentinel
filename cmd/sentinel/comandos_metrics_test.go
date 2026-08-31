package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/metrics"
	"github.com/ISeoane-Quental/vas.sentinel/internal/ops"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

func TestRenderMetricsJSONUsesStableUnitsAndNullForUnknown(t *testing.T) {
	report := metrics.Report{Executions: metrics.ExecutionAggregate{
		LogicalRuns:  1,
		MeasuredRuns: 1,
		Duration:     metrics.Measurement{Total: 1, Coverage: metrics.Coverage{Total: 1}},
	}}
	var output bytes.Buffer
	if err := renderMetricsJSON(&output, report); err != nil {
		t.Fatalf("renderMetricsJSON failed: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatalf("metrics JSON is invalid: %v\n%s", err, output.String())
	}
	executions, ok := decoded["executions"].(map[string]any)
	if !ok {
		t.Fatalf("executions object missing: %#v", decoded)
	}
	duration, ok := executions["duration_nanos"].(map[string]any)
	if !ok {
		t.Fatalf("duration_nanos object missing: %#v", executions)
	}
	assertJSONNull(t, duration, "value")
	if duration["observed"] != float64(0) || duration["total"] != float64(1) {
		t.Errorf("duration coverage fields are not explicit: %#v", duration)
	}
	if _, ok := executions["duration"]; ok {
		t.Error("duration must use a unit-bearing JSON key")
	}
}

func TestRenderMetricsJSONUsesFixedTypedContract(t *testing.T) {
	report := completeMetricsReport()
	report.Findings.ByDimension = []metrics.DimensionAggregate{{Dimension: "logic"}, {Dimension: "security"}}
	report.Findings.ByModel = []metrics.ModelAggregate{{Model: "model-a"}, {Model: "model-b"}}
	report.Findings.ByAgent = []metrics.AgentAggregate{{Agent: "agent-a"}, {Agent: "agent-b"}}
	report.Costs = []metrics.CostAggregate{{Currency: "EUR"}, {Currency: "USD"}}
	report.Stages = []metrics.StageAggregate{{Stage: "stage-a"}, {Stage: "stage-b"}}
	var first, second bytes.Buffer
	if err := renderMetricsJSON(&first, report); err != nil {
		t.Fatal(err)
	}
	if err := renderMetricsJSON(&second, report); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() {
		t.Fatal("the typed metrics renderer is not deterministic")
	}
	decoded := decodeMetricsJSON(t, first.Bytes())
	for _, key := range []string{"costs", "executions", "findings", "remediation", "stages"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("typed metrics JSON is missing %q", key)
		}
	}
	findings := decoded["findings"].(map[string]any)
	if len(findings["by_dimension"].([]any)) != 2 || len(findings["by_model"].([]any)) != 2 || len(findings["by_agent"].([]any)) != 2 {
		t.Fatalf("grouped findings were not rendered: %#v", findings)
	}
	if len(decoded["costs"].([]any)) != 2 || len(decoded["stages"].([]any)) != 2 {
		t.Fatalf("cost/stage ordering inputs were not rendered: %#v", decoded)
	}
	if findings["by_dimension"].([]any)[0].(map[string]any)["dimension"] != "logic" || findings["by_model"].([]any)[0].(map[string]any)["model"] != "model-a" || findings["by_agent"].([]any)[0].(map[string]any)["agent"] != "agent-a" {
		t.Fatalf("grouped findings changed typed order: %#v", findings)
	}
	if decoded["costs"].([]any)[0].(map[string]any)["currency"] != "EUR" || decoded["stages"].([]any)[0].(map[string]any)["stage"] != "stage-a" {
		t.Fatalf("cost/stage rows changed typed order: %#v", decoded)
	}
}

func TestRenderMetricsJSONIsDeterministicForPermutedAggregateInputs(t *testing.T) {
	input := metricsInputForOrdering()
	want := metrics.Aggregate(input)
	input.Findings[0], input.Findings[1] = input.Findings[1], input.Findings[0]
	input.Remediations[0], input.Remediations[1] = input.Remediations[1], input.Remediations[0]
	input.Executions[0], input.Executions[1] = input.Executions[1], input.Executions[0]
	input.Stages[0], input.Stages[1] = input.Stages[1], input.Stages[0]
	got := metrics.Aggregate(input)
	var first, second bytes.Buffer
	if err := renderMetricsJSON(&first, want); err != nil {
		t.Fatal(err)
	}
	if err := renderMetricsJSON(&second, got); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() {
		t.Fatal("equivalent aggregate inputs produced different JSON")
	}
	decoded := decodeMetricsJSON(t, first.Bytes())
	findings := decoded["findings"].(map[string]any)
	if findings["by_dimension"].([]any)[0].(map[string]any)["dimension"] != "logic" || findings["by_model"].([]any)[0].(map[string]any)["model"] != "model-a" || findings["by_agent"].([]any)[0].(map[string]any)["agent"] != "agent-a" {
		t.Fatalf("grouped findings are not canonically ordered: %#v", findings)
	}
	if decoded["costs"].([]any)[0].(map[string]any)["currency"] != "EUR" || decoded["stages"].([]any)[0].(map[string]any)["stage"] != "stage-a" {
		t.Fatalf("cost/stage rows are not canonically ordered: %#v", decoded)
	}
}

func TestRenderMetricsHidesPartiallyCoveredStagePercentiles(t *testing.T) {
	coverageValue := 0.5
	report := completeMetricsReport()
	report.Stages = []metrics.StageAggregate{
		{Stage: "compile", Samples: 2, P50Nanos: 17, P95Nanos: 23, Coverage: metrics.Coverage{Observed: 1, Total: 2, Value: &coverageValue}},
		{Stage: "unknown", Samples: 0, P50Nanos: 31, P95Nanos: 47},
	}
	var output bytes.Buffer
	renderMetrics(&output, report)
	if !strings.Contains(output.String(), "Stage compile: samples=2 p50=unknown nanoseconds p95=unknown nanoseconds") || !strings.Contains(output.String(), "Stage unknown: samples=0 p50=unknown nanoseconds p95=unknown nanoseconds") {
		t.Fatalf("partial stage percentiles were rendered as known: %s", output.String())
	}
	output.Reset()
	if err := renderMetricsJSON(&output, report); err != nil {
		t.Fatalf("partial stage JSON could not be encoded: %v", err)
	}
	stages := decodeMetricsJSON(t, output.Bytes())["stages"].([]any)
	if len(stages) != 2 {
		t.Fatalf("partial stage JSON rows = %d, want 2", len(stages))
	}
	for _, stage := range stages {
		row := stage.(map[string]any)
		assertJSONNull(t, row, "p50_nanos")
		assertJSONNull(t, row, "p95_nanos")
	}
}

func TestMetricsWarnsForIncompleteGroupedRatios(t *testing.T) {
	report := completeMetricsReport()
	coverageValue, ratioValue := 0.5, 0.5
	partial := metrics.Ratio{
		Numerator: 1, Denominator: 2, Value: &ratioValue,
		Coverage: metrics.Coverage{Observed: 1, Total: 2, Value: &coverageValue},
	}
	report.Findings.ByModel = []metrics.ModelAggregate{{Model: "model", Observed: 2, RefutationRate: partial}}
	report.Findings.ByAgent = []metrics.AgentAggregate{{Agent: "agent", Observed: 2, RefutationRate: partial}}
	var output bytes.Buffer
	renderMetrics(&output, report)
	if !strings.Contains(output.String(), "WARNING: insufficient samples") {
		t.Fatalf("incomplete grouped ratios did not trigger warning: %s", output.String())
	}
	var jsonOutput bytes.Buffer
	if err := renderMetricsJSON(&jsonOutput, report); err != nil {
		t.Fatal(err)
	}
	findings := decodeMetricsJSON(t, jsonOutput.Bytes())["findings"].(map[string]any)
	for _, key := range []string{"by_model", "by_agent"} {
		ratio := findings[key].([]any)[0].(map[string]any)["refutation_rate"].(map[string]any)
		assertJSONNull(t, ratio, "value")
	}
}

func TestMetricsWarningCoversEveryEvidenceGroup(t *testing.T) {
	var baseline bytes.Buffer
	if err := renderMetrics(&baseline, completeMetricsReport()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(baseline.String(), "Warnings: none.") {
		t.Fatalf("complete report was not established as warning-free: %s", baseline.String())
	}
	cases := []struct {
		name   string
		mutate func(*metrics.Report)
	}{
		{"global ratio", func(r *metrics.Report) { r.Findings.ConfirmationRate = partialMetricsRatio() }},
		{"dimension ratio", func(r *metrics.Report) {
			v := completeMetricsReport().Findings.ConfirmationRate
			r.Findings.ByDimension = []metrics.DimensionAggregate{{ConfirmationRate: v, RefutationRate: v, OverrideRate: partialMetricsRatio()}}
		}},
		{"model ratio", func(r *metrics.Report) {
			r.Findings.ByModel = []metrics.ModelAggregate{{RefutationRate: partialMetricsRatio()}}
		}},
		{"agent ratio", func(r *metrics.Report) {
			r.Findings.ByAgent = []metrics.AgentAggregate{{RefutationRate: partialMetricsRatio()}}
		}},
		{"remediation ratio", func(r *metrics.Report) {
			r.Remediation.ByDimension = []metrics.RemediationDimensionAggregate{{SuccessRate: partialMetricsRatio()}}
		}},
		{"measurement", func(r *metrics.Report) { r.Executions.Duration = partialMetricsMeasurement() }},
		{"cost coverage", func(r *metrics.Report) { r.Executions.CostCoverage = partialMetricsCoverage() }},
		{"identity coverage", func(r *metrics.Report) { r.Executions.IdentityCoverage = partialMetricsCoverage() }},
		{"reuse ratio", func(r *metrics.Report) { r.Executions.Reuse.Rate = partialMetricsRatio() }},
		{"scope coverage", func(r *metrics.Report) { r.Executions.Scope.Coverage = partialMetricsCoverage() }},
		{"cost ratio", func(r *metrics.Report) { r.Costs = []metrics.CostAggregate{{CostPerConfirmed: partialMetricsRatio()}} }},
		{"stage coverage", func(r *metrics.Report) { r.Stages = []metrics.StageAggregate{{Coverage: partialMetricsCoverage()}} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := completeMetricsReport()
			tc.mutate(&report)
			var output bytes.Buffer
			renderMetrics(&output, report)
			if !strings.Contains(output.String(), "WARNING: insufficient samples") {
				t.Fatalf("incomplete %s did not trigger warning", tc.name)
			}
		})
	}
}

func TestMetricsWarnsForPartialCostTotalCoverage(t *testing.T) {
	cases := []struct {
		name         string
		observedRuns int64
		warning      bool
	}{
		{name: "partial", observedRuns: 1, warning: true},
		{name: "complete", observedRuns: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ratio := completeMetricsReport().Findings.ConfirmationRate
			report := completeMetricsReport()
			report.Costs = []metrics.CostAggregate{{
				Currency: "USD", TotalMicros: 9, ObservedRuns: tc.observedRuns, TotalRuns: 2,
				CostPerConfirmed: ratio,
			}}
			var output bytes.Buffer
			if err := renderMetrics(&output, report); err != nil {
				t.Fatal(err)
			}
			text := output.String()
			if tc.warning {
				if !strings.Contains(text, "WARNING: insufficient samples") {
					t.Fatalf("partial total-cost coverage did not trigger warning: %s", text)
				}
				return
			}
			if !strings.Contains(text, "Warnings: none.") || strings.Contains(text, "WARNING: insufficient samples") {
				t.Fatalf("complete total-cost coverage warning state = %s", text)
			}
		})
	}
}

func TestHumanAndJSONHidePartiallyCoveredMeasurements(t *testing.T) {
	value, coverageValue := int64(42), 0.5
	report := completeMetricsReport()
	report.Executions.Duration = metrics.Measurement{
		Value: &value, Observed: 1, Total: 2,
		Coverage: metrics.Coverage{Observed: 1, Total: 2, Value: &coverageValue},
	}
	var human bytes.Buffer
	renderMetrics(&human, report)
	if !strings.Contains(human.String(), "duration (nanoseconds): unknown") {
		t.Fatalf("partial duration was rendered as known: %s", human.String())
	}
	var jsonOutput bytes.Buffer
	if err := renderMetricsJSON(&jsonOutput, report); err != nil {
		t.Fatalf("partial duration JSON failed: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(jsonOutput.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	executions := decoded["executions"].(map[string]any)
	duration := executions["duration_nanos"].(map[string]any)
	assertJSONNull(t, duration, "value")
}

func TestMetricsJSONPreservesUnitsAvailabilityAndEmptyArrays(t *testing.T) {
	report := completeMetricsReport()
	partialValue, partialCoverage := int64(7), 0.5
	report.Executions.InputTokens = metrics.Measurement{Observed: 0, Total: 1, Coverage: metrics.Coverage{Total: 1}}
	report.Executions.OutputTokens = metrics.Measurement{Value: &partialValue, Observed: 1, Total: 2, Coverage: metrics.Coverage{Observed: 1, Total: 2, Value: &partialCoverage}}
	completeRatio := completeMetricsReport().Findings.ConfirmationRate
	report.Costs = []metrics.CostAggregate{
		{Currency: "USD", TotalMicros: 9, ObservedRuns: 1, TotalRuns: 2, CostPerConfirmed: completeRatio},
		{Currency: "EUR", TotalMicros: 11, ObservedRuns: 2, TotalRuns: 2, CostPerConfirmed: metrics.Ratio{Coverage: partialMetricsCoverage()}},
	}
	completeCoverage := completeMetricsReport().Executions.Duration.Coverage
	report.Stages = []metrics.StageAggregate{{Stage: "known", Samples: 1, P50Nanos: 11, P95Nanos: 22, Coverage: completeCoverage}}
	var output bytes.Buffer
	if err := renderMetricsJSON(&output, report); err != nil {
		t.Fatal(err)
	}
	decoded := decodeMetricsJSON(t, output.Bytes())
	executions := decoded["executions"].(map[string]any)
	for _, key := range []string{"duration_nanos", "input_tokens", "output_tokens", "total_tokens", "cached_input_tokens", "reasoning_tokens"} {
		if _, ok := executions[key]; !ok {
			t.Errorf("missing unit-bearing execution field %q", key)
		}
	}
	assertJSONNull(t, executions["input_tokens"].(map[string]any), "value")
	assertJSONNull(t, executions["output_tokens"].(map[string]any), "value")
	if executions["total_tokens"].(map[string]any)["value"] != float64(1) {
		t.Fatalf("known token value was not preserved: %#v", executions["total_tokens"])
	}
	costs := decoded["costs"].([]any)
	partialCost := costs[0].(map[string]any)
	assertJSONNull(t, partialCost, "total_micros")
	if partialCost["cost_per_confirmed"].(map[string]any)["value"] != float64(1) {
		t.Fatalf("complete ratio evidence was hidden: %#v", partialCost)
	}
	completeCost := costs[1].(map[string]any)
	if completeCost["total_micros"] != float64(11) {
		t.Fatalf("complete total cost was hidden: %#v", completeCost)
	}
	assertJSONNull(t, completeCost["cost_per_confirmed"].(map[string]any), "value")
	var human bytes.Buffer
	renderMetrics(&human, report)
	if !strings.Contains(human.String(), "Cost USD: total=unknown micros") {
		t.Fatalf("partial cost was rendered as known: %s", human.String())
	}
	if !strings.Contains(human.String(), "Cost EUR: total=11 micros") {
		t.Fatalf("complete total cost was rendered as unknown: %s", human.String())
	}
	stage := decoded["stages"].([]any)[0].(map[string]any)
	if stage["p50_nanos"] != float64(11) || stage["p95_nanos"] != float64(22) {
		t.Fatalf("known stage percentiles were not preserved: %#v", stage)
	}
	for _, key := range []string{"by_agent", "by_dimension", "by_model"} {
		if values, ok := decoded["findings"].(map[string]any)[key].([]any); !ok || values == nil {
			t.Errorf("findings %s is not a stable array: %#v", key, decoded["findings"])
		}
	}
}

func TestMetricsJSONUsesEmptyArraysForEmptyReport(t *testing.T) {
	var output bytes.Buffer
	if err := renderMetricsJSON(&output, metrics.Report{}); err != nil {
		t.Fatal(err)
	}
	decoded := decodeMetricsJSON(t, output.Bytes())
	for _, path := range [][2]string{{"", "costs"}, {"", "stages"}, {"findings", "by_agent"}, {"findings", "by_dimension"}, {"findings", "by_model"}, {"remediation", "by_dimension"}, {"executions", "failures"}} {
		container := decoded
		if path[0] != "" {
			container = decoded[path[0]].(map[string]any)
		}
		values, ok := container[path[1]].([]any)
		if !ok || values == nil {
			t.Errorf("JSON %s.%s is not an empty array: %#v", path[0], path[1], container[path[1]])
		}
	}
}

func TestMetricsJSONMapsRemediationDimensionsAndExecutionFailures(t *testing.T) {
	report := completeMetricsReport()
	report.Remediation.ByDimension = []metrics.RemediationDimensionAggregate{{
		Dimension: "logic", Attempts: 2, Succeeded: 1, Failed: 1,
		SuccessRate: report.Findings.ConfirmationRate,
	}}
	report.Executions.Failures = []metrics.FailureAggregate{{Class: "timeout", Count: 3}}
	var output bytes.Buffer
	if err := renderMetricsJSON(&output, report); err != nil {
		t.Fatal(err)
	}
	decoded := decodeMetricsJSON(t, output.Bytes())
	remediation := decoded["remediation"].(map[string]any)
	dimension := remediation["by_dimension"].([]any)[0].(map[string]any)
	if dimension["dimension"] != "logic" || dimension["attempts"] != float64(2) || dimension["succeeded"] != float64(1) || dimension["failed"] != float64(1) {
		t.Fatalf("remediation dimension mapping = %#v", dimension)
	}
	if dimension["success_rate"].(map[string]any)["value"] != float64(1) {
		t.Fatalf("remediation dimension ratio mapping = %#v", dimension)
	}
	failure := decoded["executions"].(map[string]any)["failures"].([]any)[0].(map[string]any)
	if failure["class"] != "timeout" || failure["count"] != float64(3) {
		t.Fatalf("execution failure mapping = %#v", failure)
	}
}

func TestExecuteMetricsPropagatesHumanWriterFailure(t *testing.T) {
	repo, _ := newMetricsRepository(t)
	if code := executeMetrics(metricsFailWriter{}, repo, nil); code != 1 {
		t.Fatalf("writer failure returned exit code %d, want 1", code)
	}
}

func TestExecuteMetricsPropagatesJSONWriterFailure(t *testing.T) {
	repo, _ := newMetricsRepository(t)
	if code := executeMetrics(metricsFailWriter{}, repo, []string{"--json"}); code != 1 {
		t.Fatalf("JSON writer failure returned exit code %d, want 1", code)
	}
}

type metricsFailWriter struct{}

func (metricsFailWriter) Write([]byte) (int, error) { return 0, errors.New("metrics writer failed") }

func completeMetricsReport() metrics.Report {
	coverage := metrics.Coverage{Observed: 1, Total: 1}
	coverageValue := 1.0
	coverage.Value = &coverageValue
	ratioValue := 1.0
	ratio := metrics.Ratio{Numerator: 1, Denominator: 1, Value: &ratioValue, Coverage: coverage}
	measurementValue := int64(1)
	measurement := metrics.Measurement{Value: &measurementValue, Observed: 1, Total: 1, Coverage: coverage}
	return metrics.Report{
		Findings:    metrics.FindingsAggregate{Observed: 1, ConfirmationRate: ratio, RefutationRate: ratio, OverrideRate: ratio},
		Remediation: metrics.RemediationAggregate{Attempts: 1, SuccessRate: ratio},
		Executions: metrics.ExecutionAggregate{
			LogicalRuns: 1, SuccessRate: ratio,
			Duration: measurement, InputTokens: measurement, OutputTokens: measurement,
			TotalTokens: measurement, CachedInputTokens: measurement, ReasoningTokens: measurement,
			CostCoverage: coverage, IdentityCoverage: coverage,
			Reuse: metrics.ReuseAggregate{Rate: ratio}, Scope: metrics.ScopeAggregate{Coverage: coverage},
		},
	}
}

func metricsInputForOrdering() metrics.Input {
	one := int64(1)
	durationA, durationB := time.Duration(10), time.Duration(20)
	snapshot := func(run, agent, model, currency string, duration *time.Duration) *store.ExecutionMetrics {
		return &store.ExecutionMetrics{
			Version: store.ExecutionMetricsSchemaVersion, RunID: run,
			Identities: []store.ObservedExecutionIdentity{{Agent: agent, Model: model}},
			Timing:     &store.ExecutionTiming{TotalDurationNanos: duration},
			Usage:      &store.ExecutionTokenUsage{InputTokens: &one, OutputTokens: &one, TotalTokens: &one, CachedInputTokens: &one, ReasoningTokens: &one},
			Cost:       &store.ExecutionCost{AmountMicros: 1, Currency: currency}, Scope: &store.ExecutionScope{Kind: store.ScopeFull},
			Reuse: &store.ExecutionReuse{ReusedCapabilityIDs: []string{"reuse"}},
		}
	}
	return metrics.Input{
		Findings: []metrics.FindingObservation{
			{Fingerprint: "finding-b", Finding: review.Hallazgo{Fingerprint: "finding-b", Dimension: review.DimSecurity, Status: review.StatusRefuted, Producer: review.Productor{Agente: "agent-b", Modelo: "model-b"}}},
			{Fingerprint: "finding-a", Finding: review.Hallazgo{Fingerprint: "finding-a", Dimension: review.DimLogic, Status: review.StatusConfirmed, Producer: review.Productor{Agente: "agent-a", Modelo: "model-a"}}},
		},
		Remediations: []metrics.RemediationObservation{{LogicalID: "remediation-b", Success: false}, {LogicalID: "remediation-a", Success: true}},
		Executions: []metrics.ExecutionObservation{
			{RunID: "run-b", Metrics: snapshot("run-b", "agent-b", "model-b", "EUR", &durationB), Outcomes: []store.AttemptOutcome{{RunID: "run-b", InvocationID: "attempt-b", Class: agentrun.OutcomeSuccess}}},
			{RunID: "run-a", Metrics: snapshot("run-a", "agent-a", "model-a", "USD", &durationA), Outcomes: []store.AttemptOutcome{{RunID: "run-a", InvocationID: "attempt-a", Class: agentrun.OutcomeSuccess}}},
		},
		Stages: []metrics.StageObservation{
			{Stage: "stage-b", DurationNanos: 20, LogicalRunID: "run-b"},
			{Stage: "stage-a", DurationNanos: 10, LogicalRunID: "run-a"},
		},
	}
}

func partialMetricsCoverage() metrics.Coverage {
	value := 0.5
	return metrics.Coverage{Observed: 1, Total: 2, Value: &value}
}

func partialMetricsRatio() metrics.Ratio {
	value := 0.5
	return metrics.Ratio{Numerator: 1, Denominator: 2, Value: &value, Coverage: partialMetricsCoverage()}
}

func partialMetricsMeasurement() metrics.Measurement {
	value := int64(42)
	return metrics.Measurement{Value: &value, Observed: 1, Total: 2, Coverage: partialMetricsCoverage()}
}

func decodeMetricsJSON(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("metrics JSON is invalid: %v\n%s", err, data)
	}
	return decoded
}

func assertJSONNull(t *testing.T, object map[string]any, key string) {
	t.Helper()
	value, ok := object[key]
	if !ok {
		t.Errorf("JSON key %q is missing; want explicit null", key)
		return
	}
	if value != nil {
		t.Errorf("JSON key %q = %#v, want null", key, value)
	}
}

func TestMetricsArgumentsAndHelp(t *testing.T) {
	for _, args := range [][]string{{"--unknown"}, {"argument"}, {"--json", "--unknown"}} {
		if message := validarArgumentos("metrics", args); message == "" {
			t.Errorf("metrics accepted undeclared arguments %v", args)
		}
	}
	if message := validarArgumentos("metrics", []string{"--json"}); message != "" {
		t.Fatalf("metrics rejected --json: %s", message)
	}
	var output bytes.Buffer
	if code := executeMetrics(&output, t.TempDir(), []string{"--unknown"}); code != 1 || !strings.Contains(output.String(), "accepts only --json") {
		t.Errorf("invalid metrics arguments were not rejected before store access: %d/%q", code, output.String())
	}
	if !strings.Contains(construirAyuda(), "metrics") || !escribirAyudaComando(&bytes.Buffer{}, "metrics") {
		t.Error("metrics help is not registered")
	}
	for _, flag := range []string{"--help", "-h"} {
		var stdout, stderr bytes.Buffer
		if !gestionarAyuda(&stdout, &stderr, "metrics", []string{flag}) || stdout.Len() == 0 || stderr.Len() != 0 {
			t.Errorf("metrics %s help streams = %q/%q", flag, stdout.String(), stderr.String())
		}
	}
}

func TestMetricsSubprocessDispatchAndInitialization(t *testing.T) {
	repo, _ := newMetricsRepository(t)
	stdout, stderr, err := runMetricsCLI(t, repo, "metrics")
	if processExitCode(err) != 1 || !strings.Contains(stdout, "no ha sido inicializado") || stderr != "" {
		t.Fatalf("uninitialized metrics process = %d/%q/%q", processExitCode(err), stdout, stderr)
	}
	initializeMetricsRepository(t, repo)
	cases := []struct {
		name, want string
		args       []string
		code       int
	}{
		{name: "human", args: []string{"metrics"}, want: "Metrics"},
		{name: "json", args: []string{"metrics", "--json"}, want: "executions"},
		{name: "metrics-help", args: []string{"metrics", "--help"}, want: "Purpose:"},
		{name: "topic-help", args: []string{"help", "metrics"}, want: "Usage:"},
		{name: "invalid", args: []string{"metrics", "--unknown"}, code: 1, want: "accepts '--json'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, err := runMetricsCLI(t, repo, tc.args...)
			if got := processExitCode(err); got != tc.code {
				t.Fatalf("process exit code = %d, want %d: %s", got, tc.code, stdout)
			}
			if stderr != "" || !strings.Contains(stdout, tc.want) {
				t.Fatalf("process output = %q/%q, want %q", stdout, stderr, tc.want)
			}
			if tc.name == "json" {
				_ = decodeMetricsJSON(t, []byte(stdout))
			}
		})
	}
}

func TestMetricsCLIHelper(t *testing.T) {
	if os.Getenv("VAS_SENTINEL_METRICS_HELPER") != "1" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("VAS_SENTINEL_METRICS_ARGS")), &args); err != nil {
		t.Fatal(err)
	}
	os.Args = append([]string{"sentinel"}, args...)
	main()
}

func runMetricsCLI(t *testing.T, repo string, args ...string) (string, string, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMetricsCLIHelper$")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "VAS_SENTINEL_METRICS_HELPER=1", "VAS_SENTINEL_METRICS_ARGS="+string(raw))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	return stdout.String(), stderr.String(), err
}

func processExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func initializeMetricsRepository(t *testing.T, repo string) {
	t.Helper()
	path := filepath.Join(repo, ".vas_sentinel", "vassentinel.yml")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# test configuration\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestMetricsStoreScenarios(t *testing.T) {
	cases := []struct {
		name, text string
		args       []string
		setup      func(*testing.T, string)
		want       int
	}{
		{name: "empty", want: 0, text: "WARNING: insufficient samples"},
		{name: "empty-json", args: []string{"--json"}, want: 0, text: "\"executions\""},
		{name: "historical", setup: writeHistoricalMetrics, want: 0, text: "confirmed=1"},
		{name: "mixed", setup: writeMixedMetrics, want: 0, text: "Remediation: attempts=1"},
		{name: "unreadable", setup: writeCorruptMetrics, want: 1, text: "cannot aggregate store"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, common := newMetricsRepository(t)
			if tc.setup != nil {
				tc.setup(t, common)
			}
			var output bytes.Buffer
			if code := executeMetrics(&output, repo, tc.args); code != tc.want {
				t.Fatalf("metrics exit code = %d, want %d: %s", code, tc.want, output.String())
			}
			if !strings.Contains(output.String(), tc.text) {
				t.Errorf("metrics output lacks %q: %s", tc.text, output.String())
			}
			if tc.want == 0 {
				var jsonOutput bytes.Buffer
				if code := executeMetrics(&jsonOutput, repo, []string{"--json"}); code != 0 {
					t.Fatalf("metrics JSON exit code = %d: %s", code, jsonOutput.String())
				}
				decoded := decodeMetricsJSON(t, jsonOutput.Bytes())
				for _, key := range []string{"costs", "stages"} {
					if values, ok := decoded[key].([]any); !ok || values == nil {
						t.Errorf("JSON %s is not a stable array: %#v", key, decoded[key])
					}
				}
				findings := decoded["findings"].(map[string]any)
				if tc.name == "historical" && (findings["observed"] != float64(1) || findings["effective"] != float64(1) || findings["confirmed"] != float64(1)) {
					t.Errorf("historical JSON findings = %#v", findings)
				}
				remediation := decoded["remediation"].(map[string]any)
				if tc.name == "mixed" && (remediation["attempts"] != float64(1) || remediation["succeeded"] != float64(1) || remediation["failed"] != float64(0)) {
					t.Errorf("mixed JSON remediation = %#v", decoded["remediation"])
				}
			}
		})
	}
}

func newMetricsRepository(t *testing.T) (string, string) {
	repo := t.TempDir()
	if output, err := exec.Command("git", "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	common, err := git.ObtenerGitCommonDir(repo)
	if err != nil {
		t.Fatalf("git common directory: %v", err)
	}
	return repo, common
}

func writeHistoricalMetrics(t *testing.T, common string) {
	t.Helper()
	err := review.NuevoLedger(common).GuardarRevision("historical", "message", "bucket", "model", review.Revision{
		AggregatedFindings: []review.Hallazgo{{Fingerprint: "finding", Dimension: review.DimLogic, Status: review.StatusConfirmed}},
	})
	if err != nil {
		t.Fatalf("write historical finding: %v", err)
	}
}

func writeMixedMetrics(t *testing.T, common string) {
	t.Helper()
	writeHistoricalMetrics(t, common)
	if err := ops.RegistrarEvento(common, "repair", 0, nil, ops.EventDetail{"kind": "remediation", "target": "finding", "dimension": "logic"}, ""); err != nil {
		t.Fatalf("write remediation event: %v", err)
	}
}

func writeCorruptMetrics(t *testing.T, common string) {
	t.Helper()
	path := filepath.Join(common, "vas-sentinel", "metrics", "v1", "corrupt.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not-json"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRenderMetricsReportsReopenCountsAsUnknown(t *testing.T) {
	var output bytes.Buffer
	report := completeMetricsReport()
	report.Findings.ByDimension = []metrics.DimensionAggregate{{
		Dimension: "logic", Observed: 2, Findings: 2,
		ConfirmationRate: report.Findings.ConfirmationRate,
		RefutationRate:   report.Findings.ConfirmationRate,
		OverrideRate:     report.Findings.ConfirmationRate,
	}}
	if err := renderMetrics(&output, report); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if !strings.Contains(text, "reopened=unknown (coverage 0/") {
		t.Fatalf("reopen count was rendered as a measured value: %s", text)
	}
	if strings.Contains(text, "reopened=0") {
		t.Fatalf("reopen count was rendered as a measured zero: %s", text)
	}
}

// jsonKeyOrder returns one JSON object's keys in their encoded order, which a
// map decode discards. The metrics contract fixes that order.
func jsonKeyOrder(t *testing.T, object []byte) []string {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(object))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		t.Fatalf("expected a JSON object, read %v (%v)", token, err)
	}
	keys := []string{}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			t.Fatalf("cannot read key: %v", err)
		}
		name, ok := key.(string)
		if !ok {
			t.Fatalf("key %v is not a string", key)
		}
		keys = append(keys, name)
		var discard json.RawMessage
		if err := decoder.Decode(&discard); err != nil {
			t.Fatalf("cannot skip the value of %q: %v", name, err)
		}
	}
	return keys
}

func rawJSONField(t *testing.T, object []byte, key string) []byte {
	t.Helper()
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(object, &fields); err != nil {
		t.Fatalf("cannot decode object for %q: %v", key, err)
	}
	value, ok := fields[key]
	if !ok {
		t.Fatalf("key %q is missing from %s", key, object)
	}
	return value
}

func rawJSONFirstElement(t *testing.T, array []byte) []byte {
	t.Helper()
	elements := []json.RawMessage{}
	if err := json.Unmarshal(array, &elements); err != nil {
		t.Fatalf("cannot decode array: %v", err)
	}
	if len(elements) == 0 {
		t.Fatalf("array is empty: %s", array)
	}
	return elements[0]
}

// reopenReportForJSON aggregates through the public surface so that the reopen
// coverage under test is the real one rather than a hand-built value.
func reopenReportForJSON() metrics.Report {
	observation := func(fingerprint, status string) metrics.FindingObservation {
		return metrics.FindingObservation{Fingerprint: fingerprint, Finding: review.Hallazgo{
			Fingerprint: fingerprint, Dimension: review.DimLogic, Status: status,
			Producer: review.Productor{Agente: "agent-a", Modelo: "model-a"},
		}}
	}
	return metrics.Aggregate(metrics.Input{Findings: []metrics.FindingObservation{
		observation("a", review.StatusReopened),
		observation("b", review.StatusConfirmed),
	}})
}

func assertJSONCoverage(t *testing.T, name string, object []byte, observed, total float64) {
	t.Helper()
	decoded := decodeMetricsJSON(t, object)
	if decoded["observed"] != observed || decoded["total"] != total {
		t.Errorf("%s = %#v, want observed=%v total=%v", name, decoded, observed, total)
	}
	if _, ok := decoded["value"]; !ok {
		t.Errorf("%s carries no value key: %#v", name, decoded)
	}
}

func TestMetricsJSONReportsUnobservableReopenCountsAsNull(t *testing.T) {
	var output bytes.Buffer
	if err := renderMetricsJSON(&output, reopenReportForJSON()); err != nil {
		t.Fatal(err)
	}
	findings := rawJSONField(t, output.Bytes(), "findings")
	dimension := rawJSONFirstElement(t, rawJSONField(t, findings, "by_dimension"))

	for name, object := range map[string][]byte{"findings": findings, "dimension": dimension} {
		assertJSONNull(t, decodeMetricsJSON(t, object), "reopened")
		assertJSONCoverage(t, name+".reopen_coverage", rawJSONField(t, object, "reopen_coverage"), 0, 2)
	}
}

func TestMetricsJSONKeyOrderSurvivesTheReopenCoverageField(t *testing.T) {
	var output bytes.Buffer
	if err := renderMetricsJSON(&output, reopenReportForJSON()); err != nil {
		t.Fatal(err)
	}
	if got := jsonKeyOrder(t, output.Bytes()); !reflect.DeepEqual(got, []string{"costs", "executions", "findings", "remediation", "stages"}) {
		t.Errorf("top-level key order = %v", got)
	}
	findings := rawJSONField(t, output.Bytes(), "findings")
	wantFindings := []string{"by_agent", "by_dimension", "by_model", "confirmation_rate", "confirmed", "effective", "observed", "override_rate", "overrides", "refutation_rate", "refuted", "reopen_coverage", "reopened"}
	if got := jsonKeyOrder(t, findings); !reflect.DeepEqual(got, wantFindings) {
		t.Errorf("findings key order = %v, want %v", got, wantFindings)
	}
	wantDimension := []string{"confirmation_rate", "confirmed", "dimension", "findings", "observed", "override_rate", "overrides", "refutation_rate", "refuted", "reopen_coverage", "reopened"}
	if got := jsonKeyOrder(t, rawJSONFirstElement(t, rawJSONField(t, findings, "by_dimension"))); !reflect.DeepEqual(got, wantDimension) {
		t.Errorf("dimension key order = %v, want %v", got, wantDimension)
	}
}

// TestNullableReopenCountFollowsItsCoverage covers the non-null branch. It
// cannot be reached through metrics.Aggregate: the reopen coverage basis is an
// unexported field that no production path increments, so ReopenCoverage() can
// never be Complete() for any aggregate the public surface can produce. Testing
// the projection directly proves the null is derived from the coverage rather
// than hardcoded, without reaching into unexported state to fake an aggregate.
func TestNullableReopenCountFollowsItsCoverage(t *testing.T) {
	complete, incomplete := metrics.Coverage{Observed: 2, Total: 2}, metrics.Coverage{Observed: 0, Total: 2}
	completeValue, incompleteValue := 1.0, 0.0
	complete.Value, incomplete.Value = &completeValue, &incompleteValue

	if got := nullableCount(7, complete); got == nil || *got != 7 {
		t.Errorf("complete coverage yielded %v, want 7", got)
	}
	if got := nullableCount(7, incomplete); got != nil {
		t.Errorf("incomplete coverage yielded %v, want null", *got)
	}
	if got := nullableCount(0, metrics.Coverage{}); got != nil {
		t.Errorf("absent population yielded %v, want null", *got)
	}
}
