package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/metrics"
)

type metricsFlags struct{ jsonOut bool }

func parseMetricsArgs(args []string) (metricsFlags, error) {
	flags := metricsFlags{}
	for _, arg := range args {
		if arg != "--json" {
			return flags, fmt.Errorf("sentinel metrics accepts only --json, received %q", arg)
		}
		flags.jsonOut = true
	}
	return flags, nil
}

func executeMetrics(out io.Writer, worktree string, args []string) int {
	flags, err := parseMetricsArgs(args)
	if err != nil {
		fmt.Fprintf(out, "metrics: %v\n", err)
		return 1
	}
	commonDir, err := git.ObtenerGitCommonDir(worktree)
	if err != nil {
		fmt.Fprintf(out, "metrics: cannot resolve Git common directory: %v\n", err)
		return 1
	}
	report, err := metrics.AggregateStore(commonDir)
	if err != nil {
		fmt.Fprintf(out, "metrics: cannot aggregate store: %v\n", err)
		return 1
	}
	if flags.jsonOut {
		return renderMetricsJSON(out, report)
	}
	renderMetrics(out, report)
	return 0
}

func renderMetricsJSON(out io.Writer, report metrics.Report) int {
	raw, err := json.Marshal(report)
	if err != nil {
		fmt.Fprintf(out, "metrics: cannot encode JSON: %v\n", err)
		return 1
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		fmt.Fprintf(out, "metrics: cannot normalize JSON: %v\n", err)
		return 1
	}
	data, err := json.MarshalIndent(normalizeMetricsJSON(value), "", "  ")
	if err != nil {
		fmt.Fprintf(out, "metrics: cannot encode normalized JSON: %v\n", err)
		return 1
	}
	if _, err := fmt.Fprintln(out, string(data)); err != nil {
		return 1
	}
	return 0
}

func normalizeMetricsJSON(value any) any {
	switch value := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, nested := range value {
			key = snakeCase(key)
			if key == "duration" {
				key = "duration_nanos"
			}
			if nested == nil && metricArrayKey(key) {
				nested = []any{}
			}
			result[key] = normalizeMetricsJSON(nested)
		}
		if _, hasValue := result["value"]; hasValue {
			if coverage, ok := result["coverage"].(map[string]any); ok {
				observed, observedOK := jsonNumber(coverage["observed"])
				total, totalOK := jsonNumber(coverage["total"])
				if coverage["value"] == nil || (observedOK && totalOK && observed < total) {
					result["value"] = nil
				}
			}
		}
		return result
	case []any:
		for i := range value {
			value[i] = normalizeMetricsJSON(value[i])
		}
	}
	return value
}

func jsonNumber(value any) (float64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	result, err := number.Float64()
	return result, err == nil
}

func metricArrayKey(key string) bool {
	switch key {
	case "by_agent", "by_dimension", "by_model", "costs", "failures", "stages":
		return true
	default:
		return false
	}
}

func snakeCase(value string) string {
	var result strings.Builder
	for i, character := range value {
		if unicode.IsUpper(character) {
			if i > 0 {
				result.WriteByte('_')
			}
			result.WriteRune(unicode.ToLower(character))
			continue
		}
		result.WriteRune(character)
	}
	return result.String()
}

