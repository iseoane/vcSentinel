package store

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
)

func TestReadExecutionRequestReturnsAdmittedIdentities(t *testing.T) {
	store := NewStore(t.TempDir())
	job := testJob()
	if err := store.CreateRun(job, RunPolicy{ID: "policy-id"}); err != nil {
		t.Fatal(err)
	}

	request, err := store.ReadExecutionRequest(string(job.RunID()))
	if err != nil {
		t.Fatalf("ReadExecutionRequest() error = %v", err)
	}
	if !reflect.DeepEqual(request, requestFor(job)) {
		t.Fatalf("request = %+v, want %+v", request, requestFor(job))
	}
}

// TestParentRunIDLinkageIsPersistedAdditively proves the ticket-11 parent
// linkage contract: a run admitted with RunPolicy.ParentRunID carries that
// parent in its immutable request record, a root run's record stays
// byte-identical to the legacy shape (no parent_run_id key at all), and old
// records without the field read back as parentless.
func TestParentRunIDLinkageIsPersistedAdditively(t *testing.T) {
	store := NewStore(t.TempDir())
	root := testJob()
	if err := store.CreateRun(root, RunPolicy{ID: "policy-root"}); err != nil {
		t.Fatal(err)
	}
	child := agentrun.NewLogicalJob(agentrun.NewRunRequest(
		agentrun.Candidate("child-candidate"),
		"private child prompt",
		[]agentrun.Capability{agentrun.NewCapability("snapshot", map[string]string{"scope": "read"})},
	))
	if err := store.CreateRun(child, RunPolicy{ID: "policy-child", ParentRunID: string(root.RunID())}); err != nil {
		t.Fatal(err)
	}

	childRequest, err := store.ReadExecutionRequest(string(child.RunID()))
	if err != nil {
		t.Fatalf("ReadExecutionRequest(child) error = %v", err)
	}
	if childRequest.ParentRunID != string(root.RunID()) {
		t.Fatalf("child ParentRunID = %q, want %q", childRequest.ParentRunID, string(root.RunID()))
	}

	rootRequestPath := filepath.Join(mustExecutionDir(t, store, string(root.RunID())), "request.json")
	rootBytes, err := os.ReadFile(rootRequestPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rootBytes), "parent_run_id") {
		t.Fatalf("root request bytes carry a parent key, want legacy shape untouched: %s", rootBytes)
	}
}

func mustExecutionDir(t *testing.T, store *Store, runID string) string {
	t.Helper()
	directory, err := store.executionDir(runID)
	if err != nil {
		t.Fatal(err)
	}
	return directory
}

func TestReadExecutionRequestClassifiesAbsentAndDamagedRecords(t *testing.T) {
	store := NewStore(t.TempDir())
	job := testJob()
	if err := store.CreateRun(job, RunPolicy{ID: "policy-id"}); err != nil {
		t.Fatal(err)
	}
	directory, err := store.executionDir(string(job.RunID()))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		runID    string
		write    func()
		wantErr  error
		wantText string
	}{
		{
			name:    "missing record",
			runID:   "absent-run-id",
			wantErr: ErrExecutionNotFound,
		},
		{
			name:  "corrupt json",
			runID: string(job.RunID()),
			write: func() {
				path := filepath.Join(directory, "request.json")
				if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: ErrRequestCorrupt,
		},
		{
			name:  "valid json with empty identities",
			runID: string(job.RunID()),
			write: func() {
				path := filepath.Join(directory, "request.json")
				if err := os.WriteFile(path, []byte(`{"run_id":"","job_id":"","request_id":""}`), 0600); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: ErrRequestCorrupt,
		},
		{
			name:     "invalid run id",
			runID:    "../outside",
			wantText: "invalid run id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.write != nil {
				tt.write()
			}
			request, err := store.ReadExecutionRequest(tt.runID)
			if request.RunID != "" || request.JobID != "" || request.RequestID != "" ||
				request.CandidateID != "" || request.PromptID != "" || err == nil {
				t.Fatalf("ReadExecutionRequest() = %+v, %v, want empty record with an error", request, err)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want it to wrap %v", err, tt.wantErr)
			}
			if tt.wantText != "" && !strings.Contains(err.Error(), tt.wantText) {
				t.Fatalf("err = %v, want text %q", err, tt.wantText)
			}
		})
	}
}
