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
