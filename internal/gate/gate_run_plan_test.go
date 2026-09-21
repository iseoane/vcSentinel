package gate

import (
	"errors"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
)

// TestGateRunPlanBuilder proves the pure decomposition of one gate execution
// into ONE root run request plus logical jobs: stage/profile/candidate
// embedding, deterministic job count and order per profile command list, and
// explicit rejection of unexplainable inputs.
func TestGateRunPlanBuilder(t *testing.T) {
	t.Run("embeds stage profile and candidate sha in the root request", func(t *testing.T) {
		plan, err := BuildGateRunPlan("pre-push", "standard", "abc123def456", []string{"go vet ./..."})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		wantCandidate := agentrun.CandidateIdentity(agentrun.Candidate("abc123def456"))
		if got := plan.Root.Request().Candidate().Identity(); got != wantCandidate {
			t.Fatalf("root candidate identity = %q, expected %q", got, wantCandidate)
		}
		prompt := string(plan.Root.Request().Prompt())
		if !strings.Contains(prompt, "pre-push") || !strings.Contains(prompt, "standard") {
			t.Fatalf("root prompt must embed stage and profile, got %q", prompt)
		}
		if plan.Stage != "pre-push" || plan.Profile != "standard" || plan.CandidateSHA != "abc123def456" {
			t.Fatalf("plan fields = %q/%q/%q", plan.Stage, plan.Profile, plan.CandidateSHA)
		}
		if plan.CommandsIdentity != ValidationCommandsIdentity([]string{"go vet ./..."}) {
			t.Fatalf("CommandsIdentity does not match the ordered command-list identity")
		}
	})

	t.Run("one validation job per command in exact profile order, and nothing else", func(t *testing.T) {
		commands := []string{"go vet ./...", "gofmt -l .", "go test ./internal/gate/"}
		plan, err := BuildGateRunPlan("pr", "full", "deadbeef", commands)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(plan.Jobs) != len(commands) {
			t.Fatalf("job count = %d, expected exactly %d validation jobs", len(plan.Jobs), len(commands))
		}
		for index, wantCommand := range commands {
			job := plan.Jobs[index]
			if job.Kind != GateJobValidation {
				t.Fatalf("job %d kind = %q, expected %q", index, job.Kind, GateJobValidation)
			}
			if job.Command != wantCommand {
				t.Fatalf("job %d command = %q, expected %q", index, job.Command, wantCommand)
			}
			prompt := string(job.Job.Request().Prompt())
			if !strings.Contains(prompt, "pr") || !strings.Contains(prompt, "full") {
				t.Fatalf("validation job %d prompt lost stage/profile context: %q", index, prompt)
			}
			layerbilities := job.Job.Request().Capabilities()
			if len(layerbilities) != 1 || layerbilities[0].Attributes()["command"] != wantCommand {
				t.Fatalf("validation job %d does not carry its command as capability evidence", index)
			}
		}
		// Piece 3: the plan carries validation jobs and nothing else. A job
		// of any other kind means the semantic phase came back through the
		// planner, which is exactly what the design removed.
		for _, job := range plan.Jobs {
			if job.Kind != GateJobValidation {
				t.Fatalf("plan carries a non-validation job: %+v", job)
			}
		}
	})

	t.Run("empty profile yields no jobs at all", func(t *testing.T) {
		plan, err := BuildGateRunPlan("pre-commit", "delegated", "cafe0000", nil)
		if err != nil {
			t.Fatalf("empty profile must build a valid plan, got %v", err)
		}
		if validations := plan.ValidationJobs(); len(validations) != 0 {
			t.Fatalf("empty profile produced %d validation jobs", len(validations))
		}
		if len(plan.Jobs) != 0 {
			t.Fatalf("empty profile must produce no jobs, got %+v", plan.Jobs)
		}
	})

	t.Run("same inputs derive identical identities", func(t *testing.T) {
		first, err := BuildGateRunPlan("pr", "standard", "aa11bb22", []string{"cmd-a", "cmd-b"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		second, err := BuildGateRunPlan("pr", "standard", "aa11bb22", []string{"cmd-a", "cmd-b"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if first.Root.ID() != second.Root.ID() || first.Root.RunID() != second.Root.RunID() {
			t.Fatalf("identical inputs derived divergent root identities")
		}
		for i := range first.Jobs {
			if first.Jobs[i].Job.ID() != second.Jobs[i].Job.ID() {
				t.Fatalf("job %d identity drifted between identical builds", i)
			}
		}
	})

	t.Run("different inputs derive different root identities", func(t *testing.T) {
		base, err := BuildGateRunPlan("pr", "standard", "sha-1", []string{"cmd-a"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		variants := map[string][4]string{
			"stage":    {"pre-commit", "standard", "sha-1", "cmd-a"},
			"profile":  {"pr", "strict", "sha-1", "cmd-a"},
			"sha":      {"pr", "standard", "sha-2", "cmd-a"},
			"commands": {"pr", "standard", "sha-1", "cmd-b"},
		}
		for name, args := range variants {
			variant, err := BuildGateRunPlan(args[0], args[1], args[2], []string{args[3]})
			if err != nil {
				t.Fatalf("variant %s: unexpected error: %v", name, err)
			}
			if variant.Root.ID() == base.Root.ID() {
				t.Fatalf("variant %s reused the base root identity", name)
			}
		}
	})

	t.Run("rejects empty or blank planning inputs explicitly", func(t *testing.T) {
		cases := []struct {
			name                string
			stage, profile, sha string
			commands            []string
			field               string
		}{
			{name: "missing stage", stage: "", profile: "p", sha: "s", field: "stage"},
			{name: "blank stage", stage: "  ", profile: "p", sha: "s", field: "stage"},
			{name: "missing profile", stage: "pr", profile: "", sha: "s", field: "profile"},
			{name: "missing candidate", stage: "pr", profile: "p", sha: "", field: "candidate"},
			{name: "blank command", stage: "pr", profile: "p", sha: "s", commands: []string{"ok", ""}, field: "commands"},
		}
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				_, err := BuildGateRunPlan(testCase.stage, testCase.profile, testCase.sha, testCase.commands)
				var planErr GatePlanError
				if !errors.As(err, &planErr) {
					t.Fatalf("expected GatePlanError, got %v", err)
				}
				if planErr.Field != testCase.field {
					t.Fatalf("error field = %q, expected %q", planErr.Field, testCase.field)
				}
			})
		}
	})
}

// TestGateDurableOrchestrationSeams pins the honest infrastructure failures
// of the single (durable) execution path: a missing durable store refuses to
// run anything, plan construction is validated before any admission, and an
// unresolved capability reference fails during planning.
func TestGateDurableOrchestrationSeams(t *testing.T) {
	cfgWithBrokenProfile := cfgWithProfile("lint", "echo ok")
	cfgWithBrokenProfile.Validation.Profiles["broken"] = []string{"missing-capability"}

	t.Run("without an injected store fails as infrastructure before any phase", func(t *testing.T) {
		opts := baseOptions(t, cfgWithProfile("lint", "echo ok"), nil)
		opts.DurableStore = nil

		result := RunGate(opts)

		if result.State != StateInfrastructureError || ExitCode(result.State) != 4 {
			t.Fatalf("missing durable store is infrastructure-class, got %q exit %d", result.State, ExitCode(result.State))
		}
		if result.Err == nil || !strings.Contains(result.Err.Error(), "injected store") {
			t.Fatalf("expected an explicit missing-store failure, got %v", result.Err)
		}
	})

	t.Run("still validates the plan before admitting anything", func(t *testing.T) {
		opts := baseOptions(t, cfgWithProfile("lint", "echo ok"), nil)
		opts.Stage = ""

		result := RunGate(opts)

		var planErr GatePlanError
		if !errors.As(result.Err, &planErr) {
			t.Fatalf("expected the plan-build failure to surface before execution, got %v", result.Err)
		}
		if result.State != StateInfrastructureError {
			t.Fatalf("a broken plan is infrastructure-class, got %q", result.State)
		}
	})

	t.Run("rejects an unresolved capability reference during planning", func(t *testing.T) {
		opts := baseOptions(t, cfgWithBrokenProfile, nil)
		opts.Profile = "broken"

		result := RunGate(opts)

		if result.Err == nil || !strings.Contains(errorMessage(result.Err), "not configured") {
			t.Fatalf("broken capability reference must fail planning, got %v", result.Err)
		}
	})
}

func errorMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// TestGateRunPlanAttemptDiscriminates proves the attempt discriminator does
// what the admission probe relies on: a later attempt derives a DIFFERENT
// identity for the root and for every job, while attempt 0 keeps the identities
// a candidate has always had. Without this, the probe could climb forever
// against an identity that never changes.
func TestGateRunPlanAttemptDiscriminates(t *testing.T) {
	commands := []string{"cmd-a", "cmd-b"}
	first, err := BuildGateRunPlanAttempt("pr", "standard", "sha-1", commands, 0)
	if err != nil {
		t.Fatalf("attempt 0: %v", err)
	}
	second, err := BuildGateRunPlanAttempt("pr", "standard", "sha-1", commands, 1)
	if err != nil {
		t.Fatalf("attempt 1: %v", err)
	}
	if first.Root.RunID() == second.Root.RunID() {
		t.Fatal("attempt 1 reused the root run identity of attempt 0: a re-gated candidate would still be refused")
	}
	if len(first.Jobs) != len(second.Jobs) || len(first.Jobs) == 0 {
		t.Fatalf("job counts diverged: %d vs %d", len(first.Jobs), len(second.Jobs))
	}
	for i := range first.Jobs {
		if first.Jobs[i].Job.RunID() == second.Jobs[i].Job.RunID() {
			t.Errorf("job %d reused its identity across attempts: the child would be refused even with a fresh root", i)
		}
	}
	if first.Attempt != 0 || second.Attempt != 1 {
		t.Errorf("plan attempts = %d, %d; expected 0, 1", first.Attempt, second.Attempt)
	}

	// Attempt 0 must stay byte-identical to the historical identity, or every
	// durable record written before the discriminator existed is orphaned.
	rebuilt, err := BuildGateRunPlanAttempt("pr", "standard", "sha-1", commands, 0)
	if err != nil {
		t.Fatalf("attempt 0 rebuild: %v", err)
	}
	if rebuilt.Root.RunID() != first.Root.RunID() {
		t.Error("attempt 0 is no longer deterministic")
	}
	if _, err := BuildGateRunPlanAttempt("pr", "standard", "sha-1", commands, -1); err == nil {
		t.Error("a negative attempt should be rejected explicitly")
	}
}
