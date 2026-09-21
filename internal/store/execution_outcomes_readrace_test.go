// Read-side consistency regression for ticket 14: ReadAttemptOutcomes reads
// the legacy sidecar records before scanning the event log, so a lock-free
// reader racing AppendTerminalEvent's locked write sequence can never observe
// a fresh outcomes/<invocation>.json together with a stale event log. Any
// "outcome has no terminal event" verdict seen here is therefore the split
// read the ordering fix exists to prevent.
package store

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
)

func TestReadAttemptOutcomesNeverSplitsATerminalPersistenceWrite(t *testing.T) {
	const iterations = 15
	for i := 0; i < iterations; i++ {
		s := NewStore(t.TempDir())
		job := agentrun.NewLogicalJob(agentrun.NewRunRequest(
			agentrun.Candidate("candidate:read-race"), agentrun.Prompt("read-race"), nil))
		runID := string(job.RunID())
		if err := s.CreateRun(job, RunPolicy{ID: "policy:test"}); err != nil {
			t.Fatal(err)
		}
		invocation, err := agentrun.NewRootInvocation(job, 1, agentrun.DecisionStart)
		if err != nil {
			t.Fatal(err)
		}

		var wg sync.WaitGroup
		writerDone := make(chan struct{})
		wg.Add(1)
		go func() {
			defer wg.Done()
			at := time.Unix(1000, 0).UTC()
			transitions := []struct {
				from, to agentrun.LifecycleState
			}{
				{agentrun.StateCreated, agentrun.StateQueued},
				{agentrun.StateQueued, agentrun.StateAdmitted},
				{agentrun.StateAdmitted, agentrun.StateRunning},
			}
			for revision, transition := range transitions {
				event, eventErr := agentrun.NewNormalizedEvent(invocation,
					transition.from, transition.to, agentrun.DecisionStart, at.Add(time.Duration(revision)*time.Second))
				if eventErr != nil {
					t.Error(eventErr)
					return
				}
				if _, appendErr := s.AppendEvent(runID, event, uint64(revision)); appendErr != nil {
					t.Errorf("AppendEvent error = %v", appendErr)
					return
				}
			}
			event, eventErr := agentrun.NewNormalizedEvent(invocation,
				agentrun.StateRunning, agentrun.StateFailed, agentrun.DecisionNone, at.Add(10*time.Second))
			if eventErr != nil {
				t.Error(eventErr)
				return
			}
			if _, appendErr := s.AppendTerminalEvent(runID, event, 3, AttemptOutcome{
				RunID: runID, JobID: string(job.ID()), InvocationID: string(invocation.InvocationID()),
				LineageID: string(invocation.LineageIdentity()), Class: agentrun.OutcomeFailure,
				Error: "raced persistence", At: at.Add(10 * time.Second),
			}); appendErr != nil {
				t.Errorf("AppendTerminalEvent error = %v", appendErr)
			}
			close(writerDone)
		}()

		var forbidden []error
		for closed := false; !closed; <-time.After(time.Microsecond) {
			select {
			case <-writerDone:
				closed = true
			default:
			}
			_, readErr := s.ReadAttemptOutcomes(runID)
			if readErr != nil && strings.Contains(readErr.Error(), "outcome has no terminal event") {
				forbidden = append(forbidden, readErr)
			}
		}
		wg.Wait()

		if len(forbidden) > 0 {
			t.Fatalf("reader observed %d split mid-persistence reads, first: %v", len(forbidden), forbidden[0])
		}
		outcomes, readErr := s.ReadAttemptOutcomes(runID)
		if readErr != nil || len(outcomes) != 1 || outcomes[0].Class != agentrun.OutcomeFailure {
			t.Fatalf("final outcomes = %+v, %v; want the single settled failure", outcomes, readErr)
		}
	}
}
