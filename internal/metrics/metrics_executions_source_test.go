// FU-8 failure source tagging: every row of the failure breakdown states
// which population produced its class and the evidence behind its count.
// Outcome classes come from the live attempt stream, which observes every
// logical run; semantic classes come from producer-reported metrics
// snapshots, which exist only for measured runs. The per-class tag was
// chosen over splitting the breakdown into two lists (see FailureAggregate).
package metrics

import (
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// TestFailureSourceTagsPopulations proves the source tag and coverage
// denominator across the three populations: an unmeasured run with a live
// failure outcome, a measured run whose snapshot reports a semantic failure,
// and a second unmeasured run with no snapshot at all.
func TestFailureSourceTagsPopulations(t *testing.T) {
	report := Aggregate(Input{Executions: []ExecutionObservation{
		{
			RunID: "run-outcome",
			Outcomes: []store.AttemptOutcome{{
				RunID:        "run-outcome",
				InvocationID: "a1",
				Class:        agentrun.OutcomeFailure,
			}},
		},
		{
			RunID: "run-semantic",
			Metrics: &store.ExecutionMetrics{
				Version:  store.ExecutionMetricsSchemaVersion,
				RunID:    "run-semantic",
				Failures: []store.ExecutionFailure{{Class: store.FailureInvalidOutput}},
			},
		},
		{
			RunID: "run-unmeasured",
			Outcomes: []store.AttemptOutcome{{
				RunID:        "run-unmeasured",
				InvocationID: "u1",
				Class:        agentrun.OutcomeTimeout,
			}},
		},
	}})
	executions := report.Executions
	if executions.LogicalRuns != 3 || executions.MeasuredRuns != 1 {
		t.Fatalf("logical=%d measured=%d, want 3/1", executions.LogicalRuns, executions.MeasuredRuns)
	}
	failures := make(map[string]FailureAggregate, len(executions.Failures))
	for _, failure := range executions.Failures {
		if _, exists := failures[failure.Class]; exists {
			t.Fatalf("failures = %+v, want one entry per class", executions.Failures)
		}
		failures[failure.Class] = failure
	}
	// A live failure outcome is observed for every logical run, so its
	// coverage covers the full logical population.
	outcome := failures[string(agentrun.OutcomeFailure)]
	if outcome.Source != "outcome" {
		t.Fatalf("failure source = %q, want \"outcome\"", outcome.Source)
	}
	if outcome.Coverage.Observed != 3 || outcome.Coverage.Total != 3 {
		t.Fatalf("failure coverage = %+v, want observed 3 of 3 logical runs", outcome.Coverage)
	}
	// A snapshot-reported semantic class is observed only where metrics were
	// measured, while the denominator still reports the whole population.
	semantic := failures[string(store.FailureInvalidOutput)]
	if semantic.Source != "semantic" {
		t.Fatalf("invalid_output source = %q, want \"semantic\"", semantic.Source)
	}
	if semantic.Coverage.Observed != 1 || semantic.Coverage.Total != 3 {
		t.Fatalf("invalid_output coverage = %+v, want observed 1 of 3 logical runs", semantic.Coverage)
	}
	if timeout := failures[string(agentrun.OutcomeTimeout)]; timeout.Source != "outcome" {
		t.Fatalf("timeout source = %q, want \"outcome\"", timeout.Source)
	}
}
