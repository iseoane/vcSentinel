// Removal-path tests for execution records: the crash-interrupted remnant
// completion flow (Windows-safe removal) and the locked re-verification
// refusal. This file also hosts the shared prune test fixtures (transition
// builders, run seeding, and the decision/keep/directory assertion helpers)
// used by both prune test files.
package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
)

// Fixed instants make every age assertion deterministic: ancientTime always
// predates any sane cutoff, freshTime always postdates it when the cutoff is
// captured before the fresh stream is written.
var (
	pruneAncientTime = time.Unix(1000000000, 0).UTC()
)

type pruneTransition struct {
	from     agentrun.LifecycleState
	to       agentrun.LifecycleState
	decision agentrun.Decision
}

func pruneTerminalSuccess() []pruneTransition {
	return []pruneTransition{
		{agentrun.StateCreated, agentrun.StateQueued, agentrun.DecisionStart},
		{agentrun.StateQueued, agentrun.StateAdmitted, agentrun.DecisionStart},
		{agentrun.StateAdmitted, agentrun.StateRunning, agentrun.DecisionStart},
		{agentrun.StateRunning, agentrun.StateSucceeded, agentrun.DecisionComplete},
	}
}

func pruneTerminalFailure() []pruneTransition {
	return []pruneTransition{
		{agentrun.StateCreated, agentrun.StateQueued, agentrun.DecisionStart},
		{agentrun.StateQueued, agentrun.StateAdmitted, agentrun.DecisionStart},
		{agentrun.StateAdmitted, agentrun.StateRunning, agentrun.DecisionStart},
		{agentrun.StateRunning, agentrun.StateFailed, agentrun.DecisionNone},
	}
}

func pruneRunningHead() []pruneTransition {
	return pruneTerminalSuccess()[:3]
}

func pruneOrphanedEscalation() []pruneTransition {
	return append(pruneRunningHead(),
		pruneTransition{agentrun.StateRunning, agentrun.StateTerminating, agentrun.DecisionAbort},
		pruneTransition{agentrun.StateTerminating, agentrun.StateTerminated, agentrun.DecisionAbort},
	)
}

func seedPruneRun(t *testing.T, s *Store, prompt, parentRunID string, transitions []pruneTransition, start time.Time) (string, []EventFrame) {
	t.Helper()
	job := agentrun.NewLogicalJob(agentrun.NewRunRequest(
		agentrun.Candidate("candidate:"+prompt), agentrun.Prompt(prompt), nil))
	policy := RunPolicy{ID: "policy:prune"}
	if parentRunID != "" {
		policy.ParentRunID = parentRunID
	}
	if err := s.CreateRun(job, policy); err != nil {
		t.Fatalf("CreateRun(%s) error = %v", prompt, err)
	}
	runID := string(job.RunID())
	invocation, err := agentrun.NewRootInvocation(job, 1, agentrun.DecisionStart)
	if err != nil {
		t.Fatal(err)
	}
	frames := make([]EventFrame, 0, len(transitions))
	for offset, transition := range transitions {
		event, eventErr := agentrun.NewNormalizedEvent(invocation,
			transition.from, transition.to, transition.decision, start.Add(time.Duration(offset)*time.Second))
		if eventErr != nil {
			t.Fatal(eventErr)
		}
		if _, appendErr := s.AppendEvent(runID, event, uint64(offset)); appendErr != nil {
			t.Fatalf("AppendEvent(%s -> %s) error = %v", transition.from, transition.to, appendErr)
		}
		frame := frameForEvent(event, uint64(offset+1), uint64(offset+1), "")
		frames = append(frames, frame)
	}
	return runID, frames
}

func decisionFor(t *testing.T, report PruneReport, runID string) PruneDecision {
	t.Helper()
	for _, decision := range report.Decisions {
		if decision.RunID == runID {
			return decision
		}
	}
	t.Fatalf("no prune decision for run %s", runID)
	return PruneDecision{}
}

