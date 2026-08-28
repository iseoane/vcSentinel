package store

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestSaveAndReadExecutionMetricsRoundTrip(t *testing.T) {
	store := NuevoStore(t.TempDir())
	job := testJob()
	if err := store.CreateRun(job, RunPolicy{ID: "policy-id"}); err != nil {
		t.Fatal(err)
	}

	total := 2 * time.Second
	input := int64(1200)
	output := int64(300)
	cached := int64(25)
	reasoning := int64(44)
	savedDuration := 500 * time.Millisecond
	savedInput := int64(400)
	savedOutput := int64(100)
	metrics := ExecutionMetrics{
		Version: ExecutionMetricsSchemaVersion,
		RunID:   string(job.RunID()),
		Identities: []ObservedExecutionIdentity{{
			InvocationID: "attempt-1",
			Agent:        "reviewer",
			Model:        "observed-model",
			Effort:       "high",
			Source:       ObservationSourceAdapter,
		}},
		Timing: &ExecutionTiming{
			TotalDurationNanos: &total,
			ByCapability: []CapabilityTiming{{
				CapabilityID:  "capability-a",
				DurationNanos: 450 * time.Millisecond,
			}},
			ByAgent: []AgentTiming{{
				Identity: ObservedExecutionIdentity{
					Agent:  "reviewer",
					Model:  "observed-model",
					Effort: "high",
					Source: ObservationSourceAdapter,
				},
				DurationNanos: 1500 * time.Millisecond,
			}},
		},
		Usage: &ExecutionTokenUsage{
			InputTokens:       &input,
			OutputTokens:      &output,
			CachedInputTokens: &cached,
			ReasoningTokens:   &reasoning,
			Source:            ObservationSourceAdapter,
		},
		Cost: &ExecutionCost{
			AmountMicros: 123456,
			Currency:     "USD",
			Provenance: CostProvenance{
				Source:    CostSourceEstimate,
				Reference: "pricing/v1",
			},
		},
		Scope: &ExecutionScope{
			Kind: ScopeAffected,
			Savings: &ExecutionSavings{
				DurationNanos: &savedDuration,
				InputTokens:   &savedInput,
				OutputTokens:  &savedOutput,
				Cost: &ExecutionCost{
					AmountMicros: 4200,
					Currency:     "USD",
					Provenance: CostProvenance{
						Source:    CostSourceEstimate,
						Reference: "pricing/v1",
					},
				},
			},
		},
		Reuse: &ExecutionReuse{
			ReusedCapabilityIDs:     []string{"capability-cache"},
			RecomputedCapabilityIDs: []string{"capability-live"},
		},
		Failures: []ExecutionFailure{{
			InvocationID: "attempt-1",
			Class:        FailureInvalidOutput,
			Detail:       "missing manifest",
		}},
	}

	if err := store.SaveExecutionMetrics(metrics); err != nil {
		t.Fatalf("SaveExecutionMetrics() error = %v", err)
	}
	got, err := store.ReadExecutionMetrics(string(job.RunID()))
	if err != nil {
		t.Fatalf("ReadExecutionMetrics() error = %v", err)
	}
	if !reflect.DeepEqual(got, &metrics) {
		t.Fatalf("ReadExecutionMetrics() = %#v, want %#v", got, &metrics)
	}

	directory, err := store.executionDir(string(job.RunID()))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(directory, "metrics.json"))
	if err != nil {
		t.Fatal(err)
	}
	wantJSON := fmt.Sprintf(`{
  "version": 1,
  "run_id": %q,
  "identities": [
    {
      "invocation_id": "attempt-1",
      "agent": "reviewer",
      "model": "observed-model",
      "effort": "high",
      "source": "adapter"
    }
  ],
  "timing": {
    "total_duration_ns": 2000000000,
    "by_capability": [
      {
        "capability_id": "capability-a",
        "duration_ns": 450000000
      }
    ],
    "by_agent": [
      {
        "identity": {
          "agent": "reviewer",
          "model": "observed-model",
          "effort": "high",
          "source": "adapter"
        },
        "duration_ns": 1500000000
      }
    ]
  },
  "usage": {
    "input_tokens": 1200,
    "output_tokens": 300,
    "cached_input_tokens": 25,
    "reasoning_tokens": 44,
    "source": "adapter"
  },
  "cost": {
    "amount_micros": 123456,
    "currency": "USD",
    "provenance": {
      "source": "estimate",
      "reference": "pricing/v1"
    }
  },
  "scope": {
    "kind": "affected",
    "savings": {
      "duration_ns": 500000000,
      "input_tokens": 400,
      "output_tokens": 100,
      "cost": {
        "amount_micros": 4200,
        "currency": "USD",
        "provenance": {
          "source": "estimate",
          "reference": "pricing/v1"
        }
      }
    }
  },
  "reuse": {
    "reused_capability_ids": [
      "capability-cache"
    ],
    "recomputed_capability_ids": [
      "capability-live"
    ]
  },
  "failures": [
    {
      "invocation_id": "attempt-1",
      "class": "invalid_output",
      "detail": "missing manifest"
    }
  ]
}`, string(job.RunID()))
	if !bytes.Equal(data, []byte(wantJSON)) {
		t.Fatalf("metrics.json = %s, want stable JSON %s", data, wantJSON)
	}
}

