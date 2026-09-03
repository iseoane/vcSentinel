// T9.5 retention guards for PruneExecutions: a run is collectible only when
// its measured evidence is fully represented in its immutable metrics
// snapshot. A missing snapshot keeps the execution (absence is unknown,
// never zero), and retry multiplicity lives only in the event stream, so a
// multi-attempt run is never collectible from its snapshot alone.
package store

import (
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
)

// TestPruneExecutionsKeepsRunsWithoutMetricsSnapshot proves the phase
// invariant: nothing is deleted before its metrics snapshot exists. The
// fixture is terminal and old enough to prune on every other guard, so only
// the snapshot guard can keep it.
func TestPruneExecutionsKeepsRunsWithoutMetricsSnapshot(t *testing.T) {
	s := NuevoStore(t.TempDir())
	runID, _ := seedPruneRun(t, s, "no-snapshot", "", pruneTerminalSuccess(), pruneAncientTime)
	if got, err := s.ReadExecutionMetrics(runID); err != nil || got != nil {
		t.Fatalf("fixture run %s must carry no snapshot: got=%v err=%v", runID, got, err)
	}

	report, err := s.PruneExecutions(time.Now().Add(-24*time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
	assertKept(t, report, runID, "snapshot")
	if !executionDirectoryExists(t, s, runID) {
		t.Fatalf("run %s without a snapshot was removed", runID)
	}
}

// TestPruneExecutionsKeepsMultiAttemptRuns proves retry evidence is never
// collected through the snapshot: the schema records no attempt
// multiplicity, so deleting the stream would move the retried-runs
// aggregate. The fixture pins both halves: two reconciled outcomes (so the
// guard, not the snapshot absence, is what keeps it) and a saved snapshot.
func TestPruneExecutionsKeepsMultiAttemptRuns(t *testing.T) {
	s := NuevoStore(t.TempDir())
	job := agentrun.NewLogicalJob(agentrun.NewRunRequest(
		agentrun.Candidate("candidate:retried"), agentrun.Prompt("retried"), nil))
	if err := s.CreateRun(job, RunPolicy{ID: "policy:prune"}); err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	runID := string(job.RunID())
	root, err := agentrun.NewRootInvocation(job, 1, agentrun.DecisionStart)
	if err != nil {
		t.Fatal(err)
	}
	var revision uint64
	appendLifecycle := func(invocation agentrun.InvocationEnvelope, from, to agentrun.LifecycleState, decision agentrun.Decision) {
		t.Helper()
		event, err := agentrun.NewNormalizedEvent(invocation, from, to, decision, pruneAncientTime.Add(time.Duration(revision)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.AppendEvent(runID, event, revision); err != nil {
			t.Fatalf("AppendEvent(%s -> %s) error = %v", from, to, err)
		}
		revision++
	}
	// First attempt fails: Created through Running, then a terminal failure.
	// A retry cannot relaunch from success ("success stays final"), so the
	// retried fixture must fail first.
	appendLifecycle(root, agentrun.StateCreated, agentrun.StateQueued, agentrun.DecisionStart)
	appendLifecycle(root, agentrun.StateQueued, agentrun.StateAdmitted, agentrun.DecisionStart)
	appendLifecycle(root, agentrun.StateAdmitted, agentrun.StateRunning, agentrun.DecisionStart)
	appendLifecycle(root, agentrun.StateRunning, agentrun.StateFailed, agentrun.DecisionNone)
	// Relaunch as attempt two, then succeed: this is the controller's retry
	// shape (Failed -> Running under DecisionRetry, then to Succeeded).
	retry, err := agentrun.NewChildInvocation(root, 2, agentrun.DecisionRetry)
	if err != nil {
		t.Fatal(err)
	}
	appendLifecycle(retry, agentrun.StateFailed, agentrun.StateRunning, agentrun.DecisionRetry)
	appendLifecycle(retry, agentrun.StateRunning, agentrun.StateSucceeded, agentrun.DecisionComplete)
	outcomes, err := s.ReadAttemptOutcomes(runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 2 {
		t.Fatalf("fixture run %s has %d outcomes, want 2 attempts", runID, len(outcomes))
	}
	if err := s.SaveExecutionMetrics(ExecutionMetrics{Version: ExecutionMetricsSchemaVersion, RunID: runID}); err != nil {
		t.Fatalf("SaveExecutionMetrics() error = %v", err)
	}

	report, err := s.PruneExecutions(time.Now().Add(-24*time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
	assertKept(t, report, runID, "attempt")
	if !executionDirectoryExists(t, s, runID) {
		t.Fatalf("multi-attempt run %s was removed", runID)
	}
}

// TestPruneExecutionsStillCollectsSnapshottedSingleAttemptRuns proves the
// guards keep only what they must: a terminal old single-attempt run WITH a
// snapshot and no references is still collected.
func TestPruneExecutionsStillCollectsSnapshottedSingleAttemptRuns(t *testing.T) {
	s := NuevoStore(t.TempDir())
	runID, _ := seedPruneRun(t, s, "collectible", "", pruneTerminalSuccess(), pruneAncientTime)
	if err := s.SaveExecutionMetrics(ExecutionMetrics{Version: ExecutionMetricsSchemaVersion, RunID: runID}); err != nil {
		t.Fatalf("SaveExecutionMetrics() error = %v", err)
	}

	report, err := s.PruneExecutions(time.Now().Add(-24*time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
	if decision := decisionFor(t, report, runID); decision.Action != PruneActionPruned {
		t.Fatalf("snapshotted single-attempt run action = %q (%s), want pruned", decision.Action, decision.Reason)
	}
	if executionDirectoryExists(t, s, runID) {
		t.Fatalf("collectible run %s directory still exists", runID)
	}
	if got, err := s.ReadExecutionMetrics(runID); err != nil || got == nil {
		t.Fatalf("snapshot must survive collection: got=%v err=%v", got, err)
	}
}
