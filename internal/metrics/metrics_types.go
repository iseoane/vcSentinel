package metrics

import (
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/ops"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// Input is the storage-independent input to Aggregate. Readers may populate
// it from the durable store, while tests and future importers can construct it
// directly without touching the filesystem.
type Input struct {
	Findings     []FindingObservation
	Decisions    []store.Decision
	Remediations []RemediationObservation
	Executions   []ExecutionObservation
	Stages       []StageObservation
	Events       []ops.Evento
}

// FindingObservation is one persisted observation of a logical finding.
// Fingerprint is preferred; when it is empty the embedded finding is
// fingerprinted from its stable fields. Superseded observations never enter
// any aggregate.
type FindingObservation struct {
	Fingerprint string
	Commit      string
	Revision    int
	At          time.Time
	Origin      string
	Superseded  bool
	Finding     review.Hallazgo
}

// RemediationObservation records one logical remediation outcome. Repeated
// physical attempts with the same LogicalID count once.
type RemediationObservation struct {
	Target    string
	LogicalID string
	Dimension string
	Success   bool
	At        time.Time
}

// ExecutionObservation groups one logical run's immutable metrics and its
// physical attempt outcomes. A retry remains one logical run.
type ExecutionObservation struct {
	RunID        string
	LogicalRunID string
	Metrics      *store.ExecutionMetrics
	Outcomes     []store.AttemptOutcome
}

// StageObservation is an optional stage timing supplied by an event reader.
// Execution metric capability timings are also projected to stages.
type StageObservation struct {
	Stage         string
	DurationNanos int64
	LogicalRunID  string
}

// Report is the complete deterministic aggregate. Slices are sorted by their
// documented keys before this value is returned.
type Report struct {
	Findings    FindingsAggregate
	Remediation RemediationAggregate
	Executions  ExecutionAggregate
	Costs       []CostAggregate
	Stages      []StageAggregate
}

type FindingsAggregate struct {
	Effective        int64
	Observed         int64
	Confirmed        int64
	Refuted          int64
	Overrides        int64
	Reopened         int64
	ConfirmationRate Ratio
	RefutationRate   Ratio
	OverrideRate     Ratio
	ByDimension      []DimensionAggregate
	ByModel          []ModelAggregate
	ByAgent          []AgentAggregate
}

type DimensionAggregate struct {
	Dimension        string
	Findings         int64
	Observed         int64
	Confirmed        int64
	Refuted          int64
	Overrides        int64
	Reopened         int64
	ConfirmationRate Ratio
	RefutationRate   Ratio
	OverrideRate     Ratio
}

type ModelAggregate struct {
	Model          string
	Observed       int64
	Confirmed      int64
	Refuted        int64
	RefutationRate Ratio
}

type AgentAggregate struct {
	Agent          string
	Observed       int64
	Confirmed      int64
	Refuted        int64
	RefutationRate Ratio
}

type RemediationAggregate struct {
	Attempts    int64
	Succeeded   int64
	Failed      int64
	SuccessRate Ratio
	ByDimension []RemediationDimensionAggregate
}

type RemediationDimensionAggregate struct {
	Dimension   string
	Attempts    int64
	Succeeded   int64
	Failed      int64
	SuccessRate Ratio
}

type ExecutionAggregate struct {
	LogicalRuns       int64
	MeasuredRuns      int64
	SuccessfulRuns    int64
	FailedRuns        int64
	RetriedRuns       int64
	SuccessRate       Ratio
	Duration          Measurement
	InputTokens       Measurement
	OutputTokens      Measurement
	TotalTokens       Measurement
	CachedInputTokens Measurement
	ReasoningTokens   Measurement
	CostCoverage      Coverage
	IdentityCoverage  Coverage
	Reuse             ReuseAggregate
	Scope             ScopeAggregate
	Failures          []FailureAggregate
}

type Measurement struct {
	Value    *int64
	Observed int64
	Total    int64
	Coverage Coverage
}

type ReuseAggregate struct {
	Reused     int64
	Recomputed int64
	Rate       Ratio
}

type ScopeAggregate struct {
	Full     int64
	Affected int64
	Unknown  int64
	Coverage Coverage
}

type FailureAggregate struct {
	Class string
	Count int64
}

type CostAggregate struct {
	Currency         string
	TotalMicros      int64
	ObservedRuns     int64
	TotalRuns        int64
	CostPerConfirmed Ratio
}

// TotalCoverage describes the evidence available for the aggregate cost.
func (v CostAggregate) TotalCoverage() Coverage {
	return coverage(v.ObservedRuns, v.TotalRuns)
}

type StageAggregate struct {
	Stage    string
	Samples  int64
	P50Nanos int64
	P95Nanos int64
	Coverage Coverage
}

// Coverage describes how much of a population had usable evidence. Value is
// rounded to four decimal places and is nil when Total is zero.
type Coverage struct {
	Observed int64
	Total    int64
	Value    *float64
}

// Ratio carries all parts of a ratio. Value is nil when Denominator is zero;
// it is never NaN or infinity. Value and coverage values use four decimal
// places, rounded half away from zero.
type Ratio struct {
	Numerator   int64
	Denominator int64
	Coverage    Coverage
	Value       *float64
}

// Known reports whether the coverage percentage itself is available. A zero
// percentage is known evidence; a nil value means that no population exists.
func (v Coverage) Known() bool { return v.Value != nil }

// Complete reports whether every member of the population has evidence.
func (v Coverage) Complete() bool {
	return v.Known() && v.Total > 0 && v.Observed >= v.Total
}

// Known reports whether the ratio can be presented as a measured value.
func (v Ratio) Known() bool { return v.Value != nil && v.Coverage.Complete() }

// Known reports whether the measurement can be presented as a measured value.
func (v Measurement) Known() bool { return v.Value != nil && v.Coverage.Complete() }

// HasIncompleteEvidence applies the domain evidence policy to every value the
// metrics command exposes. It keeps renderers from independently rebuilding
// the meaning of partial or unavailable observations.
func (r Report) HasIncompleteEvidence() bool {
	f, e := r.Findings, r.Executions
	if f.Observed == 0 || r.Remediation.Attempts == 0 || e.LogicalRuns == 0 {
		return true
	}
	ratios := []Ratio{f.ConfirmationRate, f.RefutationRate, f.OverrideRate, r.Remediation.SuccessRate, e.SuccessRate, e.Reuse.Rate}
	for _, v := range ratios {
		if !v.Known() {
			return true
		}
	}
	for _, v := range f.ByDimension {
		if !v.ConfirmationRate.Known() || !v.RefutationRate.Known() || !v.OverrideRate.Known() {
			return true
		}
	}
	for _, v := range f.ByModel {
		if !v.RefutationRate.Known() {
			return true
		}
	}
	for _, v := range f.ByAgent {
		if !v.RefutationRate.Known() {
			return true
		}
	}
	for _, v := range r.Remediation.ByDimension {
		if !v.SuccessRate.Known() {
			return true
		}
	}
	for _, v := range []Measurement{e.Duration, e.InputTokens, e.OutputTokens, e.TotalTokens, e.CachedInputTokens, e.ReasoningTokens} {
		if !v.Known() {
			return true
		}
	}
	for _, v := range []Coverage{e.CostCoverage, e.IdentityCoverage, e.Scope.Coverage} {
		if !v.Complete() {
			return true
		}
	}
	for _, v := range r.Costs {
		if !v.TotalCoverage().Complete() || !v.CostPerConfirmed.Known() {
			return true
		}
	}
	for _, v := range r.Stages {
		if !v.Coverage.Complete() {
			return true
		}
	}
	return false
}