func TestReadExecutionMetricsTreatsHistoricalExecutionAsAbsent(t *testing.T) {
	store := NuevoStore(t.TempDir())
	job := testJob()
	if err := store.CreateRun(job, RunPolicy{ID: "policy-id"}); err != nil {
		t.Fatal(err)
	}

	metrics, err := store.ReadExecutionMetrics(string(job.RunID()))
	if err != nil {
		t.Fatalf("ReadExecutionMetrics() error = %v", err)
	}
	if metrics != nil {
		t.Fatalf("ReadExecutionMetrics() = %#v, want nil for a historical execution without metrics", metrics)
	}
}
func TestExecutionMetricsPreservesObservedZeroAndUnavailable(t *testing.T) {
	store := NuevoStore(t.TempDir())
	job := testJob()
	if err := store.CreateRun(job, RunPolicy{ID: "policy-id"}); err != nil {
		t.Fatal(err)
	}

	zeroDuration := time.Duration(0)
	zeroTokens := int64(0)
	metrics := ExecutionMetrics{
		Version: ExecutionMetricsSchemaVersion,
		RunID:   string(job.RunID()),
		Timing: &ExecutionTiming{
			TotalDurationNanos: &zeroDuration,
			ByCapability: []CapabilityTiming{{
				CapabilityID:  "capability-zero",
				DurationNanos: 0,
			}},
		},
		Usage: &ExecutionTokenUsage{
			InputTokens: &zeroTokens,
			Source:      ObservationSourceAdapter,
		},
		Cost: &ExecutionCost{
			AmountMicros: 0,
			Currency:     "USD",
			Provenance: CostProvenance{
				Source: CostSourceEstimate,
			},
		},
		Scope: &ExecutionScope{Kind: ScopeFull},
	}
	if err := store.SaveExecutionMetrics(metrics); err != nil {
		t.Fatalf("SaveExecutionMetrics() error = %v", err)
	}

	got, err := store.ReadExecutionMetrics(string(job.RunID()))
	if err != nil {
		t.Fatalf("ReadExecutionMetrics() error = %v", err)
	}
	if got.Timing == nil || got.Timing.TotalDurationNanos == nil || *got.Timing.TotalDurationNanos != 0 {
		t.Fatalf("timing total = %#v, want an observed zero", got.Timing)
	}
	if len(got.Timing.ByCapability) != 1 || got.Timing.ByCapability[0].DurationNanos != 0 {
		t.Fatalf("capability timing = %#v, want an observed zero row", got.Timing.ByCapability)
	}
	if got.Usage == nil || got.Usage.InputTokens == nil || *got.Usage.InputTokens != 0 {
		t.Fatalf("usage input = %#v, want an observed zero", got.Usage)
	}
	if got.Usage.OutputTokens != nil {
		t.Fatalf("usage output = %#v, want unavailable", got.Usage.OutputTokens)
	}
	if got.Cost == nil || got.Cost.AmountMicros != 0 {
		t.Fatalf("cost = %#v, want an observed zero", got.Cost)
	}
	if got.Scope == nil || got.Scope.Kind != ScopeFull || got.Scope.Savings != nil {
		t.Fatalf("scope = %#v, want full scope with unavailable savings", got.Scope)
	}
}

func TestExecutionMetricsAcceptsInvocationIdentityWithoutAgentObservation(t *testing.T) {
	store := NuevoStore(t.TempDir())
	job := testJob()
	if err := store.CreateRun(job, RunPolicy{ID: "policy-id"}); err != nil {
		t.Fatal(err)
	}
	metrics := ExecutionMetrics{
		Version: ExecutionMetricsSchemaVersion,
		RunID:   string(job.RunID()),
		Identities: []ObservedExecutionIdentity{{
			InvocationID: "attempt-1",
			Source:       ObservationSourceAdapter,
		}},
	}
	if err := store.SaveExecutionMetrics(metrics); err != nil {
		t.Fatalf("SaveExecutionMetrics() error = %v", err)
	}
	got, err := store.ReadExecutionMetrics(string(job.RunID()))
	if err != nil {
		t.Fatalf("ReadExecutionMetrics() error = %v", err)
	}
	if len(got.Identities) != 1 || got.Identities[0].InvocationID != "attempt-1" {
		t.Fatalf("identities = %#v, want invocation identity without agent fields", got.Identities)
	}
}

