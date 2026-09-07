package main

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/daemon"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// `runs daemon` CLI contract (ticket 14 slice 3b): status/stop report a
// deterministic not-running answer with exit 2 when nothing lives for this
// repository, surface live owner fields verbatim, and stop drives the real
// graceful wire op against a serving daemon.

// startLiveTestDaemon claims and serves one daemon over the repository's
// platform default transport without going through daemon.Run, so the CLI
// surfaces are exercised against a live endpoint with full test cleanup.
func startLiveTestDaemon(t *testing.T, worktree string) (commonDir string, serveErr <-chan error, owner daemon.Owner) {
	t.Helper()
	commonDir, err := git.GetGitCommonDir(worktree)
	if err != nil {
		t.Fatal(err)
	}
	owner, err = daemon.Claim(commonDir)
	if err != nil {
		t.Fatal(err)
	}
	dir := daemon.Dir(commonDir)
	endpoint := daemon.DefaultEndpoint(dir)
	listener, err := daemon.Listen(endpoint)
	if err != nil {
		_ = daemon.Release(commonDir)
		if endpoint.Network == "unix" {
			t.Skipf("unix domain sockets unavailable: %v", err)
		}
		t.Fatalf("listen %s endpoint: %v", endpoint.Network, err)
	}
	endpoint.Address = listener.Addr().String()
	if err := daemon.SaveEndpoint(dir, endpoint, owner); err != nil {
		t.Fatal(err)
	}
	controller := execution.NewController(store.NewStore(commonDir), nil)
	server := daemon.NewServer(controller, daemon.FingerprintRepository(commonDir))
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(server.Close)
	t.Cleanup(func() { _ = listener.Close() })
	t.Cleanup(func() { _ = daemon.Release(commonDir) })
	// Mirrors daemon's fixed record name for best-effort teardown only.
	t.Cleanup(func() { _ = os.Remove(filepath.Join(dir, "endpoint.json")) })
	return commonDir, done, owner
}

func TestRunsDaemonStatusAndStopWithoutDaemon(t *testing.T) {
	worktree := t.TempDir()
	initGitRepo(t, worktree)

	for _, subcommand := range []string{"status", "stop"} {
		output, code := captureRunsOutput(t, func(w io.Writer) int {
			return executeRunsDaemon(w, worktree, []string{subcommand})
		})
		if code != runExitRunNotFound {
			t.Fatalf("daemon %s exit = %d, want %d (not-running): %s", subcommand, code, runExitRunNotFound, output)
		}
		if output != daemonNotRunningMessage {
			t.Fatalf("daemon %s output = %q, want the deterministic not-running message %q", subcommand, output, daemonNotRunningMessage)
		}
	}
}

func TestRunsDaemonUsageErrors(t *testing.T) {
	worktree := t.TempDir()
	tests := [][]string{
		nil,                  // bare dispatch
		{"teleport"},         // unknown subcommand
		{"status", "--json"}, // flags are rejected by contract
	}
	for _, args := range tests {
		output, code := captureRunsOutput(t, func(w io.Writer) int {
			return executeRunsDaemon(w, worktree, args)
		})
		if code != runExitUsage {
			t.Fatalf("executeRunsDaemon(%v) exit = %d, want usage 1, output:\n%s", args, code, output)
		}
		if !strings.Contains(output, "❌") {
			t.Fatalf("usage rejection printed nothing actionable: %q", output)
		}
	}
}

func TestRunsDaemonStatusAgainstLiveServer(t *testing.T) {
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	commonDir, _, owner := startLiveTestDaemon(t, worktree)

	output, code := captureRunsOutput(t, func(w io.Writer) int {
		return executeRunsDaemonStatus(w, worktree)
	})
	if code != runExitSuccess {
		t.Fatalf("status exit = %d, want success, output:\n%s", code, output)
	}
	dir := daemon.Dir(commonDir)
	endpoint, loadedOwner, loadErr := daemon.LoadEndpoint(dir)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	for _, want := range []string{
		"pid " + strconv.Itoa(owner.PID),
		"host ",
		"transport " + endpoint.Network + "://" + endpoint.Address,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("status output missing %q:\n%s", want, output)
		}
	}
	if loadedOwner.PID != owner.PID {
		t.Fatalf("endpoint owner pid = %d, want %d", loadedOwner.PID, owner.PID)
	}
}

func TestRunsDaemonStopStopsLiveServer(t *testing.T) {
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	commonDir, serveErr, _ := startLiveTestDaemon(t, worktree)
	_ = commonDir // the CLI stop path owns discovery; only Serve's exit matters here

	output, code := captureRunsOutput(t, func(w io.Writer) int {
		return executeRunsDaemonStop(w, worktree)
	})
	if code != runExitSuccess {
		t.Fatalf("stop exit = %d, want success, output:\n%s", code, output)
	}
	if !strings.Contains(output, "🛑 daemon stopped (orphaned runs: 0)") {
		t.Fatalf("stop output lacked the orphaned summary:\n%s", output)
	}

	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("Serve returned %v after CLI stop", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("served daemon never stopped after CLI stop")
	}

	// The stop goal now holds: repeating it follows the not-running contract.
	repeatOut, repeatCode := captureRunsOutput(t, func(w io.Writer) int {
		return executeRunsDaemonStop(w, worktree)
	})
	if repeatCode != runExitRunNotFound || repeatOut != daemonNotRunningMessage {
		t.Fatalf("repeat stop = (%d, %q), want the not-running contract", repeatCode, repeatOut)
	}
}

func TestRunsDaemonStatusCorruptEndpointIsExplicitError(t *testing.T) {
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	commonDir, err := git.GetGitCommonDir(worktree)
	if err != nil {
		t.Fatal(err)
	}
	dir := daemon.Dir(commonDir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "endpoint.json"), []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}

	output, code := captureRunsOutput(t, func(w io.Writer) int {
		return executeRunsDaemonStatus(w, worktree)
	})
	if code != runExitInfrastructure {
		t.Fatalf("corrupt-record status exit = %d, want infrastructure 5, output:\n%s", code, output)
	}
	if !strings.Contains(output, "❌") || !strings.Contains(output, "endpoint record") {
		t.Fatalf("corrupt record was not reported explicitly:\n%s", output)
	}
}
