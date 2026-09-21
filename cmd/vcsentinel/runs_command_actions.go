package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vcSentinel/internal/execution"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
)

func executeRunsStart(out io.Writer, worktree string, args []string) int {
	return runStartCommand(out, worktree, args, "start")
}

// finalizeStableRunMetrics persists metrics only when the observed durable
// head is intrinsically final. Awaiting and retryable terminal heads are
// intentionally left unfrozen so response/retry/recover can still advance
// the same RunID.
func finalizeStableRunMetrics(controller *execution.Controller, projection store.RunProjection) error {
	if projection.State.TerminalClass() == agentrun.TerminalNone || projection.State.Retryable() {
		return nil
	}
	_, err := controller.FinalizeMetrics(context.Background(), agentrun.Identity(projection.RunID), nil)
	return err
}

func runStartCommand(out io.Writer, worktree string, args []string, subcommand string) int {
	options, err := parseRunOptions(subcommand, args)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitUsage
	}
	if strings.TrimSpace(options.prompt) == "" {
		fmt.Fprintln(out, "❌ "+runsStartUsage)
		return runExitUsage
	}
	controller, err := buildRunsController(worktree)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitCode(err)
	}
	host, closeRemote := runsHostWithDaemonPreference(worktree, controller)
	defer closeRemote()
	principal, err := resolveRunsPrincipal()
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitCode(err)
	}
	candidate := fmt.Sprintf("operator:%s:%d-%d-%06d",
		options.policyID, time.Now().UnixNano(), os.Getpid(), runsStartSequence.Add(1))
	capabilities, err := runsAdmissionCapabilities(worktree)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitCode(err)
	}
	// The admission travels as the explicit Candidate/Prompt form: it is the
	// transport-safe spelling both hosts resolve into the canonical
	// agentrun.RunRequest, so the same envelope works in-process and across
	// the daemon wire. Only a locally admitted run whose delegate declares an
	// enforcement backend promotes into the canonical form carrying the
	// agent.enforcement capability; relayed admissions keep the explicit form
	// because capabilities cannot cross the daemon wire yet.
	handle, err := host.Start(context.Background(),
		runsStartAdmission(candidate, options.prompt, options.policyID, principal,
			capabilities, runsRelayedAdmission(worktree)))
	if err != nil {
		fmt.Fprintf(out, "❌ The run could not be admitted: %v\n", err)
		return runExitCode(err)
	}
	projection, err := observeUntilSettled(context.Background(), controller, handle.RunID)
	if err != nil {
		fmt.Fprintf(out, "❌ Run %s never reached a stable state: %v\n", handle.RunID, err)
		return runExitCode(err)
	}
	if finalizeErr := finalizeStableRunMetrics(controller, projection); finalizeErr != nil {
		fmt.Fprintf(out, "❌ Could not finalize metrics for run %s: %v\n", handle.RunID, finalizeErr)
		return runExitCode(finalizeErr)
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
		fmt.Fprintln(out, "❌ "+runsRespondUsage)
		return runExitUsage
	}
	controller, err := buildRunsController(worktree)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitCode(err)
	}
	host, closeRemote := runsHostWithDaemonPreference(worktree, controller)
	defer closeRemote()
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
	if finalizeErr := finalizeStableRunMetrics(controller, projection); finalizeErr != nil {
		fmt.Fprintf(out, "❌ Could not finalize metrics for run %s: %v\n", options.runID, finalizeErr)
		return runExitCode(finalizeErr)
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

// runningSatisfies says whether a RUNNING head already satisfies the caller's
// goal. It does for retry and recover, whose goal is "there is a live attempt".
// It never does for abort: a running head is precisely what the abort failed to
// stop, so accepting it reported "✅ accepted=true / resulting state running"
// and exit 0 while writing nothing — telling an operator a run was canceled
// while it was still alive. Observed on three orphaned runs whose owner process
// was gone.
func idempotentHeadOf(host execution.RepositoryHost, principal string, runID agentrun.Identity, err error, runningSatisfies bool) (idempotentHead, bool) {
	if !errors.Is(err, execution.ErrRunNotActive) {
		return idempotentHead{}, false
	}
	inspection, inspectErr := host.Inspect(context.Background(), execution.InspectRequest{
		RunID:       runID,
		AuthContext: execution.AuthContext{Principal: principal},
	})
	if inspectErr != nil || len(inspection.Events) == 0 {
		return idempotentHead{}, false
	}
	state := inspection.Projection.State
	if state.TerminalClass() == agentrun.TerminalNone && !(runningSatisfies && state == agentrun.StateRunning) {
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
		fmt.Fprintln(out, "❌ "+runsAbortUsage)
		return runExitUsage
	}
	// Declarative flag validation, before any controller, host, principal or
	// action identity is built: the pairing rule holds for every run, not only
	// for the branch that ends up settling one. Inside settleOrphanedRun the
	// same check only fired after host.Apply answered ErrRunNotActive, so on a
	// LIVE run both --orphaned and a missing --reason were silently ignored.
	if options.orphaned && strings.TrimSpace(options.reason) == "" {
		fmt.Fprintln(out, "❌ --orphaned requires --reason \"...\": the CLI cannot prove the owner process is gone, so the durable settlement records whose assertion it rests on")
		return runExitUsage
	}
	if !options.orphaned && strings.TrimSpace(options.reason) != "" {
		fmt.Fprintln(out, "❌ --reason only applies with --orphaned: an ordinary abort records no operator assertion, so the words would be silently discarded")
		return runExitUsage
	}
	controller, err := buildRunsController(worktree)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitCode(err)
	}
	host, closeRemote := runsHostWithDaemonPreference(worktree, controller)
	defer closeRemote()
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
		if head, ok := idempotentHeadOf(host, principal, agentrun.Identity(options.runID), applyErr, false); ok {
			settled := execution.ApplyResult{
				RunID: agentrun.Identity(options.runID), InvocationID: agentrun.Identity(head.InvocationID),
				Accepted: true,
			}
			return printApplyResult(out, options.jsonOut, settled, &head.State)
		}
		// ErrRunNotActive proves only that the CONSULTED host holds no live
		// state for this run — the daemon when one is resolved, this process
		// otherwise. It does NOT prove the run is unowned: a separate
		// in-process controller can still be supervising it, which is exactly
		// why OrphanOwnedRuns exists alongside OrphanActiveRuns. No per-run
		// owner identity is recorded anywhere in the store, so that absence
		// cannot be verified from here at all. --orphaned therefore rests on
		// the operator's assertion and records it as such.
		if errors.Is(applyErr, execution.ErrRunNotActive) {
			if options.orphaned {
				return settleOrphanedRun(out, options, controller, agentrun.Identity(options.runID))
			}
			fmt.Fprintf(out, "❌ abort rejected: %v\n", applyErr)
			fmt.Fprintf(out, "   If you can confirm its owner process is gone, retire it with:\n")
			fmt.Fprintf(out, "   vcsentinel runs abort --run %s --orphaned --reason \"...\"\n", options.runID)
			return runExitCode(applyErr)
		}
		fmt.Fprintf(out, "❌ abort rejected: %v\n", applyErr)
		return runExitCode(applyErr)
	}
	// Cooperative cancellation is not waited on: canceling only signals the
	// worker context, and the underlying adapter call may settle much later.
	return printApplyResult(out, options.jsonOut, result, nil)
}

