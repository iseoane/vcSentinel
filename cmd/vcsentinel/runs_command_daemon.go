package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/daemon"
	"github.com/ISeoane-Quental/vcSentinel/internal/execution"
	"github.com/ISeoane-Quental/vcSentinel/internal/git"
)

// daemonNotRunningMessage is the deterministic answer of `runs daemon status`
// and `runs daemon stop` when no live daemon is running for this repository.
// The exact text is part of the CLI contract: automation matches it verbatim.
const daemonNotRunningMessage = "📭 No daemon is running for this repository.\n"

// executeRunsDaemon routes `vcsentinel runs daemon <subcommand>`. The three
// subcommands take no flags by contract: any argument beyond the subcommand
// name is rejected as a usage error instead of being silently ignored, and an
// unknown subcommand prints the concrete rejection plus exits with the runs
// usage code.
func executeRunsDaemon(out io.Writer, worktree string, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(out, "❌ "+runsDaemonUsage)
		return runExitUsage
	}
	subcommand, flags := args[0], args[1:]
	if len(flags) > 0 {
		fmt.Fprintf(out, "❌ Unknown flag for 'vcsentinel runs daemon %s': %s\n", subcommand, flags[0])
		return runExitUsage
	}
	switch subcommand {
	case "start":
		return executeRunsDaemonStart(out, worktree)
	case "status":
		return executeRunsDaemonStatus(out, worktree)
	case "stop":
		return executeRunsDaemonStop(out, worktree)
	default:
		fmt.Fprintf(out, "❌ Unknown subcommand for 'vcsentinel runs daemon': %q\n", subcommand)
		return runExitUsage
	}
}

// executeRunsDaemonStart runs the foreground daemon lifecycle for this
// repository until it stops. It blocks: the ready line (transport, address,
// pid) is printed first, then the command returns only after a graceful stop,
// printing the stopped summary. Exit codes follow the runs contract with one
// documented addition: exit 4 reports that another live daemon already owns
// the repository — a lifecycle-state conflict for this action, exactly like
// every other invalid-state refusal — while any other failure is
// infrastructure (5).
func executeRunsDaemonStart(out io.Writer, worktree string) int {
	commonDir, err := git.GetGitCommonDir(worktree)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitInfrastructure
	}
	// The daemon serves the same controller construction every other runs
	// command uses (configured agent adapter over the shared backing store),
	// so a real foreground daemon executes wire starts instead of refusing
	// them with ErrControllerNotReady.
	controller, buildErr := buildRunsController(worktree)
	if buildErr != nil {
		fmt.Fprintf(out, "❌ %v\n", buildErr)
		return runExitCode(buildErr)
	}
	startErr := daemon.Run(commonDir, controller, daemon.DefaultGracePeriod, out)
	if startErr == nil {
		return runExitSuccess
	}
	var owned *daemon.OwnedError
	if errors.As(startErr, &owned) {
		fmt.Fprintf(out, "❌ %v\n", startErr)
		return runExitInvalidState
	}
	fmt.Fprintf(out, "❌ %v\n", startErr)
	return runExitInfrastructure
}

// executeRunsDaemonStatus reports the live daemon owner. The endpoint record
// plus the owner claim must both agree on a live pid; anything else is
// deterministically not-running. Exit codes: 0 live, 2 no live daemon
// (reusing the runs not-found vocabulary: the addressed thing does not exist),
// 5 when endpoint.json exists but is unreadable or incomplete — corruption is
// never silently downgraded to "not running".
func executeRunsDaemonStatus(out io.Writer, worktree string) int {
	commonDir, err := git.GetGitCommonDir(worktree)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitInfrastructure
	}
	endpoint, owner, loadErr := daemon.LoadEndpoint(daemon.Dir(commonDir))
	if errors.Is(loadErr, os.ErrNotExist) {
		fmt.Fprint(out, daemonNotRunningMessage)
		return runExitRunNotFound
	}
	if loadErr != nil {
		fmt.Fprintf(out, "❌ Cannot read the daemon endpoint record: %v\n", loadErr)
		return runExitInfrastructure
	}
	_, alive, inspectErr := daemon.InspectOwner(commonDir)
	if inspectErr != nil || !alive {
		// A recorded endpoint whose owner pid is provably gone (or whose
		// claim cannot be judged) is stale residue; the next start reclaims
		// it deterministically, so status honestly reports no live daemon.
		fmt.Fprint(out, daemonNotRunningMessage)
		return runExitRunNotFound
	}
	fmt.Fprintf(out, "🔎 Daemon for this repository\n   pid %d\n   started %s\n   host %s\n   transport %s://%s\n",
		owner.PID, owner.StartedAt.UTC().Format(time.RFC3339), owner.Host, endpoint.Network, endpoint.Address)
	return runExitSuccess
}

// executeRunsDaemonStop asks the running daemon to shut down gracefully over
// the authenticated wire op and prints its orphaned-runs summary. A missing
// or unreachable endpoint follows the same not-running contract as status
// (exit 2): the stop goal already holds when nothing answers, mirroring the
// idempotent semantics of abort on a settled run. A corrupt record still
// exits 5 explicitly, and a daemon-side shutdown failure exits 5.
func executeRunsDaemonStop(out io.Writer, worktree string) int {
	commonDir, err := git.GetGitCommonDir(worktree)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitInfrastructure
	}
	endpoint, _, loadErr := daemon.LoadEndpoint(daemon.Dir(commonDir))
	if errors.Is(loadErr, os.ErrNotExist) {
		fmt.Fprint(out, daemonNotRunningMessage)
		return runExitRunNotFound
	}
	if loadErr != nil {
		fmt.Fprintf(out, "❌ Cannot read the daemon endpoint record: %v\n", loadErr)
		return runExitInfrastructure
	}
	principal, principalErr := resolveRunsPrincipal()
	if principalErr != nil {
		fmt.Fprintf(out, "❌ %v\n", principalErr)
		return runExitInfrastructure
	}
	host, dialErr := daemon.DialRemoteHost(endpoint, daemon.FingerprintRepository(commonDir))
	if dialErr != nil {
		// Nobody answers the recorded endpoint: stale residue left by a dead
		// owner, so the not-running contract applies.
		fmt.Fprint(out, daemonNotRunningMessage)
		return runExitRunNotFound
	}
	defer func() { _ = host.Close() }()
	result, shutdownErr := host.Shutdown(context.Background(), daemon.ShutdownRequest{
		AuthContext: execution.AuthContext{Principal: principal},
	})
	if shutdownErr != nil {
		fmt.Fprintf(out, "❌ Could not stop the daemon: %v\n", shutdownErr)
		return runExitInfrastructure
	}
	fmt.Fprintf(out, "🛑 daemon stopped (orphaned runs: %d)\n", result.OrphanedRuns)
	return runExitSuccess
}
