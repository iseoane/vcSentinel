// Liveness proof for DurableTransport.Run + WithRunObserver: observation is
// strictly post-admission diagnostics, so even an observer that stays ACTIVE
// and unable to return inside its callback must never prevent the provider
// worker already launched by Start from being entered and completing. The
// proof is a deterministic gated handshake: the observer signals that it is
// active, then — and only then — the reviewer's entry gate opens and its
// entry signal must arrive while the observer remains unable to return.
// Timeouts are hang guards only, never elapsed-silence evidence. This proves
// nothing about observer-vs-worker-launch ordering; only that observation
// does not gate execution.
package reviewexec

import (
	"sync"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// entryGatedReviewer waits for allowEntry BEFORE signaling entered, so the test
// controls exactly when provider entry may happen; it answers immediately
// once through the gate.
type entryGatedReviewer struct {
	allowEntry chan struct{}
	entered    chan struct{}
	enteredOne sync.Once
}

const wantOutput = `{"dim":"logic","verdict":"ok","findings":[]}`

func (r *entryGatedReviewer) EjecutarRevision(prompt, sha string, paths []string) (string, error) {
	<-r.allowEntry // hold entry until the test opens the gate
	r.enteredOne.Do(func() { close(r.entered) })
	return wantOutput, nil
}

func TestBlockingObserverDoesNotPreventReviewerEntry(t *testing.T) {
	reviewer := &entryGatedReviewer{allowEntry: make(chan struct{}), entered: make(chan struct{})}
	observerStarted := make(chan struct{})
	releaseObserver := make(chan struct{})
	observerReturned := make(chan struct{})
	backing := store.NuevoStore(t.TempDir())
	transport := NewDurableTransport(backing, store.RunPolicy{ID: "policy:review"}, "abc123", nil,
		WithEvidenceAdmission(false), // lenient mode: admission verification is not under test here
		WithRunObserver(func(string) {
			defer close(observerReturned)
			close(observerStarted) // signal that the callback is active...
			<-releaseObserver      // ...then stay unable to return until released
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

	// Step 1 — wait for the observer callback to be active (timeout is a hang
	// guard only). From here on the observer cannot return until released:
	// its only exit path is the deferred close after <-releaseObserver.
	select {
	case <-observerStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("hang guard: the observer never reached its callback")
	}

	// Step 2 — open the reviewer gate ONLY now, so provider entry is produced
	// while the observer callback is known-active and unable to return.
	close(reviewer.allowEntry)
	select {
	case <-reviewer.entered:
		// Provider entry happened without any observer cooperation.
	case <-time.After(5 * time.Second):
		t.Fatal("hang guard: reviewer was never entered although the observer stayed blocked")
	}
	// Confirm the observer still has not returned (non-blocking check).
	select {
	case <-observerReturned:
		t.Fatal("the observer returned before being released; the blocking premise is broken")
	default:
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
