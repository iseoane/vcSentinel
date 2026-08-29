package execution

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

type observedAdapter struct {
	observation AdapterObservation
}

func (a observedAdapter) Execute(context.Context, agentrun.LogicalJob, agentrun.InvocationEnvelope, string) (AdapterResult, error) {
	return AdapterResult{Output: "observed output", Observation: &a.observation}, nil
}

func TestControllerPersistsPerAttemptObservationAndFinalizesMetrics(t *testing.T) {
	input, output := int64(0), int64(3)
	adapter := observedAdapter{observation: AdapterObservation{
		Agent:           "acpx:claude",
		Model:           "wire/model",
		RequestedModel:  "configured/model",
		Effort:          "wire-high",
		RequestedEffort: "configured-high",
		StopReason:      "end_turn",
		Enforcement:     "none",
		Usage:           &AdapterUsage{InputTokens: &input, OutputTokens: &output},
	}}
	backing := store.NuevoStore(t.TempDir())
	controller := NewControllerWithClock(backing, adapter, fixedClock())
	handle, err := controller.Start(context.Background(), testRequest("instrumentation"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}

	inspection, err := controller.Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(inspection.Outcomes) != 1 || inspection.Outcomes[0].Observation == nil {
		t.Fatalf("outcomes = %+v, want one observed outcome", inspection.Outcomes)
	}
	got := inspection.Outcomes[0].Observation
	if got.Model != "wire/model" || got.RequestedModel != "configured/model" ||
		got.Effort != "wire-high" || got.RequestedEffort != "configured-high" {
		t.Fatalf("identity observation = %+v, want distinct requested and observed model/effort", got)
	}
	if got.Usage == nil || got.Usage.InputTokens == nil || *got.Usage.InputTokens != 0 {
		t.Fatalf("usage = %+v, want explicit zero input tokens", got.Usage)
	}
	if got.DurationNanos == nil || *got.DurationNanos < 0 {
		t.Fatalf("duration = %+v, want non-negative monotonic duration", got.DurationNanos)
	}

	metrics, err := controller.FinalizeMetrics(context.Background(), handle.RunID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.Usage == nil || metrics.Usage.InputTokens == nil || *metrics.Usage.InputTokens != 0 {
		t.Fatalf("metrics usage = %+v, want explicit zero input tokens", metrics.Usage)
	}
	if metrics.Timing == nil || metrics.Timing.TotalDurationNanos == nil {
		t.Fatalf("metrics timing = %+v, want known total duration", metrics.Timing)
	}
	if len(metrics.Identities) != 1 || metrics.Identities[0].Model != "wire/model" ||
		metrics.Identities[0].Effort != "wire-high" ||
		metrics.Identities[0].RequestedEffort != "configured-high" {
		t.Fatalf("metrics identities = %+v, want observed and configured effort provenance", metrics.Identities)
	}
	stored, err := backing.ReadExecutionMetrics(string(handle.RunID))
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.RunID != string(handle.RunID) {
		t.Fatalf("stored metrics = %+v, want immutable snapshot", stored)
	}
	if _, err := controller.FinalizeMetrics(context.Background(), handle.RunID, nil); err != nil {
		t.Fatalf("equal finalization should be idempotent: %v", err)
	}
}
func TestFinalizeMetricsRejectsAwaitingAndRetryableHeads(t *testing.T) {
	t.Run("awaiting", func(t *testing.T) {
		controller := NewControllerWithClock(store.NuevoStore(t.TempDir()), &scriptedAdapter{
			result: AdapterResult{AwaitingDecision: true},
		}, fixedClock())
		handle, err := controller.Start(context.Background(), testRequest("metrics-awaiting"), testPolicy())
		if err != nil {
			t.Fatal(err)
		}
		waitForState(t, controller, handle.RunID, agentrun.StateAwaitingDecision)
		if _, err := controller.FinalizeMetrics(context.Background(), handle.RunID, nil); !errors.Is(err, ErrMetricsNotFinal) {
			t.Fatalf("awaiting finalization error = %v, want ErrMetricsNotFinal", err)
		}
	})

	t.Run("retryable terminal", func(t *testing.T) {
		controller := NewControllerWithClock(store.NuevoStore(t.TempDir()), &scriptedAdapter{
			adapterErr: errors.New("provider failed"),
		}, fixedClock())
		handle, err := controller.Start(context.Background(), testRequest("metrics-retryable"), testPolicy())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := handle.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := controller.FinalizeMetrics(context.Background(), handle.RunID, nil); !errors.Is(err, ErrMetricsNotFinal) {
			t.Fatalf("retryable finalization error = %v, want ErrMetricsNotFinal", err)
		}
	})
}

func TestFinalizeMetricsForDispositionFreezesRetryableRun(t *testing.T) {
	controller := NewControllerWithClock(store.NuevoStore(t.TempDir()), &scriptedAdapter{
		adapterErr: errors.New("provider failed"),
	}, fixedClock())
	handle, err := controller.Start(context.Background(), testRequest("explicit-disposition"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.FinalizeMetricsForDisposition(context.Background(), handle.RunID, nil); err != nil {
		t.Fatalf("explicit finalization error = %v", err)
	}
	if _, err := controller.Retry(context.Background(), handle.RunID, 0); !errors.Is(err, ErrMetricsFinalized) {
		t.Fatalf("Retry() error = %v, want ErrMetricsFinalized after explicit disposition", err)
	}
}

func TestFoldExecutionMetricsAggregatesByAgentAndPreservesUnknownAttempts(t *testing.T) {
	firstDuration, secondDuration := 3*time.Nanosecond, 4*time.Nanosecond
	input, output := int64(2), int64(5)
	observed := func(_ string, agent string, duration time.Duration) *store.AttemptObservation {
		return &store.AttemptObservation{
			Agent:         agent,
			DurationNanos: &duration,
			Usage:         &store.ExecutionTokenUsage{InputTokens: &input, OutputTokens: &output, Source: store.ObservationSourceAdapter},
		}
	}
	complete := foldExecutionMetrics("run-fold", []store.AttemptOutcome{
		{InvocationID: "inv-1", Class: agentrun.OutcomeSuccess, Observation: observed("inv-1", "agent-a", firstDuration)},
		{InvocationID: "inv-2", Class: agentrun.OutcomeSuccess, Observation: observed("inv-2", "agent-a", secondDuration)},
	}, nil)
	if complete.Timing == nil || complete.Timing.TotalDurationNanos == nil || *complete.Timing.TotalDurationNanos != firstDuration+secondDuration {
		t.Fatalf("timing = %+v, want sum of both physical attempts", complete.Timing)
	}
	if len(complete.Timing.ByAgent) != 1 || complete.Timing.ByAgent[0].Identity.Agent != "agent-a" ||
		complete.Timing.ByAgent[0].DurationNanos != firstDuration+secondDuration {
		t.Fatalf("ByAgent = %+v, want summed truthful agent timing", complete.Timing.ByAgent)
	}
	if complete.Usage == nil || complete.Usage.InputTokens == nil || *complete.Usage.InputTokens != 2*input {
		t.Fatalf("usage = %+v, want summed usage", complete.Usage)
	}

	mixed := foldExecutionMetrics("run-fold-unknown", []store.AttemptOutcome{
		{InvocationID: "inv-orphan", Class: agentrun.OutcomeCancellation, Error: "owner loss"},
		{InvocationID: "inv-success", Class: agentrun.OutcomeSuccess, Observation: observed("inv-success", "agent-a", firstDuration)},
	}, nil)
	if mixed.Timing != nil || mixed.Usage != nil {
		t.Fatalf("mixed metrics = %+v, want unknown timing and usage after nil-observation attempt", mixed)
	}
	if len(mixed.Failures) != 1 || mixed.Failures[0].Class != store.FailureClass(agentrun.OutcomeCancellation) {
		t.Fatalf("mixed failures = %+v, want orphan cancellation retained", mixed.Failures)
	}
}

func TestControllerFinalizationPreservesSemanticInvalidOutput(t *testing.T) {
	backing := store.NuevoStore(t.TempDir())
	controller := NewControllerWithClock(backing, observedAdapter{}, fixedClock())
	handle, err := controller.Start(context.Background(), testRequest("semantic-disposition"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	failures := []store.ExecutionFailure{{InvocationID: string(handle.InvocationID), Class: store.FailureInvalidOutput, Detail: "schema_invalid"}}
	metrics, err := controller.FinalizeMetrics(context.Background(), handle.RunID, failures)
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics.Failures) != 1 || metrics.Failures[0].Class != store.FailureInvalidOutput {
		t.Fatalf("failures = %+v, want semantic invalid-output evidence", metrics.Failures)
	}
}

func TestControllerObservationDurationUsesMonotonicClock(t *testing.T) {
	adapter := observedAdapter{}
	controller := NewControllerWithClock(store.NuevoStore(t.TempDir()), adapter, func() time.Time {
		return time.Unix(1700000000, 0).UTC()
	})
	handle, err := controller.Start(context.Background(), testRequest("duration"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	inspection, err := controller.Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Outcomes[0].DurationNanos == nil {
		t.Fatal("duration missing from terminal attempt observation")
	}
}

// TestFinalizeMetricsPreservesAnExistingSnapshotOnWriteConflict covers the
// metrics-write failure path: a snapshot already exists for this run and does
// not match what the durable evidence folds to. The producer must surface the
// immutable conflict and leave the stored record exactly as it found it,
// because silently overwriting it would rewrite a measurement that later
// aggregation already treats as final.
func TestFinalizeMetricsPreservesAnExistingSnapshotOnWriteConflict(t *testing.T) {
	backing := store.NuevoStore(t.TempDir())
	controller := NewControllerWithClock(backing, observedAdapter{observation: AdapterObservation{
		Agent: "acpx:claude",
		Model: "wire/model",
	}}, fixedClock())
	handle, err := controller.Start(context.Background(), testRequest("metrics-write-conflict"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}

	existing := store.ExecutionMetrics{
		Version: store.ExecutionMetricsSchemaVersion,
		RunID:   string(handle.RunID),
		Identities: []store.ObservedExecutionIdentity{{
			Agent:  "another-producer",
			Source: store.ObservationSourceAdapter,
		}},
	}
	if err := backing.SaveExecutionMetrics(existing); err != nil {
		t.Fatal(err)
	}

	if _, err := controller.FinalizeMetrics(context.Background(), handle.RunID, nil); !errors.Is(err, store.ErrImmutableConflict) {
		t.Fatalf("finalization error = %v, want store.ErrImmutableConflict", err)
	}

	stored, err := backing.ReadExecutionMetrics(string(handle.RunID))
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || len(stored.Identities) != 1 || stored.Identities[0].Agent != "another-producer" {
		t.Fatalf("stored metrics = %+v, want the existing snapshot untouched", stored)
	}
}
