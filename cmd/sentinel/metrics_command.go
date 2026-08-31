package main

import (
	"fmt"
	"io"
	"strings"

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
		if err := renderMetricsJSON(out, report); err != nil {
			return 1
		}
		return 0
	}
	if err := renderMetrics(out, report); err != nil {
		return 1
	}
	return 0
}

func renderMetrics(out io.Writer, report metrics.Report) error {
	var text strings.Builder
	f := report.Findings
	fmt.Fprintln(&text, "Metrics")
	fmt.Fprintf(&text, "Findings: observed=%d effective=%d confirmed=%d refuted=%d overrides=%d reopened=%s\n", f.Observed, f.Effective, f.Confirmed, f.Refuted, f.Overrides, formatCount(f.Reopened, f.ReopenCoverage()))
	fmt.Fprintf(&text, "  confirmation rate: %s\n  refutation rate: %s\n  override rate: %s\n", formatRatio(f.ConfirmationRate), formatRatio(f.RefutationRate), formatRatio(f.OverrideRate))
	for _, v := range f.ByDimension {
		fmt.Fprintf(&text, "  dimension %s: findings=%d confirmed=%d refuted=%d reopened=%s; confirmation=%s; refutation=%s; override=%s\n", v.Dimension, v.Findings, v.Confirmed, v.Refuted, formatCount(v.Reopened, v.ReopenCoverage()), formatRatio(v.ConfirmationRate), formatRatio(v.RefutationRate), formatRatio(v.OverrideRate))
	}
	for _, v := range f.ByModel {
		fmt.Fprintf(&text, "  model %s: observed=%d confirmed=%d refuted=%d; refutation=%s\n", v.Model, v.Observed, v.Confirmed, v.Refuted, formatRatio(v.RefutationRate))
	}
	for _, v := range f.ByAgent {
		fmt.Fprintf(&text, "  agent %s: observed=%d confirmed=%d refuted=%d; refutation=%s\n", v.Agent, v.Observed, v.Confirmed, v.Refuted, formatRatio(v.RefutationRate))
	}
	r := report.Remediation
	fmt.Fprintf(&text, "Remediation: attempts=%d succeeded=%d failed=%d; success rate=%s\n", r.Attempts, r.Succeeded, r.Failed, formatRatio(r.SuccessRate))
	e := report.Executions
	fmt.Fprintf(&text, "Executions: logical runs=%d measured=%d successful=%d failed=%d retried=%d; success rate=%s\n", e.LogicalRuns, e.MeasuredRuns, e.SuccessfulRuns, e.FailedRuns, e.RetriedRuns, formatRatio(e.SuccessRate))
	fmt.Fprintf(&text, "  duration (nanoseconds): %s\n  input tokens: %s\n  output tokens: %s\n  total tokens: %s\n  cached input tokens: %s\n  reasoning tokens: %s\n", formatMeasurement(e.Duration), formatMeasurement(e.InputTokens), formatMeasurement(e.OutputTokens), formatMeasurement(e.TotalTokens), formatMeasurement(e.CachedInputTokens), formatMeasurement(e.ReasoningTokens))
	fmt.Fprintf(&text, "  cost coverage: %s\n  identity coverage: %s\n  reuse: reused=%d recomputed=%d rate=%s\n  scope: full=%d affected=%d unknown=%d coverage=%s\n", formatCoverage(e.CostCoverage), formatCoverage(e.IdentityCoverage), e.Reuse.Reused, e.Reuse.Recomputed, formatRatio(e.Reuse.Rate), e.Scope.Full, e.Scope.Affected, e.Scope.Unknown, formatCoverage(e.Scope.Coverage))
	for _, v := range report.Costs {
		fmt.Fprintf(&text, "Cost %s: total=%s micros observed_runs=%d/%d; per confirmed=%s\n", v.Currency, formatCostTotal(v), v.ObservedRuns, v.TotalRuns, formatRatio(v.CostPerConfirmed))
	}
	for _, v := range report.Stages {
		fmt.Fprintf(&text, "Stage %s: samples=%d p50=%s nanoseconds p95=%s nanoseconds; coverage=%s\n", v.Stage, v.Samples, formatStageValue(v.P50Nanos, v.Coverage), formatStageValue(v.P95Nanos, v.Coverage), formatCoverage(v.Coverage))
	}
	// Report the evidence gap, not a reason for it. A Report carries counts and
	// coverage and nothing about which producers exist, so any claim here about
	// why the evidence is missing would be an assertion this layer cannot check
	// and would go stale the day a producer appears. An absent population has
	// nothing missing, so it warns about nothing.
	if reopen := f.ReopenCoverage(); reopen.Total > 0 && !reopen.Complete() {
		fmt.Fprintf(&text, "WARNING: reopen evidence is missing for %d of %d findings; reopen counts are reported as unknown (coverage %s).\n", reopen.Total-reopen.Observed, reopen.Total, formatCoverage(reopen))
	}
	if report.HasIncompleteEvidence() {
		fmt.Fprintln(&text, "WARNING: insufficient samples or partial evidence; unknown values are shown as unknown and never as zero.")
	} else {
		fmt.Fprintln(&text, "Warnings: none.")
	}
	_, err := io.WriteString(out, text.String())
	return err
}

func formatCoverage(v metrics.Coverage) string {
	if !v.Known() {
		return fmt.Sprintf("%d/%d (unknown)", v.Observed, v.Total)
	}
	return fmt.Sprintf("%d/%d (%.2f%%)", v.Observed, v.Total, *v.Value*100)
}

func formatRatio(v metrics.Ratio) string {
	if !v.Known() {
		return fmt.Sprintf("%d/%d (unknown; coverage %s)", v.Numerator, v.Denominator, formatCoverage(v.Coverage))
	}
	return fmt.Sprintf("%d/%d (%.2f%%; coverage %s)", v.Numerator, v.Denominator, *v.Value*100, formatCoverage(v.Coverage))
}

// formatCount presents a count that only means something when its attribute was
// observable for the whole population. Under partial evidence the count is a
// lower bound rather than a measurement, so it reads unknown for the same reason
// a partially covered measurement does; the coverage still shows the evidence.
func formatCount(value int64, cov metrics.Coverage) string {
	if !cov.Complete() {
		return fmt.Sprintf("unknown (coverage %s)", formatCoverage(cov))
	}
	return fmt.Sprintf("%d (coverage %s)", value, formatCoverage(cov))
}

func formatMeasurement(v metrics.Measurement) string {
	if !v.Known() {
		return fmt.Sprintf("unknown (coverage %s)", formatCoverage(v.Coverage))
	}
	return fmt.Sprintf("%d (coverage %s)", *v.Value, formatCoverage(v.Coverage))
}

func formatCostTotal(v metrics.CostAggregate) string {
	if !v.TotalCoverage().Complete() {
		return "unknown"
	}
	return fmt.Sprintf("%d", v.TotalMicros)
}

func formatStageValue(value int64, coverage metrics.Coverage) string {
	if !coverage.Complete() {
		return "unknown"
	}
	return fmt.Sprintf("%d", value)
}
