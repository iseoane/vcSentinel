package attach

import (
	"testing"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
)

// Ticket 17 slice 1: BuildRunView is the pure merge of replayed frames,
// admitted outcomes, and the durable projection. These tests pin the five
// observation shapes the TUI will render later: running, awaiting-decision
// with a response chain, succeeded, canceled, and failed-with-evidence.

var buildAt = time.Unix(1700000000, 0).UTC()

func transition(seq uint64, invocation string, from, to agentrun.LifecycleState, decision agentrun.Decision) store.EventFrame {
	return store.EventFrame{
		Sequence: seq, Revision: seq, At: buildAt,
		RunID: "run-1", JobID: "job-1",
		InvocationID: invocation, LineageID: "lineage-1",
		From: from, To: to, Decision: decision,
		Terminal: to.TerminalClass(),
	}
}

func admissionFrames(invocation string) []store.EventFrame {
	return []store.EventFrame{
		transition(1, invocation, agentrun.StateCreated, agentrun.StateQueued, agentrun.DecisionStart),
		transition(2, invocation, agentrun.StateQueued, agentrun.StateAdmitted, agentrun.DecisionStart),
		transition(3, invocation, agentrun.StateAdmitted, agentrun.StateRunning, agentrun.DecisionStart),
	}
}

func TestBuildRunViewRunningRunHasNoOutcomeYet(t *testing.T) {
	frames := admissionFrames("inv-root")
	view := BuildRunView(frames, nil, store.RunProjection{
		RunID: "run-1", Sequence: 4, Revision: 4, State: agentrun.StateRunning,
	})
	if view.RunID != "run-1" || view.JobID != "job-1" {
		t.Fatalf("identity = %s/%s, want run-1/job-1", view.RunID, view.JobID)
	}
	if view.IsTerminal() || view.Outcome != "" || view.Error != "" {
		t.Fatalf("running view reported terminal evidence: %+v", view)
	}
	if view.Sequence != 4 || view.Revision != 4 {
		t.Fatalf("position = seq %d rev %d, want the projection position 4/4", view.Sequence, view.Revision)
	}
	if len(view.Invocations) != 1 {
		t.Fatalf("invocations = %+v, want exactly the root invocation", view.Invocations)
	}
	root := view.Invocations[0]
	if root.Order != 1 || root.InvocationID != "inv-root" || root.Decision != agentrun.DecisionStart {
		t.Fatalf("root evidence = %+v, want order 1 with start decision", root)
	}
	if len(view.Responses) != 0 {
		t.Fatalf("responses = %+v, want none for a plain running run", view.Responses)
	}
}

func TestBuildRunViewAwaitingDecisionWithResponseChain(t *testing.T) {
	frames := append(admissionFrames("inv-root"),
		transition(4, "inv-root", agentrun.StateRunning, agentrun.StateAwaitingDecision, agentrun.DecisionNone),
	)
	response := transition(5, "inv-child", agentrun.StateAwaitingDecision, agentrun.StateRunning, agentrun.DecisionRespond)
	response.ParentInvocationID = "inv-root"
	response.ResponseHash = "resp-hash-1"
	settle := transition(6, "inv-child", agentrun.StateRunning, agentrun.StateSucceeded, agentrun.DecisionComplete)
	frames = append(frames, response, settle)
	view := BuildRunView(frames, nil, store.RunProjection{
		RunID: "run-1", Sequence: 6, Revision: 6,
		State: agentrun.StateSucceeded, Terminal: agentrun.TerminalSuccess,
	})
	if len(view.Invocations) != 2 {
		t.Fatalf("invocations = %+v, want root plus child", view.Invocations)
	}
	child := view.Invocations[1]
	if child.Order != 2 || child.InvocationID != "inv-child" || child.ParentInvocationID != "inv-root" {
		t.Fatalf("child evidence = %+v, want second-order child under inv-root", child)
	}
	if child.Decision != agentrun.DecisionRespond {
		t.Fatalf("child decision = %q, want respond lineage", child.Decision)
	}
	if len(view.Responses) != 1 {
		t.Fatalf("responses = %+v, want exactly the recorded response", view.Responses)
	}
	recorded := view.Responses[0]
	if recorded.InvocationID != "inv-child" || recorded.ResponseHash != "resp-hash-1" || recorded.Sequence != 5 {
		t.Fatalf("response record = %+v, want inv-child at sequence 5 with resp-hash-1", recorded)
	}
	if !view.IsTerminal() || view.Outcome != agentrun.OutcomeSuccess {
		t.Fatalf("terminal view = state %s outcome %q, want succeeded/success", view.State, view.Outcome)
	}
}

