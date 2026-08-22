package store

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReadExecutionRequestReturnsAdmittedIdentities(t *testing.T) {
	store := NuevoStore(t.TempDir())
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

func TestReadExecutionRequestClassifiesAbsentAndDamagedRecords(t *testing.T) {
	store := NuevoStore(t.TempDir())
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
