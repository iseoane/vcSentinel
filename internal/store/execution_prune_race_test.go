// Classify→delete TOCTOU window tests for PruneExecutions (ticket 13
// hardening pool, JD-R10 W1): provenance references or parent linkage that
// appear BETWEEN classification and the locked removal must refuse the
// deletion. Both the end-to-end race (via the pruneRaceWindowHook seam) and
// the direct in-lock refusal are proven here.
package store

import (
	"errors"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
)

// setRaceWindowHook installs a hook that runs inside PruneExecutions after
// classification and before any removal, restoring the previous hook when
// the test finishes.
func setRaceWindowHook(t *testing.T, hook func(s *Store)) {
	t.Helper()
	previous := pruneRaceWindowHook
	pruneRaceWindowHook = hook
	t.Cleanup(func() { pruneRaceWindowHook = previous })
}

// TestPruneExecutionsRefusesReferencePersistedBetweenClassifyAndLock proves
// the closed TOCTOU window: a review reference persisted after the run was
// classified as prunable still refuses its deletion, because the locked
// section re-checks the reference set before removing anything.
func TestPruneExecutionsRefusesReferencePersistedBetweenClassifyAndLock(t *testing.T) {
	s := NewStore(t.TempDir())
	runID, frames := seedPruneRun(t, s, "late-reference", "", pruneTerminalSuccess(), pruneAncientTime)
	// The run must clear classification — including the T9.5 snapshot
	// guard — or the race hook it exists to exercise never fires.
	if err := s.SaveExecutionMetrics(ExecutionMetrics{Version: ExecutionMetricsSchemaVersion, RunID: runID}); err != nil {
		t.Fatalf("SaveExecutionMetrics() error = %v", err)
	}
	references := map[string]bool{}

	setRaceWindowHook(t, func(s *Store) {
		// The review finding lands AFTER classification saw an empty set.
		references[frames[0].InvocationID] = true
	})

	report, err := s.PruneExecutions(time.Now().Add(-24*time.Hour), references)
	if err != nil {
		t.Fatal(err)
	}
	assertKept(t, report, runID, "review provenance references invocation "+frames[0].InvocationID)
	if report.Pruned != 0 || report.Kept != 1 {
		t.Fatalf("report counts = pruned %d, kept %d; want 0/1", report.Pruned, report.Kept)
	}
	if !executionDirectoryExists(t, s, runID) {
		t.Fatal("referenced record was removed despite the in-lock refusal")
	}
}

// TestPruneExecutionsRefusesChildPersistedBetweenClassifyAndLock proves the
// second half of the window: a child run admitted after classification names
// the prunable record as its parent, and the locked re-scan keeps the root.
func TestPruneExecutionsRefusesChildPersistedBetweenClassifyAndLock(t *testing.T) {
	s := NewStore(t.TempDir())
	parentRun, _ := seedPruneRun(t, s, "gate-root", "", pruneTerminalSuccess(), pruneAncientTime)
	// The root must clear classification — including the T9.5 snapshot
	// guard — or the late-child hook it exists to exercise never fires.
	if err := s.SaveExecutionMetrics(ExecutionMetrics{Version: ExecutionMetricsSchemaVersion, RunID: parentRun}); err != nil {
		t.Fatalf("SaveExecutionMetrics() error = %v", err)
	}
	setRaceWindowHook(t, func(s *Store) {
		job := agentrun.NewLogicalJob(agentrun.NewRunRequest(
			agentrun.Candidate("candidate:late-child"), agentrun.Prompt("late-child"), nil))
		if err := s.CreateRun(job, RunPolicy{ID: "policy:prune", ParentRunID: parentRun}); err != nil {
			t.Fatalf("CreateRun(late child) error = %v", err)
		}
	})

	report, err := s.PruneExecutions(time.Now().Add(-24*time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
	assertKept(t, report, parentRun, "parent of surviving run ")
	if report.Pruned != 0 {
		t.Fatalf("pruned %d runs, want zero: %+v", report.Pruned, report.Decisions)
	}
	if !executionDirectoryExists(t, s, parentRun) {
		t.Fatal("parent of a late-appearing child was removed")
	}
}

// TestRemoveExecutionDirectoryRefusesReferencedInvocationUnderLock proves
// the typed in-lock refusal directly: a populated reference set passed into
// the removal path stops the deletion with a pruneInLockRefusal carrying the
// stable provenance reason, leaving every byte in place.
func TestRemoveExecutionDirectoryRefusesReferencedInvocationUnderLock(t *testing.T) {
	s := NewStore(t.TempDir())
	runID, frames := seedPruneRun(t, s, "locked-reference", "", pruneTerminalSuccess(), pruneAncientTime)

	err := s.removeExecutionDirectory(runID, map[string]bool{frames[0].InvocationID: true})
	var refusal pruneInLockRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("removeExecutionDirectory error = %v, want a typed in-lock refusal", err)
	}
	want := "review provenance references invocation " + frames[0].InvocationID
	if refusal.reason != want {
		t.Fatalf("refusal reason = %q, want %q", refusal.reason, want)
	}
	if !executionDirectoryExists(t, s, runID) {
		t.Fatal("refused removal deleted the record anyway")
	}
}

// TestRemoveExecutionDirectoryRefusesLateChildUnderLock proves the locked
// child re-scan: another run whose persisted request names this record as
// ParentRunID blocks the deletion with a typed refusal, even though the
// reference set is empty.
func TestRemoveExecutionDirectoryRefusesLateChildUnderLock(t *testing.T) {
	s := NewStore(t.TempDir())
	parentRun, _ := seedPruneRun(t, s, "gate-root", "", pruneTerminalSuccess(), pruneAncientTime)
	childJob := agentrun.NewLogicalJob(agentrun.NewRunRequest(
		agentrun.Candidate("candidate:child"), agentrun.Prompt("child"), nil))
	if err := s.CreateRun(childJob, RunPolicy{ID: "policy:prune", ParentRunID: parentRun}); err != nil {
		t.Fatal(err)
	}

	err := s.removeExecutionDirectory(parentRun, nil)
	var refusal pruneInLockRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("removeExecutionDirectory error = %v, want a typed in-lock refusal", err)
	}
	if want := "parent of surviving run " + string(childJob.RunID()); refusal.reason != want {
		t.Fatalf("refusal reason = %q, want %q", refusal.reason, want)
	}
	if !executionDirectoryExists(t, s, parentRun) {
		t.Fatal("parent of a surviving child was removed")
	}
}
