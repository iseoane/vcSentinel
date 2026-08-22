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

func executeRunsStart(salida io.Writer, worktree string, args []string) int {
	return runStartCommand(salida, worktree, args, "start")
}

func runStartCommand(salida io.Writer, worktree string, args []string, subcommand string) int {
	options, err := parseRunOptions(subcommand, args)
	if err != nil {
		fmt.Fprintf(salida, "❌ %v\n", err)
		return runExitUsage
	}
	if strings.TrimSpace(options.prompt) == "" {
		fmt.Fprintln(salida, "❌ Usage: sentinel runs start --prompt <text> [--policy-id <id>]")
		return runExitUsage
	}
	controller, err := buildRunsController(worktree)
	if err != nil {
		fmt.Fprintf(salida, "❌ %v\n", err)
		return runExitCode(err)
	}
	candidate := fmt.Sprintf("operator:%s:%d-%d-%06d",
		options.policyID, time.Now().UnixNano(), os.Getpid(), runsStartSequence.Add(1))
	request := agentrun.NewRunRequest(agentrun.Candidate(candidate), agentrun.Prompt(options.prompt), nil)
	handle, err := controller.Start(context.Background(), request, store.RunPolicy{ID: options.policyID})
	if err != nil {
		fmt.Fprintf(salida, "❌ The run could not be admitted: %v\n", err)
		return runExitCode(err)
	}
	projection, err := observeUntilSettled(context.Background(), controller, handle.RunID)
	if err != nil {
		fmt.Fprintf(salida, "❌ Run %s never reached a stable state: %v\n", handle.RunID, err)
		return runExitCode(err)
	}
	return printRunActionResult(salida, options.jsonOut, handle, projection.State)
}

func executeRunsRespond(salida io.Writer, worktree string, args []string) int {
	options, err := parseRunOptions("respond", args)
	if err != nil {
		fmt.Fprintf(salida, "❌ %v\n", err)
		return runExitUsage
	}
	if options.runID == "" || strings.TrimSpace(options.text) == "" {
		fmt.Fprintln(salida, "❌ Usage: sentinel runs respond --run <id> --text <answer>")
		return runExitUsage
	}
	controller, err := buildRunsController(worktree)
	if err != nil {
		fmt.Fprintf(salida, "❌ %v\n", err)
		return runExitCode(err)
	}
	result, applyErr := controller.Apply(context.Background(), agentrun.Identity(options.runID),
		execution.ControlAction{Kind: execution.ActionRespond, Response: options.text})
	if applyErr != nil {
		fmt.Fprintf(salida, "❌ respond rejected: %v\n", applyErr)
		return runExitCode(applyErr)
	}
	projection, waitErr := observeUntilSettled(context.Background(), controller, agentrun.Identity(options.runID))
	if waitErr != nil {
		fmt.Fprintf(salida, "❌ Run %s never reached a stable state after respond: %v\n", options.runID, waitErr)
		return runExitCode(waitErr)
	}
	return printApplyResult(salida, options.jsonOut, result, &projection.State)
}

func executeRunsAbort(salida io.Writer, worktree string, args []string) int {
	options, err := parseRunOptions("abort", args)
	if err != nil {
		fmt.Fprintf(salida, "❌ %v\n", err)
		return runExitUsage
	}
	if options.runID == "" {
		fmt.Fprintln(salida, "❌ Usage: sentinel runs abort --run <id>")
		return runExitUsage
	}
	controller, err := buildRunsController(worktree)
	if err != nil {
		fmt.Fprintf(salida, "❌ %v\n", err)
		return runExitCode(err)
	}
	result, applyErr := controller.Apply(context.Background(), agentrun.Identity(options.runID),
		execution.ControlAction{Kind: execution.ActionAbort})
	if applyErr != nil {
		fmt.Fprintf(salida, "❌ abort rejected: %v\n", applyErr)
		return runExitCode(applyErr)
	}
	// Cooperative cancellation is not waited on: canceling only signals the
	// worker context, and the underlying adapter call may settle much later.
	return printApplyResult(salida, options.jsonOut, result, nil)
}

