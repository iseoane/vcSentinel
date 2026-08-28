package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const ExecutionMetricsSchemaVersion uint32 = 1

var (
	ErrExecutionMetricsCorrupt            = errors.New("store: corrupt execution metrics")
	ErrUnsupportedExecutionMetricsVersion = errors.New("store: unsupported execution metrics version")
)

// ExecutionMetrics is the versioned, immutable metric snapshot for one durable
// execution. Nil metric groups mean the producer did not observe that fact;
// they never mean a measured zero.
type ExecutionMetrics struct {
	Version    uint32                      `json:"version"`
	RunID      string                      `json:"run_id"`
	Identities []ObservedExecutionIdentity `json:"identities,omitempty"`
	Timing     *ExecutionTiming            `json:"timing,omitempty"`
	Usage      *ExecutionTokenUsage        `json:"usage,omitempty"`
	Cost       *ExecutionCost              `json:"cost,omitempty"`
	Scope      *ExecutionScope             `json:"scope,omitempty"`
	Reuse      *ExecutionReuse             `json:"reuse,omitempty"`
	Failures   []ExecutionFailure          `json:"failures,omitempty"`
}

// ObservationSource identifies the evidence surface that reported a fact.
// Readers intentionally retain unrecognized values for forward compatibility.
type ObservationSource string

const ObservationSourceAdapter ObservationSource = "adapter"

// ObservedExecutionIdentity holds only values actually reported by an evidence
// source. In particular, an empty Model means no effective model was observed.
type ObservedExecutionIdentity struct {
	InvocationID string            `json:"invocation_id,omitempty"`
	Agent        string            `json:"agent,omitempty"`
	Model        string            `json:"model,omitempty"`
	Effort       string            `json:"effort,omitempty"`
	Source       ObservationSource `json:"source"`
}

// ExecutionTiming records known execution durations in nanoseconds. A present
// zero duration remains distinct from an absent duration because totals use
// pointers; per-capability and per-agent rows are themselves presence records.
type ExecutionTiming struct {
	TotalDurationNanos *time.Duration     `json:"total_duration_ns,omitempty"`
	ByCapability       []CapabilityTiming `json:"by_capability,omitempty"`
	ByAgent            []AgentTiming      `json:"by_agent,omitempty"`
}

type CapabilityTiming struct {
	CapabilityID  string        `json:"capability_id"`
	DurationNanos time.Duration `json:"duration_ns"`
}

type AgentTiming struct {
	Identity      ObservedExecutionIdentity `json:"identity"`
	DurationNanos time.Duration             `json:"duration_ns"`
}

// ExecutionTokenUsage keeps every optional token count as a pointer, so a
// reported zero is not conflated with usage that no provider exposed.
type ExecutionTokenUsage struct {
	InputTokens       *int64            `json:"input_tokens,omitempty"`
	OutputTokens      *int64            `json:"output_tokens,omitempty"`
	CachedInputTokens *int64            `json:"cached_input_tokens,omitempty"`
	ReasoningTokens   *int64            `json:"reasoning_tokens,omitempty"`
	Source            ObservationSource `json:"source"`
}

// CostSource identifies how a cost was obtained. Values remain extensible so
// future sources can be read by this schema version without data loss.
type CostSource string

const CostSourceEstimate CostSource = "estimate"

// ExecutionCost is an exact micro-unit amount plus the evidence that produced
// it. A nil *ExecutionCost means cost was unavailable, while AmountMicros == 0
// is an observed zero-cost result.
type ExecutionCost struct {
	AmountMicros int64          `json:"amount_micros"`
	Currency     string         `json:"currency"`
	Provenance   CostProvenance `json:"provenance"`
}

type CostProvenance struct {
	Source    CostSource `json:"source"`
	Reference string     `json:"reference,omitempty"`
}

// ScopeKind distinguishes the full evaluation from an affected-only one.
// Readers retain future non-empty values.
type ScopeKind string

const ScopeAffected ScopeKind = "affected"

