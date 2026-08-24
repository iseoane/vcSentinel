package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"sync/atomic"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// `sentinel runs` exit codes and terminal-state mapping. Every subcommand
// obeys this contract and routes controller/store sentinels through
// runExitCode; the mapping lives next to the dispatch it constrains.
const (
	runExitSuccess        = 0 // success, including idempotent repeats and valid verify verdicts
	runExitUsage          = 1 // unknown subcommand/flag, missing or incomplete required flag
	runExitRunNotFound    = 2 // no durable execution record for --run
	runExitStaleRevision  = 3 // --expected-revision does not match the stream head
	runExitInvalidState   = 4 // action impossible for current lifecycle state
	runExitInfrastructure = 5 // store corruption, adapter failure, or any other infrastructure error
)

const (
	runsDefaultPolicyID  = "operator"
	runsLogsDefaultLimit = 100
	runsObserveInterval  = 50 * time.Millisecond
)

const runsUsage = `sentinel runs <subcommand> [flags]

Operator commands over durable runs.

Subcommands:
  start    --prompt <text> [--policy-id <id>] [--json]
  status   [--run <id>] [--json]
  logs     --run <id> [--after <cursor>] [--limit N] [--json]
  respond  --run <id> --text <response> [--json]
  abort    --run <id> [--json]
  retry    --run <id> [--expected-revision N] [--json]
  recover  --run <id> [--expected-revision N] [--json]
           --repair <id> rebuilds the lagging state snapshot of one run the
           classifier proved terminal-but-unprojected; every other class is
           refused with its reason
           without --run or --repair: list the read-only recovery scan of
           every non-terminal run with its evidence-based class; exits 4 when
           any entry requires an operator decision; --expected-revision is
           rejected on the scan and with --repair
  verify   --run <id> [--json]
  daemon   start|status|stop
           manage the repository-local foreground daemon (no flags accepted)
           start claims the repository, settles auto-recoverable interrupted
           runs, serves the local transport, and blocks until SIGINT/SIGTERM
           or a remote stop; exits 0 on a clean stop, 4 when another live
           daemon already owns the repository (its pid is named), 5 otherwise
           status prints the live owner pid/started-at/host/transport/address;
           exits 2 with a deterministic message when no live daemon is running
           for this repository (the runs not-found vocabulary: the addressed
           thing does not exist), and 5 when endpoint.json exists but is
           unreadable or incomplete — corruption is never silent
           stop asks the running daemon to shut down gracefully and prints its
           orphaned-runs summary; a missing or unreachable endpoint follows
           the same not-running contract as status (exit 2)
  prune    --older-than <duration> [--json]
           explicit operator maintenance: removes ONLY terminal execution
           records whose last event predates the cutoff and that no review
           provenance references; non-terminal, corrupt, orphaned-canceled,
           provenance-referenced, and parent-of-surviving records are kept
           with an explicit reason. Nothing purges automatically.

Exit codes:
  0 success (including idempotent repeats)   3 stale revision
  1 usage error                              4 invalid state for the action
  2 run not found                            5 infrastructure or corruption

Each subcommand accepts ONLY the flags listed above; any other flag is
rejected with a usage error (exit 1) instead of being ignored silently.

See docs/runs-cli.md for JSON shapes and terminal-state mapping.
`

// runsStartSequence keeps operator-started admission candidates unique within
// this process; the nanosecond timestamp and pid separate concurrent ones.
var runsStartSequence atomic.Uint64

// runsActionSequence keeps respond/abort control-action identities unique
// within this process, mirroring the admission candidate pattern: a kind
// prefix, nanosecond timestamp, pid, and process-local counter.
var runsActionSequence atomic.Uint64

// resolveRunsPrincipal resolves the local operating principal stamped into
// every repository-host envelope: USERNAME (Windows standard) first, then
// USER, then the os/user fallback. It fails only when no principal can be
// established at all.
func resolveRunsPrincipal() (string, error) {
	for _, key := range []string{"USERNAME", "USER"} {
		if principal := os.Getenv(key); principal != "" {
			return principal, nil
		}
	}
	current, err := user.Current()
	if err != nil || current.Username == "" {
		return "", errors.New("could not resolve the operating principal for this run request")
	}
	return current.Username, nil
}

