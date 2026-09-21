package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/ISeoane-Quental/vcSentinel/internal/metrics"
)

func renderMetricsJSON(out io.Writer, report metrics.Report) error {
	data, err := json.MarshalIndent(newMetricsJSONReport(report), "", "  ")
	if err != nil {
		return fmt.Errorf("metrics: cannot encode JSON: %w", err)
	}
	_, err = fmt.Fprintln(out, string(data))
	return err
}

type metricsJSONReport struct {
	Costs       []metricsJSONCost      `json:"costs"`
	Executions  metricsJSONExecutions  `json:"executions"`
	Findings    metricsJSONFindings    `json:"findings"`
	Remediation metricsJSONRemediation `json:"remediation"`
	Stages      []metricsJSONStage     `json:"stages"`
}

type metricsJSONCoverage struct {
	Observed int64    `json:"observed"`
	Total    int64    `json:"total"`
	Value    *float64 `json:"value"`
}

type metricsJSONRatio struct {
	Coverage    metricsJSONCoverage `json:"coverage"`
	Denominator int64               `json:"denominator"`
	Numerator   int64               `json:"numerator"`
	Value       *float64            `json:"value"`
}

type metricsJSONMeasurement struct {
	Coverage metricsJSONCoverage `json:"coverage"`
	Observed int64               `json:"observed"`
	Total    int64               `json:"total"`
	Value    *int64              `json:"value"`
}

type metricsJSONFindings struct {
	ByAgent          []metricsJSONAgent     `json:"by_agent"`
	ByDimension      []metricsJSONDimension `json:"by_dimension"`
	ByModel          []metricsJSONModel     `json:"by_model"`
	ConfirmationRate metricsJSONRatio       `json:"confirmation_rate"`
	Confirmed        int64                  `json:"confirmed"`
	Effective        int64                  `json:"effective"`
	Observed         int64                  `json:"observed"`
	OverrideRate     metricsJSONRatio       `json:"override_rate"`
	Overrides        int64                  `json:"overrides"`
	RefutationRate   metricsJSONRatio       `json:"refutation_rate"`
	Refuted          int64                  `json:"refuted"`
	ReopenCoverage   metricsJSONCoverage    `json:"reopen_coverage"`
	Reopened         *int64                 `json:"reopened"`
}

type metricsJSONDimension struct {
	ConfirmationRate metricsJSONRatio    `json:"confirmation_rate"`
	Confirmed        int64               `json:"confirmed"`
	Dimension        string              `json:"dimension"`
	Findings         int64               `json:"findings"`
	Observed         int64               `json:"observed"`
	OverrideRate     metricsJSONRatio    `json:"override_rate"`
	Overrides        int64               `json:"overrides"`
	RefutationRate   metricsJSONRatio    `json:"refutation_rate"`
	Refuted          int64               `json:"refuted"`
	ReopenCoverage   metricsJSONCoverage `json:"reopen_coverage"`
	Reopened         *int64              `json:"reopened"`
}

type metricsJSONModel struct {
	Confirmed      int64            `json:"confirmed"`
	Model          string           `json:"model"`
	Observed       int64            `json:"observed"`
	RefutationRate metricsJSONRatio `json:"refutation_rate"`
	Refuted        int64            `json:"refuted"`
}

type metricsJSONAgent struct {
	Agent          string           `json:"agent"`
	Confirmed      int64            `json:"confirmed"`
	Observed       int64            `json:"observed"`
	RefutationRate metricsJSONRatio `json:"refutation_rate"`
	Refuted        int64            `json:"refuted"`
}

type metricsJSONRemediation struct {
	Attempts    int64                             `json:"attempts"`
	ByDimension []metricsJSONRemediationDimension `json:"by_dimension"`
	Failed      int64                             `json:"failed"`
	Succeeded   int64                             `json:"succeeded"`
	SuccessRate metricsJSONRatio                  `json:"success_rate"`
}

type metricsJSONRemediationDimension struct {
	Attempts    int64            `json:"attempts"`
	Dimension   string           `json:"dimension"`
	Failed      int64            `json:"failed"`
	Succeeded   int64            `json:"succeeded"`
	SuccessRate metricsJSONRatio `json:"success_rate"`
}

