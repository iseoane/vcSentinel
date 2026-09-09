// Ordering proofs and durable-state evidence checks for the real durable
// gate orchestration (R9 slice 2). This file owns the behavioral seams that
// the facade harness cannot see: the transport-factory counter seam proves
// review never starts after a validation failure, root settlements keep
// their failing layer distinct across terminal classes, validation evidence
// stays hash-bound to its recorded digest, and sabotaged settlement paths
// surface as honest infrastructure instead of a silent green gate.
package gate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

// cfgWithTwoCapabilities is a multi-command profile fixture: two layerbilities in
// exact profile order, both judged by exit code.
func cfgWithTwoCapabilities() config.Config {
	return config.Config{
		Validation: config.ValidationConfig{
			Capabilities: map[string]config.CapabilityConfig{
				"lint": {Command: "echo lint", FailsWhen: config.FailsWhenExitCode},
				"test": {Command: "echo test", FailsWhen: config.FailsWhenExitCode},
			},
			Profiles: map[string][]string{
				"testprofile": {"lint", "test"},
			},
			Mode: config.ModeInplace,
		},
	}
}

// selectedExecutor fails exactly the listed commands with the given exit
// status and output; every other command passes silently.
func selectedExecutor(failing map[string]validation.ValidationRun) validation.CommandRunner {
	return func(command string) (int, string, error) {
		if run, ok := failing[command]; ok {
			return run.Exit, run.Output, nil
		}
		return 0, "", nil
	}
}

// inspectRoot reconstructs the root run exclusively from admitted
// durable state, proving the settled terminal state and its layer detail.
func inspectRoot(t *testing.T, st *store.Store, opts Options) execution.Inspection {
	t.Helper()
	plan, err := buildDurableGatePlan(opts)
	if err != nil {
		t.Fatalf("plan rebuild failed: %v", err)
	}
	inspection, err := execution.NewController(st, nil).Inspect(context.Background(), plan.Root.RunID())
	if err != nil {
		t.Fatalf("root inspection failed: %v", err)
	}
	return inspection
}

// TestGateDurableReviewNeverStartsOnValidationFailure proves the legacy
// ordering rule on the durable path: when any validation command fails, the
// review transport factory is NEVER invoked, the root run settles failed
// naming the validation layer plus its child enumeration, and each validation
// job carries its own deterministic settlement in durable state — the FAILED
// job included, whose AttemptOutcome keeps class=failure AND the non-empty
// evidence OutputHash.
func TestGateDurableValidationFailureSettlesFailed(t *testing.T) {
	base := baseOptions(t, cfgWithTwoCapabilities(), selectedExecutor(map[string]validation.ValidationRun{
		"echo test": {Exit: 3, Output: "deterministic failure of the test command"},
	}))
	var captured []validation.ValidationRun
	base.RunValidation = func(profile string, scope []string, o validation.RunOptions) ([]validation.ValidationRun, error) {
		runs, err := runProfileWithoutCandidate(profile, scope, o)
		captured = runs
		return runs, err
	}
	durableOpts := durableOptions(t, base)

	result := RunGate(durableOpts)

	if result.State != StateValidationFailed {
		t.Fatalf("state = %q, expected %q", result.State, StateValidationFailed)
	}

	inspection := inspectRoot(t, durableOpts.DurableStore, durableOpts)
	if inspection.Projection.State != agentrun.StateFailed {
		t.Fatalf("root state = %q, expected %q", inspection.Projection.State, agentrun.StateFailed)
	}
	jobs := planValidationJobsForTest(t, durableOpts)
	wantRootError := layerValidationDetail + "|children=" +
		string(jobs[0].Job.RunID()) + "," + string(jobs[1].Job.RunID())
	var rootError string
	for _, outcome := range inspection.Outcomes {
		if strings.HasPrefix(outcome.Error, layerValidationDetail) {
			rootError = outcome.Error
		}
	}
	if rootError != wantRootError {
		t.Fatalf("root settlement detail = %q, expected %q", rootError, wantRootError)
	}

	// Every planned validation job settled durably, pass AND fail alike; the
	// failed one keeps its evidence digest hash-bound despite its failure.
	evidence := RecordValidationEvidence(captured)
	settledClasses := map[agentrun.OutcomeClass]int{}
	for index, job := range jobs {
		jobInspection, err := execution.NewController(durableOpts.DurableStore, nil).Inspect(context.Background(), job.Job.RunID())
		if err != nil {
			t.Fatalf("validation job inspection failed: %v", err)
		}
		for _, outcome := range jobInspection.Outcomes {
			settledClasses[outcome.Class]++
			if outcome.Class == agentrun.OutcomeSuccess && outcome.OutputHash == "" {
				t.Fatalf("passed validation job %q settled without hash-bound evidence", job.Command)
			}
			if outcome.Class == agentrun.OutcomeFailure {
				wantHash := execution.HashAdapterOutput(evidence[index].String())
				if outcome.OutputHash == "" || outcome.OutputHash != wantHash {
					t.Fatalf("failed validation job %q lost its evidence: OutputHash=%q want %q", job.Command, outcome.OutputHash, wantHash)
				}
			}
		}
	}
	if settledClasses[agentrun.OutcomeSuccess] != 1 || settledClasses[agentrun.OutcomeFailure] != 1 {
		t.Fatalf("validation settlements = %v, expected one success and one failure", settledClasses)
	}
}

