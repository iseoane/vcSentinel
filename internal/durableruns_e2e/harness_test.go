// Package durableruns_e2e proves the durable-run lifecycle end to end: a real
// store plus controller (plus the transport seam where relevant) on temporary
// repositories, one test per scenario, asserting honest terminal states and
// `sentinel runs`-style inspectability. Nothing here fabricates completions:
// every assertion reads back through the same durable evidence an operator
// would see after a restart.
package durableruns_e2e

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

const observationBudget = 5 * time.Second

func newE2EStore(t *testing.T) *store.Store {
	t.Helper()
	return store.NewStore(t.TempDir())
}

func e2eRequest(name string) agentrun.RunRequest {
	return agentrun.NewRunRequest(agentrun.Candidate("candidate:"+name), agentrun.Prompt("prompt:"+name), nil)
}

func e2ePolicy() store.RunPolicy { return store.RunPolicy{ID: "policy:e2e"} }

func fixedClock() func() time.Time {
	return func() time.Time { return time.Unix(1700000000, 0).UTC() }
}

func requireLinux(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("subprocess fixtures require Linux")
	}
}

// blockingAdapter simulates a provider call that never observes its context:
// it ignores cancellation entirely and only unblocks on Release, exactly like
// a legacy non-contextual reviewer whose provider process missed the cancel.
type blockingAdapter struct {
	started chan struct{}
	release chan struct{}
	// returned closes when Execute hands back its late result, so tests can
	// prove the controller dropped it.
	returned chan struct{}
	once     sync.Once
}

func newBlockingAdapter() *blockingAdapter {
	return &blockingAdapter{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		returned: make(chan struct{}),
	}
}

func (a *blockingAdapter) Execute(context.Context, agentrun.LogicalJob, agentrun.InvocationEnvelope, string) (execution.AdapterResult, error) {
	a.once.Do(func() { close(a.started) })
	<-a.release
	close(a.returned)
	return execution.AdapterResult{Output: "late provider output"}, nil
}

// Release lets the blocked provider call finish late.
func (a *blockingAdapter) Release() { close(a.release) }

// waitReturned proves the late result was produced (and therefore offered to
// a run that is already settled).
func (a *blockingAdapter) waitReturned(t *testing.T) {
	t.Helper()
	select {
	case <-a.returned:
	case <-time.After(observationBudget):
		t.Fatal("late adapter result never returned")
	}
}

// failingAdapter settles every attempt as an operational failure.
type failingAdapter struct {
	detail string
}

func (a failingAdapter) Execute(context.Context, agentrun.LogicalJob, agentrun.InvocationEnvelope, string) (execution.AdapterResult, error) {
	return execution.AdapterResult{}, execution.NewAdapterError(agentrun.OutcomeFailure, errors.New(a.detail))
}

// deadlineAdapter simulates an adapter that enforces its own budget: it waits
// past the deadline and reports the timeout classification itself.
type deadlineAdapter struct{ budget time.Duration }

func (a deadlineAdapter) Execute(ctx context.Context, _ agentrun.LogicalJob, _ agentrun.InvocationEnvelope, _ string) (execution.AdapterResult, error) {
	timer := time.NewTimer(a.budget * 10)
	defer timer.Stop()
	select {
	case <-timer.C:
		return execution.AdapterResult{}, execution.NewAdapterError(agentrun.OutcomeTimeout,
			fmt.Errorf("adapter budget expired: %w", context.DeadlineExceeded))
	case <-ctx.Done():
		return execution.AdapterResult{}, ctx.Err()
	}
}

// subprocessDeadlineAdapter owns a real sleeping child killed by its own
// context deadline — the subprocess variant of the timeout scenario.
type subprocessDeadlineAdapter struct{ budget time.Duration }

func (a subprocessDeadlineAdapter) Execute(context.Context, agentrun.LogicalJob, agentrun.InvocationEnvelope, string) (execution.AdapterResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), a.budget)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sleep", "30")
	if err := cmd.Start(); err != nil {
		return execution.AdapterResult{}, execution.NewAdapterError(agentrun.OutcomeProcessError, err)
	}
	pid := cmd.Process.Pid
	err := cmd.Wait()
	class := agentrun.OutcomeProcessError
	if ctx.Err() != nil {
		class = agentrun.OutcomeTimeout
	}
	return execution.AdapterResult{}, execution.NewAdapterError(class,
		fmt.Errorf("adapter child %d interrupted at its deadline: %w", pid, err))
}

// awaitingAdapter answers the first attempt with a pending decision and the
// continuation with settled output.
type awaitingAdapter struct {
	mu    sync.Mutex
	calls []string
}

func (a *awaitingAdapter) Execute(_ context.Context, _ agentrun.LogicalJob, _ agentrun.InvocationEnvelope, response string) (execution.AdapterResult, error) {
	a.mu.Lock()
	a.calls = append(a.calls, response)
	a.mu.Unlock()
	if response == "" {
		return execution.AdapterResult{AwaitingDecision: true}, nil
	}
	return execution.AdapterResult{Output: "response accepted"}, nil
}

// chainPromptAdapter routes one physical invocation through the production
// prompt chain (CadenaAdaptador), exactly like reviewexec.ReviewAdapter does:
// the adapter classifies infrastructure failures without deciding verdicts.
type chainPromptAdapter struct {
	chain agentadapter.PromptAdapter
}

