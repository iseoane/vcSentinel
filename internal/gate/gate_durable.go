// Real durable orchestration for the gate command (R9 slice 2): RunGate
// routes through ONE root durable run whose phases mirror the historical
// fixed validation-before-review ordering.
//
// Ticket 13 (R11): with the gate.durable_runs switch removed, this flow IS
// RunGate — the non-durable legacy orchestration was deleted and there
// is exactly one execution path.
//
// This file owns the ORCHESTRATION FLOW: plan construction and validation,
// root-run admission, the two phases (validation then review), root
// settlement mapping and divergence checking, and the per-job validation
// settlement loop. The execution adapters and their small supporting helpers
// live in gate_durable_adapters.go.
//
// Layer-separation rules pinned by this wiring:
//
//   - The root run is admitted BEFORE any phase executes and settles LAST, so
//     its terminal state always names the failing LAYER distinctly. Existing
//     agentrun vocabulary covers every layer without an additive state:
//     validation findings and semantic blockers settle the root failed
//     (OutcomeFailure → StateFailed) while plan/store/controller/transport
//     failures settle it unavailable (OutcomeUnavailable → StateUnavailable);
//     which layer failed travels verbatim in the settled error text
//     ("gate: failing layer: validation|review|infrastructure").
//   - Every child job run admitted by this orchestrator carries a PERSISTED
//     parent linkage (RunPolicy.ParentRunID → request.json parent_run_id),
//     and the root's terminal settlement detail enumerates all settled child
//     job run IDs ("|children=<id,id,...>"), so post-settlement
//     reconstruction from the root record alone is possible.
//   - Validation commands execute DIRECTLY through the exact injected seam
//     validation.RunProfileOnCandidate (or the test substitute) — no
//     agent anywhere on this path. Each validation logical job is then
//     settled deterministically per exit status through controller APIs only,
//     carrying its RecordValidationEvidence serialization as the adapter
//     output so the evidence digest is hash-bound to that job's durable
//     AttemptOutcome — INCLUDING failed commands, whose AttemptOutcome keeps
//     both class=failure AND the non-empty evidence OutputHash.
//   - If ANY validation job fails, the review transport factory is NEVER
//     invoked: review does not start.
//   - The review phase reuses the historical tail verbatim —
//     review.AuditCommit plus translateVerdict — with reviewer invocations
//     routed through the injected DurableReviewTransportFactory, i.e. the
//     same construction path `sentinel review` wires today. Review runs are
//     constructed at that different site, so the gate hands its root run ID
//     to the factory for production wiring to thread the parent linkage;
//     there is no second execution path.
package gate

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

// DurableGateRunPolicyID identifies every gate-side durable run admitted by
// this wiring: one root run per gate execution plus one deterministic
// settlement run per validation logical job under it.
const DurableGateRunPolicyID = "policy:gate"

// gateRootOperation labels the gate root run for activity surfaces: the
// lowercase lifecycle stage the gate is running under, which is the only
// context this admission site honestly holds. An empty stage degrades to the
// plain "gate" minimum instead of a dangling space.
func gateRootOperation(stage string) string {
	if stage == "" {
		return "gate"
	}
	return "gate " + stage
}

// gateRootOperationAttempt makes a climbed attempt visible to every operator
// surface that reads the run policy. A gate that quietly re-admitted under a new
// identity would hide a repeating infrastructure failure behind a growing pile
// of run trees.
func gateRootOperationAttempt(stage string, attempt int) string {
	if attempt == 0 {
		return gateRootOperation(stage)
	}
	return gateRootOperation(stage) + " (attempt " + strconv.Itoa(attempt+1) + ")"
}

