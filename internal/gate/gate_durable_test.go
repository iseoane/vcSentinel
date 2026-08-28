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
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

// cfgDosCapabilities is a multi-command profile fixture: two capabilities in
// exact profile order, both judged by exit code.
func cfgDosCapabilities() config.Config {
	return config.Config{
		Validation: config.ValidationConfig{
			Capabilities: map[string]config.CapabilityConfig{
				"lint": {Command: "echo lint", FailsWhen: config.FailsWhenExitCode},
				"test": {Command: "echo test", FailsWhen: config.FailsWhenExitCode},
			},
			Profiles: map[string][]string{
				"testperfil": {"lint", "test"},
			},
			Mode: config.ModeInplace,
		},
	}
}

// ejecutorSeleccionado fails exactly the listed commands with the given exit
// status and output; every other command passes silently.
func ejecutorSeleccionado(fallidos map[string]validation.ValidationRun) validation.EjecutorComando {
	return func(comando string) (int, string, error) {
		if run, ok := fallidos[comando]; ok {
			return run.Exit, run.Salida, nil
		}
		return 0, "", nil
	}
}

// inspeccionarRaiz reconstructs the root run exclusively from admitted
// durable state, proving the settled terminal state and its layer detail.
func inspeccionarRaiz(t *testing.T, st *store.Store, opts Opciones) execution.Inspection {
	t.Helper()
	plan, err := buildDurableGatePlan(opts)
	if err != nil {
		t.Fatalf("plan rebuild failed: %v", err)
	}
	inspeccion, err := execution.NewController(st, nil).Inspect(context.Background(), plan.Root.RunID())
	if err != nil {
		t.Fatalf("root inspection failed: %v", err)
	}
	return inspeccion
}