func (a chainPromptAdapter) Execute(_ context.Context, job agentrun.LogicalJob, _ agentrun.InvocationEnvelope, response string) (execution.AdapterResult, error) {
	prompt := string(job.Request().Prompt())
	if response != "" {
		prompt += "\n\n" + response
	}
	output, err := a.chain.RunPrompt(prompt)
	if err != nil {
		return execution.AdapterResult{}, execution.NewAdapterError(agentrun.OutcomeFailure, err)
	}
	return execution.AdapterResult{Output: output}, nil
}

// waitForProjection polls until the durable projection reaches want, bounded.
func waitForProjection(t *testing.T, observer *execution.Controller, runID agentrun.Identity, want agentrun.LifecycleState) store.RunProjection {
	t.Helper()
	deadline := time.Now().Add(observationBudget)
	for time.Now().Before(deadline) {
		inspection, err := observer.Inspect(context.Background(), runID)
		if err == nil && inspection.Projection.State == want {
			return inspection.Projection
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("projection never reached %q within the observation budget", want)
	return store.RunProjection{}
}

// assertRunsInspection is the `sentinel runs`-style consistency proof shared
// by every scenario: the inspected projection state matches the expected
// terminal (or current) shape, the reconciled read view agrees with it, and
// the run appears exactly once in the store listing.
func assertRunsInspection(t *testing.T, backing *store.Store, observer *execution.Controller, runID string, wantState agentrun.LifecycleState, wantTerminal agentrun.TerminalClass) execution.Inspection {
	t.Helper()
	inspection, err := observer.Inspect(context.Background(), agentrun.Identity(runID))
	if err != nil {
		t.Fatalf("Inspect(%s): %v", runID, err)
	}
	if inspection.Projection.State != wantState || inspection.Projection.Terminal != wantTerminal {
		t.Fatalf("inspected projection = %s/%s, want %s/%s",
			inspection.Projection.State, inspection.Projection.Terminal, wantState, wantTerminal)
	}
	reconciled, err := backing.ReadReconciledProjection(runID)
	if err != nil {
		t.Fatalf("ReadReconciledProjection(%s): %v", runID, err)
	}
	if reconciled.State != inspection.Projection.State || reconciled.Terminal != inspection.Projection.Terminal {
		t.Fatalf("reconciled view = %s/%s disagrees with inspected projection %s/%s",
			reconciled.State, reconciled.Terminal, inspection.Projection.State, inspection.Projection.Terminal)
	}
	assertSingleRun(t, backing, runID)
	return inspection
}

// assertSingleRun proves `sentinel runs` sees exactly this run and nothing else.
func assertSingleRun(t *testing.T, backing *store.Store, runID string) {
	t.Helper()
	ids, err := backing.ListExecutionIDs()
	if err != nil {
		t.Fatalf("ListExecutionIDs: %v", err)
	}
	if len(ids) != 1 || ids[0] != runID {
		t.Fatalf("ListExecutionIDs = %v, want exactly [%s]", ids, runID)
	}
}

// seedOrphanedCancellationStream appends a valid owner-loss stream through
// the real store machinery: admission progress followed by a terminating
// escalation transition and NO canceled settlement frame — the durable
// fingerprint of a process that died while its own cancellation was settling.
func seedOrphanedCancellationStream(t *testing.T, backing *store.Store, candidate string) (agentrun.LogicalJob, agentrun.Identity, []store.EventFrame) {
	t.Helper()
	job := agentrun.NewLogicalJob(e2eRequest(candidate))
	if err := backing.CreateRun(job, e2ePolicy()); err != nil {
		t.Fatal(err)
	}
	invocation, err := agentrun.NewRootInvocation(job, 1, agentrun.DecisionStart)
	if err != nil {
		t.Fatal(err)
	}
	transitions := []struct{ from, to agentrun.LifecycleState }{
		{agentrun.StateCreated, agentrun.StateQueued},
		{agentrun.StateQueued, agentrun.StateAdmitted},
		{agentrun.StateAdmitted, agentrun.StateRunning},
		{agentrun.StateRunning, agentrun.StateTerminating},
	}
	for offset, transition := range transitions {
		event, eventErr := agentrun.NewNormalizedEvent(invocation,
			transition.from, transition.to, agentrun.DecisionNone,
			time.Unix(1700000000+int64(offset), 0).UTC())
		if eventErr != nil {
			t.Fatal(eventErr)
		}
		if _, appendErr := backing.AppendRunEvent(string(job.RunID()), event, uint64(offset)); appendErr != nil {
			t.Fatal(appendErr)
		}
	}
	page, err := backing.ReadEvents(string(job.RunID()), 0, 128)
	if err != nil {
		t.Fatal(err)
	}
	return job, invocation.InvocationID(), page.Events
}

// countTransitions counts frames recording one lifecycle edge.
func countTransitions(t *testing.T, events []store.EventFrame, from, to agentrun.LifecycleState) int {
	t.Helper()
	count := 0
	for _, frame := range events {
		if frame.From == from && frame.To == to {
			count++
		}
	}
	return count
}

// writeFakeAgent writes an executable fake provider binary into dir.
func writeFakeAgent(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// runGit executes one git command in dir and fails the test on error.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

// writeRepoFile writes a tracked file into a repository working tree.
func writeRepoFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// waitStarted bounds the wait for an adapter's started signal so a controller
// regression fails fast with a clear message instead of hanging to the
// go-test timeout.
func waitStarted(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("adapter never reported started within 5s")
	}
}
