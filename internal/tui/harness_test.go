package tui

import (
	"context"
	"io"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/bubbletea"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/attach"
	"github.com/ISeoane-Quental/vas.sentinel/internal/daemon"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// The acceptance harness for ticket 17: a REAL foreground daemon started
// through daemon.Run over a temp repository common dir, the attach Model
// driven headlessly through Update against hosts dialed with
// DialRemoteHost(FingerprintRepository(...)) to THAT daemon, and durable
// effects asserted through direct store reads. No terminal is ever spawned:
// poll ticks and backoff ticks are synthesized as messages, which is exactly
// the seam bubbletea gives us and the one slice 2's transition tests already
// pin.

const harnessPrincipal = "tui-harness"

// gateAdapter keeps every Execute blocked until its release channel closes,
// so the harness decides exactly when durable progress happens relative to
// the attach session's connection state.
type gateAdapter struct {
	mu      sync.Mutex
	release chan struct{}
	closed  bool
}

func newGateAdapter() *gateAdapter {
	return &gateAdapter{release: make(chan struct{})}
}

func (a *gateAdapter) Execute(ctx context.Context, _ agentrun.LogicalJob, _ agentrun.InvocationEnvelope, _ string) (execution.AdapterResult, error) {
	select {
	case <-a.release:
		return execution.AdapterResult{Output: "harness completed output"}, nil
	case <-ctx.Done():
		return execution.AdapterResult{}, ctx.Err()
	}
}

func (a *gateAdapter) open() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.closed {
		close(a.release)
		a.closed = true
	}
}

// harnessDaemon bundles everything one harness scenario needs: the host
// provider dialing the real endpoint, the backing store for direct durable
// reads, the daemon's own controller for quiescence checks, and a graceful
// stop wired into t.Cleanup.
type harnessDaemon struct {
	provider   HostProvider
	backing    *store.Store
	controller *execution.Controller
}

// startHarnessDaemon launches daemon.Run in a goroutine with a controller
// built the way production builds it (execution.NewController over
// store.NuevoStore(commonDir)), waits until the persisted endpoint answers,
// and wires a graceful wire shutdown into cleanup.
func startHarnessDaemon(t *testing.T, adapter execution.Adapter) harnessDaemon {
	t.Helper()
	// Short-lived directory with a SHORT prefix: the unix transport binds
	// <dir>/vas-sentinel/daemon/daemon.sock inside sun_path's hard 108-byte
	// budget, and t.TempDir() would spend most of it on the test name.
	commonDir, err := os.MkdirTemp("", "vh")
	if err != nil {
		t.Fatalf("create harness common dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(commonDir) })
	controller := execution.NewController(store.NuevoStore(commonDir), adapter)
	done := make(chan error, 1)
	go func() { done <- daemon.Run(commonDir, controller, daemon.DefaultGracePeriod, io.Discard) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, _, err := daemon.LoadEndpoint(daemon.Dir(commonDir)); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("daemon exited before becoming ready: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("harness daemon never became ready")
		}
		time.Sleep(5 * time.Millisecond)
	}
	harness := harnessDaemon{
		backing:    store.NuevoStore(commonDir),
		controller: controller,
	}
	harness.provider = func() (execution.RepositoryHost, error) {
		endpoint, _, err := daemon.LoadEndpoint(daemon.Dir(commonDir))
		if err != nil {
			return nil, err
		}
		return daemon.DialRemoteHost(endpoint, daemon.FingerprintRepository(commonDir))
	}
	t.Cleanup(func() {
		if endpoint, _, err := daemon.LoadEndpoint(daemon.Dir(commonDir)); err == nil {
			if host, dialErr := daemon.DialRemoteHost(endpoint, daemon.FingerprintRepository(commonDir)); dialErr == nil {
				request := daemon.ShutdownRequest{AuthContext: execution.AuthContext{Principal: harnessPrincipal}}
				_, _ = host.Shutdown(context.Background(), request)
				_ = host.Close()
			}
		}
		select {
		case runErr := <-done:
			if runErr != nil {
				t.Errorf("harness daemon Run returned %v on shutdown", runErr)
			}
		case <-time.After(10 * time.Second):
			t.Error("harness daemon never returned after graceful stop")
		}
	})
	return harness
}

// startHarnessRun admits one gated run through a dedicated dialed host and
// returns its identity; the caller owns closing that host.
func startHarnessRun(t *testing.T, h harnessDaemon) (agentrun.Identity, execution.RepositoryHost) {
	t.Helper()
	host, err := h.provider()
	if err != nil {
		t.Fatalf("dial harness daemon: %v", err)
	}
	handle, err := host.Start(context.Background(), execution.StartRequest{
		Candidate:   "candidate:harness",
		Prompt:      "prompt:harness",
		Policy:      store.RunPolicy{ID: "policy:harness"},
		AuthContext: execution.AuthContext{Principal: harnessPrincipal},
	})
	if err != nil {
		t.Fatalf("wire start failed: %v", err)
	}
	return handle.RunID, host
}

// step feeds one message through Update, asserting the model type survives.
func step(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	typed, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want tui.Model", next)
	}
	return typed, cmd
}

// observeCycle drives one full synchronous observe round: synthesize the poll
// tick, execute whatever command it schedules (the dial-and-exchange completes
// without wall-clock waits), and apply the resulting message.
func observeCycle(t *testing.T, m Model) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := step(t, m, pollTickMsg{})
	if cmd == nil {
		return next, nil // frozen or mid-reconnect: nothing scheduled
	}
	msg := cmd() // never a tick: observeCmd resolves and exchanges synchronously
	return step(t, next, msg)
}