// ExecutionScope records the execution scope and any measured savings.
type ExecutionScope struct {
	Kind    ScopeKind         `json:"kind"`
	Savings *ExecutionSavings `json:"savings,omitempty"`
}

// ExecutionSavings records facts avoided by an affected-only execution.
type ExecutionSavings struct {
	DurationNanos *time.Duration `json:"duration_ns,omitempty"`
	InputTokens   *int64         `json:"input_tokens,omitempty"`
	OutputTokens  *int64         `json:"output_tokens,omitempty"`
	Cost          *ExecutionCost `json:"cost,omitempty"`
}

// ExecutionReuse separates capabilities served from reuse from capabilities
// recomputed by the current execution.
type ExecutionReuse struct {
	ReusedCapabilityIDs     []string `json:"reused_capability_ids,omitempty"`
	RecomputedCapabilityIDs []string `json:"recomputed_capability_ids,omitempty"`
}

// FailureClass is a producer-reported operational failure category.
type FailureClass string

const FailureInvalidOutput FailureClass = "invalid_output"

// ExecutionFailure records one observed failure without changing the durable
// event stream's lifecycle authority.
type ExecutionFailure struct {
	InvocationID string       `json:"invocation_id,omitempty"`
	Class        FailureClass `json:"class"`
	Detail       string       `json:"detail,omitempty"`
}

// SaveExecutionMetrics writes the single immutable metrics snapshot alongside
// its durable execution records. The execution directory is the authority;
// legacy store.Run records are intentionally never written or extended here.
func (s *Store) SaveExecutionMetrics(metrics ExecutionMetrics) error {
	if err := validateExecutionMetrics(metrics, metrics.RunID); err != nil {
		return err
	}
	directory, err := s.executionDir(metrics.RunID)
	if err != nil {
		return err
	}
	if err := ensureExecutionExists(directory); err != nil {
		return err
	}
	data, err := marshalRecord(metrics)
	if err != nil {
		return err
	}
	return withExecutionLock(directory, func() error {
		return writeImmutableRecord(filepath.Join(directory, "metrics.json"), data)
	})
}

// ReadExecutionMetrics returns nil, nil when a durable execution predates the
// metrics schema. A nil result is absence of evidence, never a zero-valued
// metric snapshot; callers must preserve that distinction.
func (s *Store) ReadExecutionMetrics(runID string) (*ExecutionMetrics, error) {
	directory, err := s.executionDir(runID)
	if err != nil {
		return nil, err
	}
	if err := ensureExecutionExists(directory); err != nil {
		return nil, err
	}
	path := filepath.Join(directory, "metrics.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var metrics ExecutionMetrics
	if err := json.Unmarshal(data, &metrics); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrExecutionMetricsCorrupt, path)
	}
	if err := validateExecutionMetrics(metrics, runID); err != nil {
		return nil, fmt.Errorf("%w: %s", err, path)
	}
	return &metrics, nil
}