// RunGate executes one gate run durably — the only execution path since
// ticket 13 (R11). It always builds and validates the GateRunPlan first so
// plan-construction bugs surface before anything is admitted, then admits ONE
// root run, runs the validation phase, and only on a fully green validation
// starts the review phase through the shared durable transport path.
func RunGate(opts Options) Result {
	plan, err := buildDurableGatePlan(opts)
	if err != nil {
		return Result{
			State: StateInfrastructureError,
			// A plan that cannot be built is infrastructure, not a code
			// finding: same classification rule as legacy validation
			// orchestration failures.
			Messages: []string{fmt.Sprintf("gate durable run plan failed before wiring: %v", err)},
			Err:      err,
		}
	}
	if opts.DurableStore == nil {
		return infraResult(errors.New("gate: durable runs require an injected store"))
	}

	settle := make(chan rootSettlement, 1)
	controller := execution.NewController(opts.DurableStore, rootRunAdapter{settle: settle})
	plan, root, err := admitGateRoot(controller, opts, plan)
	if err != nil {
		return infraResult(err)
	}

	// Panic guard: a panic inside any phase must never leave the root worker
	// blocked forever on a settlement that will never be sent. The deferred
	// send delivers an honest unavailable settlement during unwinding; on
	// every normal path the flag makes it a no-op.
	settled := false
	defer func() {
		if !settled {
			settle <- rootSettlement{class: agentrun.OutcomeUnavailable, detail: layerInfrastructureDetail}
		}
	}()

	result, settlement := runDurableGatePhases(plan, opts, root.RunID)
	settle <- settlement
	settled = true

	completion, waitErr := root.Wait(context.Background())
	if waitErr != nil {
		return infraResult(fmt.Errorf("gate: durable execution failed to settle: %w", waitErr))
	}
	if divergence := settlementDivergence(completion, settlement); divergence != nil {
		return infraResult(divergence)
	}
	return result
}

// maxAttemptsGate bounds the admission probe. Each climb means one previously
// settled gate execution over the same candidate, so the real count is tiny; the
// bound only stops a pathological store from spinning forever.
// It is a var, not a const, so the exhaustion branch can be exercised with a
// low bound instead of 64 real gate executions.
var maxAttemptsGate = 64

// admitGateRoot admits the root run of a gate execution, climbing attempts
// when the candidate was already gated before.
//
// Every identity of the plan derives deterministically from (stage, profile,
// candidate SHA, commands), and Controller.Start refuses an identity that
// already carries lifecycle events. Without this probe a candidate got exactly
// ONE gate execution ever: a second run — after a green gate, or after one
// aborted by an infrastructure failure — could never be admitted, leaving the
// guardian unable to re-certify a commit.
//
// The refusal itself is right and stays: reusing a spent run identity would
// append a new lifecycle onto a settled stream. What changes is that a NEW gate
// execution is what it says it is, a new run, and takes the next attempt.
//
// EVERY terminal class may climb, a failed verdict included — not only the green
// and infrastructure cases that exposed the defect. The gate is a check, not a
// ledger of verdicts: re-running it over an unchanged candidate recomputes the
// same deterministic commands and reaches the same conclusion, and refusing to
// re-check after a failure would recreate this very defect in the case where
// re-checking matters most, right after fixing whatever the environment broke.
// The verdict authority lives in the review ledger, not in the admission probe.
//
// A pre-existing run that is NOT terminal is a different situation entirely:
// another gate is executing this candidate right now, and refusing is correct.
func admitGateRoot(controller *execution.Controller, opts Options, plan GateRunPlan) (GateRunPlan, execution.Handle, error) {
	for attempt := plan.Attempt; attempt < maxAttemptsGate; attempt++ {
		if attempt != plan.Attempt {
			next, err := BuildDurableGatePlanAttempt(opts, attempt)
			if err != nil {
				return GateRunPlan{}, execution.Handle{}, fmt.Errorf("gate: durable run plan failed for attempt %d: %w", attempt, err)
			}
			plan = next
		}
		root, err := controller.Start(context.Background(), plan.Root.Request(), store.RunPolicy{
			ID:        DurableGateRunPolicyID,
			Operation: gateRootOperationAttempt(opts.Stage, attempt),
			Commit:    shortCommitLabel(opts.CandidateSHA),
			Worktree:  opts.ValidationOptions.Worktree,
		})
		if err == nil {
			return plan, root, nil
		}
		if !errors.Is(err, execution.ErrRunAlreadyExists) {
			return GateRunPlan{}, execution.Handle{}, fmt.Errorf("gate: durable root run not admitted: %w", err)
		}
		inspection, inspectErr := controller.Inspect(context.Background(), plan.Root.RunID())
		if inspectErr != nil {
			return GateRunPlan{}, execution.Handle{}, fmt.Errorf("gate: durable root run not admitted and its existing record is unreadable: %w", inspectErr)
		}
		if inspection.Projection.Terminal == agentrun.TerminalNone {
			return GateRunPlan{}, execution.Handle{}, fmt.Errorf("gate: another gate execution is already running this candidate (run %s, state %s): wait for it to settle instead of starting a second one", plan.Root.RunID(), inspection.Projection.State)
		}
	}
	return GateRunPlan{}, execution.Handle{}, fmt.Errorf("gate: this candidate already has %d settled gate executions; refusing to open another", maxAttemptsGate)
}

