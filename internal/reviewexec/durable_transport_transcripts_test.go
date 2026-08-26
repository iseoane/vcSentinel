package reviewexec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// attributedReviewer is a fake provider whose effective identity is known
// after answering, mirroring how CLI adapters and chains report
// AgenteEfectivo only once a real responder exists.
type attributedReviewer struct {
	output string
}

func (r *attributedReviewer) EjecutarRevision(prompt, sha string, paths []string) (string, error) {
	return r.output, nil
}

func (r *attributedReviewer) AgenteEfectivo() (agentadapter.AgenteEfectivo, bool) {
	return agentadapter.AgenteEfectivo{Binario: "claude", Modelo: "test-model", Esfuerzo: "high"}, true
}

func runAttributedReview(t *testing.T, reviewer RestrictedReviewer, wantOutput string) (*store.Store, string, Evidence, string) {
	t.Helper()
	backing := store.NuevoStore(t.TempDir())
	transport := NewDurableTransport(backing, store.RunPolicy{ID: "policy:test"}, "sha123", nil)
	output, evidence, err := transport.Run(reviewer, "quality/logic", "the prompt")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if output != wantOutput {
		t.Fatalf("output = %q, want %q", output, wantOutput)
	}
	execDir, err := backing.ExecutionDir(evidence.RunID)
	if err != nil {
		t.Fatal(err)
	}
	return backing, execDir, evidence, output
}

func TestDurableTransportPersistsTranscriptSidecar(t *testing.T) {
	reviewer := &attributedReviewer{output: "raw verdict text"}
	_, execDir, evidence, output := runAttributedReview(t, reviewer, reviewer.output)

	sidecarPath := filepath.Join(execDir, "transcripts", evidence.InvocationID+".json")
	raw, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatalf("transcript sidecar unreadable: %v", err)
	}
	var sidecar map[string]string
	if err := json.Unmarshal(raw, &sidecar); err != nil {
		t.Fatalf("sidecar is not a JSON object: %v", err)
	}
	wantKeys := map[string]bool{"invocation_id": true, "at": true, "output": true}
	if len(sidecar) != len(wantKeys) {
		t.Fatalf("sidecar keys = %v, want exactly invocation_id, at, output", sidecar)
	}
	for key := range wantKeys {
		if _, ok := sidecar[key]; !ok {
			t.Fatalf("sidecar keys = %v, want exactly invocation_id, at, output", sidecar)
		}
	}
	if sidecar["invocation_id"] != evidence.InvocationID || sidecar["output"] != output {
		t.Fatalf("sidecar = %v, want invocation %s with raw output %q", sidecar, evidence.InvocationID, output)
	}
	if _, err := time.Parse(time.RFC3339, sidecar["at"]); err != nil {
		t.Fatalf("sidecar at = %q is not an RFC3339 timestamp: %v", sidecar["at"], err)
	}

	outcomeData, err := os.ReadFile(filepath.Join(execDir, "outcomes", evidence.InvocationID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var outcome store.AttemptOutcome
	if err := json.Unmarshal(outcomeData, &outcome); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if outcome.TranscriptSHA256 != hex.EncodeToString(sum[:]) || outcome.TranscriptSize != int64(len(raw)) {
		t.Fatalf("recorded tamper evidence = (%s, %d), want digest over sidecar bytes (%d of them)",
			outcome.TranscriptSHA256, outcome.TranscriptSize, len(raw))
	}
	if outcome.Agent != "claude" || outcome.Model != "test-model" || outcome.Effort != "high" {
		t.Fatalf("observed identity = (%q, %q, %q), want the reported effective agent",
			outcome.Agent, outcome.Model, outcome.Effort)
	}
	if outcome.StopReason != "" {
		t.Fatalf("stop_reason = %q, want empty because the provider output exposes none", outcome.StopReason)
	}

	if ok, reason := store.VerifyTranscript(execDir, evidence.InvocationID); !ok {
		t.Fatalf("VerifyTranscript() = false (%q), want true for intact sidecar", reason)
	}
}

func TestDurableTransportSurvivesTranscriptWriteFailure(t *testing.T) {
	backing := store.NuevoStore(t.TempDir())
	reviewer := newGatedReviewer()
	transport := NewDurableTransport(backing, store.RunPolicy{ID: "policy:test"}, "sha123", nil,
		WithRunObserver(func(runID string) {
			execDir, err := backing.ExecutionDir(runID)
			if err != nil {
				t.Error(err)
			}
			// Occupy the transcripts path with a regular file so the atomic
			// sidecar write cannot even create its directory. The gated
			// reviewer stays blocked until the sabotage is in place, so the
			// injection never races the controller worker.
			if err := os.WriteFile(filepath.Join(execDir, "transcripts"), []byte("not a directory"), 0600); err != nil {
				t.Error(err)
			}
			reviewer.finish()
		}))

	output, evidence, err := transport.Run(reviewer, "quality/logic", "the prompt")
	if err != nil {
		t.Fatalf("Run() error = %v, want transcript failure to leave the review unaffected", err)
	}
	const wantOutput = "late verdict that must never influence semantics"
	if output != wantOutput || evidence.OutputHash == "" || evidence.Class != agentrun.OutcomeSuccess {
		t.Fatalf("Run() = %q, %+v, want admitted success with bound evidence", output, evidence)
	}
	execDir, err := backing.ExecutionDir(evidence.RunID)
	if err != nil {
		t.Fatal(err)
	}
	outcomeData, err := os.ReadFile(filepath.Join(execDir, "outcomes", evidence.InvocationID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var outcome store.AttemptOutcome
	if err := json.Unmarshal(outcomeData, &outcome); err != nil {
		t.Fatal(err)
	}
	if outcome.TranscriptSHA256 != "" || outcome.TranscriptSize != 0 || outcome.Agent != "" {
		t.Fatalf("outcome = %+v, want no transcript or identity provenance after failed capture", outcome)
	}
	if ok, reason := store.VerifyTranscript(execDir, evidence.InvocationID); ok {
		t.Fatal("VerifyTranscript() = true, want false when no digest was recorded")
	} else if !strings.Contains(reason, "no transcript digest") {
		t.Fatalf("reason = %q, want the missing-digest explanation", reason)
	}
}

func TestDurableTransportFailedInvocationWritesNoTranscript(t *testing.T) {
	backing := store.NuevoStore(t.TempDir())
	var runID string
	transport := NewDurableTransport(backing, store.RunPolicy{ID: "policy:test"}, "sha456", nil,
		WithRunObserver(func(id string) { runID = id }))
	failing := &scriptedReviewer{name: "dimension-logic", err: errors.New("provider exploded")}

	output, evidence, err := transport.Run(failing, "quality/logic", "the prompt")
	if output != "" || evidence != (Evidence{}) || err == nil {
		t.Fatalf("Run() = %q, %+v, %v, want terminal failure unchanged by transcripts", output, evidence, err)
	}
	var terminal *TerminalError
	if !errors.As(err, &terminal) || terminal.Class != agentrun.OutcomeFailure {
		t.Fatalf("err = %v, want a failure-class TerminalError", err)
	}
	if runID == "" {
		t.Fatal("run observer never fired, cannot locate the durable run")
	}
	execDir, err := backing.ExecutionDir(runID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(execDir, "transcripts")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("transcripts dir stat = %v, want it never created for a failed invocation", err)
	}
}
