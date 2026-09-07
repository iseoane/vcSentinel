package reviewexec

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// contextRecordingReviewer implements both reviewer contracts. The legacy
// method must never run when the contextual one exists; the contextual one
// blocks until its context is canceled and reports that fact, mirroring a
// provider process dying on cooperative cancellation.
type contextRecordingReviewer struct {
	mu             sync.Mutex
	receivedCtx    context.Context
	contextualRuns int
	legacyRuns     int
	entered        chan struct{}
}

func (r *contextRecordingReviewer) RunReview(prompt, sha string, paths []string) (string, error) {
	r.mu.Lock()
	r.legacyRuns++
	r.mu.Unlock()
	return "legacy", nil
}

func (r *contextRecordingReviewer) ReviewWithContext(ctx context.Context, prompt, sha string, paths []string) (string, error) {
	r.mu.Lock()
	r.receivedCtx = ctx
	r.contextualRuns++
	r.mu.Unlock()
	close(r.entered)
	<-ctx.Done()
	return "", ctx.Err()
}

func TestExecuteForwardsWorkerContextToContextualReviewer(t *testing.T) {
	reviewer := &contextRecordingReviewer{entered: make(chan struct{})}
	backingStore := store.NewStore(t.TempDir())
	controller := execution.NewControllerWithClock(backingStore, NewReviewAdapter(reviewer, "abc123", nil, nil), fixedClock())

	handle, err := controller.Start(context.Background(), testRequest("forward-cancel"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-reviewer.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("contextual reviewer was never entered")
	}

	waitContext, cancelWait := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelWait()
	if _, err := controller.Apply(context.Background(), handle.RunID, execution.ControlAction{Kind: execution.ActionAbort}); err != nil {
		t.Fatal(err)
	}
	completion, err := handle.Wait(waitContext)
	if err != nil {
		t.Fatalf("Wait() after abort = %v; cancellation must settle without the reviewer returning first", err)
	}
	if completion.State != agentrun.StateCanceled || completion.Outcome != agentrun.OutcomeCancellation {
		t.Fatalf("completion = %+v, want canceled settlement", completion)
	}

	reviewer.mu.Lock()
	forwarded := reviewer.receivedCtx
	contextualRuns, legacyRuns := reviewer.contextualRuns, reviewer.legacyRuns
	reviewer.mu.Unlock()
	if forwarded == nil {
		t.Fatal("ReviewWithContext received no context")
	}
	if forwarded.Err() != context.Canceled {
		t.Fatalf("forwarded context error = %v, want context.Canceled: the worker cancellation must reach the reviewer", forwarded.Err())
	}
	if contextualRuns != 1 || legacyRuns != 0 {
		t.Fatalf("calls = contextual %d / legacy %d, want exactly one contextual call and no legacy fallback", contextualRuns, legacyRuns)
	}

	inspection, err := execution.NewController(backingStore, nil).Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Projection.State != agentrun.StateCanceled || len(inspection.Outcomes) != 1 ||
		inspection.Outcomes[0].Class != agentrun.OutcomeCancellation {
		t.Fatalf("inspection = %+v, want durable canceled settlement", inspection)
	}
}

// legacyOnlyReviewer keeps the pre-R7 contract; Execute must still route
// through it unchanged.
type legacyOnlyReviewer struct{ calls int }

func (r *legacyOnlyReviewer) RunReview(prompt, sha string, paths []string) (string, error) {
	r.calls++
	return "legacy output", nil
}

func TestExecuteStillUsesLegacyReviewerWithoutContextualContract(t *testing.T) {
	reviewer := &legacyOnlyReviewer{}
	backingStore := store.NewStore(t.TempDir())
	controller := execution.NewControllerWithClock(backingStore, NewReviewAdapter(reviewer, "abc123", nil, nil), fixedClock())

	handle, completion := startAndWait(t, controller, "legacy-path")
	if completion.State != agentrun.StateSucceeded || completion.Output != "legacy output" {
		t.Fatalf("completion = %+v, want legacy success path untouched", completion)
	}
	if reviewer.calls != 1 {
		t.Fatalf("legacy reviewer calls = %d, want exactly one", reviewer.calls)
	}
	if _, err := execution.NewController(backingStore, nil).Inspect(context.Background(), handle.RunID); err != nil {
		t.Fatal(err)
	}
}