func TestSaveExecutionMetricsRejectsSecondWrite(t *testing.T) {
	store := NuevoStore(t.TempDir())
	job := testJob()
	if err := store.CreateRun(job, RunPolicy{ID: "policy-id"}); err != nil {
		t.Fatal(err)
	}
	metrics := ExecutionMetrics{
		Version: ExecutionMetricsSchemaVersion,
		RunID:   string(job.RunID()),
	}
	if err := store.SaveExecutionMetrics(metrics); err != nil {
		t.Fatalf("first SaveExecutionMetrics() error = %v", err)
	}

	if err := store.SaveExecutionMetrics(metrics); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("second SaveExecutionMetrics() error = %v, want immutable conflict", err)
	}
	conflicting := metrics
	conflicting.Failures = []ExecutionFailure{{Class: FailureInvalidOutput, Detail: "changed"}}
	if err := store.SaveExecutionMetrics(conflicting); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("conflicting SaveExecutionMetrics() error = %v, want immutable conflict", err)
	}
	got, err := store.ReadExecutionMetrics(string(job.RunID()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, &metrics) {
		t.Fatalf("conflicting write changed the immutable snapshot: got %#v, want %#v", got, &metrics)
	}
}

func TestReadExecutionMetricsAcceptsUnknownFieldsAndValues(t *testing.T) {
	store := NuevoStore(t.TempDir())
	job := testJob()
	if err := store.CreateRun(job, RunPolicy{ID: "policy-id"}); err != nil {
		t.Fatal(err)
	}
	directory, err := store.executionDir(string(job.RunID()))
	if err != nil {
		t.Fatal(err)
	}
	data := fmt.Sprintf(`{
  "version": 1,
  "run_id": %q,
  "identities": [{"agent":"future-agent","model":"future-model","source":"future-observer"}],
  "usage": {"input_tokens": 0, "source":"future-meter"},
  "cost": {"amount_micros": 0, "currency":"USD", "provenance":{"source":"future-cost"}},
  "scope": {"kind":"future-scope"},
  "failures": [{"class":"future-failure"}],
  "future_extension": {"enabled": true}
}`, string(job.RunID()))
	if err := os.WriteFile(filepath.Join(directory, "metrics.json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}

	metrics, err := store.ReadExecutionMetrics(string(job.RunID()))
	if err != nil {
		t.Fatalf("ReadExecutionMetrics() error = %v", err)
	}
	if got, want := metrics.Identities[0].Source, ObservationSource("future-observer"); got != want {
		t.Fatalf("identity source = %q, want %q", got, want)
	}
	if metrics.Usage == nil || metrics.Usage.InputTokens == nil || *metrics.Usage.InputTokens != 0 {
		t.Fatalf("usage input tokens = %#v, want a present zero", metrics.Usage)
	}
	if metrics.Cost == nil || metrics.Cost.AmountMicros != 0 || metrics.Cost.Provenance.Source != CostSource("future-cost") {
		t.Fatalf("cost = %#v, want a present zero from the future source", metrics.Cost)
	}
	if metrics.Scope == nil || metrics.Scope.Kind != ScopeKind("future-scope") {
		t.Fatalf("scope = %#v, want unknown scope value retained", metrics.Scope)
	}
	if len(metrics.Failures) != 1 || metrics.Failures[0].Class != FailureClass("future-failure") {
		t.Fatalf("failures = %#v, want unknown failure value retained", metrics.Failures)
	}
}

func TestReadExecutionMetricsRejectsUnsupportedSchemaVersion(t *testing.T) {
	store := NuevoStore(t.TempDir())
	job := testJob()
	if err := store.CreateRun(job, RunPolicy{ID: "policy-id"}); err != nil {
		t.Fatal(err)
	}
	directory, err := store.executionDir(string(job.RunID()))
	if err != nil {
		t.Fatal(err)
	}
	data := fmt.Sprintf(`{"version":2,"run_id":%q}`, string(job.RunID()))
	if err := os.WriteFile(filepath.Join(directory, "metrics.json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}

	_, err = store.ReadExecutionMetrics(string(job.RunID()))
	if !errors.Is(err, ErrUnsupportedExecutionMetricsVersion) {
		t.Fatalf("ReadExecutionMetrics() error = %v, want unsupported schema version", err)
	}
}
func TestSaveExecutionMetricsRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ExecutionMetrics)
	}{
		{
			name: "invalid run id",
			mutate: func(metrics *ExecutionMetrics) {
				metrics.RunID = "../escape"
			},
		},
		{
			name: "identity source missing",
			mutate: func(metrics *ExecutionMetrics) {
				metrics.Identities = []ObservedExecutionIdentity{{Agent: "agent"}}
			},
		},
		{
			name: "identity values missing",
			mutate: func(metrics *ExecutionMetrics) {
				metrics.Identities = []ObservedExecutionIdentity{{Source: ObservationSourceAdapter}}
			},
		},
		{
			name: "identity invocation id invalid",
			mutate: func(metrics *ExecutionMetrics) {
				metrics.Identities = []ObservedExecutionIdentity{{
					InvocationID: "../attempt",
					Agent:        "agent",
					Source:       ObservationSourceAdapter,
				}}
			},
		},
		{
			name: "total duration negative",
			mutate: func(metrics *ExecutionMetrics) {
				duration := -time.Nanosecond
				metrics.Timing = &ExecutionTiming{TotalDurationNanos: &duration}
			},
		},
		{
			name: "capability timing invalid",
			mutate: func(metrics *ExecutionMetrics) {
				metrics.Timing = &ExecutionTiming{ByCapability: []CapabilityTiming{{
					CapabilityID:  "",
					DurationNanos: -time.Nanosecond,
				}}}
			},
		},
		{
			name: "agent timing negative",
			mutate: func(metrics *ExecutionMetrics) {
				metrics.Timing = &ExecutionTiming{ByAgent: []AgentTiming{{
					Identity:      ObservedExecutionIdentity{Agent: "agent", Source: ObservationSourceAdapter},
					DurationNanos: -time.Nanosecond,
				}}}
			},
		},
		{
			name: "usage source missing",
			mutate: func(metrics *ExecutionMetrics) {
				metrics.Usage = &ExecutionTokenUsage{}
			},
		},
		{
			name: "token count negative",
			mutate: func(metrics *ExecutionMetrics) {
				tokens := int64(-1)
				metrics.Usage = &ExecutionTokenUsage{
					InputTokens: &tokens,
					Source:      ObservationSourceAdapter,
				}
			},
		},
		{
			name: "cost currency missing",
			mutate: func(metrics *ExecutionMetrics) {
				metrics.Cost = &ExecutionCost{
					Provenance: CostProvenance{Source: CostSourceEstimate},
				}
			},
		},
		{
			name: "cost source missing",
			mutate: func(metrics *ExecutionMetrics) {
				metrics.Cost = &ExecutionCost{Currency: "USD"}
			},
		},
		{
			name: "cost amount negative",
			mutate: func(metrics *ExecutionMetrics) {
				metrics.Cost = &ExecutionCost{
					AmountMicros: -1,
					Currency:     "USD",
					Provenance:   CostProvenance{Source: CostSourceEstimate},
				}
			},
		},
		{
			name: "scope kind missing",
			mutate: func(metrics *ExecutionMetrics) {
				metrics.Scope = &ExecutionScope{}
			},
		},
		{
			name: "saved duration negative",
			mutate: func(metrics *ExecutionMetrics) {
				duration := -time.Nanosecond
				metrics.Scope = &ExecutionScope{
					Kind:    ScopeAffected,
					Savings: &ExecutionSavings{DurationNanos: &duration},
				}
			},
		},
		{
			name: "reuse capability id missing",
			mutate: func(metrics *ExecutionMetrics) {
				metrics.Reuse = &ExecutionReuse{ReusedCapabilityIDs: []string{""}}
			},
		},
		{
			name: "failure class missing",
			mutate: func(metrics *ExecutionMetrics) {
				metrics.Failures = []ExecutionFailure{{}}
			},
		},
		{
			name: "failure invocation id invalid",
			mutate: func(metrics *ExecutionMetrics) {
				metrics.Failures = []ExecutionFailure{{
					InvocationID: "../attempt",
					Class:        FailureInvalidOutput,
				}}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NuevoStore(t.TempDir())
			job := testJob()
			if err := store.CreateRun(job, RunPolicy{ID: "policy-id"}); err != nil {
				t.Fatal(err)
			}
			metrics := ExecutionMetrics{
				Version: ExecutionMetricsSchemaVersion,
				RunID:   string(job.RunID()),
			}
			test.mutate(&metrics)
			if err := store.SaveExecutionMetrics(metrics); !errors.Is(err, ErrExecutionMetricsCorrupt) {
				t.Fatalf("SaveExecutionMetrics() error = %v, want corrupt metrics", err)
			}
		})
	}
}

func TestReadExecutionMetricsRejectsCorruptJSON(t *testing.T) {
	store := NuevoStore(t.TempDir())
	job := testJob()
	if err := store.CreateRun(job, RunPolicy{ID: "policy-id"}); err != nil {
		t.Fatal(err)
	}
	directory, err := store.executionDir(string(job.RunID()))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "metrics.json"), []byte(`{"version":1,`), 0600); err != nil {
		t.Fatal(err)
	}

	_, err = store.ReadExecutionMetrics(string(job.RunID()))
	if !errors.Is(err, ErrExecutionMetricsCorrupt) {
		t.Fatalf("ReadExecutionMetrics() error = %v, want corrupt metrics", err)
	}
	if errors.Is(err, ErrUnsupportedExecutionMetricsVersion) {
		t.Fatalf("ReadExecutionMetrics() error = %v, malformed JSON must not be classified as an unsupported version", err)
	}
}

