// Real durable orchestration for the gate command (R9 slice 2): with
// Opciones.DurableRuns true, EjecutarGate routes through ONE root durable run
// whose phases mirror today's fixed validation-before-review ordering.
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
//     the legacy path uses (validation.EjecutarPerfilSobreCandidato or the
//     test substitute) — no agent anywhere on this path. Each validation
//     logical job is then settled deterministically per exit status through
//     controller APIs only, carrying its RecordValidationEvidence
//     serialization as the adapter output so the evidence digest is
//     hash-bound to that job's durable AttemptOutcome — INCLUDING failed
//     commands, whose AttemptOutcome keeps both class=failure AND the
//     non-empty evidence OutputHash.
//   - If ANY validation job fails, the review transport factory is NEVER
//     invoked: review does not start, mirroring the legacy rule.
//   - The review phase reuses the legacy tail verbatim — review.AuditarCommit
//     plus traducirVeredicto — with reviewer invocations routed through the
//     injected DurableReviewTransportFactory, i.e. the same construction path
//     `sentinel review` wires today. Review runs are constructed at that
//     different site, so the gate hands its root run ID to the factory for
//     production wiring to thread the parent linkage; there is no second
//     execution path.
package gate

import (
	"context"
	"errors"
	"fmt"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

// DurableGateRunPolicyID identifies every gate-side durable run admitted by
// this wiring: one root run per gate execution plus one deterministic
// settlement run per validation logical job under it.
const DurableGateRunPolicyID = "policy:gate"

// ejecutarGateDurable executes one gate run durably. It always builds and
// validates the GateRunPlan first so plan-construction bugs surface before
// anything is admitted, then admits ONE root run, runs the validation phase,
// and only on a fully green validation starts the review phase through the
// shared durable transport path.
func ejecutarGateDurable(opts Opciones) Resultado {
	plan, err := buildDurableGatePlan(opts)
	if err != nil {
		return Resultado{
			Estado: EstadoReviewInfrastructureError,
			// A plan that cannot be built is infrastructure, not a code
			// finding: same classification rule as legacy validation
			// orchestration failures.
			Mensajes: []string{fmt.Sprintf("gate durable run plan failed before wiring: %v", err)},
			Err:      err,
		}
	}
	if opts.DurableStore == nil {
		return infraResultado(errors.New("gate: durable runs require an injected store"))
	}

	settle := make(chan rootSettlement, 1)
	controller := execution.NewController(opts.DurableStore, rootRunAdapter{settle: settle})
	root, err := controller.Start(context.Background(), plan.Root.Request(), store.RunPolicy{ID: DurableGateRunPolicyID})
	if err != nil {
		return infraResultado(fmt.Errorf("gate: durable root run not admitted: %w", err))
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

	resultado, settlement := fasesGateDurables(plan, opts, root.RunID)
	settle <- settlement
	settled = true

	completion, waitErr := root.Wait(context.Background())
	if waitErr != nil {
		return infraResultado(fmt.Errorf("gate: la ejecución duradera no pudo asentarse: %w", waitErr))
	}
	if divergence := settlementDivergence(completion, settlement); divergence != nil {
		return infraResultado(divergence)
	}
	return resultado
}

// buildDurableGatePlan derives the deterministic command list for the
// resolved profile and validates the full plan construction path without
// executing anything.
func buildDurableGatePlan(opts Opciones) (GateRunPlan, error) {
	commands, err := durableGateCommands(opts.OpcionesValidacion.Cfg, opts.Perfil)
	if err != nil {
		return GateRunPlan{}, err
	}
	return BuildGateRunPlan(opts.Stage, opts.Perfil, opts.CandidateSHA, commands)
}

// infraResultado classifies a durable-infrastructure failure: never a code
// finding, always EstadoReviewInfrastructureError with the typed Err set for
// callers that need it (the CLI facade reads only Estado/Mensajes).
func infraResultado(err error) Resultado {
	return Resultado{
		Estado:   EstadoReviewInfrastructureError,
		Mensajes: []string{err.Error()},
		Err:      err,
	}
}

// fasesGateDurables runs the two gate phases against an already-admitted
// root run and returns the facade result plus the root settlement that names
// the failing layer. Every return path pairs a Resultado with exactly one
// settlement so the blocked root adapter can always finish. Settlements with
// a layer detail also enumerate every settled child job run ID so the root
// record alone supports post-settlement reconstruction.
func fasesGateDurables(plan GateRunPlan, opts Opciones, rootRunID agentrun.Identity) (Resultado, rootSettlement) {
	ejecutarValidacion := opts.EjecutarValidacion
	if ejecutarValidacion == nil {
		ejecutarValidacion = validation.EjecutarPerfilSobreCandidato
	}

	runs, err := ejecutarValidacion(opts.Perfil, opts.RutasCambiadas, opts.OpcionesValidacion)
	if err != nil {
		// Un fallo al ORQUESTAR la validación es infraestructura, no un
		// hallazgo del código: same rule and same message as the legacy
		// path, so equivalent inputs render byte-identical facade text.
		return Resultado{
			Estado:   EstadoReviewInfrastructureError,
			Mensajes: []string{mensajeValidacionNoEjecutada(err)},
			Err:      err,
		}, rootSettlement{class: agentrun.OutcomeUnavailable, detail: layerInfrastructureDetail}
	}

	evidencia := RecordValidationEvidence(runs)
	hallazgos := validation.Hallazgos(runs, opts.OpcionesValidacion.Cfg.Validation.Capabilities)
	children, err := asentarTrabajosValidacion(plan.ValidationJobs(), runs, evidencia, opts, rootRunID)
	if err != nil {
		return Resultado{
			Estado:   EstadoReviewInfrastructureError,
			Mensajes: []string{fmt.Sprintf("No se pudo registrar la validación duradera: %v", err)},
			Err:      err,
		}, rootSettlement{class: agentrun.OutcomeUnavailable, detail: withChildren(layerInfrastructureDetail, children)}
	}

	if len(hallazgos) > 0 {
		return Resultado{Estado: EstadoValidationFailed, Mensajes: mensajesValidacionFallida(hallazgos)},
			rootSettlement{class: agentrun.OutcomeFailure, detail: withChildren(layerValidationDetail, children)}
	}

	opcionesRevision := opts.OpcionesRevision
	opcionesRevision.FabricaRefutador = opts.FabricaRefutador
	if opts.DurableReviewTransportFactory != nil {
		opcionesRevision.ReviewTransport = opts.DurableReviewTransportFactory(rootRunID)
	}
	resultado := traducirVeredicto(review.AuditarCommit(opts.FabricaAuditor, opts.Parallel, opcionesRevision))
	settlement := settlementPorEstado(resultado.Estado)
	if settlement.detail != "" {
		if reviewChild, resolvable := resolvableReviewChild(opts.DurableStore, plan); resolvable {
			children = append(children, reviewChild)
		}
		// Cutover follow-up resolved (ticket 11 slice 3): review candidate
		// identities are process-salted inside the shared durable transport,
		// so the orchestrator learns the actually-admitted review runs from
		// the wiring-time observer sink instead of the plan. Consuming it
		// here keeps the machine-parseable enumeration equal to the
		// persisted ParentRunID scan on every failing layer.
		if opts.DurableReviewChildren != nil {
			children = append(children, opts.DurableReviewChildren()...)
		}
		settlement.detail = withChildren(settlement.detail, children)
	}
	return resultado, settlement
}

// asentarTrabajosValidacion settles every validation logical job through the
// execution controller, deterministically per exit status, each bound to its
// RecordValidationEvidence serialization. Every admitted job run carries the
// root run's ID as its persisted ParentRunID. Commands were already executed
// directly; this only records honest lifecycle outcomes, so any store or
// controller failure here is infrastructure. It returns the admitted child
// run IDs in profile order (a prefix of it when a settlement fails midway).
func asentarTrabajosValidacion(jobs []GateJobPlan, runs []validation.ValidationRun, evidencia []ValidationEvidence, opts Opciones, parentRunID agentrun.Identity) ([]agentrun.Identity, error) {
	capacidades := opts.OpcionesValidacion.Cfg.Validation.Capabilities
	// A delegated profile yields narrative runs without planned validation
	// jobs; clamp keeps the mapping positional and never invents settlements.
	limite := len(jobs)
	if len(runs) < limite {
		limite = len(runs)
	}
	children := make([]agentrun.Identity, 0, limite)
	for index := 0; index < limite; index++ {
		run := runs[index]
		class := agentrun.OutcomeSuccess
		detail := "gate: validation command passed"
		if validation.Fallo(run, capacidades[run.Capability]) {
			class = agentrun.OutcomeFailure
			detail = "gate: validation command failed"
		}
		job := executedValidationJob(jobs[index], run)
		adapter := settledValidationAdapter{
			class:  class,
			detail: detail,
			output: salidaEvidencia(index, evidencia),
		}
		controller := execution.NewController(opts.DurableStore, adapter)
		handle, err := controller.Start(context.Background(), job.Request(), store.RunPolicy{
			ID:          DurableGateRunPolicyID,
			ParentRunID: string(parentRunID),
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

// settlementPorEstado maps the existing facade vocabulary onto the root
// run's terminal outcome so the failing LAYER stays distinct: validation and
// review blockers share the failure class but carry different detail texts;
// infrastructure failures (and any unknown state, which CodigoSalida also
// treats as infrastructure) settle unavailable.
func settlementPorEstado(estado string) rootSettlement {
	switch estado {
	case EstadoPass:
		return rootSettlement{class: agentrun.OutcomeSuccess}
	case EstadoValidationFailed:
		return rootSettlement{class: agentrun.OutcomeFailure, detail: layerValidationDetail}
	case EstadoCodeReviewFailed, EstadoNeedsUserReview:
		return rootSettlement{class: agentrun.OutcomeFailure, detail: layerReviewDetail}
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