func TestBuildRunViewSucceededRunDerivesSemanticOutcome(t *testing.T) {
	frames := append(admissionFrames("inv-root"),
		transition(4, "inv-root", agentrun.StateRunning, agentrun.StateSucceeded, agentrun.DecisionComplete),
	)
	view := BuildRunView(frames, nil, store.RunProjection{
		RunID: "run-1", Sequence: 4, Revision: 4,
		State: agentrun.StateSucceeded, Terminal: agentrun.TerminalSuccess,
	})
	if view.Outcome != agentrun.OutcomeSuccess || view.Error != "" {
		t.Fatalf("outcome = %q error %q, want success without error text", view.Outcome, view.Error)
	}
}

func TestBuildRunViewCanceledRunCarriesCancellationOutcome(t *testing.T) {
	frames := append(admissionFrames("inv-root"),
		transition(4, "inv-root", agentrun.StateRunning, agentrun.StateCanceled, agentrun.DecisionAbort),
	)
	view := BuildRunView(frames, nil, store.RunProjection{
		RunID: "run-1", Sequence: 4, Revision: 4,
		State: agentrun.StateCanceled, Terminal: agentrun.TerminalCancellation,
	})
	if view.Outcome != agentrun.OutcomeCancellation {
		t.Fatalf("outcome = %q, want cancellation", view.Outcome)
	}
	root := view.Invocations[0]
	// Decision records the CREATING control decision (first applied frame),
	// so the root invocation keeps its start lineage even when a later abort
	// settled it; the semantic outcome carries the cancellation itself.
	if root.Decision != agentrun.DecisionStart {
		t.Fatalf("root decision = %q, want the creating start lineage", root.Decision)
	}
}

func TestBuildRunViewFailedRunSurfacesEvidenceWithoutOutputBytes(t *testing.T) {
	frames := admissionFrames("inv-root")
	failure := transition(4, "inv-root", agentrun.StateRunning, agentrun.StateFailed, agentrun.DecisionNone)
	failure.OutcomeError = "adapter exploded"
	failure.OutputHash = "hash-output-1"
	frames = append(frames, failure)
	outcomes := []store.AttemptOutcome{{
		RunID: "run-1", JobID: "job-1", InvocationID: "inv-root", LineageID: "lineage-1",
		Class: agentrun.OutcomeFailure, Error: "adapter exploded", OutputHash: "hash-output-1", At: buildAt,
	}}
	view := BuildRunView(frames, outcomes, store.RunProjection{
		RunID: "run-1", Sequence: 4, Revision: 4,
		State: agentrun.StateFailed, Terminal: agentrun.TerminalFailure,
	})
	if view.Outcome != agentrun.OutcomeFailure || view.Error != "adapter exploded" {
		t.Fatalf("outcome/error = %q/%q, want failure with the terminal error text", view.Outcome, view.Error)
	}
	root := view.Invocations[0]
	if !root.HasOutputHash {
		t.Fatal("root evidence lost its output-hash presence flag")
	}
	if root.Error != "adapter exploded" {
		t.Fatalf("root error = %q, want the admitted evidence text", root.Error)
	}
}

func TestBuildRunViewAppendsOutcomesForUnseenInvocations(t *testing.T) {
	// A staged outcome recorded before any lifecycle event stays admitted
	// evidence: the view must not drop it just because no frame names it.
	outcomes := []store.AttemptOutcome{
		{InvocationID: "inv-staged", Class: agentrun.OutcomeTimeout, Error: "deadline", OutputHash: "hash-x"},
	}
	view := BuildRunView(nil, outcomes, store.RunProjection{
		RunID: "run-1", Sequence: 0, Revision: 0, State: agentrun.StateCreated,
	})
	if len(view.Invocations) != 1 {
		t.Fatalf("invocations = %+v, want the staged outcome as trailing evidence", view.Invocations)
	}
	staged := view.Invocations[0]
	if staged.InvocationID != "inv-staged" || staged.Order != 1 ||
		staged.OutcomeClass != agentrun.OutcomeTimeout || !staged.HasOutputHash {
		t.Fatalf("staged evidence = %+v, want the merged timeout outcome", staged)
	}
}

func TestBuildRunViewEmptyStreamFallsBackToProjectionOnly(t *testing.T) {
	view := BuildRunView(nil, nil, store.RunProjection{
		RunID: "run-1", Sequence: 0, Revision: 0,
		State: agentrun.StateCreated, Terminal: agentrun.TerminalNone,
	})
	if view.State != agentrun.StateCreated || view.Terminal != agentrun.TerminalNone {
		t.Fatalf("view = %+v, want the created non-terminal fallback", view)
	}
	if view.Invocations == nil || view.Responses == nil {
		t.Fatal("empty views must marshal as empty arrays, never null")
	}
}
