package main

// `sentinel tui` (control-center slice 9): opens the Bubble Tea control
// center over the repository registry snapshot. The command owns the daemon
// lifecycle of the CURRENT repository for the session: when no live daemon
// answers, it spawns `<selfexe> runs daemon start` detached, waits a bounded
// window for the discovery record to appear, and marks the daemon as owned;
// a dialable record means a foreign daemon that must survive the session
// untouched. When the program exits, ONLY an owned daemon receives the
// graceful wire shutdown — exactly the `runs daemon stop` sequence — under a
// bounded context; any stop failure prints one error line while the command
// still succeeds, because the session itself already succeeded.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/bubbletea"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/daemon"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/overview"
	"github.com/ISeoane-Quental/vas.sentinel/internal/tui/control"
)

const (
	// tuiDaemonReadyBudget bounds how long the TUI waits for a freshly
	// spawned daemon to publish its endpoint record before declaring the
	// startup failed.
	tuiDaemonReadyBudget = 5 * time.Second

	// tuiDaemonReadyPoll is the readiness poll cadence over the loader.
	tuiDaemonReadyPoll = 50 * time.Millisecond

	// tuiStopMargin is the headroom added on top of the daemon grace period
	// for the bounded wire shutdown issued when an owned session ends.
	tuiStopMargin = 5 * time.Second
)

// tuiDaemonHost is the slice of the daemon wire client the TUI stop path
// needs. *daemon.RemoteHost satisfies it; tests substitute recorders.
type tuiDaemonHost interface {
	Shutdown(ctx context.Context, request daemon.ShutdownRequest) (daemon.ShutdownResult, error)
	Close() error
}

// tuiDaemonGate bundles every external effect of the ownership decision and
// the session-end shutdown behind injectable closures, so the whole matrix
// is unit-testable without real daemons or filesystems. The zero value is
// not usable; build it through newTuiDaemonGate in production or with fakes
// in tests.
type tuiDaemonGate struct {
	// loadEndpoint reads the repository's daemon discovery record. It wraps
	// os.ErrNotExist when no record exists; any other error means an
	// unreadable or corrupt residue. Both shapes are reclaimable states.
	loadEndpoint func() (daemon.Endpoint, error)
	// dial connects to a loaded endpoint with the repository fingerprint
	// handshake. A success proves a live (foreign) daemon is serving.
	dial func(endpoint daemon.Endpoint) (tuiDaemonHost, error)
	// spawn launches `<selfexe> runs daemon start` detached.
	spawn func() error
	// waitReady reports whether the freshly spawned daemon published its
	// endpoint record within its startup budget.
	waitReady func() bool
}

// resolveOwnership applies the ownership decision matrix:
//
//   - record absent (os.ErrNotExist) or unreadable/corrupt: stale-reclaimable
//     state, spawn and own;
//   - record loads but nobody answers the dial: stale residue from a dead
//     owner, whose claim is reclaimed deterministically by the next start,
//     so spawn and own;
//   - record loads and dials: a foreign daemon is live — never touched here.
//
// A spawn failure or readiness timeout returns an error so the caller exits
// infrastructure BEFORE opening the TUI: the session must never open
// half-owned. The dialable path closes the probed connection immediately:
// the probe exists only to classify ownership, never to hold the wire.
func (g tuiDaemonGate) resolveOwnership() (owned bool, err error) {
	endpoint, loadErr := g.loadEndpoint()
	if loadErr == nil {
		host, dialErr := g.dial(endpoint)
		if dialErr == nil {
			_ = host.Close()
			return false, nil
		}
		// Nobody answers the recorded endpoint: stale residue left by a dead
		// owner; fall through to the reclaiming spawn below.
	}
	if err := g.spawn(); err != nil {
		return false, fmt.Errorf("could not start the repository daemon: %w", err)
	}
	if !g.waitReady() {
		return false, errors.New("the repository daemon did not become reachable before its startup budget expired")
	}
	return true, nil
}

// waitTuiDaemonReady polls load until it reports nil once, giving up after
// budget has elapsed; interval spaces the attempts. Timeout is failure. The
// loop is wall-clock bounded regardless of cadence, so callers may tighten
// both durations freely in tests without any fake clock.
func waitTuiDaemonReady(load func() error, budget, interval time.Duration) bool {
	deadline := time.Now().Add(budget)
	for {
		if load() == nil {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(interval)
	}
}

// spawnDetachedTuiDaemon starts `<selfexe> runs daemon start` detached from
// this session: stdio discarded, platform detach attributes applied through
// detachSysProcAttr (see the build-tagged helpers). The child is reaped on a
// throwaway goroutine — waiting synchronously would tie this process to the
// daemon's whole lifetime, and never waiting would leave a zombie behind if
// the daemon dies mid-session.
func spawnDetachedTuiDaemon() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not resolve the sentinel binary: %w", err)
	}
	cmd := exec.Command(exe, "runs", "daemon", "start")
	cmd.SysProcAttr = detachSysProcAttr()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not launch 'runs daemon start': %w", err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// newTuiDaemonGate binds the gate to one repository's real daemon effects.
