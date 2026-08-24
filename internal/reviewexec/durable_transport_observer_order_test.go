// Liveness proof for DurableTransport.Run + WithRunObserver: observation is
// strictly post-admission diagnostics, so even an observer that BLOCKS
// FOREVER must never prevent the admitted provider from being entered and
// completing. The proof is a deterministic two-signal channel handshake — an
// explicit observer-start signal followed by the reviewer's own entry signal,
// proving provider entry happens while the observer is provably parked inside
// its callback — with timeouts used only as hang guards, never as
// elapsed-silence evidence.
package reviewexec

import (
	"sync"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// signalingReviewer closes entered as its first statement and answers
// immediately, so its entry depends on nothing but Start launching the worker.
type signalingReviewer struct {
	entered    chan struct{}
	enteredOne sync.Once
}

const wantOutput = `{"dim":"logic","verdict":"ok","findings":[]}`

func (r *signalingReviewer) EjecutarRevision(prompt, sha string, paths []string) (string, error) {
	r.enteredOne.Do(func() { close(r.entered) })
	return wantOutput, nil
}

func TestBlockingObserverDoesNotPreventReviewerEntry(t *testing.T) {
	reviewer := &signalingReviewer{entered: make(chan struct{})}
	observerStarted := make(chan struct{})
	releaseObserver := make(chan struct{})
	observerReturned := make(chan struct{})
	backing := store.NuevoStore(t.TempDir())
	transport := NewDurableTransport(backing, store.RunPolicy{ID: "policy:review"}, "abc123", nil,
		WithEvidenceAdmission(false), // lenient mode: admission verification is not under test here
		WithRunObserver(func(string) {
			defer close(observerReturned)
			close(observerStarted) // signal first, THEN block
			<-releaseObserver      // block observation indefinitely: it must not gate the provider
		}),
	)

	type runResult struct {
		output string
		err    error
	}
	runSettled := make(chan runResult, 1)
	go func() {
		output, _, runErr := transport.Run(reviewer, "logic", "prompt")
		runSettled <- runResult{output: output, err: runErr}
	}()

	// Step 1 — wait for the observer to actually be inside its callback and
	// parked on its release channel (timeout is a hang guard only).
	select {
	case <-observerStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("hang guard: the observer never reached its callback")
	}

	// Step 2 — core invariant: with the observer PROVABLY blocked, the
	// reviewer still gets entered through Start's independent worker launch.
	// Entry is signaled through a channel handshake, never elapsed silence.
	select {
	case <-reviewer.entered:
		// Provider entry happened without any observer cooperation.
	case <-time.After(5 * time.Second):
		t.Fatal("hang guard: reviewer was never entered although the observer stayed blocked")
	}

	// Step 3 — release observation so Run can settle; nothing stays blocked.
	close(releaseObserver)
	select {
	case result := <-runSettled:
		if result.err != nil {
			t.Fatalf("Run() error = %v", result.err)
		}
		if result.output != wantOutput {
			t.Fatalf("output = %q, want %q", result.output, wantOutput)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hang guard: Run did not settle after the observer was released")
	}
	<-observerReturned // clean join: Run invokes the observer synchronously
}