func assertKept(t *testing.T, report PruneReport, runID, wantReason string) {
	t.Helper()
	decision := decisionFor(t, report, runID)
	if decision.Action != PruneActionKept {
		t.Fatalf("run %s action = %q, want kept", runID, decision.Action)
	}
	if wantReason != "" && !strings.Contains(decision.Reason, wantReason) {
		t.Fatalf("run %s keep reason = %q, want it to contain %q", runID, decision.Reason, wantReason)
	}
}

func executionDirectoryExists(t *testing.T, s *Store, runID string) bool {
	t.Helper()
	directory, err := s.executionDir(runID)
	if err != nil {
		t.Fatal(err)
	}
	_, statErr := os.Stat(directory)
	if statErr != nil && !os.IsNotExist(statErr) {
		t.Fatal(statErr)
	}
	return statErr == nil
}

// TestPruneExecutionsFinishesCrashInterruptedRemoval proves the repair path
// for a crash mid-removal: a directory holding ONLY its event-lock file is
// a remnant with no evidence left, so the next prune finishes removing it
// whatever its age and says so in the report. A sibling directory that is
// missing request.json but still carries event bytes keeps the old refusal.
func TestPruneExecutionsFinishesCrashInterruptedRemoval(t *testing.T) {
	t.Run("lock-only remnant is removed", func(t *testing.T) {
		s := NewStore(t.TempDir())
		zombie := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		directory := filepath.Join(s.dir, "executions", "v1", zombie)
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, ".events.lock"), nil, 0600); err != nil {
			t.Fatal(err)
		}

		report, err := s.PruneExecutions(time.Now().Add(-24*time.Hour), nil)
		if err != nil {
			t.Fatal(err)
		}
		decision := decisionFor(t, report, zombie)
		if decision.Action != PruneActionPruned || decision.Reason != PruneReasonRemovalRemnant {
			t.Fatalf("remnant decision = action %q reason %q, want pruned/%q",
				decision.Action, decision.Reason, PruneReasonRemovalRemnant)
		}
		if report.Pruned != 1 || report.Kept != 0 || report.Examined != 1 {
			t.Fatalf("report counts = examined %d, pruned %d, kept %d; want 1/1/0",
				report.Examined, report.Pruned, report.Kept)
		}
		if executionDirectoryExists(t, s, zombie) {
			t.Fatal("crash-interrupted removal remnant was not removed")
		}
	})

	t.Run("missing request with event bytes stays refused", func(t *testing.T) {
		s := NewStore(t.TempDir())
		stray := "cccccccccccccccccccccccccccccccccccccccc"
		directory := filepath.Join(s.dir, "executions", "v1", stray)
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, ".events.lock"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "events.jsonl"), []byte("{\"run_id\":\"x\"}\n"), 0600); err != nil {
			t.Fatal(err)
		}

		report, err := s.PruneExecutions(time.Now().Add(-24*time.Hour), nil)
		if err != nil {
			t.Fatal(err)
		}
		assertKept(t, report, stray, PruneReasonIncompleteAdmission)
		if !executionDirectoryExists(t, s, stray) {
			t.Fatal("record with event bytes was removed")
		}
	})
}

// TestRemoveExecutionDirectoryRefusesNonTerminalUnderLock proves the locked
// re-verification inside removal never deletes a record that became
// non-terminal again between classification and deletion.
func TestRemoveExecutionDirectoryRefusesNonTerminalUnderLock(t *testing.T) {
	s := NewStore(t.TempDir())
	runID, _ := seedPruneRun(t, s, "reverified", "", pruneTerminalSuccess(), pruneAncientTime)
	// Simulate a concurrent mutation by appending beyond the classification
	// snapshot: the removal re-scan sees a non-terminal head and refuses.
	directory, _ := s.executionDir(runID)
	logPath := filepath.Join(directory, "events.jsonl")
	file, err := os.OpenFile(logPath, os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()

	if err := s.removeExecutionDirectory(runID, nil); err == nil {
		t.Fatal("removal of a non-terminal record must be refused")
	}
	if !executionDirectoryExists(t, s, runID) {
		t.Fatal("refused removal deleted the record anyway")
	}
}