// driveUntilRunning observes in cycles until the projection shows the run
// live, failing on timeout. Each cycle synthesizes its own poll tick, so no
// real tea.Tick timer ever needs to fire.
func driveUntilRunning(t *testing.T, m Model) Model {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for m.view.State != agentrun.StateRunning {
		m, _ = observeCycle(t, m)
		if time.Now().After(deadline) {
			t.Fatalf("attach never observed the running state: %+v", m.view)
		}
	}
	return m
}

// awaitRunQuiescence proves no settlement writer is left touching the
// harness common dir before its RemoveAll cleanup: it polls the daemon
// controller's inspections until two consecutive snapshots taken one poll
// cycle apart are identical. This is the drain discipline established in
// internal/daemon/server_shutdown_test.go: a run whose durable projection
// already reads terminal can still have a worker finishing its writes.
func awaitRunQuiescence(t *testing.T, h harnessDaemon, ids ...agentrun.Identity) {
	t.Helper()
	snapshot := make([]execution.Inspection, 0, len(ids))
	for _, id := range ids {
		inspection, err := h.controller.Inspect(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		snapshot = append(snapshot, inspection)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		time.Sleep(5 * time.Millisecond)
		next := make([]execution.Inspection, 0, len(ids))
		for _, id := range ids {
			inspection, err := h.controller.Inspect(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			next = append(next, inspection)
		}
		if reflect.DeepEqual(snapshot, next) {
			return
		}
		snapshot = next
		if time.Now().After(deadline) {
			t.Fatal("harness run store never quiesced before teardown")
		}
	}
}

// TestHarnessAbortThroughKeyboardProducesDurableCancellation is AC1+AC3
// evidence: the abort key path reaches the REAL daemon over the wire, the
// controller authors the same canceled settlement its CLI twin produces, the
// projection freezes with the exit hint, and direct store reads prove the
// durable evidence (cancellation class, orphan-free).
func TestHarnessAbortThroughKeyboardProducesDurableCancellation(t *testing.T) {
	gate := newGateAdapter()
	harness := startHarnessDaemon(t, gate)
	runID, seedHost := startHarnessRun(t, harness)
	defer func() {
		if closer, ok := seedHost.(*daemon.RemoteHost); ok {
			_ = closer.Close()
		}
	}()

	model := New(runID, harnessPrincipal, harness.provider, attach.NewReplayCollector(0), attach.RunView{}, context.Background())
	model = driveUntilRunning(t, model)

	// Dispatch the abort key path: 'a' schedules the action command, whose
	// message lands as an accepted action result, and the follow-up observe
	// picks the settled cancellation up and freezes the view.
	keyed, actionCmd := step(t, model, keyMsg("a"))
	result := actionCmd() // dial + Apply complete synchronously
	actionMsg, ok := result.(actionResultMsg)
	if !ok || actionMsg.Err != nil {
		t.Fatalf("abort key path produced %v, want an accepted action", result)
	}
	afterAction, observeCmd := step(t, keyed, result)
	pageMsg := observeCmd()
	if _, ok := pageMsg.(pagesAppliedMsg); !ok {
		t.Fatalf("post-abort observe produced %T, want pagesAppliedMsg", pageMsg)
	}
	final, _ := step(t, afterAction, pageMsg)

	if !final.Frozen() {
		t.Fatal("terminal cancellation did not freeze the attach view")
	}
	if final.Snapshot().State != agentrun.StateCanceled || final.Snapshot().Outcome != agentrun.OutcomeCancellation {
		t.Fatalf("observed state/outcome = %s/%s, want canceled/cancellation",
			final.Snapshot().State, final.Snapshot().Outcome)
	}
	// Teardown drain: the abort settled the run, but the controller worker
	// may still be finishing its durable writes. Require store quiescence
	// before the direct reads below (so the single-outcome assertion is not
	// racing an in-flight append) and before cleanup removes the common dir.
	awaitRunQuiescence(t, harness, runID)

	// Durable evidence via direct store reads: the settlement class is
	// cancellation, authored by the controller (same as `runs abort`), with
	// no orphaned-cancellation reconciliation verdict attached.
	projection, err := harness.backing.ReadReconciledProjection(string(runID))
	if err != nil {
		t.Fatalf("read reconciled projection: %v", err)
	}
	if projection.State != agentrun.StateCanceled || projection.Terminal != agentrun.TerminalCancellation {
		t.Fatalf("durable projection = %s/%s, want canceled/cancellation", projection.State, projection.Terminal)
	}
	if projection.OrphanedCancellation {
		t.Fatal("an aborted live run must settle orphan-free")
	}
	outcomes, err := harness.backing.ReadAttemptOutcomes(string(runID))
	if err != nil || len(outcomes) != 1 {
		t.Fatalf("durable outcomes = %+v (err %v), want exactly one", outcomes, err)
	}
	if outcomes[0].Class != agentrun.OutcomeCancellation {
		t.Fatalf("durable outcome class = %q, want %q", outcomes[0].Class, agentrun.OutcomeCancellation)
	}
}