// TestGateDurableRootLayerDistinction pins that the failing LAYER stays
// distinct on the root run across the two failing classes a deterministic
// gate can reach: validation blockers settle failed with the validation
// marker, and infrastructure failures settle unavailable with the
// infrastructure marker. The review layer is gone with piece 3.
func TestGateDurableRootLayerDistinction(t *testing.T) {
	t.Run("validation blocker names the validation layer", func(t *testing.T) {
		base := baseOptions(t, cfgWithProfile("lint", "echo boom"),
			func(string) (int, string, error) { return 1, "red", nil })
		base.RunValidation = runProfileWithoutCandidate
		durableOpts := durableOptions(t, base)

		result := RunGate(durableOpts)

		if result.State != StateValidationFailed {
			t.Fatalf("state = %q, expected %q", result.State, StateValidationFailed)
		}
		inspection := inspectRoot(t, durableOpts.DurableStore, durableOpts)
		if inspection.Projection.State != agentrun.StateFailed {
			t.Fatalf("root state = %q, expected failed", inspection.Projection.State)
		}
		if !rootNamesLayer(inspection, "validation") {
			t.Fatalf("root outcomes never named the validation layer: %+v", inspection.Outcomes)
		}
	})

	t.Run("infrastructure failure settles the root unavailable", func(t *testing.T) {
		base := baseOptions(t, cfgWithProfile("lint", "echo ok"), nil)
		base.RunValidation = func(profile string, scope []string, o validation.RunOptions) ([]validation.ValidationRun, error) {
			return nil, errAgentUnavailableTest
		}
		durableOpts := durableOptions(t, base)

		result := RunGate(durableOpts)

		if result.State != StateInfrastructureError {
			t.Fatalf("state = %q, expected %q", result.State, StateInfrastructureError)
		}
		inspection := inspectRoot(t, durableOpts.DurableStore, durableOpts)
		if inspection.Projection.State != agentrun.StateUnavailable {
			t.Fatalf("root state = %q, expected unavailable", inspection.Projection.State)
		}
		if !rootNamesLayer(inspection, "infrastructure") {
			t.Fatalf("root outcomes never named the infrastructure layer: %+v", inspection.Outcomes)
		}
	})
}

