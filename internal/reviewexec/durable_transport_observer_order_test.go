// Ordering proof for DurableTransport.Run + WithRunObserver: the observer must
// fire after the run is durably admitted but BEFORE the reviewer can be
// entered, so an operator announcement (sentinel review run IDs) can never lag
// the provider start. Before the StartObserved seam existed, Run invoked the
// observer only after Controller.Start returned — which is after the worker
// goroutine was already launched — so this test failed against the old wiring.
package reviewexec

import (
	"sync"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// entryBarrierReviewer signals entry by closing entered as its first
// statement, then stays blocked until release is closed, so the observer can
// detect deterministically whether execution already began.
type entryBarrierReviewer struct {
	entered    chan struct{}
	enteredOne sync.Once
	release    chan struct{}
}

func (r *entryBarrierReviewer) EjecutarRevision(prompt, sha string, paths []string) (string, error) {
	r.enteredOne.Do(func() { close(r.entered) })
	<-r.release // stay blocked until the test releases the provider
	return `{"dim":"logic","verdict":"ok","findings":[]}`, nil
}

func TestRunObserverFiresBeforeReviewerExecution(t *testing.T) {
	reviewer := &entryBarrierReviewer{entered: make(chan struct{}), release: make(chan struct{})}
	backing := store.NuevoStore(t.TempDir())
	transport := NewDurableTransport(backing, store.RunPolicy{ID: "policy:review"}, "abc123", nil,
		WithEvidenceAdmission(false), // lenient mode: admission verification is not under test here
		WithRunObserver(func(string) {
			select {
			case <-reviewer.entered:
				t.Error("the reviewer was entered before the admitted-run observation fired")
			case <-time.After(200 * time.Millisecond):
				// Observation won the race: the provider has not been entered.
			}
		}),
	)

	type runResult struct {
		output string
		err    error
	}
	done := make(chan runResult, 1)
	go func() {
		output, _, runErr := transport.Run(reviewer, "logic", "prompt")
		done <- runResult{output: output, err: runErr}
	}()

	// The observer fires synchronously inside Run; give the whole flow a
	// bounded window so a regression surfaces as a test failure, not a hang.
	select {
	case result := <-done:
		t.Fatalf("Run settled without the expected barrier handshake: output=%q err=%v", result.output, result.err)
	case <-time.After(500 * time.Millisecond):
	}

	// Release the blocked provider so Run settles, then verify the output.
	close(reviewer.release)
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("Run() error = %v", result.err)
		}
		want := `{"dim":"logic","verdict":"ok","findings":[]}`
		if result.output != want {
			t.Fatalf("output = %q, want %q", result.output, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not settle after the provider was released")
	}
}