func renderMetrics(out io.Writer, report metrics.Report) {
	f := report.Findings
	fmt.Fprintln(out, "Metrics")
	fmt.Fprintf(out, "Findings: observed=%d effective=%d confirmed=%d refuted=%d overrides=%d reopened=%d\n", f.Observed, f.Effective, f.Confirmed, f.Refuted, f.Overrides, f.Reopened)
	fmt.Fprintf(out, "  confirmation rate: %s\n  refutation rate: %s\n  override rate: %s\n", formatRatio(f.ConfirmationRate), formatRatio(f.RefutationRate), formatRatio(f.OverrideRate))
	for _, v := range f.ByDimension {
		fmt.Fprintf(out, "  dimension %s: findings=%d confirmed=%d refuted=%d; confirmation=%s; refutation=%s; override=%s\n", v.Dimension, v.Findings, v.Confirmed, v.Refuted, formatRatio(v.ConfirmationRate), formatRatio(v.RefutationRate), formatRatio(v.OverrideRate))
	}
	for _, v := range f.ByModel {
		fmt.Fprintf(out, "  model %s: observed=%d confirmed=%d refuted=%d; refutation=%s\n", v.Model, v.Observed, v.Confirmed, v.Refuted, formatRatio(v.RefutationRate))
	}
	for _, v := range f.ByAgent {
		fmt.Fprintf(out, "  agent %s: observed=%d confirmed=%d refuted=%d; refutation=%s\n", v.Agent, v.Observed, v.Confirmed, v.Refuted, formatRatio(v.RefutationRate))
	}
	r := report.Remediation
	fmt.Fprintf(out, "Remediation: attempts=%d succeeded=%d failed=%d; success rate=%s\n", r.Attempts, r.Succeeded, r.Failed, formatRatio(r.SuccessRate))
	e := report.Executions
	fmt.Fprintf(out, "Executions: logical runs=%d measured=%d successful=%d failed=%d retried=%d; success rate=%s\n", e.LogicalRuns, e.MeasuredRuns, e.SuccessfulRuns, e.FailedRuns, e.RetriedRuns, formatRatio(e.SuccessRate))
	fmt.Fprintf(out, "  duration (nanoseconds): %s\n  input tokens: %s\n  output tokens: %s\n  total tokens: %s\n  cached input tokens: %s\n  reasoning tokens: %s\n", formatMeasurement(e.Duration), formatMeasurement(e.InputTokens), formatMeasurement(e.OutputTokens), formatMeasurement(e.TotalTokens), formatMeasurement(e.CachedInputTokens), formatMeasurement(e.ReasoningTokens))
	fmt.Fprintf(out, "  cost coverage: %s\n  identity coverage: %s\n  reuse: reused=%d recomputed=%d rate=%s\n  scope: full=%d affected=%d unknown=%d coverage=%s\n", formatCoverage(e.CostCoverage), formatCoverage(e.IdentityCoverage), e.Reuse.Reused, e.Reuse.Recomputed, formatRatio(e.Reuse.Rate), e.Scope.Full, e.Scope.Affected, e.Scope.Unknown, formatCoverage(e.Scope.Coverage))
	for _, v := range report.Costs {
		fmt.Fprintf(out, "Cost %s: total=%d micros observed_runs=%d/%d; per confirmed=%s\n", v.Currency, v.TotalMicros, v.ObservedRuns, v.TotalRuns, formatRatio(v.CostPerConfirmed))
	}
	for _, v := range report.Stages {
		fmt.Fprintf(out, "Stage %s: samples=%d p50=%d nanoseconds p95=%d nanoseconds; coverage=%s\n", v.Stage, v.Samples, v.P50Nanos, v.P95Nanos, formatCoverage(v.Coverage))
	}
	if metricsInsufficient(report) {
		fmt.Fprintln(out, "WARNING: insufficient samples or partial evidence; unknown values are shown as unknown and never as zero.")
	} else {
		fmt.Fprintln(out, "Warnings: none.")
	}
}

func formatCoverage(v metrics.Coverage) string {
	if v.Value == nil {
		return fmt.Sprintf("%d/%d (unknown)", v.Observed, v.Total)
	}
	return fmt.Sprintf("%d/%d (%.2f%%)", v.Observed, v.Total, *v.Value*100)
}

func formatRatio(v metrics.Ratio) string {
	if v.Value == nil || v.Coverage.Value == nil || v.Coverage.Observed < v.Coverage.Total {
		return fmt.Sprintf("%d/%d (unknown; coverage %s)", v.Numerator, v.Denominator, formatCoverage(v.Coverage))
	}
	return fmt.Sprintf("%d/%d (%.2f%%; coverage %s)", v.Numerator, v.Denominator, *v.Value*100, formatCoverage(v.Coverage))
}

func formatMeasurement(v metrics.Measurement) string {
	if v.Value == nil {
		return fmt.Sprintf("unknown (coverage %s)", formatCoverage(v.Coverage))
	}
	return fmt.Sprintf("%d (coverage %s)", *v.Value, formatCoverage(v.Coverage))
}

func metricsInsufficient(r metrics.Report) bool {
	f, e := r.Findings, r.Executions
	for _, v := range f.ByDimension {
		if v.ConfirmationRate.Value == nil || v.ConfirmationRate.Coverage.Observed < v.ConfirmationRate.Coverage.Total || v.RefutationRate.Value == nil || v.RefutationRate.Coverage.Observed < v.RefutationRate.Coverage.Total || v.OverrideRate.Value == nil || v.OverrideRate.Coverage.Observed < v.OverrideRate.Coverage.Total {
			return true
		}
	}
	for _, v := range r.Costs {
		if v.CostPerConfirmed.Value == nil || v.CostPerConfirmed.Coverage.Observed < v.CostPerConfirmed.Coverage.Total {
			return true
		}
	}
	for _, v := range r.Stages {
		if v.Coverage.Value == nil || v.Coverage.Observed < v.Coverage.Total {
			return true
		}
	}
	return f.Observed == 0 || r.Remediation.Attempts == 0 || e.LogicalRuns == 0 || f.ConfirmationRate.Value == nil || f.RefutationRate.Value == nil || f.OverrideRate.Value == nil || r.Remediation.SuccessRate.Value == nil || e.SuccessRate.Value == nil || e.Duration.Value == nil || e.InputTokens.Value == nil || e.OutputTokens.Value == nil || e.TotalTokens.Value == nil || e.CachedInputTokens.Value == nil || e.ReasoningTokens.Value == nil || e.CostCoverage.Value == nil || e.IdentityCoverage.Value == nil || e.Reuse.Rate.Value == nil || e.Scope.Coverage.Value == nil || e.CostCoverage.Observed < e.CostCoverage.Total || e.IdentityCoverage.Observed < e.IdentityCoverage.Total || e.Scope.Coverage.Observed < e.Scope.Coverage.Total
}
