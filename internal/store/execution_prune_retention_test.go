// T9.5 retention guards for PruneExecutions: a run is collectible only when
// its measured evidence is fully represented in its immutable metrics
// snapshot. A missing snapshot keeps the execution (absence is unknown,
// never zero), and retry multiplicity lives only in the event stream, so a
// multi-attempt run is never collectible from its snapshot alone.
package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
)

// TestPruneExecutionsKeepsRunsWithoutMetricsSnapshot proves the phase
// invariant: nothing is deleted before its metrics snapshot exists. The
// fixture is terminal and old enough to prune on every other guard, so only
// the snapshot guard can keep it.
func TestPruneExecutionsKeepsRunsWithoutMetricsSnapshot(t *testing.T) {
	s := NewStore(t.TempDir())
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

// seedPruneRetryRun admits a run through the controller's retry shape:
// first attempt fails terminally, a DecisionRetry event relaunches attempt
// two, which succeeds. It returns the run identity; the fixture always
// carries two reconciled outcomes, pinned below so a silent fixture change
// cannot turn the multi-attempt guards it exercises into dead coverage.
func seedPruneRetryRun(t *testing.T, s *Store) string {
	t.Helper()
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
	return runID
}

// TestPruneExecutionsKeepsMultiAttemptRuns proves retry evidence is never
// collected through the snapshot: the schema records no attempt
// multiplicity, so deleting the stream would move the retried-runs
// aggregate. The saved snapshot isolates the guard: snapshot absence would
// keep the run for the wrong reason.
func TestPruneExecutionsKeepsMultiAttemptRuns(t *testing.T) {
	s := NewStore(t.TempDir())
	runID := seedPruneRetryRun(t, s)
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

// TestRemoveExecutionDirectoryRefusesMultiAttemptUnderLock proves the
// locked re-verification closes the classify→delete window for retries: a
// second attempt settling after classification still refuses the deletion
// with a typed refusal instead of destroying retry evidence.
func TestRemoveExecutionDirectoryRefusesMultiAttemptUnderLock(t *testing.T) {
	s := NewStore(t.TempDir())
	runID := seedPruneRetryRun(t, s)

	err := s.removeExecutionDirectory(runID, nil)
	var refusal pruneInLockRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("removeExecutionDirectory error = %v, want a typed in-lock refusal", err)
	}
	if !strings.Contains(refusal.reason, "attempt") {
		t.Fatalf("refusal reason = %q, want the multi-attempt guard", refusal.reason)
	}
	if !executionDirectoryExists(t, s, runID) {
		t.Fatalf("refused removal deleted the record anyway")
	}
}

// TestPruneExecutionsStillCollectsSnapshottedSingleAttemptRuns proves the
// guards keep only what they must: a terminal old single-attempt run WITH a
// snapshot and no references is still collected.
func TestPruneExecutionsStillCollectsSnapshottedSingleAttemptRuns(t *testing.T) {
	s := NewStore(t.TempDir())
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

// TestPruneExecutionsKeepsContradictorySnapshots proves collection needs
// agreement, not just presence: a terminal-success stream with a failing
// snapshot would flip the run's verdict from success to failed on
// collection, so the stream stays. The fixture mirrors the review engine's
// corrective-retry shape (semantic failure recorded, attempt ultimately
// succeeding) with a saved snapshot carrying the failure.
func TestPruneExecutionsKeepsContradictorySnapshots(t *testing.T) {
	s := NewStore(t.TempDir())
	runID, _ := seedPruneRun(t, s, "contradicted", "", pruneTerminalSuccess(), pruneAncientTime)
	snapshot := ExecutionMetrics{
		Version: ExecutionMetricsSchemaVersion,
		RunID:   runID,
		Failures: []ExecutionFailure{{
			Class:  FailureInvalidOutput,
			Detail: "recovered later in the same attempt",
		}},
	}
	if err := s.SaveExecutionMetrics(snapshot); err != nil {
		t.Fatalf("SaveExecutionMetrics() error = %v", err)
	}

	report, err := s.PruneExecutions(time.Now().Add(-24*time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
	assertKept(t, report, runID, "contradict")
	if !executionDirectoryExists(t, s, runID) {
		t.Fatalf("contradicted run %s was removed", runID)
	}
}
