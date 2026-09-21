package reviewexec

import (
	"context"
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/acpadapter"
)

// TestReviewAdapterCarriesCacheWriteTokens pins the mapping layer: a provider
// result carrying an observed cache-write count must survive
// observationFromResult into execution.AdapterObservation.Usage as an
// independent clone, while a result without one maps to nil — never a
// fabricated zero.
func TestReviewAdapterCarriesCacheWriteTokens(t *testing.T) {
	written := int64(13)
	adapter := NewReviewAdapter(
		richReviewStub{result: acpadapter.Result{
			Output:     "ok",
			StopReason: "end_turn",
			Usage:      &acpadapter.Usage{CacheWriteInputTokens: &written},
		}},
		"sha", nil, nil,
	)
	got, err := adapter.Execute(context.Background(), testJob(t), mustInvocation(t), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Observation == nil || got.Observation.Usage == nil || got.Observation.Usage.CacheWriteInputTokens == nil {
		t.Fatalf("observation = %+v, want a non-nil cache-write count", got.Observation)
	}
	if *got.Observation.Usage.CacheWriteInputTokens != 13 {
		t.Errorf("CacheWriteInputTokens = %d, want 13", *got.Observation.Usage.CacheWriteInputTokens)
	}
	if got.Observation.Usage.CacheWriteInputTokens == &written {
		t.Error("CacheWriteInputTokens aliases the provider result's own pointer, want a clone")
	}

	absent := NewReviewAdapter(
		richReviewStub{result: acpadapter.Result{Output: "ok", StopReason: "end_turn"}},
		"sha", nil, nil,
	)
	gotAbsent, err := absent.Execute(context.Background(), testJob(t), mustInvocation(t), "")
	if err != nil {
		t.Fatal(err)
	}
	if gotAbsent.Observation == nil {
		t.Fatal("observation = nil, want a non-nil observation")
	}
	if gotAbsent.Observation.Usage != nil && gotAbsent.Observation.Usage.CacheWriteInputTokens != nil {
		t.Errorf("CacheWriteInputTokens = %d, want nil", *gotAbsent.Observation.Usage.CacheWriteInputTokens)
	}
}
