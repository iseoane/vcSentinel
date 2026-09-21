package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
)

func TestAttemptOutcomeAndResponseRecordsAreImmutableAndInspectable(t *testing.T) {
	store := NewStore(t.TempDir())
	job := testJob()
	if err := store.CreateRun(job, RunPolicy{ID: "policy-id"}); err != nil {
		t.Fatal(err)
	}
	root, err := agentrun.NewRootInvocation(job, 1, agentrun.DecisionStart)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Unix(200, 0).UTC()
	outcome := AttemptOutcome{
		RunID: string(job.RunID()), JobID: string(job.ID()), InvocationID: string(root.InvocationID()),
		LineageID: string(root.LineageIdentity()), Class: agentrun.OutcomeFailure,
		Error: "provider failed", OutputHash: "output-hash", At: at,
	}
	if err := store.SaveAttemptOutcome(outcome); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAttemptOutcome(outcome); err != nil {
		t.Fatalf("identical outcome retry = %v", err)
	}
	changed := outcome
	changed.Error = "different failure"
	if err := store.SaveAttemptOutcome(changed); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("changed outcome error = %v, want immutable conflict", err)
	}

	child, err := agentrun.NewChildInvocation(root, 2, agentrun.DecisionRespond)
	if err != nil {
		t.Fatal(err)
	}
	response := InvocationResponse{
		RunID: string(job.RunID()), ParentInvocationID: string(root.InvocationID()),
		InvocationID: string(child.InvocationID()), LineageID: string(child.LineageIdentity()),
		ResponseHash: "response-hash", At: at.Add(time.Second),
	}
	if err := store.SaveInvocationResponse(response); err != nil {
		t.Fatal(err)
	}
	outcomes, err := store.ReadAttemptOutcomes(string(job.RunID()))
	if err != nil || len(outcomes) != 1 || outcomes[0].InvocationID != string(root.InvocationID()) {
		t.Fatalf("outcomes = %+v, error = %v", outcomes, err)
	}
	responses, err := store.ReadInvocationResponses(string(job.RunID()))
	if err != nil || len(responses) != 1 || responses[0].InvocationID != string(child.InvocationID()) {
		t.Fatalf("responses = %+v, error = %v", responses, err)
	}

	directory, err := store.executionDir(string(job.RunID()))
	if err != nil {
		t.Fatal(err)
	}
	responseData, err := os.ReadFile(filepath.Join(directory, "responses", string(child.InvocationID())+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(responseData), "continue") {
		t.Fatalf("response record leaked raw answer: %s", responseData)
	}
}

func TestReadAttemptOutcomesRetainsLegacyCompatibilityForUnembeddedTerminalEvent(t *testing.T) {
	store := NewStore(t.TempDir())
	job := testJob()
	runID := string(job.RunID())
	if err := store.CreateRun(job, RunPolicy{ID: "policy-id"}); err != nil {
		t.Fatal(err)
	}
	invocation, err := agentrun.NewRootInvocation(job, 1, agentrun.DecisionStart)
	if err != nil {
		t.Fatal(err)
	}
	transitions := []struct {
		from, to agentrun.LifecycleState
	}{
		{agentrun.StateCreated, agentrun.StateQueued},
		{agentrun.StateQueued, agentrun.StateAdmitted},
		{agentrun.StateAdmitted, agentrun.StateRunning},
	}
	for revision, transition := range transitions {
		event, err := agentrun.NewNormalizedEvent(invocation, transition.from, transition.to, agentrun.DecisionStart, time.Unix(int64(revision), 0))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.AppendEvent(runID, event, uint64(revision)); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Unix(300, 0).UTC()
	event, err := agentrun.NewNormalizedEvent(invocation, agentrun.StateRunning, agentrun.StateFailed, agentrun.DecisionNone, at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(runID, event, uint64(len(transitions))); err != nil {
		t.Fatal(err)
	}
	outcome := AttemptOutcome{
		RunID: string(job.RunID()), JobID: string(job.ID()), InvocationID: string(invocation.InvocationID()),
		LineageID: string(invocation.LineageIdentity()), Class: agentrun.OutcomeFailure,
		Error: "legacy failure", OutputHash: "legacy-output", At: at,
	}
	if err := store.SaveAttemptOutcome(outcome); err != nil {
		t.Fatal(err)
	}

	outcomes, err := store.ReadAttemptOutcomes(runID)
	if err != nil || len(outcomes) != 1 || outcomes[0] != outcome {
		t.Fatalf("outcomes = %+v, error = %v, want legacy outcome", outcomes, err)
	}

	directory, err := store.executionDir(runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "outcomes", string(invocation.InvocationID())+".json"), []byte("not-json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadAttemptOutcomes(runID); !errors.Is(err, ErrAttemptOutcomeCorrupt) {
		t.Fatalf("corrupt legacy outcome error = %v, want corrupt outcome", err)
	}
}

func TestReadDerivedProjectionUsesValidatedEventsWhenStateFileIsStale(t *testing.T) {
	store := NewStore(t.TempDir())
	job := testJob()
	runID := string(job.RunID())
	if err := store.CreateRun(job, RunPolicy{ID: "policy-id"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(runID, testEvent(t, job, agentrun.StateCreated, agentrun.StateQueued), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(runID, testEvent(t, job, agentrun.StateQueued, agentrun.StateAdmitted), 1); err != nil {
		t.Fatal(err)
	}
	directory, err := store.executionDir(runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "state.json"), []byte(`{"run_id":"`+runID+`","state":"created"}`), 0600); err != nil {
		t.Fatal(err)
	}
	projection, err := store.ReadDerivedProjection(runID)
	if err != nil {
		t.Fatal(err)
	}
	if projection.State != agentrun.StateAdmitted || projection.Revision != 2 {
		t.Fatalf("derived projection = %+v, want admitted revision 2", projection)
	}
}
