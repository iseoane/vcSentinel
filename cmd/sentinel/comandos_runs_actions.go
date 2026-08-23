package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

func executeRunsStart(out io.Writer, worktree string, args []string) int {
	return runStartCommand(out, worktree, args, "start")
}

func runStartCommand(out io.Writer, worktree string, args []string, subcommand string) int {
	options, err := parseRunOptions(subcommand, args)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitUsage
	}
	if strings.TrimSpace(options.prompt) == "" {
		fmt.Fprintln(out, "❌ Usage: sentinel runs start --prompt <text> [--policy-id <id>]")
		return runExitUsage
	}
	controller, err := buildRunsController(worktree)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitCode(err)
	}
	host := execution.NewInProcessHost(controller)
	principal, err := resolveRunsPrincipal()
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitCode(err)
	}
	candidate := fmt.Sprintf("operator:%s:%d-%d-%06d",
		options.policyID, time.Now().UnixNano(), os.Getpid(), runsStartSequence.Add(1))
	request := agentrun.NewRunRequest(agentrun.Candidate(candidate), agentrun.Prompt(options.prompt), nil)
	handle, err := host.Start(context.Background(),
		execution.StartRequest{
			Request:     request,
			Policy:      store.RunPolicy{ID: options.policyID},
			AuthContext: execution.AuthContext{Principal: principal},
		})
	if err != nil {
		fmt.Fprintf(out, "❌ The run could not be admitted: %v\n", err)
		return runExitCode(err)
	}
	projection, err := observeUntilSettled(context.Background(), controller, handle.RunID)
	if err != nil {
		fmt.Fprintf(out, "❌ Run %s never reached a stable state: %v\n", handle.RunID, err)
		return runExitCode(err)
	}
	return printRunActionResult(out, options.jsonOut, handle, projection.State)
}

func executeRunsRespond(out io.Writer, worktree string, args []string) int {
	options, err := parseRunOptions("respond", args)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitUsage
	}
	if options.runID == "" || strings.TrimSpace(options.text) == "" {
		fmt.Fprintln(out, "❌ Usage: sentinel runs respond --run <id> --text <answer>")
		return runExitUsage
	}
	controller, err := buildRunsController(worktree)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitCode(err)
	}
	host := execution.NewInProcessHost(controller)
	principal, err := resolveRunsPrincipal()
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitCode(err)
	}
	actionID := fmt.Sprintf("action:%s:%d-%d-%06d",
		execution.ActionRespond, time.Now().UnixNano(), os.Getpid(), runsActionSequence.Add(1))
	result, applyErr := host.Apply(context.Background(), execution.ApplyRequest{
		RunID:       agentrun.Identity(options.runID),
		Action:      execution.ControlAction{Kind: execution.ActionRespond, Response: options.text},
		ActionID:    actionID,
		AuthContext: execution.AuthContext{Principal: principal},
	})
	if applyErr != nil {
		fmt.Fprintf(out, "❌ respond rejected: %v\n", applyErr)
		return runExitCode(applyErr)
	}
	projection, waitErr := observeUntilSettled(context.Background(), controller, agentrun.Identity(options.runID))
	if waitErr != nil {
		fmt.Fprintf(out, "❌ Run %s never reached a stable state after respond: %v\n", options.runID, waitErr)
		return runExitCode(waitErr)
	}
	return printApplyResult(out, options.jsonOut, result, &projection.State)
}

// idempotentHead reports the settled durable head when repeating an action
// whose goal already holds: abort on any settled run, retry or recover on a
// run that already has a live attempt. Respond is never idempotent because
// every response extends the decision lineage.
type idempotentHead struct {
	JobID        string
	InvocationID string
	State        agentrun.LifecycleState
}

