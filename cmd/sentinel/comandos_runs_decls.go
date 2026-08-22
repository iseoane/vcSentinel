package main

import (
	"context"
	"fmt"
	"io"
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
  verify   --run <id> [--json]

Exit codes:
  0 success (including idempotent repeats)   3 stale revision
  1 usage error                              4 invalid state for the action
  2 run not found                            5 infrastructure or corruption

See docs/runs-cli.md for JSON shapes and terminal-state mapping.
`

// runsStartSequence keeps operator-started admission candidates unique within
// this process; the nanosecond timestamp and pid separate concurrent ones.
var runsStartSequence atomic.Uint64

// runOptions carries every flag value a `runs` subcommand may accept;
// irrelevant fields stay zero for each subcommand.
type runOptions struct {
	jsonOut          bool
	runID            string
	text             string
	prompt           string
	policyID         string
	afterCursor      uint64
	expectedRevision uint64
	limit            int
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
