package gate

import (
	"errors"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
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

	t.Run("one validation job per command in exact profile order plus one review job", func(t *testing.T) {
		commands := []string{"go vet ./...", "gofmt -l .", "go test ./internal/gate/"}
		plan, err := BuildGateRunPlan("pr", "full", "deadbeef", commands)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(plan.Jobs) != len(commands)+1 {
			t.Fatalf("job count = %d, expected %d validation jobs plus one review job", len(plan.Jobs), len(commands)+1)
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
			capabilities := job.Job.Request().Capabilities()
			if len(capabilities) != 1 || capabilities[0].Attributes()["command"] != wantCommand {
				t.Fatalf("validation job %d does not carry its command as capability evidence", index)
			}
		}
		reviewJob := plan.ReviewJob()
		if reviewJob.Kind != GateJobReview || reviewJob.Command != "" {
			t.Fatalf("last job must be the single review descriptor, got %+v", reviewJob)
		}
	})

	t.Run("empty profile still yields exactly one review job", func(t *testing.T) {
		plan, err := BuildGateRunPlan("pre-commit", "delegated", "cafe0000", nil)
		if err != nil {
			t.Fatalf("empty profile must build a valid plan, got %v", err)
		}
		if validations := plan.ValidationJobs(); len(validations) != 0 {
			t.Fatalf("empty profile produced %d validation jobs", len(validations))
		}
		reviewJob := plan.ReviewJob()
		if reviewJob.Kind != GateJobReview {
			t.Fatalf("empty profile must keep the review job, got %+v", reviewJob)
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

// TestGateDurableRunsSwitch proves the reversible seam: default FALSE keeps
// legacy behavior untouched, TRUE routes through the real durable
// orchestration and fails honestly as infrastructure before any phase runs
// when its required seams are absent.
func TestGateDurableRunsSwitch(t *testing.T) {
	cfgWithBrokenProfile := cfgConPerfil("lint", "echo ok")
	cfgWithBrokenProfile.Validation.Profiles["roto"] = []string{"missing-capability"}

	t.Run("default false keeps the legacy path and never sets Err", func(t *testing.T) {
		llamadas := 0
		opts := opcionesBase(cfgConPerfil("lint", "echo ok"), nil, fabricaContadora(&llamadas, `{"dim":"logic","verdict":"ok","findings":[]}`, nil))
		// Injected validation seam: the assertion must stay independent of
		// the real git state of whichever tree runs the tests.
		opts.EjecutarValidacion = func(perfil string, _ []string, _ validation.OpcionesEjecucion) ([]validation.ValidationRun, error) {
			return nil, nil
		}

		resultado := EjecutarGate(opts)

		if resultado.Estado != EstadoPass {
			t.Fatalf("legacy default must stay byte-green: estado = %q", resultado.Estado)
		}
		if resultado.Err != nil {
			t.Fatalf("legacy default must leave Err nil, got %v", resultado.Err)
		}
		if llamadas != 1 {
			t.Fatalf("expected exactly one review invocation, got %d", llamadas)
		}
	})

	t.Run("true without an injected store fails as infrastructure before any phase", func(t *testing.T) {
		llamadas := 0
		opts := opcionesBase(cfgConPerfil("lint", "echo ok"), nil, fabricaContadora(&llamadas, "", nil))
		opts.DurableRuns = true
		opts.Stage = "pre-push"
		opts.CandidateSHA = "abc123def456"

		resultado := EjecutarGate(opts)

		if resultado.Estado != EstadoReviewInfrastructureError || CodigoSalida(resultado.Estado) != 4 {
			t.Fatalf("missing durable store is infrastructure-class, got %q exit %d", resultado.Estado, CodigoSalida(resultado.Estado))
		}
		if resultado.Err == nil || !strings.Contains(resultado.Err.Error(), "injected store") {
			t.Fatalf("expected an explicit missing-store failure, got %v", resultado.Err)
		}
		if llamadas != 0 {
			t.Fatalf("review must never start without a durable store, got %d calls", llamadas)
		}
	})

	t.Run("true still validates the plan before admitting anything", func(t *testing.T) {
		opts := opcionesBase(cfgConPerfil("lint", "echo ok"), nil, nil)
		opts.DurableRuns = true
		opts.Stage = ""
		opts.CandidateSHA = "abc123def456"

		resultado := EjecutarGate(opts)

		var planErr GatePlanError
		if !errors.As(resultado.Err, &planErr) {
			t.Fatalf("expected the plan-build failure to surface before execution, got %v", resultado.Err)
		}
		if resultado.Estado != EstadoReviewInfrastructureError {
			t.Fatalf("a broken plan is infrastructure-class, got %q", resultado.Estado)
		}
	})

	t.Run("true rejects an unresolved capability reference during planning", func(t *testing.T) {
		opts := opcionesBase(cfgWithBrokenProfile, nil, nil)
		opts.Perfil = "roto"
		opts.DurableRuns = true
		opts.Stage = "pr"
		opts.CandidateSHA = "abc123def456"

		resultado := EjecutarGate(opts)

		if resultado.Err == nil || !strings.Contains(errorMessage(resultado.Err), "not configured") {
			t.Fatalf("broken capability reference must fail planning, got %v", resultado.Err)
		}
	})
}

func errorMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