// buildDurableGatePlan derives the deterministic command list for the
// resolved profile and validates the full plan construction path without
// executing anything.
func buildDurableGatePlan(opts Options) (GateRunPlan, error) {
	return BuildDurableGatePlanAttempt(opts, 0)
}

// buildDurableGatePlanAttempt is buildDurableGatePlan for a specific execution
// attempt. Attempt 0 is what every caller wants; the admission probe in
// RunGate is the only place that climbs.
func BuildDurableGatePlanAttempt(opts Options, attempt int) (GateRunPlan, error) {
	commands, err := durableGateCommands(opts.ValidationOptions.Cfg, opts.Profile)
	if err != nil {
		return GateRunPlan{}, err
	}
	return BuildGateRunPlanAttempt(opts.Stage, opts.Profile, opts.CandidateSHA, commands, attempt)
}

// infraResult classifies a durable-infrastructure failure: never a code
// finding, always StateInfrastructureError with the typed Err set for
// callers that need it (the CLI facade reads only State/Messages).
func infraResult(err error) Result {
	return Result{
		State:    StateInfrastructureError,
		Messages: []string{err.Error()},
		Err:      err,
	}
}

// runDurableGatePhases runs the two gate phases against an already-admitted
// root run and returns the facade result plus the root settlement that names
// the failing layer. Every return path pairs a Result with exactly one
// settlement so the blocked root adapter can always finish. Settlements with
// a layer detail also enumerate every settled child job run ID so the root
// record alone supports post-settlement reconstruction.
func runDurableGatePhases(plan GateRunPlan, opts Options, rootRunID agentrun.Identity) (Result, rootSettlement) {
	runValidation := opts.RunValidation
	if runValidation == nil {
		runValidation = validation.RunProfileOnCandidate
	}

	runs, err := runValidation(opts.Profile, opts.ChangedPaths, opts.ValidationOptions)
	if err != nil {
		// A failure ORCHESTRATING the validation is infrastructure, not a
		// code finding: never invent a VALIDATION_FAILED for
		// something that never executed.
		return Result{
			State:    StateInfrastructureError,
			Messages: []string{validationNotRunMessage(err)},
			Err:      err,
		}, rootSettlement{class: agentrun.OutcomeUnavailable, detail: layerInfrastructureDetail}
	}

	evidence := RecordValidationEvidence(runs)
	findings := validation.Findings(runs, opts.ValidationOptions.Cfg.Validation.Capabilities)
	children, err := settleValidationJobs(plan.ValidationJobs(), runs, evidence, opts, rootRunID)
	if err != nil {
		return Result{
			State:    StateInfrastructureError,
			Messages: []string{fmt.Sprintf("Could not record the durable validation: %v", err)},
			Err:      err,
		}, rootSettlement{class: agentrun.OutcomeUnavailable, detail: withChildren(layerInfrastructureDetail, children)}
	}

	if len(findings) > 0 {
		return Result{State: StateValidationFailed, Messages: validationFailedMessages(findings)},
			rootSettlement{class: agentrun.OutcomeFailure, detail: withChildren(layerValidationDetail, children)}
	}

	// Piece 3 of docs/design/review-flow-ownership.md: the gate no longer
	// audits. "Compiles and passes its checks" is a property of the tree at one
	// moment; "is this piece well made" is a property of one commit, and the two
	// cannot share an owner. `sentinel review` owns the second and is the only
	// writer of per-commit verdicts, so a green validation IS the whole gate
	// verdict now, and reaching this point is that verdict.
	return Result{
			State:    StatePass,
			Messages: []string{"✅ Validation green.", ValidationCoverageNotice},
		},
		rootSettlement{class: agentrun.OutcomeSuccess}
}

