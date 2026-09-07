package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

type transcriptConflictAdapter struct {
	backing *store.Store
	at      time.Time
	saveErr error
}

func (a *transcriptConflictAdapter) Execute(_ context.Context, job agentrun.LogicalJob, invocation agentrun.InvocationEnvelope, _ string) (AdapterResult, error) {
	a.saveErr = a.backing.SaveAttemptOutcome(store.AttemptOutcome{
		RunID:        invocation.RunID().String(),
		JobID:        job.ID().String(),
		InvocationID: invocation.InvocationID().String(),
		LineageID:    invocation.LineageIdentity().String(),
		Class:        agentrun.OutcomeSuccess,
		OutputHash:   "conflicting-outcome",
		At:           a.at,
	})
	return AdapterResult{Output: "raw provider output"}, nil
}

func (*transcriptConflictAdapter) TranscriptMetadata() TranscriptIdentity {
	return TranscriptIdentity{Agent: "test-agent"}
}

func TestControllerRemovesTranscriptWhenTerminalPersistenceFails(t *testing.T) {
	at := fixedClock()()
	backing := store.NewStore(t.TempDir())
	adapter := &transcriptConflictAdapter{backing: backing, at: at}
	controller := NewControllerWithClock(backing, adapter, func() time.Time { return at })

	handle, err := controller.Start(context.Background(), testRequest("transcript-cleanup"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	_, err = handle.Wait(context.Background())
	if err == nil {
		t.Fatal("Wait() error = nil, want terminal persistence failure")
	}
	if adapter.saveErr != nil {
		t.Fatalf("SaveAttemptOutcome() error = %v", adapter.saveErr)
	}
	executionDir, err := backing.ExecutionDir(handle.RunID.String())
	if err != nil {
		t.Fatal(err)
	}
	_, err = os.Stat(filepath.Join(executionDir, "transcripts", handle.InvocationID.String()+".json"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("transcript sidecar stat = %v, want not exist after terminal persistence failure", err)
	}
}
