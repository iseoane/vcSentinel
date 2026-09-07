package reviewexec

// Binding tests for ticket 07 slice 2a: snapshot freshness, lineage, and
// prompt binding validated against the durable request record immediately
// after Start, before any provider answer can be waited on.
//
// Divergence branches are exercised directly against real stores, mirroring
// the slice-1 verifier precedent: CreateRun refuses divergent bytes for an
// existing run identity, so post-admission record divergence is unreachable
// deterministically through Run(). The one production-reachable divergence —
// a transport bound to a malformed audited SHA whose readable candidate
// carries zero matching segments — is proven end-to-end through Run() below.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// bindingRecord admits a crafted request into a real store and returns its
// logical job, mirroring how other tests write events/outcomes bytes: the
// record lands in the genuine execution directory layout.
func bindingRecord(t *testing.T, backing *store.Store, candidateRaw, prompt string) agentrun.LogicalJob {
	t.Helper()
	request := agentrun.NewRunRequest(agentrun.Candidate(candidateRaw), agentrun.Prompt(prompt), nil)
	job := agentrun.NewLogicalJob(request)
	if err := backing.CreateRun(job, store.RunPolicy{ID: "policy:binding"}); err != nil {
		t.Fatal(err)
	}
	return job
}

// bindingRequestPath rebuilds the durable request.json path from the store
// root passed to NewStore, which appends vas-sentinel itself.
func bindingRequestPath(t *testing.T, root, runID string) string {
	t.Helper()
	return filepath.Join(root, "vas-sentinel", "executions", "v1", runID, "request.json")
}

func TestBindSnapshotAdmitsCoherentSnapshot(t *testing.T) {
	backing := store.NewStore(t.TempDir())
	transport := NewDurableTransport(backing, store.RunPolicy{ID: "policy:binding"}, "binding-sha", []string{"a.go"})
	candidate := agentrun.Candidate("review:quality/logic:binding-sha:salt:000001")
	job := bindingRecord(t, backing, string(candidate), "admitted prompt")

	err := transport.validateSnapshotBinding("quality/logic", job.RunID(), "admitted prompt", candidate)
	if err != nil {
		t.Fatalf("validateSnapshotBinding() error = %v, want a coherent snapshot admitted", err)
	}
}

func TestBindSnapshotRejectsDivergentIdentities(t *testing.T) {
	tests := []struct {
		name         string
		recordSha    string
		transportSha string
		admittedRaw  string
		livePrompt   string
		wantInReason []string
	}{
		{
			name:         "candidate sha diverges from audited sha",
			recordSha:    "stale-sha",
			transportSha: "fresh-sha",
			admittedRaw:  "review:quality/logic:fresh-sha:salt:000002",
			livePrompt:   "shared prompt",
			wantInReason: []string{"durable request candidate identity", "does not match admitted candidate"},
		},
		{
			name:         "prompt identity diverges from admitted request",
			recordSha:    "binding-sha",
			transportSha: "binding-sha",
			admittedRaw:  "review:quality/logic:binding-sha:salt:000001",
			livePrompt:   "tampered prompt",
			wantInReason: []string{
				"prompt identity",
				"does not match admitted request",
				string(agentrun.PromptIdentity("tampered prompt")),
			},
		},
		{
			name:         "audited sha appears in several segments",
			recordSha:    "dup-segment",
			transportSha: "dup-segment",
			admittedRaw:  "review:quality/logic:dup-segment:dup-segment:000004",
			livePrompt:   "any prompt",
			wantInReason: []string{"exactly one segment"},
		},
		{
			name:         "empty audited sha",
			recordSha:    "whatever-sha",
			transportSha: "",
			admittedRaw:  "review:quality/logic::salt:000005",
			livePrompt:   "any prompt",
			wantInReason: []string{"audited sha is empty"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backing := store.NewStore(t.TempDir())
			transport := NewDurableTransport(backing, store.RunPolicy{ID: "policy:binding"}, tt.transportSha, nil)
			job := bindingRecord(t, backing,
				"review:quality/logic:"+tt.recordSha+":salt:000001", "admitted prompt")

			err := transport.validateSnapshotBinding("quality/logic", job.RunID(), tt.livePrompt,
				agentrun.Candidate(tt.admittedRaw))
			if err == nil {
				t.Fatal("validateSnapshotBinding() = nil, want an admission rejection")
			}
			var admission *AdmissionError
			if !errors.As(err, &admission) {
				t.Fatalf("err = %T(%v), want AdmissionError", err, err)
			}
			if admission.Identity != "quality/logic" {
				t.Fatalf("identity = %q, want the audited identity key", admission.Identity)
			}
			for _, want := range tt.wantInReason {
				if !strings.Contains(admission.Reason, want) {
					t.Fatalf("reason = %q, want it to name %q", admission.Reason, want)
				}
			}
			if admission.Error() != "admission: "+admission.Reason {
				t.Fatalf("Error() = %q, want literal \"admission: \" prefix before %q", admission.Error(), admission.Reason)
			}
		})
	}
}