// settleValidationJobs settles every validation logical job through the
// execution controller, deterministically per exit status, each bound to its
// RecordValidationEvidence serialization. Every admitted job run carries the
// root run's ID as its persisted ParentRunID. Commands were already executed
// directly; this only records honest lifecycle outcomes, so any store or
// controller failure here is infrastructure. It returns the admitted child
// run IDs in profile order (a prefix of it when a settlement fails midway).
func settleValidationJobs(jobs []GateJobPlan, runs []validation.ValidationRun, evidence []ValidationEvidence, opts Options, parentRunID agentrun.Identity) ([]agentrun.Identity, error) {
	layerbilities := opts.ValidationOptions.Cfg.Validation.Capabilities
	// A delegated profile yields narrative runs without planned validation
	// jobs; clamp keeps the mapping positional and never invents settlements.
	limit := len(jobs)
	if len(runs) < limit {
		limit = len(runs)
	}
	children := make([]agentrun.Identity, 0, limit)
	for index := 0; index < limit; index++ {
		run := runs[index]
		class := agentrun.OutcomeSuccess
		detail := "gate: validation command passed"
		if validation.Failed(run, layerbilities[run.Capability]) {
			class = agentrun.OutcomeFailure
			detail = "gate: validation command failed"
		}
		job := executedValidationJob(jobs[index], run)
		adapter := settledValidationAdapter{
			class:  class,
			detail: detail,
			output: evidenceOutput(index, evidence),
		}
		controller := execution.NewController(opts.DurableStore, adapter)
		// The exact command is what identifies this child job at admission
		// time (the plan pins it in deterministic profile order), so the
		// label carries it verbatim after the "validate" layer verb; long
		// commands are a rendering-truncation concern, not a data concern.
		handle, err := controller.Start(context.Background(), job.Request(), store.RunPolicy{
			ID:          DurableGateRunPolicyID,
			ParentRunID: string(parentRunID),
			Operation:   "validate " + jobs[index].Command,
			Commit:      shortCommitLabel(opts.CandidateSHA),
			Worktree:    opts.ValidationOptions.Worktree,
		})
		if err != nil {
			return children, fmt.Errorf("validation job %d (%s) not admitted: %w", index, jobs[index].Command, err)
		}
		children = append(children, handle.RunID)
		completion, err := handle.Wait(context.Background())
		if err != nil {
			return children, fmt.Errorf("validation job %d (%s) could not settle: %w", index, jobs[index].Command, err)
		}
		expected := agentrun.StateSucceeded
		if class != agentrun.OutcomeSuccess {
			expected = agentrun.StateFailed
		}
		if completion.State != expected {
			return children, fmt.Errorf("validation job %d (%s) settled %q instead of %q", index, jobs[index].Command, completion.State, expected)
		}
	}
	return children, nil
}

// shortCommitLabel keeps the activity label compact without assuming the gate
// candidate is a full Git SHA; tests and synthetic callers may use short ids.
func shortCommitLabel(sha string) string {
	if len(sha) <= 7 {
		return sha
	}
	return sha[:7]
}

// settlementForState maps the existing facade vocabulary onto the root
// run's terminal outcome so the failing LAYER stays distinct: validation and
// review blockers share the failure class but carry different detail texts;
// infrastructure failures (and any unknown state, which ExitCode also
// treats as infrastructure) settle unavailable.
func settlementForState(state string) rootSettlement {
	switch state {
	case StatePass:
		return rootSettlement{class: agentrun.OutcomeSuccess}
	case StateValidationFailed:
		return rootSettlement{class: agentrun.OutcomeFailure, detail: layerValidationDetail}
	default:
		return rootSettlement{class: agentrun.OutcomeUnavailable, detail: layerInfrastructureDetail}
	}
}

// expectedRootState is the lifecycle state each settlement class must
// produce, used to prove the root settled honestly before the facade result
// is returned.
func expectedRootState(class agentrun.OutcomeClass) agentrun.LifecycleState {
	switch class {
	case agentrun.OutcomeSuccess:
		return agentrun.StateSucceeded
	case agentrun.OutcomeUnavailable:
		return agentrun.StateUnavailable
	default:
		return agentrun.StateFailed
	}
}

// settlementDivergence reports an honest infrastructure failure when the
// durable record of the root run does not match the settlement this process
// authored: a gate whose own history cannot be trusted must not pass its
// facade result through untouched.
func settlementDivergence(completion execution.Completion, settlement rootSettlement) error {
	expected := expectedRootState(settlement.class)
	if completion.State != expected {
		return fmt.Errorf("gate: root run settled %q instead of %q (%s)", completion.State, expected, settlement.detail)
	}
	if settlement.detail != "" && completion.Error != settlement.detail {
		return fmt.Errorf("gate: root run settled detail %q instead of %q", completion.Error, settlement.detail)
	}
	return nil
}

// rootSettlement is the outcome the orchestrator decides for the root run
// once both phases finished. The channel carries exactly one value per gate
// execution.
type rootSettlement struct {
	class  agentrun.OutcomeClass
	detail string
}
