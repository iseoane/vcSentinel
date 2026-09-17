package store

import (
	"errors"
	"fmt"
	"testing"
)

// TestReadExecutionMetricsDecodesRecordWithoutCacheWriteAsNil pins backward
// compatibility: a record persisted before the cache-write field existed
// carries no such member and must still decode, with the new field nil —
// not zero. The store is append-only, so old bytes are never rewritten.
func TestReadExecutionMetricsDecodesRecordWithoutCacheWriteAsNil(t *testing.T) {
	store := NewStore(t.TempDir())
	job := testJob()
	if err := store.CreateRun(job, RunPolicy{ID: "policy-id"}); err != nil {
		t.Fatal(err)
	}
	data := fmt.Sprintf(`{
  "version": 1,
  "run_id": %q,
  "usage": {"input_tokens": 5, "cached_input_tokens": 3, "source": "adapter"}
}`, string(job.RunID()))
	writeExecutionMetricsTestRecord(t, store, string(job.RunID()), []byte(data))

	metrics, err := store.ReadExecutionMetrics(string(job.RunID()))
	if err != nil {
		t.Fatalf("ReadExecutionMetrics() error = %v", err)
	}
	if metrics.Usage == nil {
		t.Fatal("Usage = nil, want the persisted usage member")
	}
	if metrics.Usage.CacheWriteInputTokens != nil {
		t.Errorf("CacheWriteInputTokens = %d, want nil for a pre-change record", *metrics.Usage.CacheWriteInputTokens)
	}
	if metrics.Usage.InputTokens == nil || *metrics.Usage.InputTokens != 5 {
		t.Errorf("InputTokens = %+v, want 5 (existing measurements keep their values)", metrics.Usage.InputTokens)
	}
}

// TestSaveAndReadExecutionMetricsRoundTripsCacheWriteTokens pins that an
// observed cache-write count (including an observed zero) survives the
// retained record byte-identical, following the pointer convention: nil when
// never observed, non-nil even when zero.
func TestSaveAndReadExecutionMetricsRoundTripsCacheWriteTokens(t *testing.T) {
	store := NewStore(t.TempDir())
	job := testJob()
	if err := store.CreateRun(job, RunPolicy{ID: "policy-id"}); err != nil {
		t.Fatal(err)
	}
	written := int64(11)
	metrics := ExecutionMetrics{
		Version: ExecutionMetricsSchemaVersion,
		RunID:   string(job.RunID()),
		Usage:   &ExecutionTokenUsage{CacheWriteInputTokens: &written, Source: ObservationSourceAdapter},
	}
	if err := store.SaveExecutionMetrics(metrics); err != nil {
		t.Fatalf("SaveExecutionMetrics() error = %v", err)
	}
	got, err := store.ReadExecutionMetrics(string(job.RunID()))
	if err != nil {
		t.Fatalf("ReadExecutionMetrics() error = %v", err)
	}
	if got.Usage == nil || got.Usage.CacheWriteInputTokens == nil || *got.Usage.CacheWriteInputTokens != 11 {
		t.Fatalf("CacheWriteInputTokens = %+v, want 11", got.Usage)
	}
	if *got.Usage.CacheWriteInputTokens == written && got.Usage.CacheWriteInputTokens == &written {
		t.Error("CacheWriteInputTokens aliases the saved pointer, want an independent decode")
	}
}

// TestSaveExecutionMetricsRejectsNegativeCacheWriteTokens pins that the new
// field joins the token-value coverage list: a negative cache-write count is
// corrupt evidence, exactly like every other negative token count.
func TestSaveExecutionMetricsRejectsNegativeCacheWriteTokens(t *testing.T) {
	store := NewStore(t.TempDir())
	job := testJob()
	if err := store.CreateRun(job, RunPolicy{ID: "policy-id"}); err != nil {
		t.Fatal(err)
	}
	negative := int64(-1)
	metrics := ExecutionMetrics{
		Version: ExecutionMetricsSchemaVersion,
		RunID:   string(job.RunID()),
		Usage:   &ExecutionTokenUsage{CacheWriteInputTokens: &negative, Source: ObservationSourceAdapter},
	}
	if err := store.SaveExecutionMetrics(metrics); !errors.Is(err, ErrExecutionMetricsCorrupt) {
		t.Fatalf("SaveExecutionMetrics() error = %v, want corrupt metrics for a negative cache-write count", err)
	}
}

// TestCloneAttemptObservationDeepCopiesCacheWriteTokens pins the clone
// contract for the new field: the clone is independent of the original, an
// observed zero survives, and nil stays nil.
func TestCloneAttemptObservationDeepCopiesCacheWriteTokens(t *testing.T) {
	written := int64(9)
	original := &AttemptObservation{Usage: &ExecutionTokenUsage{CacheWriteInputTokens: &written, Source: ObservationSourceAdapter}}
	clone := cloneAttemptObservation(original)
	if clone == nil || clone.Usage == nil || clone.Usage.CacheWriteInputTokens == nil {
		t.Fatalf("clone = %+v, want a non-nil cloned cache-write count", clone)
	}
	if clone.Usage.CacheWriteInputTokens == original.Usage.CacheWriteInputTokens {
		t.Fatal("clone aliases the original pointer, want an independent copy")
	}
	*clone.Usage.CacheWriteInputTokens = 99
	if *original.Usage.CacheWriteInputTokens != 9 {
		t.Errorf("original = %d, want 9 (unaffected by mutating the clone)", *original.Usage.CacheWriteInputTokens)
	}

	zero := int64(0)
	zeroClone := cloneAttemptObservation(&AttemptObservation{Usage: &ExecutionTokenUsage{CacheWriteInputTokens: &zero, Source: ObservationSourceAdapter}})
	if zeroClone == nil || zeroClone.Usage == nil || zeroClone.Usage.CacheWriteInputTokens == nil || *zeroClone.Usage.CacheWriteInputTokens != 0 {
		t.Fatalf("zero clone = %+v, want a non-nil pointer to the observed zero", zeroClone)
	}

	nilClone := cloneAttemptObservation(&AttemptObservation{Usage: &ExecutionTokenUsage{Source: ObservationSourceAdapter}})
	if nilClone == nil || nilClone.Usage == nil {
		t.Fatal("clone = nil, want a non-nil observation")
	}
	if nilClone.Usage.CacheWriteInputTokens != nil {
		t.Errorf("CacheWriteInputTokens = %d, want nil", *nilClone.Usage.CacheWriteInputTokens)
	}
}
