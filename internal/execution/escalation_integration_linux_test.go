package execution

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/process"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// The integration proofs below run the real stack — owned process-tree spawn,
// controller-authored bounded escalation, and the durable store — against
// real shell trees where a child spawns a sleeping grandchild. They are
// Linux-only because Debian CI is the platform under test; Windows ownership
// coverage is compile-time (internal/process/process_windows_test.go).
func requireLinuxIntegration(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("process-tree integration requires Linux")
	}
}

// treeSpawnAdapter mirrors the production review adapter's contract: it owns
// a spawned child through process.Spawn — the exact seam inside the restricted
// reviewer's startOwnedCommand — and exposes its tree for controller escalation.
type treeSpawnAdapter struct {
	tree    atomic.Pointer[process.Tree]
	script  []string
	started chan struct{}
	mu      sync.Mutex
	cmd     *exec.Cmd
	once    sync.Once
}

func newTreeSpawnAdapter(script []string) *treeSpawnAdapter {
	return &treeSpawnAdapter{script: script, started: make(chan struct{})}
}

func (a *treeSpawnAdapter) Execute(ctx context.Context, _ agentrun.LogicalJob, _ agentrun.InvocationEnvelope, _ string) (AdapterResult, error) {
	cmd, tree, err := process.Spawn(ctx, a.script[0], a.script[1:], nil)
	if err != nil {
		close(a.started)
		return AdapterResult{}, err
	}
	a.mu.Lock()
	a.cmd = cmd
	a.mu.Unlock()
	a.tree.Store(tree)
	a.once.Do(func() { close(a.started) })

	err = cmd.Wait()
	tree.MarkExited()
	if ctx.Err() != nil {
		return AdapterResult{}, fmt.Errorf("spawned child interrupted by its context: %w", ctx.Err())
	}
	return AdapterResult{Output: "provider output"}, nil
}

// OwnedTree exposes the live tree to the controller exactly as the production
// reviewer wrappers do.
func (a *treeSpawnAdapter) OwnedTree() *process.Tree { return a.tree.Load() }

func waitForFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			return strings.TrimSpace(string(data))
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never appeared", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func procExists(pid int) bool {
	_, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
	return err == nil
}

