package execution

import (
	"context"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/process"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// ownedSleepAdapter spawns a real `sleep 30` child through the same ownership
// seam production uses and exposes its tree to the controller. The test
// decides when (or whether) exit confirmation happens, which is exactly the
// freedom the escalation state machine's outcomes depend on.
type ownedSleepAdapter struct {
	mu        sync.Mutex
	tree      *process.Tree
	cmd       *exec.Cmd
	started   chan struct{}
	release   chan struct{}
	returned  chan struct{}
	markDelay time.Duration // after context cancellation; negative never confirms
	command   []string      // child command; defaults to a long sleep
	once      sync.Once
}

func newOwnedSleepAdapter(markDelay time.Duration) *ownedSleepAdapter {
	return &ownedSleepAdapter{
		started: make(chan struct{}), release: make(chan struct{}), returned: make(chan struct{}),
		markDelay: markDelay,
		command:   []string{"sleep", "30"},
	}
}

func (a *ownedSleepAdapter) Execute(ctx context.Context, _ agentrun.LogicalJob, _ agentrun.InvocationEnvelope, _ string) (AdapterResult, error) {
	cmd, tree, err := process.Spawn(ctx, a.command[0], a.command[1:], nil)
	if err != nil {
		close(a.returned)
		return AdapterResult{}, err
	}
	a.mu.Lock()
	a.cmd, a.tree = cmd, tree
	a.mu.Unlock()
	// started fires only once the owned tree is registered, so tests can read
	// OwnedTree() without racing the spawn.
	a.once.Do(func() { close(a.started) })

	<-ctx.Done()
	go func() {
		if a.markDelay >= 0 {
			time.Sleep(a.markDelay)
			_ = cmd.Wait()
			tree.MarkExited()
		} else {
			// Unconfirmable reap: the tree dies but exit confirmation never
			// reaches the controller.
			_ = cmd.Wait()
		}
		tree.Release()
	}()
	<-a.release
	close(a.returned)
	return AdapterResult{}, nil
}

func (a *ownedSleepAdapter) OwnedTree() *process.Tree {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tree
}

// killChild contains the spawned child no matter how the test ends.
func (a *ownedSleepAdapter) killChild() {
	a.mu.Lock()
	tree := a.tree
	cmd := a.cmd
	a.mu.Unlock()
	if tree == nil {
		return
	}
	_ = process.Terminate(tree)
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// TestAbortWithOwnedTreeCooperativeExitSettlesSilently proves that a child
// exiting inside the grace window settles canceled with NO extra evidence:
// existing paths write no new events beyond slice 1.
func TestAbortWithOwnedTreeCooperativeExitSettlesSilently(t *testing.T) {
	if !isLinux() {
		t.Skip("real process-tree escalation requires Linux")
	}
	adapter := newOwnedSleepAdapter(0)
	// The child exits on its own long before the grace budget expires, which
	// is what makes this a genuinely cooperative cancellation.
	adapter.command = []string{"sleep", "0.05"}
	controller := NewControllerWithClockAndEscalation(store.NewStore(t.TempDir()), adapter, fixedClock(),
		EscalationPolicy{Grace: time.Second, FinalBudget: time.Second})
	handle, err := controller.Start(context.Background(), testRequest("coop"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	<-adapter.started

	if _, err := controller.Apply(context.Background(), handle.RunID, ControlAction{Kind: ActionAbort}); err != nil {
		t.Fatal(err)
	}
	waitContext, cancelWait := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelWait()
	completion, err := handle.Wait(waitContext)
	if err != nil {
		t.Fatalf("Wait() = %v", err)
	}
	if completion.State != agentrun.StateCanceled || completion.Error != "aborted while running" {
		t.Fatalf("completion = %+v, want plain canceled settlement", completion)
	}
	inspection, err := NewController(controller.store, nil).Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	assertNoEscalationFrames(t, inspection.Events)
	close(adapter.release)
	<-adapter.returned
	adapter.killChild()
}

// TestAbortWithOwnedTreeReapedEvidenceExactlyOnce proves the full escalation
// trail for a tree that dies only after termination: one terminating frame,
// one terminated frame, then the authoritative canceled settlement carrying
// the recorded pid.
func TestAbortWithOwnedTreeReapedEvidenceExactlyOnce(t *testing.T) {
	if !isLinux() {
		t.Skip("real process-tree escalation requires Linux")
	}
	policy := EscalationPolicy{Grace: 100 * time.Millisecond, FinalBudget: time.Second}
	adapter := newOwnedSleepAdapter(300 * time.Millisecond) // after grace, inside final budget
	controller := NewControllerWithClockAndEscalation(store.NewStore(t.TempDir()), adapter, fixedClock(), policy)
	handle, err := controller.Start(context.Background(), testRequest("reaped"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	<-adapter.started
	pid := adapter.OwnedTree().Pid()

	start := time.Now()
	if _, err := controller.Apply(context.Background(), handle.RunID, ControlAction{Kind: ActionAbort}); err != nil {
		t.Fatal(err)
	}
	waitContext, cancelWait := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWait()
	completion, err := handle.Wait(waitContext)
	if err != nil {
		t.Fatalf("Wait() = %v", err)
	}
	elapsed := time.Since(start)

	inspection, err := NewController(controller.store, nil).Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	counts := transitionCounts(inspection.Events)
	if counts[transitionKey(agentrun.StateRunning, agentrun.StateTerminating)] != 1 {
		t.Fatalf("termination-attempted frames = %d, want exactly one", counts[transitionKey(agentrun.StateRunning, agentrun.StateTerminating)])
	}
	if counts[transitionKey(agentrun.StateTerminating, agentrun.StateTerminated)] != 1 {
		t.Fatalf("reaped frames = %d, want exactly one", counts[transitionKey(agentrun.StateTerminating, agentrun.StateTerminated)])
	}
	last := inspection.Events[len(inspection.Events)-1]
	if last.From != agentrun.StateTerminated || last.To != agentrun.StateCanceled || last.Decision != agentrun.DecisionAbort {
		t.Fatalf("terminal event = %s->%s/%s, want terminated->canceled by abort", last.From, last.To, last.Decision)
	}
	if completion.Outcome != agentrun.OutcomeCancellation || !strings.Contains(completion.Error, "reaped process tree") ||
		!strings.Contains(completion.Error, strconv.Itoa(pid)) {
		t.Fatalf("completion = %+v, want cancellation with reaped detail naming pid %d", completion, pid)
	}
	// The whole escalation must stay bounded by roughly grace + final budget.
	if elapsed > policy.Grace+policy.FinalBudget+2*time.Second {
		t.Fatalf("escalation took %s, exceeded grace+budget bound", elapsed)
	}
	close(adapter.release)
	<-adapter.returned
}

// TestAbortUnconfirmableReapSettlesOrphanedOnce proves orphan honesty: when
// even hard termination cannot be confirmed within the final budget, the run
// settles canceled-with-orphan detail exactly once and no reaped evidence is
// ever appended.
func TestAbortUnconfirmableReapSettlesOrphanedOnce(t *testing.T) {
	if !isLinux() {
		t.Skip("real process-tree escalation requires Linux")
	}
	adapter := newOwnedSleepAdapter(-1 * time.Second) // never confirms exit
	controller := NewControllerWithClockAndEscalation(store.NewStore(t.TempDir()), adapter, fixedClock(),
		EscalationPolicy{Grace: 100 * time.Millisecond, FinalBudget: 250 * time.Millisecond})
	handle, err := controller.Start(context.Background(), testRequest("orphan"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	<-adapter.started
	pid := adapter.OwnedTree().Pid()

	if _, err := controller.Apply(context.Background(), handle.RunID, ControlAction{Kind: ActionAbort}); err != nil {
		t.Fatal(err)
	}
	waitContext, cancelWait := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWait()
	completion, err := handle.Wait(waitContext)
	if err != nil {
		t.Fatalf("Wait() = %v", err)
	}
	if completion.State != agentrun.StateCanceled || !strings.Contains(completion.Error, "orphaned") ||
		!strings.Contains(completion.Error, strconv.Itoa(pid)) {
		t.Fatalf("completion = %+v, want canceled-with-orphan detail naming pid %d", completion, pid)
	}
	inspection, err := NewController(controller.store, nil).Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	counts := transitionCounts(inspection.Events)
	if counts[transitionKey(agentrun.StateRunning, agentrun.StateTerminating)] != 1 {
		t.Fatalf("termination-attempted frames = %d, want exactly one", counts[transitionKey(agentrun.StateRunning, agentrun.StateTerminating)])
	}
	if counts[transitionKey(agentrun.StateTerminating, agentrun.StateTerminated)] != 0 {
		t.Fatalf("reaped frames = %d, want none for an unconfirmable reap", counts[transitionKey(agentrun.StateTerminating, agentrun.StateTerminated)])
	}
	outcomes := 0
	for _, outcome := range inspection.Outcomes {
		if strings.Contains(outcome.Error, "orphaned") {
			outcomes++
		}
	}
	if outcomes != 1 {
		t.Fatalf("orphaned settlements = %d, want exactly once", outcomes)
	}
	close(adapter.release)
	<-adapter.returned
	adapter.killChild()
}

// TestDoubleAbortDuringEscalationIsIdempotent proves a repeated abort while
// escalation is in flight neither duplicates evidence nor errors out.
func TestDoubleAbortDuringEscalationIsIdempotent(t *testing.T) {
	if !isLinux() {
		t.Skip("real process-tree escalation requires Linux")
	}
	adapter := newOwnedSleepAdapter(-1 * time.Second) // slow orphan path keeps the window open
	controller := NewControllerWithClockAndEscalation(store.NewStore(t.TempDir()), adapter, fixedClock(),
		EscalationPolicy{Grace: 100 * time.Millisecond, FinalBudget: 500 * time.Millisecond})
	handle, err := controller.Start(context.Background(), testRequest("double-escalation"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	<-adapter.started

	first, err := controller.Apply(context.Background(), handle.RunID, ControlAction{Kind: ActionAbort})
	if err != nil || !first.Accepted {
		t.Fatalf("first Apply(abort) = %+v, %v", first, err)
	}
	// The first Apply marked the run aborting under the state lock before
	// returning, so this second call deterministically lands inside the
	// escalation window (grace has not expired yet).
	second, err := controller.Apply(context.Background(), handle.RunID, ControlAction{Kind: ActionAbort})
	if err != nil || !second.Accepted {
		t.Fatalf("second Apply(abort) during escalation = %+v, %v; want idempotent acceptance", second, err)
	}

	waitContext, cancelWait := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWait()
	if _, err := handle.Wait(waitContext); err != nil {
		t.Fatal(err)
	}
	inspection, err := NewController(controller.store, nil).Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	counts := transitionCounts(inspection.Events)
	if counts[transitionKey(agentrun.StateRunning, agentrun.StateTerminating)] != 1 {
		t.Fatalf("termination-attempted frames = %d, want exactly one despite double abort", counts[transitionKey(agentrun.StateRunning, agentrun.StateTerminating)])
	}
	if len(inspection.Outcomes) != 1 {
		t.Fatalf("outcomes = %d, want exactly one despite double abort", len(inspection.Outcomes))
	}
	close(adapter.release)
	<-adapter.returned
	adapter.killChild()
}

// TestDisabledEscalationSettlesPromptly proves the constructor-time rollback
// seam: with escalation disabled an owned tree changes the settlement only by
// its honest accounting caveat — the abort settles promptly like slice 1,
// zero escalation frames are appended, and no kill beyond the direct child is
// ever issued by any code path.
func TestDisabledEscalationSettlesPromptly(t *testing.T) {
	if !isLinux() {
		t.Skip("real process-tree escalation requires Linux")
	}
	adapter := newOwnedSleepAdapter(-1 * time.Second)
	controller := NewControllerWithClockAndEscalation(store.NewStore(t.TempDir()), adapter, fixedClock(),
		EscalationPolicy{Disabled: true})
	handle, err := controller.Start(context.Background(), testRequest("disabled"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	<-adapter.started
	pid := adapter.OwnedTree().Pid()

	start := time.Now()
	if _, err := controller.Apply(context.Background(), handle.RunID, ControlAction{Kind: ActionAbort}); err != nil {
		t.Fatal(err)
	}
	waitContext, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	completion, err := handle.Wait(waitContext)
	if err != nil {
		t.Fatalf("Wait() = %v; disabled escalation must settle promptly", err)
	}
	if elapsed := time.Since(start); elapsed > 900*time.Millisecond {
		t.Fatalf("settlement took %s; disabled escalation must not wait for any budget", elapsed)
	}
	if completion.State != agentrun.StateCanceled ||
		!strings.Contains(completion.Error, strconv.Itoa(pid)) ||
		!strings.Contains(completion.Error, "orphan accounting unavailable") ||
		!strings.Contains(completion.Error, "whole-tree termination disabled") {
		t.Fatalf("completion = %+v, want canceled settlement with honest descendant-accounting caveat for pid %d", completion, pid)
	}
	if strings.Contains(completion.Error, "reaped process tree") {
		t.Fatalf("completion = %+v; disabled mode must never claim a reaped tree", completion)
	}
	inspection, err := NewController(controller.store, nil).Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	assertNoEscalationFrames(t, inspection.Events)
	close(adapter.release)
	<-adapter.returned
	adapter.killChild()
}

func isLinux() bool { return runtime.GOOS == "linux" }

// transitionKey names one from->to pair for counting.
func transitionKey(from, to agentrun.LifecycleState) string {
	return string(from) + ">" + string(to)
}

// transitionCounts counts from>to pairs across the event stream.
func transitionCounts(events []store.EventFrame) map[string]int {
	counts := make(map[string]int)
	for _, frame := range events {
		counts[transitionKey(frame.From, frame.To)]++
	}
	return counts
}

func assertNoEscalationFrames(t *testing.T, events []store.EventFrame) {
	t.Helper()
	for _, frame := range events {
		if frame.To == agentrun.StateTerminating || frame.To == agentrun.StateTerminated {
			t.Fatalf("event %d reached %q; this path must write no escalation evidence", frame.Sequence, frame.To)
		}
	}
}