func idempotentHeadOf(controller *execution.Controller, principal string, runID agentrun.Identity, err error) (idempotentHead, bool) {
	if !errors.Is(err, execution.ErrRunNotActive) {
		return idempotentHead{}, false
	}
	inspection, inspectErr := execution.NewInProcessHost(controller).Inspect(context.Background(), execution.InspectRequest{
		RunID:       runID,
		AuthContext: execution.AuthContext{Principal: principal},
	})
	if inspectErr != nil || len(inspection.Events) == 0 {
		return idempotentHead{}, false
	}
	state := inspection.Projection.State
	if state != agentrun.StateRunning && state.TerminalClass() == agentrun.TerminalNone {
		return idempotentHead{}, false
	}
	events := inspection.Events
	return idempotentHead{
		JobID: events[0].JobID, InvocationID: events[len(events)-1].InvocationID, State: state,
	}, true
}

func executeRunsAbort(out io.Writer, worktree string, args []string) int {
	options, err := parseRunOptions("abort", args)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitUsage
	}
	if options.runID == "" {
		fmt.Fprintln(out, "❌ Usage: sentinel runs abort --run <id>")
		return runExitUsage
	}
	controller, err := buildRunsController(worktree)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitCode(err)
	}
	host := execution.NewInProcessHost(controller)
	principal, err := resolveRunsPrincipal()
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitCode(err)
	}
	actionID := fmt.Sprintf("action:%s:%d-%d-%06d",
		execution.ActionAbort, time.Now().UnixNano(), os.Getpid(), runsActionSequence.Add(1))
	result, applyErr := host.Apply(context.Background(), execution.ApplyRequest{
		RunID:       agentrun.Identity(options.runID),
		Action:      execution.ControlAction{Kind: execution.ActionAbort},
		ActionID:    actionID,
		AuthContext: execution.AuthContext{Principal: principal},
	})
	if applyErr != nil {
		if head, ok := idempotentHeadOf(controller, principal, agentrun.Identity(options.runID), applyErr); ok {
			settled := execution.ApplyResult{
				RunID: agentrun.Identity(options.runID), InvocationID: agentrun.Identity(head.InvocationID),
				Accepted: true,
			}
			return printApplyResult(out, options.jsonOut, settled, &head.State)
		}
		fmt.Fprintf(out, "❌ abort rejected: %v\n", applyErr)
		return runExitCode(applyErr)
	}
	// Cooperative cancellation is not waited on: canceling only signals the
	// worker context, and the underlying adapter call may settle much later.
	return printApplyResult(out, options.jsonOut, result, nil)
}

func executeRunsRetry(out io.Writer, worktree string, args []string) int {
	options, err := parseRunOptions("retry", args)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitUsage
	}
	if options.runID == "" {
		fmt.Fprintln(out, "❌ Usage: sentinel runs retry --run <id> [--expected-revision N]")
		return runExitUsage
	}
	controller, err := buildRunsController(worktree)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitCode(err)
	}
	principal, err := resolveRunsPrincipal()
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitCode(err)
	}
	handle, retryErr := controller.Retry(context.Background(), agentrun.Identity(options.runID), options.expectedRevision)
	if retryErr != nil {
		if head, ok := idempotentHeadOf(controller, principal, agentrun.Identity(options.runID), retryErr); ok {
			return printRunActionResult(out, options.jsonOut, execution.Handle{
				RunID: agentrun.Identity(options.runID), JobID: agentrun.Identity(head.JobID),
				InvocationID: agentrun.Identity(head.InvocationID),
			}, head.State)
		}
		fmt.Fprintf(out, "❌ retry rejected: %v\n", retryErr)
		return runExitCode(retryErr)
	}
	projection, waitErr := observeUntilSettled(context.Background(), controller, handle.RunID)
	if waitErr != nil {
		fmt.Fprintf(out, "❌ Run %s never reached a stable state after retry: %v\n", handle.RunID, waitErr)
		return runExitCode(waitErr)
	}
	return printRunActionResult(out, options.jsonOut, handle, projection.State)
}

