package reviewexec

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// scriptedReviewer is a deterministic RestrictedReviewer for controller-routed
// tests: it counts physical calls and returns a canned output or error.
type scriptedReviewer struct {
	mu     sync.Mutex
	calls  int
	name   string
	output string
	err    error
}

// ReviewerName reports which concrete layer answered, mirroring how
// cmd/sentinel's effective-agent recorder identifies the real responder.
func (r *scriptedReviewer) ReviewerName() string { return r.name }

func (r *scriptedReviewer) EjecutarRevision(prompt, sha string, paths []string) (string, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	if r.err != nil {
		return "", r.err
	}
	return prompt + "|" + sha + "|" + strings.Join(paths, ",") + "|" + r.output, nil
}

func (r *scriptedReviewer) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// observedReviewer mirrors cmd/sentinel's effective-agent wrapper (H4): it
// records authorship only after the wrapped reviewer answers without error,
// and captures WHICH layer answered, not merely that something did. The test
// proves attribution survives routing through the durable controller.
type observedReviewer struct {
	inner      RestrictedReviewer
	mu         sync.Mutex
	successes  int
	answerer   string
	lastPrompt string
	lastSHA    string
}

func (o *observedReviewer) EjecutarRevision(prompt, sha string, paths []string) (string, error) {
	output, err := o.inner.EjecutarRevision(prompt, sha, paths)
	if err == nil {
		o.mu.Lock()
		o.successes++
		o.lastPrompt = prompt
		o.lastSHA = sha
		if named, ok := o.inner.(interface{ ReviewerName() string }); ok {
			o.answerer = named.ReviewerName()
		}
		o.mu.Unlock()
	}
	return output, err
}

// startAndWait admits one run and blocks for its terminal completion.
func startAndWait(t *testing.T, controller *execution.Controller, name string) (execution.Handle, execution.Completion) {
	t.Helper()
	handle, err := controller.Start(context.Background(), testRequest(name), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	completion, err := handle.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	return handle, completion
}

func TestExecuteRoutesSuccessfulReviewThroughController(t *testing.T) {
	inner := &scriptedReviewer{name: "dimension-logic", output: "verdict json"}
	observer := &observedReviewer{inner: inner}
	backingStore := store.NuevoStore(t.TempDir())
	controller := execution.NewControllerWithClock(backingStore, NewReviewAdapter(observer, "abc123", []string{"a.go", "b.go"}, nil), fixedClock())

	handle, completion := startAndWait(t, controller, "success")
	wantOutput := "prompt:success|abc123|a.go,b.go|verdict json"
	if completion.State != agentrun.StateSucceeded || completion.Outcome != agentrun.OutcomeSuccess || completion.Output != wantOutput {
		t.Fatalf("completion = %+v, want succeeded run with bound prompt, sha, paths, and output", completion)
	}
	if inner.callCount() != 1 {
		t.Fatalf("reviewer calls = %d, want exactly one physical invocation", inner.callCount())
	}
	observer.mu.Lock()
	attributionOK := observer.successes == 1 && observer.answerer == "dimension-logic" &&
		observer.lastSHA == "abc123" && strings.Contains(observer.lastPrompt, "prompt:success")
	observer.mu.Unlock()
	if !attributionOK {
		t.Fatalf("observer = %+v, want one post-success record attributing the answer to dimension-logic", observer)
	}

	inspection, err := execution.NewController(backingStore, nil).Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Projection.State != agentrun.StateSucceeded || len(inspection.Outcomes) != 1 || inspection.Outcomes[0].Class != agentrun.OutcomeSuccess {
		t.Fatalf("inspection = %+v, want durable success outcome", inspection)
	}
}

func TestFailurePreservesConcreteProviderTextEndToEnd(t *testing.T) {
	providerErr := errors.New("provider rejected request: quota exceeded for model")
	reviewer := &scriptedReviewer{name: "dimension-style", err: providerErr}
	backingStore := store.NuevoStore(t.TempDir())
	controller := execution.NewControllerWithClock(backingStore, NewReviewAdapter(reviewer, "def456", nil, nil), fixedClock())

	handle, completion := startAndWait(t, controller, "failure")
	if completion.State != agentrun.StateFailed || completion.Outcome != agentrun.OutcomeFailure {
		t.Fatalf("completion = %+v, want durable failure", completion)
	}
	if !strings.Contains(completion.Error, providerErr.Error()) {
		t.Fatalf("completion.Error = %q, want concrete provider text preserved", completion.Error)
	}

	inspection, err := execution.NewController(backingStore, nil).Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(inspection.Outcomes) != 1 || inspection.Outcomes[0].Error != providerErr.Error() {
		t.Fatalf("durable outcome = %+v, want exact provider evidence %q", inspection.Outcomes, providerErr.Error())
	}
}

func TestInjectedClassifierDrivesUnavailableTerminalState(t *testing.T) {
	providerErr := errors.New("binary not found in PATH")
	reviewer := &scriptedReviewer{name: "dimension-tests", err: providerErr}
	unavailable := func(error) agentrun.OutcomeClass { return agentrun.OutcomeUnavailable }
	backingStore := store.NuevoStore(t.TempDir())
	controller := execution.NewControllerWithClock(backingStore, NewReviewAdapter(reviewer, "ghi789", nil, unavailable), fixedClock())

	handle, completion := startAndWait(t, controller, "unavailable")
	if completion.State != agentrun.StateUnavailable || completion.Outcome != agentrun.OutcomeUnavailable || !strings.Contains(completion.Error, providerErr.Error()) {
		t.Fatalf("completion = %+v, want unavailable class with original detail", completion)
	}

	inspection, err := execution.NewController(backingStore, nil).Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Projection.State != agentrun.StateUnavailable || len(inspection.Outcomes) != 1 ||
		inspection.Outcomes[0].Class != agentrun.OutcomeUnavailable || inspection.Outcomes[0].Error != providerErr.Error() {
		t.Fatalf("inspection = %+v, want durable unavailable evidence preserving %q", inspection, providerErr.Error())
	}
}

func TestDefaultClassifierMapsContextErrors(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		class agentrun.OutcomeClass
	}{
		{name: "canceled", err: context.Canceled, class: agentrun.OutcomeCancellation},
		{name: "deadline", err: context.DeadlineExceeded, class: agentrun.OutcomeTimeout},
		{name: "wrapped deadline", err: errors.Join(errors.New("review timed out"), context.DeadlineExceeded), class: agentrun.OutcomeTimeout},
		{name: "other", err: errors.New("provider 500"), class: agentrun.OutcomeFailure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DefaultClassifier(tt.err); got != tt.class {
				t.Fatalf("DefaultClassifier(%v) = %q, want %q", tt.err, got, tt.class)
			}
		})
	}
}

func fixedClock() func() time.Time {
	return func() time.Time { return time.Unix(1700000000, 0).UTC() }
}

func testRequest(name string) agentrun.RunRequest {
	return agentrun.NewRunRequest(agentrun.Candidate("candidate:"+name), agentrun.Prompt("prompt:"+name), nil)
}

func testPolicy() store.RunPolicy { return store.RunPolicy{ID: "policy:test"} }