func newTuiDaemonGate(commonDir string) tuiDaemonGate {
	dir := daemon.Dir(commonDir)
	return tuiDaemonGate{
		loadEndpoint: func() (daemon.Endpoint, error) {
			endpoint, _, err := daemon.LoadEndpoint(dir)
			return endpoint, err
		},
		dial: func(endpoint daemon.Endpoint) (tuiDaemonHost, error) {
			return daemon.DialRemoteHost(endpoint, daemon.FingerprintRepository(commonDir))
		},
		spawn: spawnDetachedTuiDaemon,
		waitReady: func() bool {
			return waitTuiDaemonReady(func() error {
				_, _, err := daemon.LoadEndpoint(dir)
				return err
			}, tuiDaemonReadyBudget, tuiDaemonReadyPoll)
		},
	}
}

// stopOwnedTuiDaemon shuts down the daemon this session owns, mirroring
// `runs daemon stop`: read the recorded endpoint, dial with the repository
// fingerprint, request the graceful wire op under grace+margin, close. Any
// failure prints ONE error line; the caller keeps exit success either way,
// because the control-center session itself already succeeded. A missing
// record follows the idempotent not-running contract: the owned daemon is
// already gone, which is the desired end state, reported quietly. Foreign
// daemons can never reach this path — owned=false never calls it.
func stopOwnedTuiDaemon(out io.Writer, gate tuiDaemonGate) {
	endpoint, err := gate.loadEndpoint()
	switch {
	case errors.Is(err, os.ErrNotExist):
		return
	case err != nil:
		fmt.Fprintf(out, "❌ Could not read the daemon endpoint record: %v\n", err)
		return
	}
	principal, err := resolveRunsPrincipal()
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return
	}
	host, err := gate.dial(endpoint)
	if err != nil {
		fmt.Fprintf(out, "❌ Could not reach the owned daemon: %v\n", err)
		return
	}
	defer func() { _ = host.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), daemon.DefaultGracePeriod+tuiStopMargin)
	defer cancel()
	if _, err := host.Shutdown(ctx, daemon.ShutdownRequest{
		AuthContext: execution.AuthContext{Principal: principal},
	}); err != nil {
		fmt.Fprintf(out, "❌ Could not stop the daemon: %v\n", err)
	}
}

// tuiSessionController and tuiSessionHost mirror the runs CLI plumbing behind
// var seams so session-action unit tests can substitute fake controllers and
// fake repository hosts instead of real daemons, stores, and configured
// agents. Production code never reassigns them.
var (
	tuiSessionController = buildRunsController
	tuiSessionHost       = runsHostWithDaemonPreference
)

// sessionRunActions wires the control center's a/r keys onto the same
// durable-runs plumbing the `sentinel runs` CLI uses, restricted to the
// SESSION repository: each dispatch resolves the principal, builds the
// controller, and dials the daemon-preferred host exactly like
// executeRunsAbort and executeRunsRetry, sends Apply with a fresh unique
// ActionID or Retry WITHOUT the CLI's settle wait (the 2-second refresh loop
// observes the transition instead), applies the idempotent-head fallback on
// rejection, and reports the outcome through the control package's
// action-result channel. Actions against any other repository's path fail
// fast into an immediate error command without touching hosts or principals.
type sessionRunActions struct {
	// worktree is the session repository root; foreign paths never reach a
	// host.
	worktree string
}

// Compile-time proof that the session implementation satisfies the TUI seam.
var _ control.RunActions = sessionRunActions{}

// newSessionRunActions binds the operator actions to one session worktree,
// resolving it to an absolute root once so later registry-path comparisons
// stay stable regardless of the process working directory.
func newSessionRunActions(worktree string) sessionRunActions {
	root, err := filepath.Abs(worktree)
	if err != nil {
		root = filepath.Clean(worktree)
	}
	return sessionRunActions{worktree: root}
}

// Abort mirrors executeRunsAbort minus printing and waiting: unique ActionID,
// authenticated envelope, idempotent-head fallback on rejection. Cooperative
// cancellation is not waited on, exactly like the CLI: canceling only signals
// the worker context and may settle much later.
func (a sessionRunActions) Abort(repoPath, runID string) tea.Cmd {
	return a.dispatch(control.KindAbort, repoPath, runID,
		func(ctx context.Context, host execution.RepositoryHost, principal string) error {
			actionID := fmt.Sprintf("action:%s:%d-%d-%06d",
				execution.ActionAbort, time.Now().UnixNano(), os.Getpid(), runsActionSequence.Add(1))
			_, applyErr := host.Apply(ctx, execution.ApplyRequest{
				RunID:       agentrun.Identity(runID),
				Action:      execution.ControlAction{Kind: execution.ActionAbort},
				ActionID:    actionID,
				AuthContext: execution.AuthContext{Principal: principal},
			})
			if applyErr == nil {
				return nil
			}
			if _, ok := idempotentHeadOf(host, principal, agentrun.Identity(runID), applyErr); ok {
				return nil // the settled idempotent head proves the goal holds
			}
			return applyErr
		})
}