func executeRunsRecover(out io.Writer, worktree string, args []string) int {
	options, err := parseRunOptions("recover", args)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitUsage
	}
	// Ticket 10 slice 3 strictness: --expected-revision pins a competing
	// writer on the resume path only. The read-only scan pins nothing, and
	// --repair replays the whole verified stream under one lock, so both
	// reject the flag as a usage error instead of ignoring it silently.
	if options.expectedRevisionSet && options.repairSet {
		fmt.Fprintln(out, "❌ Usage: sentinel runs recover rejects --expected-revision with --repair; the rebuild replays the whole verified stream under one lock")
		return runExitUsage
	}
	if options.expectedRevisionSet && options.runID == "" {
		fmt.Fprintln(out, "❌ Usage: sentinel runs recover accepts --expected-revision only together with --run <id>; the read-only scan pins nothing")
		return runExitUsage
	}
	// Ticket 10 slice 2: --repair <id> is the bounded operator action that
	// rebuilds the lagging state snapshot of one classified
	// terminal-unprojected run from its verified stream. It never appends,
	// invents, or rewrites event bytes.
	if options.repairSet {
		if strings.TrimSpace(options.repairID) == "" {
			fmt.Fprintln(out, "❌ Usage: sentinel runs recover --repair <id>")
			return runExitUsage
		}
		if options.runID != "" {
			fmt.Fprintln(out, "❌ Usage: sentinel runs recover accepts either --repair <id> or --run <id>, not both")
			return runExitUsage
		}
		backing, _, buildErr := buildReadonlyController(worktree)
		if buildErr != nil {
			fmt.Fprintf(out, "❌ %v\n", buildErr)
			return runExitInfrastructure
		}
		return repairRunProjection(out, backing, strings.TrimSpace(options.repairID), options.jsonOut)
	}
	// Ticket 10 slice 1: without --run the command is an explicit READ-ONLY
	// scan that lists every non-terminal run with its evidence-based class.
	// It never writes and never resumes anything; recovery of one specific
	// run stays an operator action through Controller.Recover below.
	if options.runID == "" {
		backing, _, buildErr := buildReadonlyController(worktree)
		if buildErr != nil {
			fmt.Fprintf(out, "❌ %v\n", buildErr)
			return runExitInfrastructure
		}
		return listRecoveries(out, backing, options.jsonOut)
	}
	controller, err := buildRunsController(worktree)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitCode(err)
	}
	principal, err := resolveRunsPrincipal()
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitCode(err)
	}
	handle, recoverErr := controller.Recover(context.Background(), agentrun.Identity(options.runID), options.expectedRevision)
	if recoverErr != nil {
		if head, ok := idempotentHeadOf(controller, principal, agentrun.Identity(options.runID), recoverErr); ok {
			return printRunActionResult(out, options.jsonOut, execution.Handle{
				RunID: agentrun.Identity(options.runID), JobID: agentrun.Identity(head.JobID),
				InvocationID: agentrun.Identity(head.InvocationID),
			}, head.State)
		}
		fmt.Fprintf(out, "❌ recover rejected: %v\n", recoverErr)
		return runExitCode(recoverErr)
	}
	projection, waitErr := observeUntilSettled(context.Background(), controller, handle.RunID)
	if waitErr != nil {
		fmt.Fprintf(out, "❌ Run %s never reached a stable state after recover: %v\n", handle.RunID, waitErr)
		return runExitCode(waitErr)
	}
	return printRunActionResult(out, options.jsonOut, handle, projection.State)
}