func executeRunsRetry(salida io.Writer, worktree string, args []string) int {
	options, err := parseRunOptions("retry", args)
	if err != nil {
		fmt.Fprintf(salida, "❌ %v\n", err)
		return runExitUsage
	}
	if options.runID == "" {
		fmt.Fprintln(salida, "❌ Usage: sentinel runs retry --run <id> [--expected-revision N]")
		return runExitUsage
	}
	controller, err := buildRunsController(worktree)
	if err != nil {
		fmt.Fprintf(salida, "❌ %v\n", err)
		return runExitCode(err)
	}
	handle, retryErr := controller.Retry(context.Background(), agentrun.Identity(options.runID), options.expectedRevision)
	if retryErr != nil {
		fmt.Fprintf(salida, "❌ retry rejected: %v\n", retryErr)
		return runExitCode(retryErr)
	}
	projection, waitErr := observeUntilSettled(context.Background(), controller, handle.RunID)
	if waitErr != nil {
		fmt.Fprintf(salida, "❌ Run %s never reached a stable state after retry: %v\n", handle.RunID, waitErr)
		return runExitCode(waitErr)
	}
	return printRunActionResult(salida, options.jsonOut, handle, projection.State)
}

func executeRunsRecover(salida io.Writer, worktree string, args []string) int {
	options, err := parseRunOptions("recover", args)
	if err != nil {
		fmt.Fprintf(salida, "❌ %v\n", err)
		return runExitUsage
	}
	if options.runID == "" {
		fmt.Fprintln(salida, "❌ Usage: sentinel runs recover --run <id> [--expected-revision N]")
		return runExitUsage
	}
	controller, err := buildRunsController(worktree)
	if err != nil {
		fmt.Fprintf(salida, "❌ %v\n", err)
		return runExitCode(err)
	}
	handle, recoverErr := controller.Recover(context.Background(), agentrun.Identity(options.runID), options.expectedRevision)
	if recoverErr != nil {
		fmt.Fprintf(salida, "❌ recover rejected: %v\n", recoverErr)
		return runExitCode(recoverErr)
	}
	projection, waitErr := observeUntilSettled(context.Background(), controller, handle.RunID)
	if waitErr != nil {
		fmt.Fprintf(salida, "❌ Run %s never reached a stable state after recover: %v\n", handle.RunID, waitErr)
		return runExitCode(waitErr)
	}
	return printRunActionResult(salida, options.jsonOut, handle, projection.State)
}

func executeRunsVerify(salida io.Writer, worktree string, args []string) int {
	options, err := parseRunOptions("verify", args)
	if err != nil {
		fmt.Fprintf(salida, "❌ %v\n", err)
		return runExitUsage
	}
	if options.runID == "" {
		fmt.Fprintln(salida, "❌ Usage: sentinel runs verify --run <id>")
		return runExitUsage
	}
	_, controller, err := buildReadonlyController(worktree)
	if err != nil {
		fmt.Fprintf(salida, "❌ %v\n", err)
		return runExitInfrastructure
	}
	// A missing execution reports exit 2; every other read failure stays
	// inside Verify's integrity verdict so automation gets a concrete reason.
	if _, inspectErr := controller.Inspect(context.Background(), agentrun.Identity(options.runID)); errors.Is(inspectErr, store.ErrExecutionNotFound) {
		fmt.Fprintf(salida, "❌ Run %s does not exist\n", options.runID)
		return runExitRunNotFound
	}
	verification, verifyErr := controller.Verify(context.Background(), agentrun.Identity(options.runID))
	if verifyErr != nil {
		fmt.Fprintf(salida, "❌ verify failed: %v\n", verifyErr)
		return runExitCode(verifyErr)
	}
	output := runsVerificationOutput{
		Valid: verification.Valid, Events: verification.Events, Reason: verification.Reason,
	}
	if options.jsonOut {
		if encodeErr := encodeStableJSON(salida, output); encodeErr != nil {
			fmt.Fprintf(salida, "❌ Could not serialize the verification: %v\n", encodeErr)
			return runExitInfrastructure
		}
	} else if output.Valid {
		fmt.Fprintf(salida, "✅ Run %s is valid: %d intact events.\n", options.runID, output.Events)
	} else {
		fmt.Fprintf(salida, "❌ Run %s is invalid: %s\n", options.runID, output.Reason)
	}
	if !output.Valid {
		// An invalid verdict is a corruption/integrity finding: it exits
		// through the documented infrastructure code.
		return runExitInfrastructure
	}
	return runExitSuccess
}