type metricsJSONExecutions struct {
	CachedInputTokens     metricsJSONMeasurement `json:"cached_input_tokens"`
	CacheWriteInputTokens metricsJSONMeasurement `json:"cache_write_input_tokens"`
	CostCoverage          metricsJSONCoverage    `json:"cost_coverage"`
	DurationNanos         metricsJSONMeasurement `json:"duration_nanos"`
	FailedRuns            int64                  `json:"failed_runs"`
	Failures              []metricsJSONFailure   `json:"failures"`
	IdentityCoverage      metricsJSONCoverage    `json:"identity_coverage"`
	InputTokens           metricsJSONMeasurement `json:"input_tokens"`
	LogicalRuns           int64                  `json:"logical_runs"`
	MeasuredRuns          int64                  `json:"measured_runs"`
	OutputTokens          metricsJSONMeasurement `json:"output_tokens"`
	ReasoningTokens       metricsJSONMeasurement `json:"reasoning_tokens"`
	Reuse                 metricsJSONReuse       `json:"reuse"`
	RetriedRuns           int64                  `json:"retried_runs"`
	Scope                 metricsJSONScope       `json:"scope"`
	SuccessRate           metricsJSONRatio       `json:"success_rate"`
	SuccessfulRuns        int64                  `json:"successful_runs"`
	TotalTokens           metricsJSONMeasurement `json:"total_tokens"`
}

type metricsJSONReuse struct {
	Rate       metricsJSONRatio `json:"rate"`
	Recomputed int64            `json:"recomputed"`
	Reused     int64            `json:"reused"`
}

type metricsJSONScope struct {
	Affected int64               `json:"affected"`
	Coverage metricsJSONCoverage `json:"coverage"`
	Full     int64               `json:"full"`
	Unknown  int64               `json:"unknown"`
}

type metricsJSONFailure struct {
	Class    string              `json:"class"`
	Count    int64               `json:"count"`
	Source   string              `json:"source"`
	Coverage metricsJSONCoverage `json:"coverage"`
}

type metricsJSONCost struct {
	CostPerConfirmed metricsJSONRatio `json:"cost_per_confirmed"`
	Currency         string           `json:"currency"`
	ObservedRuns     int64            `json:"observed_runs"`
	TotalMicros      *int64           `json:"total_micros"`
	TotalRuns        int64            `json:"total_runs"`
}

type metricsJSONStage struct {
	Coverage metricsJSONCoverage `json:"coverage"`
	P50Nanos *int64              `json:"p50_nanos"`
	P95Nanos *int64              `json:"p95_nanos"`
	Samples  int64               `json:"samples"`
	Stage    string              `json:"stage"`
}

func newMetricsJSONReport(report metrics.Report) metricsJSONReport {
	result := metricsJSONReport{
		Costs:       make([]metricsJSONCost, len(report.Costs)),
		Executions:  newMetricsJSONExecutions(report.Executions),
		Findings:    newMetricsJSONFindings(report.Findings),
		Remediation: newMetricsJSONRemediation(report.Remediation),
		Stages:      make([]metricsJSONStage, len(report.Stages)),
	}
	for i, value := range report.Costs {
		result.Costs[i] = newMetricsJSONCost(value)
	}
	for i, value := range report.Stages {
		result.Stages[i] = newMetricsJSONStage(value)
	}
	return result
}

func newMetricsJSONCost(value metrics.CostAggregate) metricsJSONCost {
	result := metricsJSONCost{CostPerConfirmed: newMetricsJSONRatio(value.CostPerConfirmed), Currency: value.Currency, ObservedRuns: value.ObservedRuns, TotalRuns: value.TotalRuns}
	if value.TotalCoverage().Complete() {
		result.TotalMicros = &value.TotalMicros
	}
	return result
}

func newMetricsJSONFindings(value metrics.FindingsAggregate) metricsJSONFindings {
	result := metricsJSONFindings{
		ByAgent: make([]metricsJSONAgent, len(value.ByAgent)), ByDimension: make([]metricsJSONDimension, len(value.ByDimension)), ByModel: make([]metricsJSONModel, len(value.ByModel)),
		ConfirmationRate: newMetricsJSONRatio(value.ConfirmationRate), Confirmed: value.Confirmed, Effective: value.Effective, Observed: value.Observed,
		OverrideRate: newMetricsJSONRatio(value.OverrideRate), Overrides: value.Overrides, RefutationRate: newMetricsJSONRatio(value.RefutationRate), Refuted: value.Refuted,
		ReopenCoverage: newMetricsJSONCoverage(value.ReopenCoverage()), Reopened: nullableCount(value.Reopened, value.ReopenCoverage()),
	}
	for i, item := range value.ByAgent {
		result.ByAgent[i] = metricsJSONAgent{Agent: item.Agent, Confirmed: item.Confirmed, Observed: item.Observed, RefutationRate: newMetricsJSONRatio(item.RefutationRate), Refuted: item.Refuted}
	}
	for i, item := range value.ByDimension {
		result.ByDimension[i] = metricsJSONDimension{ConfirmationRate: newMetricsJSONRatio(item.ConfirmationRate), Confirmed: item.Confirmed, Dimension: item.Dimension, Findings: item.Findings, Observed: item.Observed, OverrideRate: newMetricsJSONRatio(item.OverrideRate), Overrides: item.Overrides, RefutationRate: newMetricsJSONRatio(item.RefutationRate), Refuted: item.Refuted, ReopenCoverage: newMetricsJSONCoverage(item.ReopenCoverage()), Reopened: nullableCount(item.Reopened, item.ReopenCoverage())}
	}
	for i, item := range value.ByModel {
		result.ByModel[i] = metricsJSONModel{Confirmed: item.Confirmed, Model: item.Model, Observed: item.Observed, RefutationRate: newMetricsJSONRatio(item.RefutationRate), Refuted: item.Refuted}
	}
	return result
}