// repairRunProjection renders the ticket 10 slice 2 repair action: rebuild
// the lagging state snapshot of one terminal-unprojected run. Exit codes
// follow the runs contract: 0 when repaired (or proven already byte-identical),
// 2 for an unknown run, 4 when the refusing class makes repair an invalid
// action right now (recoverable, orphaned-canceled, operator-required,
// settled), and 5 for corruption or any other infrastructure failure.
func repairRunProjection(out io.Writer, backing *store.Store, runID string, asJSON bool) int {
	result, err := store.RepairTerminalUnprojected(backing, runID)
	if err != nil {
		var refusal store.RecoveryNotRepairableError
		switch {
		case errors.As(err, &refusal):
			fmt.Fprintf(out, "❌ repair refused for %s (class %s): %s\n", refusal.RunID, refusal.Class, refusal.Reason)
			if refusal.Class == store.RecoveryCorrupt {
				return runExitInfrastructure
			}
			return runExitInvalidState
		case errors.Is(err, store.ErrExecutionNotFound):
			fmt.Fprintf(out, "❌ Run %s does not exist\n", runID)
			return runExitRunNotFound
		default:
			fmt.Fprintf(out, "❌ Could not repair %s: %v\n", runID, err)
			return runExitCode(err)
		}
	}
	output := runsRepairOutput{
		RunID: result.RunID, ClassBefore: string(result.ClassBefore),
		ClassAfter: string(result.ClassAfter), Rewritten: result.Rewritten,
	}
	if asJSON {
		if encodeErr := encodeStableJSON(out, output); encodeErr != nil {
			fmt.Fprintf(out, "❌ Could not serialize the repair result: %v\n", encodeErr)
			return runExitInfrastructure
		}
		return runExitSuccess
	}
	fmt.Fprintf(out, "✅ repaired projection for run %s: %s → %s\n",
		output.RunID, output.ClassBefore, output.ClassAfter)
	if output.Rewritten {
		fmt.Fprintln(out, "   state.json rebuilt from the verified stream; event bytes untouched")
	} else {
		fmt.Fprintln(out, "   replay already matched state.json byte for byte; nothing was rewritten")
	}
	return runExitSuccess
}

func executeRunsVerify(out io.Writer, worktree string, args []string) int {
	options, err := parseRunOptions("verify", args)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitUsage
	}
	if options.runID == "" {
		fmt.Fprintln(out, "❌ Usage: sentinel runs verify --run <id>")
		return runExitUsage
	}
	_, controller, err := buildReadonlyController(worktree)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitInfrastructure
	}
	principal, err := resolveRunsPrincipal()
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitInfrastructure
	}
	// A missing execution reports exit 2; every other read failure stays
	// inside Verify's integrity verdict so automation gets a concrete reason.
	_, inspectErr := execution.NewInProcessHost(controller).Inspect(context.Background(), execution.InspectRequest{
		RunID:       agentrun.Identity(options.runID),
		AuthContext: execution.AuthContext{Principal: principal},
	})
	if errors.Is(inspectErr, store.ErrExecutionNotFound) {
		fmt.Fprintf(out, "❌ Run %s does not exist\n", options.runID)
		return runExitRunNotFound
	}
	verification, verifyErr := controller.Verify(context.Background(), agentrun.Identity(options.runID))
	if verifyErr != nil {
		fmt.Fprintf(out, "❌ verify failed: %v\n", verifyErr)
		return runExitCode(verifyErr)
	}
	output := runsVerificationOutput{
		Valid: verification.Valid, Events: verification.Events, Reason: verification.Reason,
	}
	if options.jsonOut {
		if encodeErr := encodeStableJSON(out, output); encodeErr != nil {
			fmt.Fprintf(out, "❌ Could not serialize the verification: %v\n", encodeErr)
			return runExitInfrastructure
		}
	} else if output.Valid {
		fmt.Fprintf(out, "✅ Run %s is valid: %d intact events.\n", options.runID, output.Events)
	} else {
		fmt.Fprintf(out, "❌ Run %s is invalid: %s\n", options.runID, output.Reason)
	}
	if !output.Valid {
		// An invalid verdict is a corruption/integrity finding: it exits
		// through the documented infrastructure code.
		return runExitInfrastructure
	}
	return runExitSuccess
}
