package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
)

func TestAttemptOutcomeAndResponseRecordsAreImmutableAndInspectable(t *testing.T) {
	store := NuevoStore(t.TempDir())
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
