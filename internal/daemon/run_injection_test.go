package daemon

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vcSentinel/internal/execution"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
)

// recordingAdapter is a scripted adapter that records every prompt the
// controller transports to it and completes immediately with a deterministic
// output derived from that prompt.
type recordingAdapter struct {
	mu      sync.Mutex
	prompts []string
}

func (a *recordingAdapter) Execute(_ context.Context, job agentrun.LogicalJob, _ agentrun.InvocationEnvelope, _ string) (execution.AdapterResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	prompt := string(job.Request().Prompt())
	a.prompts = append(a.prompts, prompt)
	return execution.AdapterResult{Output: "executed:" + prompt}, nil
}

func (a *recordingAdapter) recorded() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.prompts...)
}

// TestRunServesWireStartThroughInjectedController is the proof of the
// dependency-injection fix: a REAL foreground daemon built through Run itself
// serves an explicit Candidate/Prompt wire start by executing the transported
// prompt on its injected adapter and recording durable success evidence.
// Under the previous nil-adapter construction this exact flow was impossible:
// OpStart deterministically failed with execution.ErrControllerNotReady.
func TestRunServesWireStartThroughInjectedController(t *testing.T) {
	commonDir := t.TempDir()
	var out bytes.Buffer
	recorder := &recordingAdapter{}

	host, done := startForegroundDaemonWithAdapter(t, commonDir, &out, recorder)

	handle, err := host.Start(context.Background(), execution.StartRequest{
		Candidate:   "candidate:proof-of-fix",
		Prompt:      "prompt:proof-of-fix",
		Policy:      store.RunPolicy{ID: "policy:proof-of-fix"},
		AuthContext: testAuth(),
	})
	if err != nil {
		t.Fatalf("wire start failed; the daemon did not serve a real adapter: %v\n%s", err, out.String())
	}

	// Wait until the run settles, then assert the durable evidence records a
	// successful execution bound to the adapter's actual output. Both signals
	// are required because Inspect reconstructs outcomes and the projection
	// from separate durable scans: a poll taken exactly while the terminal
	// append commits can observe an empty outcome page next to a terminal
	// projection, so settling means the SAME inspection carries both.
	var inspection execution.Inspection
	deadline := time.Now().Add(5 * time.Second)
	for settled := false; !settled; {
		var inspectErr error
		inspection, inspectErr = host.Inspect(context.Background(), execution.InspectRequest{RunID: handle.RunID, AuthContext: testAuth()})
		if inspectErr == nil && inspection.Projection.Terminal != agentrun.TerminalNone && len(inspection.Outcomes) > 0 {
			settled = true
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("wire-started run never settled: last inspection %+v (err %v)", inspection, inspectErr)
		}
		time.Sleep(pollInterval)
	}
	if inspection.Projection.State != agentrun.StateSucceeded {
		t.Fatalf("settled state = %q, want %q", inspection.Projection.State, agentrun.StateSucceeded)
	}
	wantOutput := "executed:prompt:proof-of-fix"
	if len(inspection.Outcomes) != 1 {
		t.Fatalf("durable outcomes = %+v, want exactly one success record", inspection.Outcomes)
	}
	if inspection.Outcomes[0].Class != agentrun.OutcomeSuccess {
		t.Fatalf("durable outcome class = %q, want %q", inspection.Outcomes[0].Class, agentrun.OutcomeSuccess)
	}
	if inspection.Outcomes[0].OutputHash != execution.HashAdapterOutput(wantOutput) {
		t.Fatalf("durable output hash = %q, want the hash of %q", inspection.Outcomes[0].OutputHash, wantOutput)
	}

	// The injected adapter executed exactly the transported prompt.
	got := recorder.recorded()
	if len(got) != 1 || got[0] != "prompt:proof-of-fix" {
		t.Fatalf("adapter prompts = %v, want exactly [prompt:proof-of-fix]", got)
	}

	stopForegroundDaemon(t, host, done, 0)
	assertDaemonResidueGone(t, commonDir)
}

// TestRunRejectsNilController pins the deterministic refusal for callers that
// inject nothing, keeping the signature honest instead of panicking later at
// boot reconciliation or first serve.
func TestRunRejectsNilController(t *testing.T) {
	err := Run(t.TempDir(), nil, DefaultGracePeriod, &bytes.Buffer{})
	if err == nil {
		t.Fatal("Run with a nil controller returned nil error")
	}
}