func newMetricsJSONRemediation(value metrics.RemediationAggregate) metricsJSONRemediation {
	result := metricsJSONRemediation{Attempts: value.Attempts, ByDimension: make([]metricsJSONRemediationDimension, len(value.ByDimension)), Failed: value.Failed, Succeeded: value.Succeeded, SuccessRate: newMetricsJSONRatio(value.SuccessRate)}
	for i, item := range value.ByDimension {
		result.ByDimension[i] = metricsJSONRemediationDimension{Attempts: item.Attempts, Dimension: item.Dimension, Failed: item.Failed, Succeeded: item.Succeeded, SuccessRate: newMetricsJSONRatio(item.SuccessRate)}
	}
	return result
}

func newMetricsJSONExecutions(value metrics.ExecutionAggregate) metricsJSONExecutions {
	result := metricsJSONExecutions{
		CachedInputTokens: newMetricsJSONMeasurement(value.CachedInputTokens), CacheWriteInputTokens: newMetricsJSONMeasurement(value.CacheWriteInputTokens), CostCoverage: newMetricsJSONCoverage(value.CostCoverage), DurationNanos: newMetricsJSONMeasurement(value.Duration), FailedRuns: value.FailedRuns, Failures: make([]metricsJSONFailure, len(value.Failures)), IdentityCoverage: newMetricsJSONCoverage(value.IdentityCoverage), InputTokens: newMetricsJSONMeasurement(value.InputTokens), LogicalRuns: value.LogicalRuns, MeasuredRuns: value.MeasuredRuns, OutputTokens: newMetricsJSONMeasurement(value.OutputTokens), ReasoningTokens: newMetricsJSONMeasurement(value.ReasoningTokens), Reuse: metricsJSONReuse{Rate: newMetricsJSONRatio(value.Reuse.Rate), Recomputed: value.Reuse.Recomputed, Reused: value.Reuse.Reused}, RetriedRuns: value.RetriedRuns, Scope: metricsJSONScope{Affected: value.Scope.Affected, Coverage: newMetricsJSONCoverage(value.Scope.Coverage), Full: value.Scope.Full, Unknown: value.Scope.Unknown}, SuccessRate: newMetricsJSONRatio(value.SuccessRate), SuccessfulRuns: value.SuccessfulRuns, TotalTokens: newMetricsJSONMeasurement(value.TotalTokens),
	}
	for i, item := range value.Failures {
		result.Failures[i] = metricsJSONFailure{Class: item.Class, Count: item.Count, Source: item.Source, Coverage: newMetricsJSONCoverage(item.Coverage)}
	}
	return result
}

// nullableCount projects a count that only means something when its attribute
// was observable. Without complete evidence the JSON carries null rather than a
// measured zero, matching the "null unknowns" clause of the metrics contract.
// The decision is derived from the coverage, so the field starts reporting a
// real number as soon as a producer supplies that evidence.
func nullableCount(value int64, cov metrics.Coverage) *int64 {
	if !cov.Complete() {
		return nil
	}
	return &value
}

func newMetricsJSONCoverage(value metrics.Coverage) metricsJSONCoverage {
	return metricsJSONCoverage{Observed: value.Observed, Total: value.Total, Value: value.Value}
}

func newMetricsJSONRatio(value metrics.Ratio) metricsJSONRatio {
	result := metricsJSONRatio{Coverage: newMetricsJSONCoverage(value.Coverage), Denominator: value.Denominator, Numerator: value.Numerator, Value: value.Value}
	if !value.Known() {
		result.Value = nil
	}
	return result
}

func newMetricsJSONMeasurement(value metrics.Measurement) metricsJSONMeasurement {
	result := metricsJSONMeasurement{Coverage: newMetricsJSONCoverage(value.Coverage), Observed: value.Observed, Total: value.Total, Value: value.Value}
	if !value.Known() {
		result.Value = nil
	}
	return result
}

func newMetricsJSONStage(value metrics.StageAggregate) metricsJSONStage {
	result := metricsJSONStage{Coverage: newMetricsJSONCoverage(value.Coverage), Samples: value.Samples, Stage: value.Stage}
	if value.Coverage.Complete() {
		result.P50Nanos = &value.P50Nanos
		result.P95Nanos = &value.P95Nanos
	}
	return result
}