// TestGateDurableSettlementFailureIsHonestInfrastructure proves that
// sabotaging a validation job's execution directory mid-run isolates exactly
// the settlement branch, which must surface as honest infrastructure instead
// of a silent green gate.
func TestGateDurableSettlementFailureIsHonestInfrastructure(t *testing.T) {
	t.Run("settlement failure is honest infrastructure with a pinned prefix", func(t *testing.T) {
		opts := baseOptions(t, cfgWithProfile("lint", "echo ok"), selectedExecutor(nil))
		opts.Stage = "pre-push"
		opts.CandidateSHA = "0123456789abcdef"
		commonDir := filepath.Join(t.TempDir(), "gate-common")
		opts.DurableStore = store.NewStore(commonDir)
		// The injected validation seam runs AFTER the root run is admitted
		// but BEFORE any validation job settles: sabotaging the FIRST
		// validation job's execution directory there isolates exactly the
		// settlement branch, which must surface as infrastructure instead of
		// a silent green gate.
		plan, err := buildDurableGatePlan(opts)
		if err != nil {
			t.Fatalf("plan rebuild failed: %v", err)
		}
		jobs := plan.ValidationJobs()
		if len(jobs) == 0 {
			t.Fatal("fixture drift: no validation jobs planned")
		}
		jobDir := filepath.Join(commonDir, "vas-sentinel", "executions", "v1", string(jobs[0].Job.RunID()))
		opts.RunValidation = func(profile string, scope []string, o validation.RunOptions) ([]validation.ValidationRun, error) {
			if err := os.WriteFile(jobDir, []byte("not a directory"), 0o644); err != nil {
				t.Fatalf("sabotage failed: %v", err)
			}
			return runProfileWithoutCandidate(profile, scope, o)
		}

		result := RunGate(opts)

		if result.State != StateInfrastructureError {
			t.Fatalf("state = %q, expected %q", result.State, StateInfrastructureError)
		}
		if len(result.Messages) != 1 || !strings.HasPrefix(result.Messages[0], "Could not record the durable validation:") {
			t.Fatalf("settlement-failure facade drifted: %q", result.Messages)
		}
	})
}

// TestGateDurableEvidenceDigestBinding proves validation evidence is
// recorded deterministically without any agent: the settled output hash of
// each passed validation job equals execution.HashAdapterOutput over the
// RecordValidationEvidence serialization of that command's tuple.
func TestGateDurableEvidenceDigestBinding(t *testing.T) {
	base := baseOptions(t, cfgWithTwoCapabilities(), selectedExecutor(nil))
	var captured []validation.ValidationRun
	base.RunValidation = func(profile string, scope []string, o validation.RunOptions) ([]validation.ValidationRun, error) {
		runs, err := runProfileWithoutCandidate(profile, scope, o)
		captured = runs
		return runs, err
	}
	durableOpts := durableOptions(t, base)

	result := RunGate(durableOpts)

	if result.State != StatePass {
		t.Fatalf("state = %q, expected %q", result.State, StatePass)
	}
	evidence := RecordValidationEvidence(captured)
	jobs := planValidationJobsForTest(t, durableOpts)
	if len(jobs) != len(evidence) {
		t.Fatalf("planned jobs = %d, evidence entries = %d", len(jobs), len(evidence))
	}
	for index, job := range jobs {
		jobInspection, err := execution.NewController(durableOpts.DurableStore, nil).Inspect(context.Background(), job.Job.RunID())
		if err != nil {
			t.Fatalf("validation job inspection failed: %v", err)
		}
		if len(jobInspection.Outcomes) != 1 {
			t.Fatalf("validation job %q recorded %d outcomes, expected 1", job.Command, len(jobInspection.Outcomes))
		}
		wantHash := execution.HashAdapterOutput(evidence[index].String())
		if got := jobInspection.Outcomes[0].OutputHash; got != wantHash {
			t.Fatalf("validation job %q output hash %q does not bind its evidence digest (want %q)", job.Command, got, wantHash)
		}
	}
}

func rootNamesLayer(inspection execution.Inspection, layer string) bool {
	for _, outcome := range inspection.Outcomes {
		if strings.Contains(outcome.Error, "failing layer: "+layer) {
			return true
		}
	}
	return false
}

func planValidationJobsForTest(t *testing.T, opts Options) []GateJobPlan {
	t.Helper()
	plan, err := buildDurableGatePlan(opts)
	if err != nil {
		t.Fatalf("plan rebuild failed: %v", err)
	}
	return plan.ValidationJobs()
}