// TestGateDurableReviewNeverStartsOnValidationFailure proves the legacy
// ordering rule on the durable path: when any validation command fails, the
// review transport factory is NEVER invoked, the root run settles failed
// naming the validation layer plus its child enumeration, and each validation
// job carries its own deterministic settlement in durable state — the FAILED
// job included, whose AttemptOutcome keeps class=failure AND the non-empty
// evidence OutputHash.
func TestGateDurableReviewNeverStartsOnValidationFailure(t *testing.T) {
	transportes := 0
	base := opcionesBase(t, cfgDosCapabilities(), ejecutorSeleccionado(map[string]validation.ValidationRun{
		"echo test": {Exit: 3, Salida: "fallo determinista del comando test"},
	}), fabricaContadora(new(int), "", nil))
	var capturados []validation.ValidationRun
	base.EjecutarValidacion = func(perfil string, alcance []string, o validation.OpcionesEjecucion) ([]validation.ValidationRun, error) {
		runs, err := ejecutarPerfilSinCandidato(perfil, alcance, o)
		capturados = runs
		return runs, err
	}
	durableOpts := opcionesDurable(t, base, &transportes)

	resultado := EjecutarGate(durableOpts)

	if resultado.Estado != EstadoValidationFailed {
		t.Fatalf("estado = %q, expected %q", resultado.Estado, EstadoValidationFailed)
	}
	if transportes != 0 {
		t.Fatalf("review transport factory was invoked %d times after a validation failure, expected 0", transportes)
	}

	inspeccion := inspeccionarRaiz(t, durableOpts.DurableStore, durableOpts)
	if inspeccion.Projection.State != agentrun.StateFailed {
		t.Fatalf("root state = %q, expected %q", inspeccion.Projection.State, agentrun.StateFailed)
	}
	jobs := planValidationJobsForTest(t, durableOpts)
	wantRootError := layerValidationDetail + "|children=" +
		string(jobs[0].Job.RunID()) + "," + string(jobs[1].Job.RunID())
	var rootError string
	for _, outcome := range inspeccion.Outcomes {
		if strings.HasPrefix(outcome.Error, layerValidationDetail) {
			rootError = outcome.Error
		}
	}
	if rootError != wantRootError {
		t.Fatalf("root settlement detail = %q, expected %q", rootError, wantRootError)
	}

	// Every planned validation job settled durably, pass AND fail alike; the
	// failed one keeps its evidence digest hash-bound despite its failure.
	evidencia := RecordValidationEvidence(capturados)
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
				wantHash := execution.HashAdapterOutput(evidencia[index].String())
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
// distinct on the root run across all three terminal classes: review blockers
// settle failed with the review marker, and infrastructure failures settle
// unavailable with the infrastructure marker.
func TestGateDurableRootLayerDistinction(t *testing.T) {
	t.Run("review blocker names the review layer", func(t *testing.T) {
		transportes := 0
		base := opcionesBase(t, cfgConPerfil("lint", "echo ok"), ejecutorSeleccionado(nil),
			fabricaContadora(new(int), `{"dim":"logic","verdict":"block","findings":[{"dimension":"logic","file":"a.go","line":1,"severity":"CRITICAL","description":"riesgo"}]}`, nil))
		base.EjecutarValidacion = ejecutarPerfilSinCandidato
		base.FabricaRefutador = func() (review.AuditorAgente, string, error) {
			return &auditorFalso{salida: `{"refuted":false,"reason":"the risk remains"}`}, "cheap", nil
		}
		durableOpts := opcionesDurable(t, base, &transportes)

		resultado := EjecutarGate(durableOpts)

		if resultado.Estado != EstadoCodeReviewFailed {
			t.Fatalf("estado = %q, expected %q", resultado.Estado, EstadoCodeReviewFailed)
		}
		inspeccion := inspeccionarRaiz(t, durableOpts.DurableStore, durableOpts)
		if inspeccion.Projection.State != agentrun.StateFailed {
			t.Fatalf("root state = %q, expected failed", inspeccion.Projection.State)
		}
		if !raizNombraCapa(inspeccion, "review") {
			t.Fatalf("root outcomes never named the review layer: %+v", inspeccion.Outcomes)
		}
	})

	t.Run("infrastructure failure settles the root unavailable", func(t *testing.T) {
		transportes := 0
		base := opcionesBase(t, cfgConPerfil("lint", "echo ok"), nil, fabricaContadora(new(int), "", nil))
		base.EjecutarValidacion = func(perfil string, alcance []string, o validation.OpcionesEjecucion) ([]validation.ValidationRun, error) {
			return nil, errAgenteNoDisponibleTest
		}
		durableOpts := opcionesDurable(t, base, &transportes)

		resultado := EjecutarGate(durableOpts)

		if resultado.Estado != EstadoReviewInfrastructureError {
			t.Fatalf("estado = %q, expected %q", resultado.Estado, EstadoReviewInfrastructureError)
		}
		inspeccion := inspeccionarRaiz(t, durableOpts.DurableStore, durableOpts)
		if inspeccion.Projection.State != agentrun.StateUnavailable {
			t.Fatalf("root state = %q, expected unavailable", inspeccion.Projection.State)
		}
		if !raizNombraCapa(inspeccion, "infrastructure") {
			t.Fatalf("root outcomes never named the infrastructure layer: %+v", inspeccion.Outcomes)
		}
	})
}

// TestGateDurableSettlementFailureIsHonestInfrastructure proves that
// sabotaging a validation job's execution directory mid-run isolates exactly
// the settlement branch, which must surface as honest infrastructure instead
// of a silent green gate.
func TestGateDurableSettlementFailureIsHonestInfrastructure(t *testing.T) {
	t.Run("settlement failure is honest infrastructure with a pinned prefix", func(t *testing.T) {
		llamadas := 0
		opts := opcionesBase(t, cfgConPerfil("lint", "echo ok"), ejecutorSeleccionado(nil), fabricaContadora(&llamadas, "", nil))
		opts.Stage = "pre-push"
		opts.CandidateSHA = opts.OpcionesRevision.SHA
		commonDir := filepath.Join(t.TempDir(), "gate-common")
		opts.DurableStore = store.NuevoStore(commonDir)
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
		opts.EjecutarValidacion = func(perfil string, alcance []string, o validation.OpcionesEjecucion) ([]validation.ValidationRun, error) {
			if err := os.WriteFile(jobDir, []byte("not a directory"), 0o644); err != nil {
				t.Fatalf("sabotage failed: %v", err)
			}
			return ejecutarPerfilSinCandidato(perfil, alcance, o)
		}

		resultado := EjecutarGate(opts)

		if resultado.Estado != EstadoReviewInfrastructureError {
			t.Fatalf("estado = %q, expected %q", resultado.Estado, EstadoReviewInfrastructureError)
		}
		if len(resultado.Mensajes) != 1 || !strings.HasPrefix(resultado.Mensajes[0], "No se pudo registrar la validación duradera:") {
			t.Fatalf("settlement-failure facade drifted: %q", resultado.Mensajes)
		}
	})
}

// TestGateDurableEvidenceDigestBinding proves validation evidence is
// recorded deterministically without any agent: the settled output hash of
// each passed validation job equals execution.HashAdapterOutput over the
// RecordValidationEvidence serialization of that command's tuple.
func TestGateDurableEvidenceDigestBinding(t *testing.T) {
	transportes := 0
	base := opcionesBase(t, cfgDosCapabilities(), ejecutorSeleccionado(nil), fabricaContadora(new(int), `{"dim":"logic","verdict":"ok","findings":[]}`, nil))
	var capturados []validation.ValidationRun
	base.EjecutarValidacion = func(perfil string, alcance []string, o validation.OpcionesEjecucion) ([]validation.ValidationRun, error) {
		runs, err := ejecutarPerfilSinCandidato(perfil, alcance, o)
		capturados = runs
		return runs, err
	}
	durableOpts := opcionesDurable(t, base, &transportes)

	resultado := EjecutarGate(durableOpts)

	if resultado.Estado != EstadoPass {
		t.Fatalf("estado = %q, expected %q", resultado.Estado, EstadoPass)
	}
	evidencia := RecordValidationEvidence(capturados)
	jobs := planValidationJobsForTest(t, durableOpts)
	if len(jobs) != len(evidencia) {
		t.Fatalf("planned jobs = %d, evidence entries = %d", len(jobs), len(evidencia))
	}
	for index, job := range jobs {
		jobInspection, err := execution.NewController(durableOpts.DurableStore, nil).Inspect(context.Background(), job.Job.RunID())
		if err != nil {
			t.Fatalf("validation job inspection failed: %v", err)
		}
		if len(jobInspection.Outcomes) != 1 {
			t.Fatalf("validation job %q recorded %d outcomes, expected 1", job.Command, len(jobInspection.Outcomes))
		}
		wantHash := execution.HashAdapterOutput(evidencia[index].String())
		if got := jobInspection.Outcomes[0].OutputHash; got != wantHash {
			t.Fatalf("validation job %q output hash %q does not bind its evidence digest (want %q)", job.Command, got, wantHash)
		}
	}
}

func raizNombraCapa(inspeccion execution.Inspection, capa string) bool {
	for _, outcome := range inspeccion.Outcomes {
		if strings.Contains(outcome.Error, "failing layer: "+capa) {
			return true
		}
	}
	return false
}

func planValidationJobsForTest(t *testing.T, opts Opciones) []GateJobPlan {
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
	transportes := 0
	base := opcionesBase(t, cfgConPerfil("lint", "echo ok"), ejecutorSeleccionado(nil),
		fabricaContadora(new(int), `{"dim":"logic","verdict":"ok","findings":[]}`, nil))
	base.EjecutarValidacion = ejecutarPerfilSinCandidato
	durableOpts := opcionesDurable(t, base, &transportes)

	primero := EjecutarGate(durableOpts)
	if primero.Estado == EstadoReviewInfrastructureError {
		t.Fatalf("first gate is infrastructure-broken before the case starts: %v", primero.Mensajes)
	}

	segundo := EjecutarGate(durableOpts)
	if segundo.Estado == EstadoReviewInfrastructureError {
		t.Fatalf("re-running the gate on the same candidate = %q %v; the guardian must be able to re-certify a commit it already gated", segundo.Estado, segundo.Mensajes)
	}
	if segundo.Estado != primero.Estado {
		t.Errorf("second gate estado = %q, expected the same verdict as the first (%q): the same candidate and the same commands cannot change the outcome", segundo.Estado, primero.Estado)
	}

	// Facade equality is not enough: a permissive Controller.Start would look
	// identical from here while appending a second lifecycle onto attempt 0's
	// settled stream. Inspect attempt 1 explicitly and prove it settled on its
	// OWN identity, distinct from attempt 0.
	intento0, err := BuildDurableGatePlanIntento(durableOpts, 0)
	if err != nil {
		t.Fatalf("attempt 0 plan: %v", err)
	}
	intento1, err := BuildDurableGatePlanIntento(durableOpts, 1)
	if err != nil {
		t.Fatalf("attempt 1 plan: %v", err)
	}
	if intento0.Root.RunID() == intento1.Root.RunID() {
		t.Fatal("both attempts derive the same root identity: the discriminator is not discriminating")
	}
	inspector := execution.NewController(durableOpts.DurableStore, nil)
	for nombre, runID := range map[string]agentrun.Identity{
		"attempt 0": intento0.Root.RunID(), "attempt 1": intento1.Root.RunID(),
	} {
		inspeccion, err := inspector.Inspect(context.Background(), runID)
		if err != nil {
			t.Fatalf("%s root inspection: %v", nombre, err)
		}
		if inspeccion.Projection.Terminal == agentrun.TerminalNone {
			t.Errorf("%s root did not settle: terminal=%q state=%q", nombre, inspeccion.Projection.Terminal, inspeccion.Projection.State)
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
	transportes := 0
	// An empty auditor output settles the review as infrastructure-unavailable.
	roto := opcionesBase(t, cfgConPerfil("lint", "echo ok"), ejecutorSeleccionado(nil), fabricaContadora(new(int), "", nil))
	roto.EjecutarValidacion = ejecutarPerfilSinCandidato
	durableOpts := opcionesDurable(t, roto, &transportes)

	primero := EjecutarGate(durableOpts)
	if primero.Estado != EstadoReviewInfrastructureError {
		t.Fatalf("first gate estado = %q, expected the infrastructure failure this case is about", primero.Estado)
	}
	intento0, err := BuildDurableGatePlanIntento(durableOpts, 0)
	if err != nil {
		t.Fatalf("attempt 0 plan: %v", err)
	}
	inspeccion, err := execution.NewController(durableOpts.DurableStore, nil).Inspect(context.Background(), intento0.Root.RunID())
	if err != nil {
		t.Fatalf("attempt 0 inspection: %v", err)
	}
	if inspeccion.Projection.Terminal != agentrun.TerminalUnavailable {
		t.Fatalf("attempt 0 terminal = %q, expected %q", inspeccion.Projection.Terminal, agentrun.TerminalUnavailable)
	}

	// Same candidate, working reviewer this time: the guardian must be able to
	// certify the commit its own infrastructure failure left uncertified.
	sano := opcionesBase(t, cfgConPerfil("lint", "echo ok"), ejecutorSeleccionado(nil),
		fabricaContadora(new(int), `{"dim":"logic","verdict":"ok","findings":[]}`, nil))
	sano.EjecutarValidacion = ejecutarPerfilSinCandidato
	segundoOpts := sano
	segundoOpts.Stage = durableOpts.Stage
	segundoOpts.CandidateSHA = durableOpts.CandidateSHA
	segundoOpts.DurableStore = durableOpts.DurableStore
	segundoOpts.DurableReviewTransportFactory = durableOpts.DurableReviewTransportFactory

	segundo := EjecutarGate(segundoOpts)
	if segundo.Estado != EstadoPass {
		t.Fatalf("second gate estado = %q, expected %q: an infrastructure failure must not brick the candidate", segundo.Estado, EstadoPass)
	}
}

// TestGateDurableRefusesAnExhaustedCandidate covers the probe's bound. With the
// real limit of 64 the branch would need 64 real gate executions, so the test
// lowers it instead of leaving the refusal unproven.
func TestGateDurableRefusesAnExhaustedCandidate(t *testing.T) {
	original := maxIntentosGate
	maxIntentosGate = 1
	t.Cleanup(func() { maxIntentosGate = original })

	transportes := 0
	base := opcionesBase(t, cfgConPerfil("lint", "echo ok"), ejecutorSeleccionado(nil),
		fabricaContadora(new(int), `{"dim":"logic","verdict":"ok","findings":[]}`, nil))
	base.EjecutarValidacion = ejecutarPerfilSinCandidato
	durableOpts := opcionesDurable(t, base, &transportes)

	if primero := EjecutarGate(durableOpts); primero.Estado == EstadoReviewInfrastructureError {
		t.Fatalf("first gate is broken before the case starts: %v", primero.Mensajes)
	}
	segundo := EjecutarGate(durableOpts)
	if segundo.Estado != EstadoReviewInfrastructureError {
		t.Fatalf("estado = %q, expected the exhausted bound to refuse", segundo.Estado)
	}
	if mensaje := strings.Join(segundo.Mensajes, "\n"); !strings.Contains(mensaje, "settled gate executions") {
		t.Errorf("message = %q, expected the exhaustion refusal to say so", mensaje)
	}
}

// bloqueanteAdapter keeps a run running until liberar is closed, so a test can
// hold a non-terminal root run in durable state while another gate tries to
// admit the same candidate.
type bloqueanteAdapter struct{ liberar chan struct{} }

func (a bloqueanteAdapter) Execute(_ context.Context, _ agentrun.LogicalJob, _ agentrun.InvocationEnvelope, _ string) (execution.AdapterResult, error) {
	<-a.liberar
	return execution.AdapterResult{}, nil
}

// TestGateDurableRefusesAConcurrentGateOnTheSameCandidate pins the other half
// of the admission probe. Climbing to the next attempt is right only when the
// previous execution SETTLED; a root run that is still alive means a second
// gate is racing the first over the same candidate, and refusing it stays
// correct — with its own message, not the raw admission error.
func TestGateDurableRefusesAConcurrentGateOnTheSameCandidate(t *testing.T) {
	transportes := 0
	base := opcionesBase(t, cfgConPerfil("lint", "echo ok"), ejecutorSeleccionado(nil),
		fabricaContadora(new(int), `{"dim":"logic","verdict":"ok","findings":[]}`, nil))
	base.EjecutarValidacion = ejecutarPerfilSinCandidato
	durableOpts := opcionesDurable(t, base, &transportes)

	plan, err := buildDurableGatePlan(durableOpts)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	liberar := make(chan struct{})
	vivo := execution.NewController(durableOpts.DurableStore, bloqueanteAdapter{liberar: liberar})
	// Timing invariant this test relies on: Start persists the running head
	// BEFORE returning its handle, so by the time EjecutarGate probes, the
	// non-terminal record is durably visible. The handle is kept and waited on
	// so the blocked worker finishes writing before t.TempDir is removed —
	// dropping it makes the cleanup race the worker.
	handle, err := vivo.Start(context.Background(), plan.Root.Request(), store.RunPolicy{
		ID: DurableGateRunPolicyID, Operation: gateRootOperation(durableOpts.Stage),
		Commit: shortCommitLabel(durableOpts.CandidateSHA), Worktree: durableOpts.OpcionesValidacion.Worktree,
	})
	if err != nil {
		t.Fatalf("could not hold a live root run: %v", err)
	}
	t.Cleanup(func() {
		close(liberar)
		if _, err := handle.Wait(context.Background()); err != nil {
			t.Logf("held run did not settle cleanly: %v", err)
		}
	})

	resultado := EjecutarGate(durableOpts)

	if resultado.Estado != EstadoReviewInfrastructureError {
		t.Fatalf("estado = %q, expected the concurrent gate to be refused", resultado.Estado)
	}
	mensaje := strings.Join(resultado.Mensajes, "\n")
	if !strings.Contains(mensaje, "already running this candidate") {
		t.Errorf("message = %q; a concurrent gate must say so instead of surfacing the raw admission error", mensaje)
	}
}