func validateExecutionMetrics(metrics ExecutionMetrics, expectedRunID string) error {
	if metrics.Version != ExecutionMetricsSchemaVersion {
		return fmt.Errorf("%w: %d", ErrUnsupportedExecutionMetricsVersion, metrics.Version)
	}
	if !validRunID(metrics.RunID) {
		return fmt.Errorf("%w: invalid run id", ErrExecutionMetricsCorrupt)
	}
	if expectedRunID != "" && metrics.RunID != expectedRunID {
		return fmt.Errorf("%w: run id does not match execution", ErrExecutionMetricsCorrupt)
	}
	for _, identity := range metrics.Identities {
		if err := validateObservedIdentity(identity); err != nil {
			return err
		}
	}
	if metrics.Timing != nil {
		if err := validateExecutionTiming(*metrics.Timing); err != nil {
			return err
		}
	}
	if metrics.Usage != nil {
		if err := validateExecutionTokenUsage(*metrics.Usage); err != nil {
			return err
		}
	}
	if metrics.Cost != nil {
		if err := validateExecutionCost(*metrics.Cost); err != nil {
			return err
		}
	}
	if metrics.Scope != nil {
		if metrics.Scope.Kind == "" {
			return fmt.Errorf("%w: scope kind is empty", ErrExecutionMetricsCorrupt)
		}
		if metrics.Scope.Savings != nil {
			if err := validateExecutionSavings(*metrics.Scope.Savings); err != nil {
				return err
			}
		}
	}
	if metrics.Reuse != nil {
		for _, capabilityID := range append(metrics.Reuse.ReusedCapabilityIDs, metrics.Reuse.RecomputedCapabilityIDs...) {
			if capabilityID == "" {
				return fmt.Errorf("%w: reused capability id is empty", ErrExecutionMetricsCorrupt)
			}
		}
	}
	for _, failure := range metrics.Failures {
		if failure.Class == "" {
			return fmt.Errorf("%w: failure class is empty", ErrExecutionMetricsCorrupt)
		}
		if failure.InvocationID != "" && !validRunID(failure.InvocationID) {
			return fmt.Errorf("%w: invalid failure invocation id", ErrExecutionMetricsCorrupt)
		}
	}
	return nil
}

func validateObservedIdentity(identity ObservedExecutionIdentity) error {
	if identity.Source == "" {
		return fmt.Errorf("%w: identity source is empty", ErrExecutionMetricsCorrupt)
	}
	if identity.Agent == "" && identity.Model == "" && identity.Effort == "" {
		return fmt.Errorf("%w: identity has no observed values", ErrExecutionMetricsCorrupt)
	}
	if identity.InvocationID != "" && !validRunID(identity.InvocationID) {
		return fmt.Errorf("%w: invalid identity invocation id", ErrExecutionMetricsCorrupt)
	}
	return nil
}

func validateExecutionTiming(timing ExecutionTiming) error {
	if timing.TotalDurationNanos != nil && *timing.TotalDurationNanos < 0 {
		return fmt.Errorf("%w: total duration is negative", ErrExecutionMetricsCorrupt)
	}
	for _, timing := range timing.ByCapability {
		if timing.CapabilityID == "" || timing.DurationNanos < 0 {
			return fmt.Errorf("%w: invalid capability timing", ErrExecutionMetricsCorrupt)
		}
	}
	for _, timing := range timing.ByAgent {
		if err := validateObservedIdentity(timing.Identity); err != nil {
			return err
		}
		if timing.DurationNanos < 0 {
			return fmt.Errorf("%w: agent duration is negative", ErrExecutionMetricsCorrupt)
		}
	}
	return nil
}

func validateExecutionTokenUsage(usage ExecutionTokenUsage) error {
	if usage.Source == "" {
		return fmt.Errorf("%w: usage source is empty", ErrExecutionMetricsCorrupt)
	}
	values := []*int64{usage.InputTokens, usage.OutputTokens, usage.CachedInputTokens, usage.ReasoningTokens}
	for _, value := range values {
		if value != nil && *value < 0 {
			return fmt.Errorf("%w: token count is negative", ErrExecutionMetricsCorrupt)
		}
	}
	return nil
}

func validateExecutionSavings(savings ExecutionSavings) error {
	if savings.DurationNanos != nil && *savings.DurationNanos < 0 {
		return fmt.Errorf("%w: saved duration is negative", ErrExecutionMetricsCorrupt)
	}
	for _, value := range []*int64{savings.InputTokens, savings.OutputTokens} {
		if value != nil && *value < 0 {
			return fmt.Errorf("%w: saved token count is negative", ErrExecutionMetricsCorrupt)
		}
	}
	if savings.Cost != nil {
		return validateExecutionCost(*savings.Cost)
	}
	return nil
}

func validateExecutionCost(cost ExecutionCost) error {
	if cost.AmountMicros < 0 || cost.Currency == "" || cost.Provenance.Source == "" {
		return fmt.Errorf("%w: invalid cost", ErrExecutionMetricsCorrupt)
	}
	return nil
}
