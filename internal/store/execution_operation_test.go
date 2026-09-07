// Slice 14 acceptance: the operator-facing Operation label rides the exact
// additive evolution pattern ParentRunID documented — persisted at admission
// through the whole-policy marshal, readable back per run, omitted from the
// bytes of unlabeled runs, and classified with the sibling readers'
// not-found/corrupt vocabulary.
package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
)

// operationTestJob mirrors testJob with a distinct candidate so every
// admission in this file lands in its own run directory.
func operationTestJob(candidate string) agentrun.LogicalJob {
	return agentrun.NewLogicalJob(agentrun.NewRunRequest(
		agentrun.Candidate(candidate),
		"private prompt",
		[]agentrun.Capability{
			agentrun.NewCapability("snapshot", map[string]string{"scope": "read"}),
		},
	))
}

func TestReadRunOperationRoundTripsAdmittedLabel(t *testing.T) {
	st := NewStore(t.TempDir())
	labeled := operationTestJob("labeled-candidate")
	if err := st.CreateRun(labeled, RunPolicy{ID: "policy:gate", Operation: "gate pre-push"}); err != nil {
		t.Fatal(err)
	}
	unlabeled := operationTestJob("unlabeled-candidate")
	if err := st.CreateRun(unlabeled, RunPolicy{ID: "policy:review"}); err != nil {
		t.Fatal(err)
	}

	operation, err := st.ReadRunOperation(string(labeled.RunID()))
	if err != nil {
		t.Fatalf("ReadRunOperation(labeled) error = %v", err)
	}
	if operation != "gate pre-push" {
		t.Fatalf("ReadRunOperation(labeled) = %q, want %q", operation, "gate pre-push")
	}

	operation, err = st.ReadRunOperation(string(unlabeled.RunID()))
	if err != nil {
		t.Fatalf("ReadRunOperation(unlabeled) error = %v", err)
	}
	if operation != "" {
		t.Fatalf("ReadRunOperation(unlabeled) = %q, want an empty label for a legacy-shaped policy", operation)
	}
}

// TestRunPolicyOperationOmittedFromLegacyBytes is the golden-ish assertion on
// policy.json content: omitempty keeps an unlabeled admission byte-identical
// to the legacy shape (no operation key at all), while a labeled one carries
// the key with its admitted value.
func TestRunPolicyOperationOmittedFromLegacyBytes(t *testing.T) {
	st := NewStore(t.TempDir())
	labeled := operationTestJob("labeled-bytes-candidate")
	if err := st.CreateRun(labeled, RunPolicy{ID: "policy:review", ParentRunID: "parent", Operation: "review"}); err != nil {
		t.Fatal(err)
	}
	unlabeled := operationTestJob("unlabeled-bytes-candidate")
	if err := st.CreateRun(unlabeled, RunPolicy{ID: "policy:review"}); err != nil {
		t.Fatal(err)
	}

	labeledBytes, err := os.ReadFile(filepath.Join(mustExecutionDir(t, st, string(labeled.RunID())), "policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"id": "policy:review"`, `"parent_run_id": "parent"`, `"operation": "review"`} {
		if !strings.Contains(string(labeledBytes), want) {
			t.Fatalf("labeled policy.json missing %s:\n%s", want, labeledBytes)
		}
	}

	unlabeledBytes, err := os.ReadFile(filepath.Join(mustExecutionDir(t, st, string(unlabeled.RunID())), "policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(unlabeledBytes), "operation") {
		t.Fatalf("unlabeled policy.json must stay in the legacy shape without an operation key:\n%s", unlabeledBytes)
	}
}

func TestReadRunOperationClassifiesAbsentAndDamagedRecords(t *testing.T) {
	st := NewStore(t.TempDir())
	job := testJob()
	if err := st.CreateRun(job, RunPolicy{ID: "policy-id", Operation: "run"}); err != nil {
		t.Fatal(err)
	}
	directory := mustExecutionDir(t, st, string(job.RunID()))

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
				path := filepath.Join(directory, "policy.json")
				if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: ErrPolicyCorrupt,
		},
		{
			name:  "missing policy identity",
			runID: string(job.RunID()),
			write: func() {
				if err := os.WriteFile(filepath.Join(directory, "policy.json"), []byte(`{"worktree":"/hidden"}`), 0600); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: ErrPolicyCorrupt,
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
			operation, err := st.ReadRunOperation(tt.runID)
			if operation != "" || err == nil {
				t.Fatalf("ReadRunOperation() = %q, %v, want empty label with an error", operation, err)
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