// orphanedAbortPrefix opens the recorded reason. It states the provenance of
// the settlement before the operator's own words: no outcome was ever observed,
// and no evidence proved the owner was gone, so a later reader can never take
// this frame for something the system watched happen.
const orphanedAbortPrefix = "operator orphaned this run through vcsentinel runs abort --orphaned; no outcome was ever observed and no owner-liveness evidence was available. Operator reason: "

// settleOrphanedRun records the operator decision the recovery classifier was
// waiting for, because the classifier is read-only by contract and never
// guesses an outcome.
//
// It uses the controller directly because the consulted host already answered
// ErrRunNotActive, so there is nothing live to route around THERE. That is not
// proof of absence: no per-run owner identity is recorded in the store, so a
// separate in-process controller could still be executing this run and its late
// result would then be dropped, exactly as a deliberate abort drops one. The
// mandatory reason is what keeps that trade visible in the durable record
// instead of implied; executeRunsAbort guarantees it is non-blank before here.
func settleOrphanedRun(out io.Writer, options runOptions, controller *execution.Controller, runID agentrun.Identity) int {
	settled, err := controller.OrphanRun(runID, orphanedAbortPrefix+options.reason)
	if err != nil {
		fmt.Fprintf(out, "❌ orphaned abort rejected: %v\n", err)
		return runExitCode(err)
	}
	if !settled {
		fmt.Fprintf(out, "❌ run %s was not orphaned: its durable head is already terminal or carries no evidence\n", runID)
		return runExitInvalidState
	}
	proyeccion, err := controller.Backing().ReadDerivedProjection(string(runID))
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitCode(err)
	}
	// Report the invocation the settlement was authored under, so the result
	// identifies the frame it wrote instead of rendering an empty identity.
	var invocation agentrun.Identity
	if page, err := controller.Backing().ReadEvents(string(runID), 0, 128); err == nil && len(page.Events) > 0 {
		invocation = agentrun.Identity(page.Events[len(page.Events)-1].InvocationID)
	}
	return printApplyResult(out, options.jsonOut, execution.ApplyResult{
		RunID: runID, InvocationID: invocation, Accepted: true,
	}, &proyeccion.State)
}