// runOptions carries every flag value a `runs` subcommand may accept;
// irrelevant fields stay zero for each subcommand.
type runOptions struct {
	jsonOut  bool
	runID    string
	repairID string
	// repairSet records that the --repair flag was seen at all, so an empty
	// identity stays an explicit usage error instead of degrading into the
	// read-only scan.
	repairSet        bool
	text             string
	prompt           string
	policyID         string
	afterCursor      uint64
	expectedRevision uint64
	// expectedRevisionSet records that --expected-revision was seen at all,
	// so subcommands where the pin does not apply reject it explicitly
	// instead of letting value zero pass silently.
	expectedRevisionSet bool
	limit               int
	// olderThan records the raw --older-than value of `runs prune`;
	// olderThanSet keeps an empty value an explicit usage error instead of
	// degrading into a silent default.
	olderThan    string
	olderThanSet bool
}

// promptRunAdapter executes arbitrary operator prompts through the configured
// agent chain, bridging agentadapter to the execution.Adapter seam. The
// request payload travels inside the logical job; a continuation response is
// appended to the prompt text so respond lineage stays visible to the model.
type promptRunAdapter struct {
	delegate agentadapter.AdaptadorPrompt
}

// newAgentForRuns resolves the configured agent chain with the same strict
// project configuration the rest of the CLI uses. Tests swap this variable
// for deterministic doubles.
var newAgentForRuns = func(worktree string) (agentadapter.AdaptadorPrompt, error) {
	cfg, err := config.CargarConfiguracionLocalEstricta(worktree)
	if err != nil {
		return nil, err
	}
	perfil := config.ResolverPerfil(cfg, "", "")
	return agentadapter.NuevoAdaptadorConPerfil(cfg, perfil)
}

// runActionResult is the stable JSON shape of start/retry/recover results.
type runActionResult struct {
	RunID        string                  `json:"run_id"`
	JobID        string                  `json:"job_id"`
	InvocationID string                  `json:"invocation_id"`
	State        agentrun.LifecycleState `json:"state"`
}

// applyResultOutput is the stable JSON shape of respond/abort applications;
// State stays nil when the command returns without waiting for a transition.
type applyResultOutput struct {
	RunID        string                   `json:"run_id"`
	InvocationID string                   `json:"invocation_id"`
	Accepted     bool                     `json:"accepted"`
	State        *agentrun.LifecycleState `json:"state,omitempty"`
}

// runsVerificationOutput is the stable JSON shape of verify verdicts.
type runsVerificationOutput struct {
	Valid  bool   `json:"valid"`
	Events int    `json:"events"`
	Reason string `json:"reason"`
}

// runsListEntry is one row of `runs status` without --run. OrphanedCancellation
// appears only when restart reconciliation (ticket 08 slice 3) classified the
// run as canceled-orphaned; it is additive and omitted otherwise.
type runsListEntry struct {
	RunID                string                  `json:"run_id"`
	State                agentrun.LifecycleState `json:"state"`
	OutcomeClass         agentrun.TerminalClass  `json:"outcome_class"`
	Revision             uint64                  `json:"revision"`
	OrphanedCancellation bool                    `json:"orphaned_cancellation,omitempty"`
}

// runsStatusSummary is the stable JSON shape of `runs status --run <id>`.
// OrphanedCancellation marks the read-time reconciliation verdict of ticket 08
// slice 3 and stays absent for every honestly settled or still-recoverable run.
type runsStatusSummary struct {
	RunID      string                     `json:"run_id"`
	JobID      string                     `json:"job_id,omitempty"`
	State      agentrun.LifecycleState    `json:"state"`
	Sequence   uint64                     `json:"sequence"`
	Revision   uint64                     `json:"revision"`
	EventCount int                        `json:"event_count"`
	Outcomes   []store.AttemptOutcome     `json:"outcomes"`
	Responses  []store.InvocationResponse `json:"responses"`
	// OrphanedCancellation is additive (ticket 08 slice 3) and omitempty so
	// existing consumers keep parsing the shape unchanged.
	OrphanedCancellation bool `json:"orphaned_cancellation,omitempty"`
}

