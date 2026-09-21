package daemon

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vcSentinel/internal/execution"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
)

// The lifecycle runner (ticket 14 slice 3b) is proven end to end against one
// temp git common dir per test: Run executes in a goroutine, readiness is
// observed through endpoint.json plus a real authenticated dial, work flows
// over the wire, and stop arrives through RemoteHost.Shutdown so every
// process-side cleanup guarantee is asserted on real exits.

// startForegroundDaemonWithAdapter launches Run in a goroutine with a
// controller built exactly the way production builds one — execution.NewController
// over store.NewStore(commonDir) with the given adapter — and returns once
// the endpoint answers a real handshake. The returned channel carries Run's
// exit. There is no package-level construction seam anymore: production and
// tests share the single Run(gitCommonDir, controller, grace, out) signature.
func startForegroundDaemonWithAdapter(t *testing.T, commonDir string, out *bytes.Buffer, adapter execution.Adapter) (*RemoteHost, <-chan error) {
	t.Helper()
	done := make(chan error, 1)
	controller := execution.NewController(store.NewStore(commonDir), adapter)
	go func() { done <- Run(commonDir, controller, DefaultGracePeriod, out) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case err := <-done:
			t.Fatalf("daemon exited before becoming ready: %v\n%s", err, out.String())
		default:
		}
		endpoint, _, loadErr := LoadEndpoint(Dir(commonDir))
		if loadErr == nil {
			host, dialErr := DialRemoteHost(endpoint, FingerprintRepository(commonDir))
			if dialErr == nil {
				return host, done
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon never became ready")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// startForegroundDaemon launches Run with an immediately-completing adapter,
// the default for lifecycle-focused tests.
func startForegroundDaemon(t *testing.T, commonDir string, out *bytes.Buffer) (*RemoteHost, <-chan error) {
	t.Helper()
	return startForegroundDaemonWithAdapter(t, commonDir, out, immediateAdapter("boot-test output"))
}

// stopForegroundDaemon drives the wire stop path, asserts the orphaned-runs
// summary, and waits for Run to return nil.
func stopForegroundDaemon(t *testing.T, host *RemoteHost, done <-chan error, wantOrphaned int) {
	t.Helper()
	result, err := host.Shutdown(context.Background(), ShutdownRequest{AuthContext: testAuth()})
	if err != nil {
		t.Fatalf("remote shutdown failed: %v", err)
	}
	if result.OrphanedRuns != wantOrphaned {
		t.Fatalf("shutdown result = %d orphaned runs, want %d", result.OrphanedRuns, wantOrphaned)
	}
	select {
	case runErr := <-done:
		if runErr != nil {
			t.Fatalf("Run returned %v after graceful wire stop", runErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run never returned after graceful wire stop")
	}
}

// assertDaemonResidueGone pins the cleanup contract: endpoint.json is removed
// and the ownership claim is free again (a fresh Claim succeeds).
func assertDaemonResidueGone(t *testing.T, commonDir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(Dir(commonDir), endpointFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("endpoint.json survived the daemon exit (stat error = %v)", err)
	}
	if _, claimErr := Claim(commonDir); claimErr != nil {
		t.Fatalf("Claim after daemon stop failed; ownership was not released: %v", claimErr)
	}
	t.Cleanup(func() { _ = Release(commonDir) })
}

func TestRunFullLifecycleThroughWireStop(t *testing.T) {
	commonDir := t.TempDir()
	var out bytes.Buffer

	host, done := startForegroundDaemon(t, commonDir, &out)

	// Start and settle one run through the real transport before stopping.
	handle, err := host.Start(context.Background(), execution.StartRequest{
		Candidate:   "candidate:lifecycle",
		Prompt:      "prompt:lifecycle",
		Policy:      store.RunPolicy{ID: "policy:lifecycle"},
		AuthContext: testAuth(),
	})
	if err != nil {
		t.Fatalf("wire start failed: %v", err)
	}
	var inspection execution.Inspection
	deadline := time.Now().Add(5 * time.Second)
	for settled := false; !settled; {
		var inspectErr error
		inspection, inspectErr = host.Inspect(context.Background(), execution.InspectRequest{RunID: handle.RunID, AuthContext: testAuth()})
		if inspectErr == nil && inspection.Projection.Terminal != agentrun.TerminalNone {
			settled = true
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("wire-started run never settled: last inspection %+v (err %v)", inspection, inspectErr)
		}
		time.Sleep(pollInterval)
	}

	stopForegroundDaemon(t, host, done, 0)

	text := out.String()
	if !strings.Contains(text, "daemon ready:") || !strings.Contains(text, "pid") {
		t.Fatalf("ready line missing from daemon output:\n%s", text)
	}
	if !strings.Contains(text, "🛑 daemon stopped (orphaned runs: 0)") {
		t.Fatalf("stopped summary missing or wrong from daemon output:\n%s", text)
	}
	assertDaemonResidueGone(t, commonDir)
}

func TestRunRefusesSecondStarterWithOwnedError(t *testing.T) {
	commonDir := t.TempDir()
	var out bytes.Buffer

	host, done := startForegroundDaemon(t, commonDir, &out)

	// The rival starter loses at Claim before its controller matters; any
	// non-nil controller satisfies the Run signature.
	rivalController := execution.NewController(store.NewStore(commonDir), immediateAdapter("unused"))
	raceErr := Run(commonDir, rivalController, DefaultGracePeriod, &bytes.Buffer{})
	var owned *OwnedError
	if !errors.As(raceErr, &owned) {
		t.Fatalf("second Run error = %v, want errors.As(*OwnedError)", raceErr)
	}
	if owned.Owner.PID != os.Getpid() {
		t.Fatalf("owned pid = %d, want the live owner %d", owned.Owner.PID, os.Getpid())
	}
	if !errors.Is(raceErr, ErrDaemonOwned) || !strings.Contains(raceErr.Error(), "pid") {
		t.Fatalf("second starter error lost its friendly owner naming: %v", raceErr)
	}

	// The loser must not have disturbed the winner's state.
	stopForegroundDaemon(t, host, done, 0)
	assertDaemonResidueGone(t, commonDir)
}

func TestRunReconcilesBeforeServing(t *testing.T) {
	root := t.TempDir()
	backing := store.NewStore(root)
	runID := appendBootFixtureStream(t, backing, "candidate:pre-serve-reconcile", bootSuccessExtra())
	snapshot := snapshotPathFor(root, runID)
	if err := os.Remove(snapshot); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	host, done := startForegroundDaemon(t, root, &out)

	text := out.String()
	if !strings.Contains(text, "settled run "+runID) {
		t.Fatalf("daemon did not reconcile before serving:\n%s", text)
	}
	if _, err := os.Stat(snapshot); err != nil {
		t.Fatalf("reconciliation did not restore the snapshot before serving: %v", err)
	}

	stopForegroundDaemon(t, host, done, 0)
	assertDaemonResidueGone(t, root)
}
