// Package attach provides the pure observation read-model for attaching to
// one durable run. It merges Inspect snapshots with cursor-based Subscribe
// replay into a stable RunView that renderers consume — today the plain-text
// `runs attach` CLI surface, tomorrow the Bubble Tea program. Nothing in this
// package performs I/O or holds transport state beyond a replay cursor.
package attach

import (
	"sort"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
)

// InvocationEvidence summarizes everything durably observable about one
// physical invocation of a run. Evidence carries presence flags and classes,
// never raw adapter output bytes: OutputHash proves output existed without
// reproducing it.
//
// Durable event frames record no numeric attempt counter, so the honest
// observables are the first-appearance Order (1-based across the applied
// stream) plus the parent lineage; a future frame vocabulary may add the
// attempt number without changing this shape.
type InvocationEvidence struct {
	InvocationID       string `json:"invocation_id"`
	ParentInvocationID string `json:"parent_invocation_id,omitempty"`
	LineageID          string `json:"lineage_id,omitempty"`
	Order              int    `json:"order"`
	// Decision records the control decision that created the invocation,
	// taken from its first applied frame.
	Decision      agentrun.Decision     `json:"decision,omitempty"`
	OutcomeClass  agentrun.OutcomeClass `json:"outcome_class,omitempty"`
	Error         string                `json:"error,omitempty"`
	HasOutputHash bool                  `json:"has_output_hash"`
	FirstSequence uint64                `json:"first_sequence"`
	LastSequence  uint64                `json:"last_sequence"`
}

// ResponseRecord is one explicit control response observed in the replayed
// stream. Only the content hash is durable evidence; the answer itself stays
// at the adapter seam.
type ResponseRecord struct {
	InvocationID       string `json:"invocation_id"`
	ParentInvocationID string `json:"parent_invocation_id,omitempty"`
	ResponseHash       string `json:"response_hash"`
	Sequence           uint64 `json:"sequence"`
}

// RunView is the human observation model of one run: lifecycle position,
// semantic outcome when terminal, the invocations seen with their decision
// lineage, the recorded responses, and how far the observation reaches.
type RunView struct {
	RunID    string                  `json:"run_id"`
	JobID    string                  `json:"job_id,omitempty"`
	State    agentrun.LifecycleState `json:"state"`
	Terminal agentrun.TerminalClass  `json:"terminal"`
	// Sequence and Revision come from the durable projection, so they stay
	// truthful even when the view was built from a partial replay window
	// (--after greater than zero).
	Sequence uint64 `json:"sequence"`
	Revision uint64 `json:"revision"`
	// Outcome is the semantic outcome class derived from the terminal
	// lifecycle state; it stays empty while the run is still in flight.
	Outcome agentrun.OutcomeClass `json:"outcome_class,omitempty"`
	// Error carries the terminal error text when terminal evidence recorded
	// one; it never contains adapter output bytes.
	Error       string               `json:"error,omitempty"`
	Invocations []InvocationEvidence `json:"invocations"`
	Responses   []ResponseRecord     `json:"responses"`
}

// IsTerminal reports whether the observed run reached a terminal lifecycle
// state, so a renderer can freeze instead of polling forever.
func (v RunView) IsTerminal() bool { return v.Terminal != agentrun.TerminalNone }

