package reviewexec

// Admission tests for ticket 07 slice 1: a succeeded completion may influence
// semantics only when its durable attempt outcome exists, records success,
// and binds the returned output through the shared hashing authority. The
// verifier is exercised directly so every divergence branch is deterministic;
// forcing divergence by rewriting persisted bytes would be inert because
// ReadAttemptOutcomes prefers embedded terminal-frame evidence over the
// outcomes directory.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

func admissionTransport(t *testing.T) (*DurableTransport, *store.Store) {
	t.Helper()
	backing := store.NewStore(t.TempDir())
	return NewDurableTransport(backing, store.RunPolicy{ID: "policy:test"}, "admission-sha", []string{"a.go"}), backing
}

// failedRunCompletion drives one real adapter failure through the controller
// so the durable record genuinely carries a non-success outcome class.
func failedRunCompletion(t *testing.T, backing *store.Store) execution.Completion {
	t.Helper()
	failing := &scriptedReviewer{name: "dimension-logic", err: errors.New("provider exploded")}
	controller := execution.NewController(backing, NewReviewAdapter(failing, "admission-sha", nil, nil))
	request := agentrun.NewRunRequest(agentrun.Candidate("candidate:admission-class"), agentrun.Prompt("prompt"), nil)
	handle, err := controller.Start(context.Background(), request, store.RunPolicy{ID: "policy:test"})
	if err != nil {
		t.Fatal(err)
	}
	completion, err := handle.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if completion.State == agentrun.StateSucceeded {
		t.Fatalf("completion = %+v, want a durably failed run", completion)
	}
	return completion
}

func TestVerifyEvidenceAdmitsVerifiedSuccess(t *testing.T) {
	transport, _ := admissionTransport(t)
	reviewer := &scriptedReviewer{name: "dimension-logic", output: "honest verdict"}

	output, completion, err := transport.Run(reviewer, "quality/logic", "the prompt")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	evidence, err := transport.verifyEvidence("quality/logic", agentrun.Identity(completion.RunID), completion.InvocationID, output)
	if err != nil {
		t.Fatalf("verifyEvidence() error = %v, want admission of matching durable evidence", err)
	}
	if evidence != completion {
		t.Fatalf("evidence = %+v, want %+v from the admitted run", evidence, completion)
	}
}

func TestVerifyEvidenceRejectsDivergentDurableEvidence(t *testing.T) {
	successTransport, _ := admissionTransport(t)
	honest := &scriptedReviewer{name: "dimension-logic", output: "honest verdict"}
	_, admitted, err := successTransport.Run(honest, "quality/logic", "the prompt")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	classTransport, classBacking := admissionTransport(t)
	failed := failedRunCompletion(t, classBacking)

	cases := []struct {
		name         string
		transport    *DurableTransport
		runID        string
		invocationID string
		output       string
		wantReason   string
	}{
		{
			name:         "missing outcome",
			transport:    successTransport,
			runID:        admitted.RunID,
			invocationID: "invocation-absent",
			output:       "honest verdict",
			wantReason:   "no durable attempt outcome for invocation invocation-absent",
		},
		{
			name:         "class mismatch",
			transport:    classTransport,
			runID:        string(failed.RunID),
			invocationID: string(failed.InvocationID),
			output:       "whatever the provider claimed",
			wantReason:   "does not record success",
		},
		{
			name:         "output hash mismatch",
			transport:    successTransport,
			runID:        admitted.RunID,
			invocationID: admitted.InvocationID,
			output:       "tampered verdict",
			wantReason:   "output hash mismatch",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			evidence, err := tc.transport.verifyEvidence("quality/logic",
				agentrun.Identity(tc.runID), tc.invocationID, tc.output)
			if evidence != (Evidence{}) || err == nil {
				t.Fatalf("verifyEvidence() = %+v, %v, want empty evidence with admission error", evidence, err)
			}
			var admission *AdmissionError
			if !errors.As(err, &admission) {
				t.Fatalf("err = %T(%v), want AdmissionError", err, err)
			}
			if admission.Identity != "quality/logic" {
				t.Fatalf("identity = %q, want the audited identity key", admission.Identity)
			}
			if !strings.Contains(admission.Reason, tc.wantReason) {
				t.Fatalf("reason = %q, want it to name %q", admission.Reason, tc.wantReason)
			}
			if admission.Error() != "admission: "+admission.Reason {
				t.Fatalf("Error() = %q, want literal \"admission: \" prefix before %q", admission.Error(), admission.Reason)
			}
		})
	}
}

func TestRunReturnsTerminalErrorWithoutAdmissionWrapping(t *testing.T) {
	transport, _ := admissionTransport(t)
	failing := &scriptedReviewer{name: "dimension-security", err: errors.New("provider exploded")}

	output, evidence, err := transport.Run(failing, "security/security", "prompt")
	if output != "" || evidence != (Evidence{}) || err == nil {
		t.Fatalf("Run() = %q, %+v, %v, want empty output and evidence with terminal error", output, evidence, err)
	}
	var terminal *TerminalError
	if !errors.As(err, &terminal) {
		t.Fatalf("err = %T(%v), want unwrapped TerminalError", err, err)
	}
	if terminal.Error() != "provider exploded" {
		t.Fatalf("Error() = %q, want byte-identical provider text without admission prefix", terminal.Error())
	}
}

// silentReviewer answers successfully with literally empty output, which the
// scripted echo reviewer cannot express. It stays mutex-free so passing it by
// value never trips copylocks.
type silentReviewer struct{ name string }

func (r silentReviewer) ReviewerName() string { return r.name }

func (silentReviewer) RunReview(string, string, []string) (string, error) { return "", nil }

func TestVerifyEvidenceAdmitsEmptyOutputAgainstEmptyDurableHash(t *testing.T) {
	transport, _ := admissionTransport(t)
	reviewer := silentReviewer{name: "dimension-logic"}

	output, completion, err := transport.Run(reviewer, "quality/logic", "the prompt")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if output != "" {
		t.Fatalf("output = %q, want the empty provider answer", output)
	}

	evidence, err := transport.verifyEvidence("quality/logic", agentrun.Identity(completion.RunID), completion.InvocationID, output)
	if err != nil {
		t.Fatalf("verifyEvidence() error = %v, want an empty output admitted against its equally-empty durable hash", err)
	}
	if evidence.OutputHash != "" {
		t.Fatalf("evidence.OutputHash = %q, want the empty hash both sides share via HashAdapterOutput", evidence.OutputHash)
	}
}
