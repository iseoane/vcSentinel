package execution

import (
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
)

// TestFoldUsageSumsCacheWriteTokens pins the fold layer: cache-write counts
// sum across observations exactly like every other token field, an observed
// zero survives, and one observation missing the field makes the folded value
// unknown (nil) instead of a partial sum.
func TestFoldUsageSumsCacheWriteTokens(t *testing.T) {
	input := int64(2)
	five, seven := int64(5), int64(7)
	complete := foldExecutionMetrics("run-cache-write", []store.AttemptOutcome{
		{InvocationID: "inv-1", Class: agentrun.OutcomeSuccess, Observation: &store.AttemptObservation{
			Usage: &store.ExecutionTokenUsage{InputTokens: &input, CacheWriteInputTokens: &five, Source: store.ObservationSourceAdapter},
		}},
		{InvocationID: "inv-2", Class: agentrun.OutcomeSuccess, Observation: &store.AttemptObservation{
			Usage: &store.ExecutionTokenUsage{InputTokens: &input, CacheWriteInputTokens: &seven, Source: store.ObservationSourceAdapter},
		}},
	}, nil)
	if complete.Usage == nil || complete.Usage.CacheWriteInputTokens == nil {
		t.Fatalf("usage = %+v, want a folded cache-write count", complete.Usage)
	}
	if *complete.Usage.CacheWriteInputTokens != 12 {
		t.Errorf("CacheWriteInputTokens = %d, want 12 (5 + 7)", *complete.Usage.CacheWriteInputTokens)
	}

	mixed := foldExecutionMetrics("run-cache-write-mixed", []store.AttemptOutcome{
		{InvocationID: "inv-1", Class: agentrun.OutcomeSuccess, Observation: &store.AttemptObservation{
			Usage: &store.ExecutionTokenUsage{InputTokens: &input, CacheWriteInputTokens: &five, Source: store.ObservationSourceAdapter},
		}},
		{InvocationID: "inv-2", Class: agentrun.OutcomeSuccess, Observation: &store.AttemptObservation{
			Usage: &store.ExecutionTokenUsage{InputTokens: &input, Source: store.ObservationSourceAdapter},
		}},
	}, nil)
	if mixed.Usage == nil {
		t.Fatal("usage = nil, want the folded input tokens")
	}
	if mixed.Usage.CacheWriteInputTokens != nil {
		t.Errorf("CacheWriteInputTokens = %d, want nil (one attempt never observed it)", *mixed.Usage.CacheWriteInputTokens)
	}
	if mixed.Usage.InputTokens == nil || *mixed.Usage.InputTokens != 4 {
		t.Errorf("InputTokens = %+v, want 4", mixed.Usage.InputTokens)
	}
}

// TestStoreObservationCarriesCacheWriteTokens pins the mapping layer: an
// observed cache-write count must survive storeObservation as an independent
// clone, an observed zero must survive, and an unobserved count stays nil.
func TestStoreObservationCarriesCacheWriteTokens(t *testing.T) {
	written := int64(21)
	mapped := storeObservation(&AdapterObservation{
		StopReason: "end_turn",
		Usage:      &AdapterUsage{CacheWriteInputTokens: &written},
	})
	if mapped == nil || mapped.Usage == nil || mapped.Usage.CacheWriteInputTokens == nil {
		t.Fatalf("mapped = %+v, want a non-nil cache-write count", mapped)
	}
	if *mapped.Usage.CacheWriteInputTokens != 21 {
		t.Errorf("CacheWriteInputTokens = %d, want 21", *mapped.Usage.CacheWriteInputTokens)
	}
	written = 99
	if *mapped.Usage.CacheWriteInputTokens != 21 {
		t.Error("CacheWriteInputTokens aliases the caller's pointer, want an independent clone")
	}

	zero := int64(0)
	zeroMapped := storeObservation(&AdapterObservation{
		StopReason: "end_turn",
		Usage:      &AdapterUsage{CacheWriteInputTokens: &zero},
	})
	if zeroMapped == nil || zeroMapped.Usage == nil || zeroMapped.Usage.CacheWriteInputTokens == nil || *zeroMapped.Usage.CacheWriteInputTokens != 0 {
		t.Fatalf("zero mapped = %+v, want a non-nil pointer to the observed zero", zeroMapped)
	}

	absent := storeObservation(&AdapterObservation{StopReason: "end_turn", Usage: &AdapterUsage{}})
	if absent == nil || absent.Usage == nil {
		t.Fatal("mapped = nil, want a non-nil observation")
	}
	if absent.Usage.CacheWriteInputTokens != nil {
		t.Errorf("CacheWriteInputTokens = %d, want nil", *absent.Usage.CacheWriteInputTokens)
	}
}