func executeRunsRetry(out io.Writer, worktree string, args []string) int {
	options, err := parseRunOptions("retry", args)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitUsage
	}
	if options.runID == "" {
		fmt.Fprintln(out, "❌ "+runsRetryUsage)
		return runExitUsage
	}
	controller, err := buildRunsController(worktree)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitCode(err)
	}
	// Retry routes through the daemon-preferred host end to end: the wire
	// port carries the retry op, so a running daemon owns its admission and
	// the direct-controller fallback only serves the no-daemon case.
	host, closeRemote := runsHostWithDaemonPreference(worktree, controller)
	defer closeRemote()
	principal, err := resolveRunsPrincipal()
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitCode(err)
	}
	handle, retryErr := host.Retry(context.Background(), execution.RetryRequest{
		RunID:            agentrun.Identity(options.runID),
		ExpectedRevision: options.expectedRevision,
		AuthContext:      execution.AuthContext{Principal: principal},
	})
	if retryErr != nil {
		if head, ok := idempotentHeadOf(host, principal, agentrun.Identity(options.runID), retryErr, true); ok {
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
	if finalizeErr := finalizeStableRunMetrics(controller, projection); finalizeErr != nil {
		fmt.Fprintf(out, "❌ Could not finalize metrics for run %s: %v\n", handle.RunID, finalizeErr)
		return runExitCode(finalizeErr)
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
		fmt.Fprintln(out, "❌ Usage: vcsentinel runs recover rejects --expected-revision with --repair; the rebuild replays the whole verified stream under one lock")
		return runExitUsage
	}
	if options.expectedRevisionSet && options.runID == "" {
		fmt.Fprintln(out, "❌ Usage: vcsentinel runs recover accepts --expected-revision only together with --run <id>; the read-only scan pins nothing")
		return runExitUsage
	}
	// Ticket 10 slice 2: --repair <id> is the bounded operator action that
	// rebuilds the lagging state snapshot of one classified
	// terminal-unprojected run from its verified stream. It never appends,
	// invents, or rewrites event bytes.
	if options.repairSet {
		if strings.TrimSpace(options.repairID) == "" {
			fmt.Fprintln(out, "❌ "+runsRecoverRepairUsage)
			return runExitUsage
		}
		if options.runID != "" {
			fmt.Fprintln(out, "❌ Usage: vcsentinel runs recover accepts either --repair <id> or --run <id>, not both")
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
	// Recover routes through the daemon-preferred host end to end: the wire
	// port carries the recover op, so a running daemon owns its admission
	// and the direct-controller fallback only serves the no-daemon case.
	host, closeRemote := runsHostWithDaemonPreference(worktree, controller)
	defer closeRemote()
	principal, err := resolveRunsPrincipal()
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitCode(err)
	}
	handle, recoverErr := host.Recover(context.Background(), execution.RecoverRequest{
		RunID:            agentrun.Identity(options.runID),
		ExpectedRevision: options.expectedRevision,
		AuthContext:      execution.AuthContext{Principal: principal},
	})
	if recoverErr != nil {
		if head, ok := idempotentHeadOf(host, principal, agentrun.Identity(options.runID), recoverErr, true); ok {
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
	if finalizeErr := finalizeStableRunMetrics(controller, projection); finalizeErr != nil {
		fmt.Fprintf(out, "❌ Could not finalize metrics for run %s: %v\n", handle.RunID, finalizeErr)
		return runExitCode(finalizeErr)
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
		fmt.Fprintln(out, "❌ "+runsVerifyUsage)
		return runExitUsage
	}
	_, controller, err := buildReadonlyController(worktree)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitInfrastructure
	}
	// The verify pre-check routes through the daemon-preferred host; the
	// integrity verdict itself stays a direct controller action because the
	// wire port does not carry verify yet.
	host, closeRemote := runsHostWithDaemonPreference(worktree, controller)
	defer closeRemote()
	principal, err := resolveRunsPrincipal()
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitInfrastructure
	}
	// A missing execution reports exit 2; every other read failure stays
	// inside Verify's integrity verdict so automation gets a concrete reason.
	_, inspectErr := host.Inspect(context.Background(), execution.InspectRequest{
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