func waitGone(t *testing.T, pid int, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for procExists(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("%s (%d) survived whole-tree termination", what, pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func countTransitions(t *testing.T, inspection Inspection, from, to agentrun.LifecycleState) int {
	t.Helper()
	count := 0
	for _, frame := range inspection.Events {
		if frame.From == from && frame.To == to {
			count++
		}
	}
	return count
}

// TestEscalationKillsGrandchildTreeWithEvidence is the descendant-coverage
// proof: a shell child spawns a sleeping grandchild; aborting the run
// terminates the whole group and appends termination-attempted plus reaped
// evidence exactly once each before the authoritative settlement.
func TestEscalationKillsGrandchildTreeWithEvidence(t *testing.T) {
	requireLinuxIntegration(t)
	dir := t.TempDir()
	gpidFile := filepath.Join(dir, "grandchild-pid")
	script := []string{"sh", "-c", "sleep 30 & printf %s $! > " + gpidFile + "\nwait"}
	policy := EscalationPolicy{Grace: 150 * time.Millisecond, FinalBudget: 2 * time.Second}

	backing := store.NewStore(dir)
	adapter := newTreeSpawnAdapter(script)
	controller := NewControllerWithClockAndEscalation(backing, adapter, nil, policy)
	handle, err := controller.Start(context.Background(),
		agentrun.NewRunRequest(agentrun.Candidate("candidate:integration"), agentrun.Prompt("prompt"), nil),
		store.RunPolicy{ID: "policy:test"})
	if err != nil {
		t.Fatal(err)
	}
	<-adapter.started

	gpid, err := strconv.Atoi(waitForFile(t, gpidFile))
	if err != nil {
		t.Fatalf("grandchild pid unreadable: %v", err)
	}
	if !procExists(gpid) {
		t.Fatalf("grandchild %d not running before abort", gpid)
	}

	start := time.Now()
	if _, err := controller.Apply(context.Background(), handle.RunID, ControlAction{Kind: ActionAbort}); err != nil {
		t.Fatal(err)
	}
	waitContext, cancelWait := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWait()
	completion, err := handle.Wait(waitContext)
	if err != nil {
		t.Fatalf("handle.Wait() = %v; escalation must settle within its budget", err)
	}
	if completion.State != agentrun.StateCanceled {
		t.Fatalf("completion state = %q, want canceled", completion.State)
	}

	waitGone(t, gpid, "sleeping grandchild")
	waitGone(t, adapter.OwnedTree().Pid(), "shell child")
	if elapsed := time.Since(start); elapsed > policy.Grace+3*time.Second {
		t.Fatalf("escalation took %s; whole-tree kill must stay bounded", elapsed)
	}

	inspection, err := NewController(backing, nil).Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if got := countTransitions(t, inspection, agentrun.StateRunning, agentrun.StateTerminating); got != 1 {
		t.Fatalf("termination-attempted evidence appended %d times, want exactly once", got)
	}
	if got := countTransitions(t, inspection, agentrun.StateTerminating, agentrun.StateTerminated); got != 1 {
		t.Fatalf("reaped evidence appended %d times, want exactly once", got)
	}
	last := inspection.Events[len(inspection.Events)-1]
	if last.From != agentrun.StateTerminated || last.To != agentrun.StateCanceled || last.Decision != agentrun.DecisionAbort {
		t.Fatalf("terminal event = %s->%s/%s, want terminated->canceled by abort", last.From, last.To, last.Decision)
	}
}

// TestDisabledEscalationNeverSignalsTheWholeTree is the acceptance proof for
// the Disabled rollback seam: a TERM-ignoring grandchild SURVIVES the abort
// while the direct child dies through the exec kill switch, zero escalation
// frames are appended, and the settlement records honest descendant
// accounting instead of a reaped or orphaned claim.
func TestDisabledEscalationNeverSignalsTheWholeTree(t *testing.T) {
	requireLinuxIntegration(t)
	dir := t.TempDir()
	gpidFile := filepath.Join(dir, "grandchild-pid")
	script := []string{"sh", "-c", "trap '' TERM\nsleep 30 & printf %s $! > " + gpidFile + "\nwait"}

	backing := store.NewStore(dir)
	adapter := newTreeSpawnAdapter(script)
	controller := NewControllerWithClockAndEscalation(backing, adapter, nil, EscalationPolicy{Disabled: true})
	handle, err := controller.Start(context.Background(),
		agentrun.NewRunRequest(agentrun.Candidate("candidate:disabled"), agentrun.Prompt("prompt"), nil),
		store.RunPolicy{ID: "policy:test"})
	if err != nil {
		t.Fatal(err)
	}
	<-adapter.started
	childPid := adapter.OwnedTree().Pid()

	gpid, err := strconv.Atoi(waitForFile(t, gpidFile))
	if err != nil {
		t.Fatalf("grandchild pid unreadable: %v", err)
	}
	if !procExists(gpid) {
		t.Fatalf("grandchild %d not running before abort", gpid)
	}

	start := time.Now()
	if _, err := controller.Apply(context.Background(), handle.RunID, ControlAction{Kind: ActionAbort}); err != nil {
		t.Fatal(err)
	}
	waitContext, cancelWait := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelWait()
	completion, err := handle.Wait(waitContext)
	if err != nil {
		t.Fatalf("handle.Wait() = %v; disabled mode must settle promptly", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("settlement took %s; disabled mode must not wait for escalation budgets", elapsed)
	}

	// The DIRECT child died through the exec kill switch...
	waitGone(t, childPid, "direct shell child in disabled mode")
	// ...and the TERM-ignoring grandchild SURVIVED it, proving no whole-tree
	// signal was ever issued.
	if !procExists(gpid) {
		t.Fatalf("grandchild %d died under Disabled policy; that proves an illegal whole-tree kill", gpid)
	}
	defer func() {
		// Containment of the survivor is the test's own responsibility now:
		// nothing else will ever terminate it.
		if proc, findErr := os.FindProcess(gpid); findErr == nil {
			_ = proc.Kill()
		}
	}()

	completion, err = handle.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if completion.State != agentrun.StateCanceled ||
		!strings.Contains(completion.Error, strconv.Itoa(childPid)) ||
		!strings.Contains(completion.Error, "orphan accounting unavailable") ||
		!strings.Contains(completion.Error, "whole-tree termination disabled") {
		t.Fatalf("completion = %+v, want canceled with honest descendant-accounting caveat naming pid %d", completion, childPid)
	}
	if strings.Contains(completion.Error, "reaped process tree") {
		t.Fatalf("completion = %+v; disabled mode must never claim a reaped tree", completion)
	}

	inspection, err := NewController(backing, nil).Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	assertNoEscalationFrames(t, inspection.Events)
	last := inspection.Events[len(inspection.Events)-1]
	if last.From != agentrun.StateRunning || last.To != agentrun.StateCanceled || last.Decision != agentrun.DecisionAbort {
		t.Fatalf("terminal event = %s->%s/%s, want running->canceled by abort straight from running", last.From, last.To, last.Decision)
	}
}

// TestEscalationBeatsSignalIgnoringTree proves bounded escalation against a
// tree that traps-and-ignores SIGTERM: the cooperative signal is useless, the
// hard kill is not, and everything dies inside grace+margin.
func TestEscalationBeatsSignalIgnoringTree(t *testing.T) {
	requireLinuxIntegration(t)
	dir := t.TempDir()
	gpidFile := filepath.Join(dir, "grandchild-pid")
	script := []string{"sh", "-c", "trap '' TERM\nsleep 30 & printf %s $! > " + gpidFile + "\nwait"}
	policy := EscalationPolicy{Grace: 200 * time.Millisecond, FinalBudget: 2 * time.Second}

	backing := store.NewStore(dir)
	adapter := newTreeSpawnAdapter(script)
	controller := NewControllerWithClockAndEscalation(backing, adapter, nil, policy)
	handle, err := controller.Start(context.Background(),
		agentrun.NewRunRequest(agentrun.Candidate("candidate:ignore-term"), agentrun.Prompt("prompt"), nil),
		store.RunPolicy{ID: "policy:test"})
	if err != nil {
		t.Fatal(err)
	}
	<-adapter.started

	gpid, err := strconv.Atoi(waitForFile(t, gpidFile))
	if err != nil {
		t.Fatalf("grandchild pid unreadable: %v", err)
	}

	start := time.Now()
	if _, err := controller.Apply(context.Background(), handle.RunID, ControlAction{Kind: ActionAbort}); err != nil {
		t.Fatal(err)
	}
	waitContext, cancelWait := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWait()
	if _, err := handle.Wait(waitContext); err != nil {
		t.Fatalf("handle.Wait() = %v; TERM-ignoring trees must still settle inside the budget", err)
	}
	if elapsed := time.Since(start); elapsed > policy.Grace+3*time.Second {
		t.Fatalf("TERM-ignoring tree survived %s; hard escalation must stay inside grace+margin", elapsed)
	}
	waitGone(t, gpid, "signal-ignoring grandchild")
	waitGone(t, adapter.OwnedTree().Pid(), "signal-ignoring shell child")

	inspection, err := NewController(backing, nil).Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if got := countTransitions(t, inspection, agentrun.StateRunning, agentrun.StateTerminating); got != 1 {
		t.Fatalf("termination-attempted evidence appended %d times, want exactly once", got)
	}
}