func TestBindSnapshotReadFailuresStayInfrastructureErrors(t *testing.T) {
	tests := []struct {
		name    string
		damage  func(path string)
		runID   string
		wantErr error
	}{
		{name: "missing request record", damage: func(string) {}, runID: "absent-run-id", wantErr: store.ErrExecutionNotFound},
		{name: "corrupt request json", damage: tamperRequestBytes("{not json"), runID: "", wantErr: store.ErrRequestCorrupt},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			backing := store.NewStore(root)
			transport := NewDurableTransport(backing, store.RunPolicy{ID: "policy:binding"}, "binding-sha", nil)
			candidate := agentrun.Candidate("review:quality/logic:binding-sha:salt:000006")
			runID := "absent-run-id"
			if tt.runID == "" {
				job := bindingRecord(t, backing, string(candidate), "admitted prompt")
				runID = string(job.RunID())
				tt.damage(bindingRequestPath(t, root, runID))
			}

			err := transport.validateSnapshotBinding("quality/logic", agentrun.Identity(runID), "admitted prompt", candidate)
			if err == nil {
				t.Fatal("validateSnapshotBinding() = nil, want a read failure")
			}
			var admission *AdmissionError
			if errors.As(err, &admission) {
				t.Fatalf("err = %v, want a wrapped infrastructure error without the admission label", err)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want it to wrap %v", err, tt.wantErr)
			}
		})
	}
}

func tamperRequestBytes(replacement string) func(string) {
	return func(path string) {
		if err := os.WriteFile(path, []byte(replacement), 0600); err != nil {
			panic(err)
		}
	}
}

// gatedReviewer blocks inside the adapter until released and counts only
// completed provider calls, so assertions made before release are immune to
// controller worker scheduling.
type gatedReviewer struct {
	once    sync.Once
	entered chan struct{}
	release chan struct{}
	mu      sync.Mutex
	calls   int
}

func newGatedReviewer() *gatedReviewer {
	return &gatedReviewer{entered: make(chan struct{}), release: make(chan struct{})}
}

func (r *gatedReviewer) RunReview(string, string, []string) (string, error) {
	r.once.Do(func() { close(r.entered) })
	<-r.release
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	return "late verdict that must never influence semantics", nil
}

func (r *gatedReviewer) finish() { close(r.release) }

func (r *gatedReviewer) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// A transport bound to a malformed audited SHA (one containing colons) can
// never embed it as exactly one candidate segment, so Run must reject the
// snapshot before waiting on any provider answer.
func TestRunRejectsSnapshotDivergenceBeforeReviewerAnswer(t *testing.T) {
	backing := store.NewStore(t.TempDir())
	transport := NewDurableTransport(backing, store.RunPolicy{ID: "policy:binding"}, "audit:divergent", []string{"a.go"})
	reviewer := newGatedReviewer()
	t.Cleanup(reviewer.finish)

	output, evidence, err := transport.Run(reviewer, "quality/logic", "the prompt")

	if output != "" || evidence != (Evidence{}) || err == nil {
		t.Fatalf("Run() = %q, %+v, %v, want empty output and evidence with an admission error", output, evidence, err)
	}
	var admission *AdmissionError
	if !errors.As(err, &admission) {
		t.Fatalf("err = %T(%v), want AdmissionError surfaced by Run before waiting", err, err)
	}
	if !strings.Contains(admission.Reason, "exactly one segment") {
		t.Fatalf("reason = %q, want the diverging candidate identity named", admission.Reason)
	}
	if got := reviewer.callCount(); got != 0 {
		t.Fatalf("reviewer completed calls = %d, want none before admission rejection", got)
	}
}
