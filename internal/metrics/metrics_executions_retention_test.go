// T9.5 aggregate invariance for retained-only snapshots: once retention
// collects an execution directory, its snapshot is the only surviving
// evidence for that run. The aggregates must read it exactly as they read
// the live stream, or retention moves the measurement it promises to leave
// untouched.
//
// A snapshot exists only for terminal runs (finalization refuses anything
// else with ErrMetricsNotFinal), so a retained-only snapshot with no
// failures IS the terminal-success evidence, not an unknown outcome.
package metrics

import (
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/store"
)

// TestAggregateCountsRetainedOnlySuccessSnapshot proves a collected
// successful run keeps counting as successful through its snapshot alone.
func TestAggregateCountsRetainedOnlySuccessSnapshot(t *testing.T) {
	report := Aggregate(Input{Executions: []ExecutionObservation{{
		RunID:   "retained-success",
		Metrics: &store.ExecutionMetrics{Version: store.ExecutionMetricsSchemaVersion, RunID: "retained-success"},
	}}})
	executions := report.Executions
	if executions.LogicalRuns != 1 || executions.MeasuredRuns != 1 {
		t.Fatalf("logical=%d measured=%d, want 1/1", executions.LogicalRuns, executions.MeasuredRuns)
	}
	if executions.SuccessfulRuns != 1 {
		t.Fatalf("successful=%d, want 1 for a retained-only snapshot without failures", executions.SuccessfulRuns)
	}
	if executions.FailedRuns != 0 {
		t.Fatalf("failed=%d, want 0", executions.FailedRuns)
	}
}

// TestAggregateCountsRetainedOnlyFailureSnapshot proves a collected failed
// run keeps counting as failed with its failure class through its snapshot
// alone.
func TestAggregateCountsRetainedOnlyFailureSnapshot(t *testing.T) {
	report := Aggregate(Input{Executions: []ExecutionObservation{{
		RunID: "retained-failure",
		Metrics: &store.ExecutionMetrics{
			Version:  store.ExecutionMetricsSchemaVersion,
			RunID:    "retained-failure",
			Failures: []store.ExecutionFailure{{Class: store.FailureInvalidOutput}},
		},
	}}})
	executions := report.Executions
	if executions.LogicalRuns != 1 || executions.MeasuredRuns != 1 {
		t.Fatalf("logical=%d measured=%d, want 1/1", executions.LogicalRuns, executions.MeasuredRuns)
	}
	if executions.FailedRuns != 1 {
		t.Fatalf("failed=%d, want 1 for a retained-only snapshot with failures", executions.FailedRuns)
	}
	if executions.SuccessfulRuns != 0 {
		t.Fatalf("successful=%d, want 0", executions.SuccessfulRuns)
	}
	// Exclusive: exactly one failure entry, the snapshot's class counted
	// once. Extra or duplicated entries would still satisfy a
	// contains-check while moving the reported measurement.
	if len(executions.Failures) != 1 {
		t.Fatalf("failures = %+v, want exactly one entry", executions.Failures)
	}
	if executions.Failures[0].Class != string(store.FailureInvalidOutput) || executions.Failures[0].Count != 1 {
		t.Fatalf("failures = %+v, want {invalid_output 1}", executions.Failures)
	}
}