// TestGateDurableRerunsTheSameCandidate is the re-admission defect: the gate
// derives its root and job identities deterministically from (stage, profile,
// candidate SHA, commands), and Controller.Start refuses any identity that
// already carries lifecycle events. A candidate therefore got exactly ONE gate
// execution ever — running it again returned "durable root run not admitted".
//
// It was found in operation, not in theory: a pre-push gate aborted because an
// unrelated file changed mid-validation, and the retry on the same commit could
// never be admitted again, leaving the guardian unable to re-certify it.
func TestGateDurableRerunsTheSameCandidate(t *testing.T) {
	base := baseOptions(t, cfgWithProfile("lint", "echo ok"), selectedExecutor(nil))
	base.RunValidation = runProfileWithoutCandidate
	durableOpts := durableOptions(t, base)

	first := RunGate(durableOpts)
	if first.State == StateInfrastructureError {
		t.Fatalf("first gate is infrastructure-broken before the case starts: %v", first.Messages)
	}

	second := RunGate(durableOpts)
	if second.State == StateInfrastructureError {
		t.Fatalf("re-running the gate on the same candidate = %q %v; the guardian must be able to re-certify a commit it already gated", second.State, second.Messages)
	}
	if second.State != first.State {
		t.Errorf("second gate state = %q, expected the same verdict as the first (%q): the same candidate and the same commands cannot change the outcome", second.State, first.State)
	}

	// Facade equality is not enough: a permissive Controller.Start would look
	// identical from here while appending a second lifecycle onto attempt 0's
	// settled stream. Inspect attempt 1 explicitly and prove it settled on its
	// OWN identity, distinct from attempt 0.
	attempt0, err := BuildDurableGatePlanAttempt(durableOpts, 0)
	if err != nil {
		t.Fatalf("attempt 0 plan: %v", err)
	}
	attempt1, err := BuildDurableGatePlanAttempt(durableOpts, 1)
	if err != nil {
		t.Fatalf("attempt 1 plan: %v", err)
	}
	if attempt0.Root.RunID() == attempt1.Root.RunID() {
		t.Fatal("both attempts derive the same root identity: the discriminator is not discriminating")
	}
	inspector := execution.NewController(durableOpts.DurableStore, nil)
	for name, runID := range map[string]agentrun.Identity{
		"attempt 0": attempt0.Root.RunID(), "attempt 1": attempt1.Root.RunID(),
	} {
		inspection, err := inspector.Inspect(context.Background(), runID)
		if err != nil {
			t.Fatalf("%s root inspection: %v", name, err)
		}
		if inspection.Projection.Terminal == agentrun.TerminalNone {
			t.Errorf("%s root did not settle: terminal=%q state=%q", name, inspection.Projection.Terminal, inspection.Projection.State)
		}
	}
}

// TestGateDurableRerunsAfterAnUnavailableRoot covers the case that actually
// exposed the defect: the first gate settles unavailable because the semantic
// review cannot run, and the candidate must still be gateable afterwards.
// TestGateDurableRerunsTheSameCandidate only proves the green path, and the
// probe treats every terminal class alike, so without this the abort-and-retry
// path stays plausible rather than pinned.
func TestGateDurableRerunsAfterAnUnavailableRoot(t *testing.T) {
	// A validation runner that cannot run settles the root unavailable.
	broken := baseOptions(t, cfgWithProfile("lint", "echo ok"), nil)
	broken.RunValidation = func(string, []string, validation.RunOptions) ([]validation.ValidationRun, error) {
		return nil, errAgentUnavailableTest
	}
	durableOpts := durableOptions(t, broken)

	first := RunGate(durableOpts)
	if first.State != StateInfrastructureError {
		t.Fatalf("first gate state = %q, expected the infrastructure failure this case is about", first.State)
	}
	attempt0, err := BuildDurableGatePlanAttempt(durableOpts, 0)
	if err != nil {
		t.Fatalf("attempt 0 plan: %v", err)
	}
	inspection, err := execution.NewController(durableOpts.DurableStore, nil).Inspect(context.Background(), attempt0.Root.RunID())
	if err != nil {
		t.Fatalf("attempt 0 inspection: %v", err)
	}
	if inspection.Projection.Terminal != agentrun.TerminalUnavailable {
		t.Fatalf("attempt 0 terminal = %q, expected %q", inspection.Projection.Terminal, agentrun.TerminalUnavailable)
	}

	// Same candidate, working reviewer this time: the guardian must be able to
	// certify the commit its own infrastructure failure left uncertified.
	healthy := baseOptions(t, cfgWithProfile("lint", "echo ok"), selectedExecutor(nil))
	healthy.RunValidation = runProfileWithoutCandidate
	secondOpts := healthy
	secondOpts.Stage = durableOpts.Stage
	secondOpts.CandidateSHA = durableOpts.CandidateSHA
	secondOpts.DurableStore = durableOpts.DurableStore

	second := RunGate(secondOpts)
	if second.State != StatePass {
		t.Fatalf("second gate state = %q, expected %q: an infrastructure failure must not brick the candidate", second.State, StatePass)
	}
}