// Retry mirrors executeRunsRetry minus printing AND minus the settle wait:
// the control center's refresh loop shows the resulting state transition, so
// no observeUntilSettled polling happens here. ExpectedRevision stays zero —
// the same skip-the-pin default the CLI uses when --expected-revision is
// absent.
func (a sessionRunActions) Retry(repoPath, runID string) tea.Cmd {
	return a.dispatch(control.KindRetry, repoPath, runID,
		func(ctx context.Context, host execution.RepositoryHost, principal string) error {
			_, retryErr := host.Retry(ctx, execution.RetryRequest{
				RunID:            agentrun.Identity(runID),
				ExpectedRevision: 0,
				AuthContext:      execution.AuthContext{Principal: principal},
			})
			if retryErr == nil {
				return nil
			}
			if _, ok := idempotentHeadOf(host, principal, agentrun.Identity(runID), retryErr); ok {
				return nil // the run already carries a live attempt
			}
			return retryErr
		})
}

// dispatch gates the action to the session repository, performs op against a
// freshly resolved daemon-preferred host inside the command goroutine (the
// host teardown is deferred there too), and translates the outcome into the
// control package's action-result command. A foreign repository path
// short-circuits into an immediate failing command before anything is
// resolved.
func (a sessionRunActions) dispatch(kind, repoPath, runID string,
	op func(context.Context, execution.RepositoryHost, string) error) tea.Cmd {
	if !sameSessionRepository(a.worktree, repoPath) {
		return control.ActionResult(kind, repoPath, runID,
			errors.New("actions are limited to the session repository"))
	}
	return func() tea.Msg {
		err := func() error {
			controller, buildErr := tuiSessionController(a.worktree)
			if buildErr != nil {
				return buildErr
			}
			host, closeHost := tuiSessionHost(a.worktree, controller)
			defer closeHost() // runs inside the command goroutine, never in Update
			principal, principalErr := resolveRunsPrincipal()
			if principalErr != nil {
				return principalErr
			}
			return op(context.Background(), host, principal)
		}()
		return control.ActionResult(kind, repoPath, runID, err)()
	}
}

// sameSessionRepository reports whether repoPath addresses the session worktree.
// Both sides are cleaned; Windows compares case-insensitively per its file-
// system convention, every other platform compares byte for byte.
func sameSessionRepository(worktree, repoPath string) bool {
	if strings.TrimSpace(repoPath) == "" {
		return false
	}
	left, right := filepath.Clean(worktree), filepath.Clean(repoPath)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

// startControlCenter launches the Bubble Tea control-center program with the
// alt screen and reports whether the session ended cleanly. Like its attach
// sibling, it is a var-indirected seam: driving a real terminal headlessly
// is not possible, so tests stub this single point. The final model result
// is deliberately ignored: the model carries no outcome state worth reading.
var startControlCenter = func(model control.Model) error {
	program := tea.NewProgram(model, tea.WithAltScreen())
	_, err := program.Run()
	return err
}

// executeTuiSession runs one full control-center session over prepared
// inputs: collect the initial snapshot, settle daemon ownership, open the
// program, then stop the owned daemon (if any) once Run returns. The a/r
// keys bind to the session worktree through sessionRunActions. Splitting
// it from executeTui keeps the snapshot/ownership wiring injectable for the
// shutdown-on-exit tests without touching real daemons.
func executeTuiSession(out io.Writer, registryPath, worktree string, gate tuiDaemonGate) int {
	initial, err := overview.Collect(registryPath)
	if err != nil {
		fmt.Fprintf(out, "❌ Could not load the repository overview: %v\n", err)
		return runExitInfrastructure
	}
	owned, err := gate.resolveOwnership()
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitInfrastructure
	}
	model := control.NewLive(initial, func() ([]overview.Repo, error) {
		return overview.Collect(registryPath)
	}, control.DefaultRefreshInterval, newSessionRunActions(worktree))
	runErr := startControlCenter(model)
	if owned {
		stopOwnedTuiDaemon(out, gate)
	}
	if runErr != nil {
		fmt.Fprintf(out, "❌ The control-center session failed: %v\n", runErr)
		return runExitInfrastructure
	}
	return runExitSuccess
}

// executeTui dispatches `sentinel tui`. Registry-open failure exits
// infrastructure before anything is spawned; per-repository degradation
// stays inside the snapshot contract. The refresh closure re-collects over
// the same registry every tick.
func executeTui(out io.Writer, worktree string) int {
	registryPath, err := resolveRepositoryRegistryPath()
	if err != nil {
		fmt.Fprintf(out, "❌ Could not resolve the repository registry path: %v\n", err)
		return runExitInfrastructure
	}
	commonDir, err := git.ObtenerGitCommonDir(worktree)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitInfrastructure
	}
	return executeTuiSession(out, registryPath, worktree, newTuiDaemonGate(commonDir))
}