func TestReadExecutionMetricsRejectsMismatchedRunID(t *testing.T) {
	store := NuevoStore(t.TempDir())
	job := testJob()
	if err := store.CreateRun(job, RunPolicy{ID: "policy-id"}); err != nil {
		t.Fatal(err)
	}
	directory, err := store.executionDir(string(job.RunID()))
	if err != nil {
		t.Fatal(err)
	}
	data := `{"version":1,"run_id":"different-run"}`
	if err := os.WriteFile(filepath.Join(directory, "metrics.json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}

	_, err = store.ReadExecutionMetrics(string(job.RunID()))
	if !errors.Is(err, ErrExecutionMetricsCorrupt) {
		t.Fatalf("ReadExecutionMetrics() error = %v, want corrupt metrics for mismatched run id", err)
	}
}

func TestSaveExecutionMetricsRejectsSavingsForFullScope(t *testing.T) {
	store := NuevoStore(t.TempDir())
	job := testJob()
	if err := store.CreateRun(job, RunPolicy{ID: "policy-id"}); err != nil {
		t.Fatal(err)
	}
	metrics := ExecutionMetrics{
		Version: ExecutionMetricsSchemaVersion,
		RunID:   string(job.RunID()),
		Scope: &ExecutionScope{
			Kind:    ScopeFull,
			Savings: &ExecutionSavings{},
		},
	}
	if err := store.SaveExecutionMetrics(metrics); !errors.Is(err, ErrExecutionMetricsCorrupt) {
		t.Fatalf("SaveExecutionMetrics() error = %v, want corrupt metrics for full-scope savings", err)
	}
}

func TestSaveExecutionMetricsConcurrentIndependentStoresWriteOnce(t *testing.T) {
	root := t.TempDir()
	first := NuevoStore(root)
	second := NuevoStore(root)
	job := testJob()
	if err := first.CreateRun(job, RunPolicy{ID: "policy-id"}); err != nil {
		t.Fatal(err)
	}
	runID := string(job.RunID())
	metrics := []ExecutionMetrics{
		{
			Version: ExecutionMetricsSchemaVersion,
			RunID:   runID,
			Failures: []ExecutionFailure{{
				Class:  FailureInvalidOutput,
				Detail: "first",
			}},
		},
		{
			Version: ExecutionMetricsSchemaVersion,
			RunID:   runID,
			Failures: []ExecutionFailure{{
				Class:  FailureInvalidOutput,
				Detail: "second",
			}},
		},
	}
	results := make(chan error, len(metrics))
	start := make(chan struct{})
	go func() {
		<-start
		results <- first.SaveExecutionMetrics(metrics[0])
	}()
	go func() {
		<-start
		results <- second.SaveExecutionMetrics(metrics[1])
	}()
	close(start)

	successes, conflicts := 0, 0
	for range metrics {
		switch err := <-results; {
		case err == nil:
			successes++
		case errors.Is(err, ErrImmutableConflict):
			conflicts++
		default:
			t.Fatalf("concurrent SaveExecutionMetrics() error = %v, want one success and one immutable conflict", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent SaveExecutionMetrics() results = %d successes, %d conflicts, want one each", successes, conflicts)
	}
	if got, err := first.ReadExecutionMetrics(runID); err != nil {
		t.Fatal(err)
	} else if got == nil || len(got.Failures) != 1 {
		t.Fatalf("persisted metrics = %#v, want exactly one winning snapshot", got)
	}
}