// TestGateDurableRefusesAnExhaustedCandidate covers the probe's bound. With the
// real limit of 64 the branch would need 64 real gate executions, so the test
// lowers it instead of leaving the refusal unproven.
func TestGateDurableRefusesAnExhaustedCandidate(t *testing.T) {
	original := maxAttemptsGate
	maxAttemptsGate = 1
	t.Cleanup(func() { maxAttemptsGate = original })

	base := baseOptions(t, cfgWithProfile("lint", "echo ok"), selectedExecutor(nil))
	base.RunValidation = runProfileWithoutCandidate
	durableOpts := durableOptions(t, base)

	if first := RunGate(durableOpts); first.State == StateInfrastructureError {
		t.Fatalf("first gate is broken before the case starts: %v", first.Messages)
	}
	second := RunGate(durableOpts)
	if second.State != StateInfrastructureError {
		t.Fatalf("state = %q, expected the exhausted bound to refuse", second.State)
	}
	if message := strings.Join(second.Messages, "\n"); !strings.Contains(message, "settled gate executions") {
		t.Errorf("message = %q, expected the exhaustion refusal to say so", message)
	}
}

// blockingAdapter keeps a run running until release is closed, so a test can
// hold a non-terminal root run in durable state while another gate tries to
// admit the same candidate.
type blockingAdapter struct{ release chan struct{} }

func (a blockingAdapter) Execute(_ context.Context, _ agentrun.LogicalJob, _ agentrun.InvocationEnvelope, _ string) (execution.AdapterResult, error) {
	<-a.release
	return execution.AdapterResult{}, nil
}

// TestGateDurableRefusesAConcurrentGateOnTheSameCandidate pins the other half
// of the admission probe. Climbing to the next attempt is right only when the
// previous execution SETTLED; a root run that is still alive means a second
// gate is racing the first over the same candidate, and refusing it stays
// correct — with its own message, not the raw admission error.
func TestGateDurableRefusesAConcurrentGateOnTheSameCandidate(t *testing.T) {
	base := baseOptions(t, cfgWithProfile("lint", "echo ok"), selectedExecutor(nil))
	base.RunValidation = runProfileWithoutCandidate
	durableOpts := durableOptions(t, base)

	plan, err := buildDurableGatePlan(durableOpts)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	release := make(chan struct{})
	live := execution.NewController(durableOpts.DurableStore, blockingAdapter{release: release})
	// Timing invariant this test relies on: Start persists the running head
	// BEFORE returning its handle, so by the time RunGate probes, the
	// non-terminal record is durably visible. The handle is kept and waited on
	// so the blocked worker finishes writing before t.TempDir is removed —
	// dropping it makes the cleanup race the worker.
	handle, err := live.Start(context.Background(), plan.Root.Request(), store.RunPolicy{
		ID: DurableGateRunPolicyID, Operation: gateRootOperation(durableOpts.Stage),
		Commit: shortCommitLabel(durableOpts.CandidateSHA), Worktree: durableOpts.ValidationOptions.Worktree,
	})
	if err != nil {
		t.Fatalf("could not hold a live root run: %v", err)
	}
	t.Cleanup(func() {
		close(release)
		if _, err := handle.Wait(context.Background()); err != nil {
			t.Logf("held run did not settle cleanly: %v", err)
		}
	})

	result := RunGate(durableOpts)

	if result.State != StateInfrastructureError {
		t.Fatalf("state = %q, expected the concurrent gate to be refused", result.State)
	}
	message := strings.Join(result.Messages, "\n")
	if !strings.Contains(message, "already running this candidate") {
		t.Errorf("message = %q; a concurrent gate must say so instead of surfacing the raw admission error", message)
	}
}