// BuildRunView derives a deterministic RunView from the applied event frames,
// the admitted attempt outcomes, and the durable projection. It is pure:
// same inputs, same view, no I/O. Frames may arrive in any order; invocation
// ordering follows first appearance by sequence, and outcome records for
// invocations absent from the frames append afterward in stable identity
// order.
func BuildRunView(frames []store.EventFrame, outcomes []store.AttemptOutcome, projection store.RunProjection) RunView {
	view := RunView{
		RunID:    projection.RunID,
		State:    projection.State,
		Terminal: projection.Terminal,
		Sequence: projection.Sequence,
		Revision: projection.Revision,
	}
	byInvocation := make(map[string]*InvocationEvidence)
	var terminalFrame *store.EventFrame
	for i, frame := range frames {
		if frame.JobID != "" && view.JobID == "" {
			view.JobID = frame.JobID
		}
		if frame.Terminal != agentrun.TerminalNone {
			// The last terminal frame wins; streams settle exactly once, so
			// this only guards degenerate replays.
			f := frames[i]
			terminalFrame = &f
		}
		evidence, seen := byInvocation[frame.InvocationID]
		if !seen {
			evidence = &InvocationEvidence{
				InvocationID:       frame.InvocationID,
				ParentInvocationID: frame.ParentInvocationID,
				LineageID:          frame.LineageID,
				Order:              len(byInvocation) + 1,
				Decision:           frame.Decision,
				FirstSequence:      frame.Sequence,
			}
			byInvocation[frame.InvocationID] = evidence
		}
		if evidence.ParentInvocationID == "" {
			evidence.ParentInvocationID = frame.ParentInvocationID
		}
		if evidence.LineageID == "" {
			evidence.LineageID = frame.LineageID
		}
		evidence.LastSequence = frame.Sequence
		if frame.OutcomeClass != "" {
			evidence.OutcomeClass = frame.OutcomeClass
			evidence.Error = frame.OutcomeError
		}
		if frame.OutputHash != "" {
			evidence.HasOutputHash = true
		}
		if frame.ResponseHash != "" {
			view.Responses = append(view.Responses, ResponseRecord{
				InvocationID:       frame.InvocationID,
				ParentInvocationID: frame.ParentInvocationID,
				ResponseHash:       frame.ResponseHash,
				Sequence:           frame.Sequence,
			})
		}
	}
	// Merge admitted outcome records. Embedded frame evidence wins when both
	// exist; records for unseen invocations become trailing entries so no
	// admitted evidence disappears from the view.
	var extra []InvocationEvidence
	for _, outcome := range outcomes {
		evidence, seen := byInvocation[outcome.InvocationID]
		if !seen {
			extra = append(extra, InvocationEvidence{
				InvocationID:  outcome.InvocationID,
				LineageID:     outcome.LineageID,
				OutcomeClass:  outcome.Class,
				Error:         outcome.Error,
				HasOutputHash: outcome.OutputHash != "",
			})
			continue
		}
		if evidence.OutcomeClass == "" {
			evidence.OutcomeClass = outcome.Class
			evidence.Error = outcome.Error
		} else if evidence.Error == "" {
			evidence.Error = outcome.Error
		}
		if outcome.OutputHash != "" {
			evidence.HasOutputHash = true
		}
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i].InvocationID < extra[j].InvocationID })
	view.Invocations = collectInvocations(byInvocation, extra)
	if projection.Terminal != agentrun.TerminalNone {
		view.Outcome = semanticOutcome(projection.State)
		view.Error = terminalError(terminalFrame, byInvocation)
	}
	if view.Responses == nil {
		view.Responses = []ResponseRecord{}
	}
	return view
}

// collectInvocations flattens the accumulation map into the stable ordering:
// framed invocations by first-appearance sequence (identity as tiebreaker),
// then the trailing outcome-only entries, renumbering Order densely from 1.
func collectInvocations(byInvocation map[string]*InvocationEvidence, extra []InvocationEvidence) []InvocationEvidence {
	ordered := make([]InvocationEvidence, 0, len(byInvocation)+len(extra))
	for _, evidence := range byInvocation {
		ordered = append(ordered, *evidence)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].FirstSequence != ordered[j].FirstSequence {
			return ordered[i].FirstSequence < ordered[j].FirstSequence
		}
		return ordered[i].InvocationID < ordered[j].InvocationID
	})
	ordered = append(ordered, extra...)
	for i := range ordered {
		ordered[i].Order = i + 1
	}
	return ordered
}

// semanticOutcome maps a terminal lifecycle state to its semantic outcome
// class. Non-terminal states yield the empty class.
func semanticOutcome(state agentrun.LifecycleState) agentrun.OutcomeClass {
	switch state {
	case agentrun.StateSucceeded:
		return agentrun.OutcomeSuccess
	case agentrun.StateFailed:
		return agentrun.OutcomeFailure
	case agentrun.StateCanceled:
		return agentrun.OutcomeCancellation
	case agentrun.StateTimedOut:
		return agentrun.OutcomeTimeout
	case agentrun.StateUnavailable:
		return agentrun.OutcomeUnavailable
	default:
		return ""
	}
}

// terminalError surfaces the error text of the terminal evidence: the
// terminal frame's embedded outcome error first, then the admitted outcome
// record of the same invocation, then nothing.
func terminalError(terminalFrame *store.EventFrame, byInvocation map[string]*InvocationEvidence) string {
	if terminalFrame == nil {
		return ""
	}
	if terminalFrame.OutcomeError != "" {
		return terminalFrame.OutcomeError
	}
	if evidence, seen := byInvocation[terminalFrame.InvocationID]; seen {
		return evidence.Error
	}
	return ""
}
