package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// appendReconciledFixtureStream writes a valid synthetic stream directly so
// the runs observation surfaces can be tested against owner death during
// cancellation without driving a live controller to that state.
func appendReconciledFixtureStream(t *testing.T, backing *store.Store, candidate string, extra []struct {
	from     agentrun.LifecycleState
	to       agentrun.LifecycleState
	decision agentrun.Decision
}) agentrun.Identity {
	t.Helper()
	job := agentrun.NewLogicalJob(agentrun.NewRunRequest(agentrun.Candidate(candidate), agentrun.Prompt("fixture"), nil))
	if err := backing.CreateRun(job, store.RunPolicy{ID: "policy:test"}); err != nil {
		t.Fatal(err)
	}
	invocation, err := agentrun.NewRootInvocation(job, 1, agentrun.DecisionStart)
	if err != nil {
		t.Fatal(err)
	}
	transitions := []struct {
		from     agentrun.LifecycleState
		to       agentrun.LifecycleState
		decision agentrun.Decision
	}{
		{agentrun.StateCreated, agentrun.StateQueued, agentrun.DecisionStart},
		{agentrun.StateQueued, agentrun.StateAdmitted, agentrun.DecisionStart},
		{agentrun.StateAdmitted, agentrun.StateRunning, agentrun.DecisionStart},
	}
	for revision, transition := range transitions {
		event, eventErr := agentrun.NewNormalizedEvent(invocation, transition.from, transition.to, transition.decision, time.Unix(1700000000, 0).UTC())
		if eventErr != nil {
			t.Fatal(eventErr)
		}
		if _, appendErr := backing.AppendEvent(string(job.RunID()), event, uint64(revision)); appendErr != nil {
			t.Fatal(appendErr)
		}
	}
	for offset, transition := range extra {
		event, eventErr := agentrun.NewNormalizedEvent(invocation, transition.from, transition.to, transition.decision, time.Unix(1700000000+int64(offset)+1, 0).UTC())
		if eventErr != nil {
			t.Fatal(eventErr)
		}
		if _, appendErr := backing.AppendEvent(string(job.RunID()), event, uint64(len(transitions)+offset)); appendErr != nil {
			t.Fatal(appendErr)
		}
	}
	return job.RunID()
}

// TestRunsObservationSurfacesReconcileOwnerDeathDuringCancellation proves the
// ticket 08 slice-3 operator contract: an orphaned-canceled run shows its
// honest terminal view in both listing and inspection, while honestly settled
// runs stay untouched.
func TestRunsObservationSurfacesReconcileOwnerDeathDuringCancellation(t *testing.T) {
	backing := store.NuevoStore(t.TempDir())
	settled := appendReconciledFixtureStream(t, backing, "candidate:settled", []struct {
		from     agentrun.LifecycleState
		to       agentrun.LifecycleState
		decision agentrun.Decision
	}{
		{agentrun.StateRunning, agentrun.StateSucceeded, agentrun.DecisionComplete},
	})
	orphaned := appendReconciledFixtureStream(t, backing, "candidate:orphaned", []struct {
		from     agentrun.LifecycleState
		to       agentrun.LifecycleState
		decision agentrun.Decision
	}{
		{agentrun.StateRunning, agentrun.StateTerminating, agentrun.DecisionNone},
	})

	var out bytes.Buffer
	if code := listExecutions(&out, backing, false); code != runExitSuccess {
		t.Fatalf("listExecutions exit = %d, output:\n%s", code, out.String())
	}
	text := out.String()
	if !strings.Contains(text, "(orphaned-canceled)") || !strings.Contains(text, "state=canceled") {
		t.Fatalf("listing did not surface the canceled-orphaned reconciliation:\n%s", text)
	}
	if lines := strings.Count(text, "(orphaned-canceled)"); lines != 1 {
		t.Fatalf("orphaned-canceled marker appeared %d times, want exactly once for run %s only:\n%s", lines, orphaned, text)
	}

	out.Reset()
	if code := listExecutions(&out, backing, true); code != runExitSuccess {
		t.Fatalf("JSON listExecutions exit = %d", code)
	}
	var decoded struct {
		Runs []runsListEntry `json:"runs"`
	}
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	flags := map[string]bool{}
	for _, entry := range decoded.Runs {
		flags[entry.RunID] = entry.OrphanedCancellation
	}
	if flags[string(settled)] {
		t.Fatalf("honestly settled run %s must not carry the reconciliation flag", settled)
	}
	if !flags[string(orphaned)] {
		t.Fatalf("orphaned-canceled run %s lost its reconciliation flag in JSON output", orphaned)
	}

	out.Reset()
	controller := execution.NewController(backing, nil)
	if code := inspectExecution(&out, controller, backing, orphaned, "test-harness", false); code != runExitSuccess {
		t.Fatalf("inspectExecution exit = %d, output:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "canceled-orphaned") || !strings.Contains(out.String(), "state canceled") {
		t.Fatalf("inspection did not surface the honest reconciled view:\n%s", out.String())
	}

	// The settled control stays byte-for-byte honest in inspection too.
	out.Reset()
	if code := inspectExecution(&out, controller, backing, settled, "test-harness", false); code != runExitSuccess {
		t.Fatalf("inspectExecution(settled) exit = %d", code)
	}
	if strings.Contains(out.String(), "canceled-orphaned") {
		t.Fatalf("inspection fabricated a reconciliation verdict for settled run %s:\n%s", settled, out.String())
	}

	// Coherence pin (ticket 08 slice 3): when reconciliation applies, EVERY
	// displayed projection field comes from the reconciled view — never a mix
	// of raw and derived values.
	out.Reset()
	if code := inspectExecution(&out, controller, backing, orphaned, "test-harness", true); code != runExitSuccess {
		t.Fatalf("JSON inspectExecution exit = %d", code)
	}
	var summary runsStatusSummary
	if err := json.Unmarshal(out.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	reconciled, err := backing.ReadReconciledProjection(string(orphaned))
	if err != nil {
		t.Fatal(err)
	}
	if summary.State != reconciled.State || summary.Sequence != reconciled.Sequence || summary.Revision != reconciled.Revision {
		t.Fatalf("displayed fields = {state:%s sequence:%d revision:%d}, want the coherent reconciled view {state:%s sequence:%d revision:%d}",
			summary.State, summary.Sequence, summary.Revision, reconciled.State, reconciled.Sequence, reconciled.Revision)
	}

	// The settled control displays exactly its raw projection fields.
	out.Reset()
	if code := inspectExecution(&out, controller, backing, settled, "test-harness", true); code != runExitSuccess {
		t.Fatalf("JSON inspectExecution(settled) exit = %d", code)
	}
	var settledSummary runsStatusSummary
	if err := json.Unmarshal(out.Bytes(), &settledSummary); err != nil {
		t.Fatal(err)
	}
	raw, err := backing.ReadDerivedProjection(string(settled))
	if err != nil {
		t.Fatal(err)
	}
	if settledSummary.State != raw.State || settledSummary.Sequence != raw.Sequence || settledSummary.Revision != raw.Revision {
		t.Fatalf("settled display drifted from the raw projection: got {state:%s sequence:%d revision:%d}, want {state:%s sequence:%d revision:%d}",
			settledSummary.State, settledSummary.Sequence, settledSummary.Revision, raw.State, raw.Sequence, raw.Revision)
	}
}