// runsLogsOutput is the stable JSON shape of `runs logs`; NextCursor is nil
// once the stream is exhausted.
type runsLogsOutput struct {
	RunID       string             `json:"run_id"`
	AfterCursor uint64             `json:"after_cursor"`
	Limit       int                `json:"limit"`
	Events      []store.EventFrame `json:"events"`
	HasMore     bool               `json:"has_more"`
	NextCursor  *uint64            `json:"next_cursor"`
}

// runsRecoveryRow is one row of the additive `runs recover` scan listing
// (ticket 10 slice 1). It mirrors store.RecoveryEntry for the CLI surface;
// Reconciled appears only when the verdict derives from the R7 restart
// reconciliation rather than the raw stream head.
type runsRecoveryRow struct {
	RunID        string `json:"run_id"`
	Class        string `json:"class"`
	Reason       string `json:"reason"`
	HeadSequence uint64 `json:"head_sequence"`
	Reconciled   bool   `json:"reconciled,omitempty"`
}

// runsRecoveryOutput is the stable JSON shape of `runs recover` without
// --run. Recoveries is empty when every run is settled or the store is new.
type runsRecoveryOutput struct {
	Recoveries []runsRecoveryRow `json:"recoveries"`
}

// runsRepairOutput is the stable JSON shape of `runs recover --repair`.
// Rewritten stays false when the replay already matched the persisted
// snapshot byte for byte (the deterministic no-op proof).
type runsRepairOutput struct {
	RunID       string `json:"run_id"`
	ClassBefore string `json:"class_before"`
	ClassAfter  string `json:"class_after"`
	Rewritten   bool   `json:"rewritten"`
}

func observeUntilSettled(ctx context.Context, c *execution.Controller, runID agentrun.Identity) (store.RunProjection, error) {
	for {
		inspection, err := c.Inspect(ctx, runID)
		if err != nil {
			return store.RunProjection{}, err
		}
		state := inspection.Projection.State
		if state == agentrun.StateAwaitingDecision || state.TerminalClass() != agentrun.TerminalNone {
			return inspection.Projection, nil
		}
		select {
		case <-ctx.Done():
			return store.RunProjection{}, ctx.Err()
		case <-time.After(runsObserveInterval):
		}
	}
}

func printRunActionResult(out io.Writer, asJSON bool, handle execution.Handle, state agentrun.LifecycleState) int {
	result := runActionResult{
		RunID: string(handle.RunID), JobID: string(handle.JobID),
		InvocationID: string(handle.InvocationID), State: state,
	}
	if asJSON {
		if err := encodeStableJSON(out, result); err != nil {
			fmt.Fprintf(out, "❌ Could not serialize the result: %v\n", err)
			return runExitInfrastructure
		}
		return runExitSuccess
	}
	fmt.Fprintf(out, "✅ run %s\n   job %s\n   invocation %s\n   state %s\n",
		result.RunID, result.JobID, result.InvocationID, result.State)
	return runExitSuccess
}

func printApplyResult(out io.Writer, asJSON bool, result execution.ApplyResult, state *agentrun.LifecycleState) int {
	output := applyResultOutput{
		RunID: string(result.RunID), InvocationID: string(result.InvocationID), Accepted: result.Accepted, State: state,
	}
	if asJSON {
		if err := encodeStableJSON(out, output); err != nil {
			fmt.Fprintf(out, "❌ Could not serialize the result: %v\n", err)
			return runExitInfrastructure
		}
		return runExitSuccess
	}
	fmt.Fprintf(out, "✅ run %s accepted=%t (invocation %s)", output.RunID, output.Accepted, output.InvocationID)
	if state != nil {
		fmt.Fprintf(out, "\n   resulting state %s", *state)
	}
	fmt.Fprintln(out)
	return runExitSuccess
}
