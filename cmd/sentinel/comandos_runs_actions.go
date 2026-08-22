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
	candidate := fmt.Sprintf("operator:%s:%d-%d-%06d",
		options.policyID, time.Now().UnixNano(), os.Getpid(), runsStartSequence.Add(1))
	request := agentrun.NewRunRequest(agentrun.Candidate(candidate), agentrun.Prompt(options.prompt), nil)
	handle, err := controller.Start(context.Background(), request, store.RunPolicy{ID: options.policyID})
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
	result, applyErr := controller.Apply(context.Background(), agentrun.Identity(options.runID),
		execution.ControlAction{Kind: execution.ActionRespond, Response: options.text})
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

func idempotentHeadOf(controller *execution.Controller, runID agentrun.Identity, err error) (idempotentHead, bool) {
	if !errors.Is(err, execution.ErrRunNotActive) {
		return idempotentHead{}, false
	}
	inspection, inspectErr := controller.Inspect(context.Background(), runID)
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
	result, applyErr := controller.Apply(context.Background(), agentrun.Identity(options.runID),
		execution.ControlAction{Kind: execution.ActionAbort})
	if applyErr != nil {
		if head, ok := idempotentHeadOf(controller, agentrun.Identity(options.runID), applyErr); ok {
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
	handle, retryErr := controller.Retry(context.Background(), agentrun.Identity(options.runID), options.expectedRevision)
	if retryErr != nil {
		if head, ok := idempotentHeadOf(controller, agentrun.Identity(options.runID), retryErr); ok {
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
	if options.runID == "" {
		fmt.Fprintln(out, "❌ Usage: sentinel runs recover --run <id> [--expected-revision N]")
		return runExitUsage
	}
	controller, err := buildRunsController(worktree)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitCode(err)
	}
	handle, recoverErr := controller.Recover(context.Background(), agentrun.Identity(options.runID), options.expectedRevision)
	if recoverErr != nil {
		if head, ok := idempotentHeadOf(controller, agentrun.Identity(options.runID), recoverErr); ok {
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
	// A missing execution reports exit 2; every other read failure stays
	// inside Verify's integrity verdict so automation gets a concrete reason.
	if _, inspectErr := controller.Inspect(context.Background(), agentrun.Identity(options.runID)); errors.Is(inspectErr, store.ErrExecutionNotFound) {
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
