package review

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/acpadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/change"
	"github.com/ISeoane-Quental/vas.sentinel/internal/process"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
	"github.com/ISeoane-Quental/vas.sentinel/internal/risk"
)

// AuditorAgente es la interfaz que el motor usa para hablar con el agente.
// CLIAdapter la implementa; los tests inyectan dobles.
type AuditorAgente interface {
	EjecutarPrompt(prompt string) (string, error)
}

type auditorConHerramientasRestringidas interface {
	EjecutarRevision(prompt, sha string, paths []string) (string, error)
}

type policyAwareReviewer interface {
	ReviewWithPolicy(prompt, sha string, paths []string, policy reviewcontract.ToolPolicy) (string, error)
}

type contextualPolicyAwareReviewer interface {
	ReviewWithContextAndPolicy(ctx context.Context, prompt, sha string, paths []string, policy reviewcontract.ToolPolicy) (string, error)
}

// policyBoundReviewer carries the resolved dimension policy through a
// transport that predates the contract registry. It deliberately exposes the
// same policy-aware method as providers so the durable adapter can require and
// invoke it without importing this package.
type policyBoundReviewer struct {
	AuditorAgente
	policy reviewcontract.ToolPolicy
}

func bindPolicy(agent AuditorAgente, policy reviewcontract.ToolPolicy) policyBoundReviewer {
	return policyBoundReviewer{AuditorAgente: agent, policy: policy}
}

func (a policyBoundReviewer) ReviewWithPolicy(prompt, sha string, paths []string, _ reviewcontract.ToolPolicy) (string, error) {
	reviewer, ok := a.AuditorAgente.(policyAwareReviewer)
	if !ok {
		return "", capacidadRestringidaAusente(a.AuditorAgente)
	}
	return reviewer.ReviewWithPolicy(prompt, sha, paths, a.policy)
}

// capacidadRestringidaAusente nombra al adaptador que no puede revisar. Sin
// esto el rechazo era "restricted reviewer capability is required" a secas,
// repetido una vez por dimensión: seis mensajes idénticos que no dicen qué
// adaptador falla, por qué, ni qué hacer. El caso real es configurar
// `kind: acpx`, cuyo adaptador no implementa ReviewWithPolicy ni expone
// superficie de permisos de herramientas.
func capacidadRestringidaAusente(agente AuditorAgente) error {
	return fmt.Errorf("%w: the configured agent %T cannot review under a tool policy; configure a CLI agent (claude or opencode) for review", ErrRestrictedRequired, agente)
}

func (a policyBoundReviewer) EjecutarRevision(prompt, sha string, paths []string) (string, error) {
	return a.ReviewWithPolicy(prompt, sha, paths, a.policy)
}

func (a policyBoundReviewer) ReviewWithContextAndPolicy(ctx context.Context, prompt, sha string, paths []string, _ reviewcontract.ToolPolicy) (string, error) {
	if reviewer, ok := a.AuditorAgente.(contextualPolicyAwareReviewer); ok {
		return reviewer.ReviewWithContextAndPolicy(ctx, prompt, sha, paths, a.policy)
	}
	return a.ReviewWithPolicy(prompt, sha, paths, a.policy)
}

// ReviewWithContextAndPolicyResult forwards a rich ACP result through the
// policy binding. Legacy reviewers remain available through the string method.
func (a policyBoundReviewer) ReviewWithContextAndPolicyResult(ctx context.Context, prompt, sha string, paths []string, _ reviewcontract.ToolPolicy) (acpadapter.Result, error) {
	if reviewer, ok := a.AuditorAgente.(interface {
		ReviewWithContextResult(context.Context, string, string, []string) (acpadapter.Result, error)
	}); ok {
		return reviewer.ReviewWithContextResult(ctx, prompt, sha, paths)
	}
	output, err := a.ReviewWithContextAndPolicy(ctx, prompt, sha, paths, a.policy)
	return acpadapter.Result{Output: output}, err
}

func (a policyBoundReviewer) ReviewToolPolicy() reviewcontract.ToolPolicy { return a.policy }

// AgenteEfectivo forwards the wrapped agent's effective-responder report so
// per-finding producer stamping keeps working through the policy binding the
// durable transport requires.
func (a policyBoundReviewer) AgenteEfectivo() (agentadapter.AgenteEfectivo, bool) {
	reporta, ok := a.AuditorAgente.(agentadapter.ReportaAgenteEfectivo)
	if !ok {
		return agentadapter.AgenteEfectivo{}, false
	}
	return reporta.AgenteEfectivo()
}

// OwnedTree forwards the wrapped reviewer's live process tree so durable
// cancellation can escalate against the whole tree despite policy binding:
// hiding this capability would silently degrade aborts to cooperative-only.
func (a policyBoundReviewer) OwnedTree() *process.Tree {
	if provider, ok := a.AuditorAgente.(interface {
		OwnedTree() *process.Tree
	}); ok {
		return provider.OwnedTree()
	}
	return nil
}

// FabricaAuditor construye el agente para un bundle y una dimensión, y devuelve
// además el nombre del perfil aplicado. Inyectable en los tests.
type FabricaAuditor func(bundle ReviewBundle, dimension string) (AuditorAgente, string, error)

// FabricaRefutador constructs the cheap, independent reviewer used to challenge
// one semantic CRITICAL finding. It is invoked once for each finding.
type FabricaRefutador func() (AuditorAgente, string, error)

// ErrRestrictedRequired is returned when a dimension's agent lacks the
// tool-restricted reviewer capability. Engine and durable transport share one
// exported wording so evidence can never drift between the two paths.
var ErrRestrictedRequired = errors.New("semantic review unavailable: restricted reviewer capability is required")

// ReviewTransport routes one dimension reviewer call through an alternative
// execution path such as the durable run controller. bundleName plus dimension
// identify the logical job; prompt is fully built by the engine so parsing
// stays shared after either path. The first result is the raw reviewer output;
// the second is the producing invocation identity reported by the transport
// (ticket 07 slice 2b): empty when the transport cannot attribute the call.
type ReviewTransport func(bundleName, dimension, prompt string, agente AuditorAgente) (string, string, error)

// ReviewEvidence carries the durable identities of one physical reviewer
// invocation. The legacy ReviewTransport callback still returns only the
// invocation ID; this richer seam lets semantic finalization reach the real
// durable run without deriving a run ID.
type ReviewEvidence struct {
	RunID        string
	InvocationID string
}

// ReviewTransportWithEvidence is the durable transport seam. It preserves
// evidence identities even when the provider returns a terminal error after
// producing partial output.
type ReviewTransportWithEvidence func(bundleName, dimension, prompt string, agente AuditorAgente) (string, ReviewEvidence, error)

// MetricsFinalizer is called once for every physically admitted durable review
// invocation, after its semantic disposition is known. Empty failureClass
// means the invocation completed without a semantic failure.
type MetricsFinalizer func(runID, invocationID, failureClass, detail string) error

// OpcionesAuditoria define un trabajo de auditoría sobre un commit.
type OpcionesAuditoria struct {
	SHA                            string
	Mensaje                        string
	Diff                           string
	Bundles                        []ReviewBundle
	Budget                         ReviewBudget
	Respuestas                     string           // --answer: aclaraciones del usuario (1 ronda extra)
	PerfilOverride                 string           // --profile: fuerza un perfil sobre el mapa
	OnDimension                    func(dim string) // opcional: avisa cuando arranca cada dimensión
	ProveedorContexto              ContextProvider
	RutasContexto                  []string
	FabricaRefutador               FabricaRefutador
	LeerContenidoSnapshot          SnapshotReader
	DescriptionSimilarityThreshold float64
	// HallazgosDeterministas are already-projected review.Hallazgo{Source:
	// SourceValidation} findings (e.g. from a failed lint/build/test command)
	// that supersede an equivalent semantic finding in the same location
	// (T6.2). Empty by default: the caller decides when both sources should
	// coexist in the same report.
	HallazgosDeterministas []Hallazgo
	// ReviewTransport, when set, routes each dimension's reviewer call through
	// an alternative execution path such as the durable run controller.
	// Production wiring always supplies the admitted durable transport;
	// nil remains only as the engine-level injection seam for direct fixtures.
	ReviewTransport ReviewTransport
	// ReviewTransportWithEvidence is the preferred durable seam. ReviewTransport
	// remains supported for direct fixtures and historical callers.
	ReviewTransportWithEvidence ReviewTransportWithEvidence
	// FinalizeMetrics is the owner-side callback for one physical run's final
	// semantic disposition. It is deliberately provider-neutral to avoid a
	// review-to-store import cycle.
	FinalizeMetrics MetricsFinalizer
	// NetUnit* relabel the prompt as a NET-unit audit (T8.3).
	NetUnitLabel   string
	NetUnitHistory string
}

// ResultadoDimension es el veredicto de una dimensión tras la auditoría.
type ResultadoDimension struct {
	Bundle    string
	Dim       string
	Perfil    string
	Resultado *DimensionResult
	Error     error
}

// ProviderExecutionFailure distinguishes provider invocation failures from
// deterministic semantic-output errors while preserving errors.Is behavior.
type ProviderExecutionFailure struct {
	Err error
}

func (e *ProviderExecutionFailure) Error() string { return e.Err.Error() }
func (e *ProviderExecutionFailure) Unwrap() error { return e.Err }

// DimensionReviewRequest is the deep seam for one resolved dimension review.
type DimensionReviewRequest struct {
	Agent    AuditorAgente
	Bundle   ReviewBundle
	Contract reviewcontract.DimensionContract
	Options  OpcionesAuditoria
	Context  string
}

// DimensionReviewer owns prompt construction and semantic answer validation.
type DimensionReviewer struct{}

// ResultadoAuditoria agrega el veredicto global del commit.
type ResultadoAuditoria struct {
	SHA       string
	Dims      []ResultadoDimension
	Veredicto string // ok | warn | block | question | unavailable
	Preguntas []AgentQuestion
	Skipped   []SkippedBundle
	Findings  []Hallazgo
	// CauseGroups groups distinct Findings that share a common root cause
	// (T6.3). It is a non-destructive view: it never merges or drops the
	// individual findings in Findings, it only groups references to them.
	CauseGroups []CauseGroup
}

const (
	BundleCorrectness     = "correctness"
	BundleQuality         = "quality"
	BundleSecurity        = "security"
	BundleContracts       = "contracts_compatibility"
	BundleConcurrencyData = "concurrency_data"
	EffortLow             = "low"
	PriorityRequired      = 1
	PriorityOptional      = 2
)

// ReviewBundle is one planned semantic-review agent and its dimensions.
type ReviewBundle struct {
	Name       string
	Dimensions []string
	Effort     string
	Priority   int
	Cost       int
}

// ReviewBudget limits the optional review bundles scheduled for one audit.
type ReviewBudget struct {
	MaxCost     int
	MaxDuration time.Duration
	Clock       func() time.Time
}

// SkippedBundle records a planned bundle that could not run within the budget.
type SkippedBundle struct {
	Name   string
	Reason string
}

// ReviewPlan is the risk-derived set of review bundles for a complete profile.
type ReviewPlan struct {
	Risk            risk.Resultado
	Characteristics []change.Caracteristica
	Bundles         []ReviewBundle
}

// BundlesForRisk selects the review agents from the shared risk profile.
// Style is intentionally absent: deterministic lint owns it.
func BundlesForRisk(resultado risk.Resultado, caracteristicas []change.Caracteristica) []ReviewBundle {
	bundles := []ReviewBundle{
		{Name: BundleCorrectness, Dimensions: []string{DimLogic, DimSpec, DimTests}, Priority: PriorityRequired, Cost: 1},
	}
	switch resultado.Nivel {
	case risk.NivelNone:
		return nil
	case risk.NivelLow:
		bundles[0].Effort = EffortLow
		return bundles
	case risk.NivelStandard:
		return append(bundles, ReviewBundle{Name: BundleQuality, Dimensions: []string{DimDesign}, Priority: PriorityRequired, Cost: 1})
	case risk.NivelElevated:
		return append(bundles,
			ReviewBundle{Name: BundleQuality, Dimensions: []string{DimDesign}, Priority: PriorityRequired, Cost: 1},
			ReviewBundle{Name: BundleSecurity, Dimensions: []string{DimSecurity}, Priority: PriorityRequired, Cost: 1})
	case risk.NivelHigh:
		bundles = append(bundles,
			ReviewBundle{Name: BundleQuality, Dimensions: []string{DimDesign}, Priority: PriorityRequired, Cost: 1},
			ReviewBundle{Name: BundleSecurity, Dimensions: []string{DimSecurity}, Priority: PriorityRequired, Cost: 1})
		if caracteristicaPresente(caracteristicas, "public_api") || caracteristicaPresente(caracteristicas, "cross_module") {
			bundles = append(bundles, ReviewBundle{Name: BundleContracts, Dimensions: []string{DimSpec}, Priority: PriorityOptional, Cost: 1})
		}
		if caracteristicaPresente(caracteristicas, "concurrency") || caracteristicaPresente(caracteristicas, "database") {
			bundles = append(bundles, ReviewBundle{Name: BundleConcurrencyData, Dimensions: []string{DimLogic}, Priority: PriorityOptional, Cost: 1})
		}
		return bundles
	default:
		return BundlesForRisk(risk.Resultado{Nivel: risk.NivelHigh}, caracteristicas)
	}
}

// PlanForProfile derives deterministic risk bundles from a complete change profile.
func PlanForProfile(profile change.ChangeProfile, paths []string) ReviewPlan {
	characteristics := change.DetectarCaracteristicas(change.EntradaCaracteristicas{Symbols: profile.Symbols, Rutas: paths})
	riskProfile := risk.Evaluar(profile, characteristics)
	return ReviewPlan{Risk: riskProfile, Characteristics: characteristics, Bundles: BundlesForRisk(riskProfile, characteristics)}
}

func caracteristicaPresente(caracteristicas []change.Caracteristica, nombre string) bool {
	for _, caracteristica := range caracteristicas {
		if caracteristica.Nombre == nombre && caracteristica.Estado == change.CaracteristicaPresente {
			return true
		}
	}
	return false
}

// AuditarCommit audita un commit contra sus dimensiones con un semáforo de
// parallel trabajos concurrentes. No toca el ledger: el llamador persiste la
// revisión. La degradación es tipada: error de ejecución o de parseo se
// convierte en veredicto unavailable con razón, nunca en fallo del motor.
func AuditarCommit(fabrica FabricaAuditor, parallel int, opts OpcionesAuditoria) ResultadoAuditoria {
	resultado := ResultadoAuditoria{SHA: opts.SHA}
	rutasRevision := rutasRevisionSeguras(opts.RutasContexto)
	reviewContext := contextoRevisor(opts.ProveedorContexto, opts.SHA, rutasRevision)
	if parallel < 1 {
		parallel = 1
	}

	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	mutex := &sync.Mutex{}

	clock := opts.Budget.Clock
	if clock == nil {
		clock = time.Now
	}
	started := clock()
	cost := 0
	scheduledBundles := make(map[string]bool)
	run := func(bundle ReviewBundle) bool {
		if scheduledBundles[bundle.Name] || len(bundle.Dimensions) == 0 {
			return false
		}
		scheduledBundles[bundle.Name] = true
		cost += bundle.Cost
		for _, dim := range bundle.Dimensions {
			wg.Add(1)
			go func(bundle ReviewBundle, dimension string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				if opts.OnDimension != nil {
					opts.OnDimension(dimension)
				}

				rd := ResultadoDimension{Bundle: bundle.Name, Dim: dimension}
				contract, contractErr := reviewcontract.Lookup(dimension)
				if contractErr != nil {
					rd.Error = contractErr
					rd.Resultado = &DimensionResult{Dim: dimension, Verdict: VerdictUnavailable, Reason: contractErr.Error()}
				} else {
					agent, profile, err := fabrica(bundle, dimension)
					rd.Perfil = profile
					if err != nil {
						rd.Error = err
						rd.Resultado = &DimensionResult{Dim: dimension, Verdict: VerdictUnavailable, Reason: err.Error()}
					} else {
						options := opts
						options.RutasContexto = rutasRevision
						rd.Resultado, rd.Error = (DimensionReviewer{}).Review(context.Background(), DimensionReviewRequest{Agent: agent, Bundle: bundle, Contract: contract, Options: options, Context: reviewContext})
					}
				}
				rd.Resultado.Bundle = bundle.Name

				mutex.Lock()
				resultado.Dims = append(resultado.Dims, rd)
				mutex.Unlock()
			}(bundle, dim)
		}
		return true
	}
	for _, bundle := range opts.Bundles {
		if bundle.Priority == PriorityOptional {
			continue
		}
		if bundle.Cost <= 0 {
			bundle.Cost = 1
		}
		run(bundle)
	}
	wg.Wait()
	for _, bundle := range opts.Bundles {
		if bundle.Priority != PriorityOptional {
			continue
		}
		if bundle.Cost <= 0 {
			bundle.Cost = 1
		}
		if scheduledBundles[bundle.Name] || len(bundle.Dimensions) == 0 {
			resultado.Skipped = append(resultado.Skipped, SkippedBundle{Name: bundle.Name, Reason: "duplicate_bundle"})
			continue
		}
		if (opts.Budget.MaxCost > 0 && cost+bundle.Cost > opts.Budget.MaxCost) || (opts.Budget.MaxDuration > 0 && !clock().Before(started.Add(opts.Budget.MaxDuration))) {
			resultado.Skipped = append(resultado.Skipped, SkippedBundle{Name: bundle.Name, Reason: "budget_exhausted"})
			continue
		}
		run(bundle)
	}
	wg.Wait()
	if opts.ReviewTransportWithEvidence != nil {
		refutarHallazgosCriticosConEvidencia(resultado.Dims, opts.FabricaRefutador, opts.SHA, opts.LeerContenidoSnapshot, opts.ReviewTransportWithEvidence, opts.FinalizeMetrics)
	} else {
		refutarHallazgosCriticos(resultado.Dims, opts.FabricaRefutador, opts.SHA, opts.LeerContenidoSnapshot, legacyReviewTransport(opts))
	}
	var findings []Hallazgo
	for _, dimension := range resultado.Dims {
		if dimension.Resultado != nil {
			findings = append(findings, dimension.Resultado.Hallazgos...)
		}
	}
	findings = SupersedeDeterministicFindings(findings, opts.HallazgosDeterministas)
	resultado.Findings = append(aggregateFindings(findings, opts.DescriptionSimilarityThreshold), opts.HallazgosDeterministas...)
	resultado.CauseGroups = correlateFindingsByCause(resultado.Findings, opts.DescriptionSimilarityThreshold)

	resultado.Veredicto, resultado.Preguntas = veredictoGlobal(resultado.Dims)
	return resultado
}

type respuestaRefutador struct {
	Refuted   bool   `json:"refuted"`
	Reason    string `json:"reason"`
	SHA       string `json:"sha"`
	File      string `json:"file"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
	Evidence  string `json:"evidence"`
}

// refutarHallazgosCriticos uses an independent SHA-bound restricted refuter once
// per semantic CRITICAL finding. Any unavailable or invalid answer preserves
// the original blocker.
//
// Ticket 12 slice 1 (envelope totality): a refutation CAN flip a verdict —
// it downgrades a confirmed semantic CRITICAL blocker to refuted — so it must
// flow through the same admitted invocation envelopes as dimension reviews:
// every refuter call is admitted through the transport, which production
// wiring always supplies (ticket 13, R11: the nil-transport rollback branch
// was removed with the review.durable_runs switch). A transport rejection
// surfaces as an error, which preserves the original blocker exactly like any
// other unavailable refuter answer.
func refutarHallazgosCriticos(dimensiones []ResultadoDimension, fabrica FabricaRefutador, sha string, leerSnapshot SnapshotReader, transport ReviewTransport) {
	if transport == nil {
		return
	}
	refutarHallazgosCriticosConEvidencia(dimensiones, fabrica, sha, leerSnapshot, func(bundle, dimension, prompt string, agente AuditorAgente) (string, ReviewEvidence, error) {
		output, invocation, err := transport(bundle, dimension, prompt, agente)
		return output, ReviewEvidence{InvocationID: invocation}, err
	}, nil)
}

func refutarHallazgosCriticosConEvidencia(dimensiones []ResultadoDimension, fabrica FabricaRefutador, sha string, leerSnapshot SnapshotReader, transport ReviewTransportWithEvidence, finalizer MetricsFinalizer) {
	if fabrica == nil || transport == nil {
		return
	}
	if leerSnapshot == nil {
		leerSnapshot = leerContenidoSnapshot
	}
	for _, dimension := range dimensiones {
		if dimension.Resultado == nil {
			continue
		}
		for i := range dimension.Resultado.Findings {
			finding := &dimension.Resultado.Findings[i]
			if finding.Severity != SevCritical || (finding.Source != "" && finding.Source != SourceReview) {
				continue
			}
			finding.Source = SourceReview
			finding.Status = StatusConfirmed
			refutador, _, err := fabrica()
			if err != nil {
				continue
			}
			contract, err := reviewcontract.Lookup(dimension.Dim)
			if err != nil {
				continue
			}
			if _, ok := refutador.(policyAwareReviewer); !ok {
				continue
			}
			prompt := construirPromptRefutacion(sha, dimension.Dim, *finding)
			// Admitted envelope flow: the refuter answer influences verdicts,
			// so it may never bypass admission.
			salida, evidence, err := transport("refutation", dimension.Dim, prompt, bindPolicy(refutador, contract.ToolPolicy))
			if err != nil {
				var identity metricsEvidenceError
				if errors.As(err, &identity) {
					evidence.RunID, evidence.InvocationID, _, _ = identity.MetricsEvidence()
				}
				if finalizer != nil && evidence.RunID != "" && evidence.InvocationID != "" {
					_ = finalizer(evidence.RunID, evidence.InvocationID, "provider_error", err.Error())
				}
				continue
			}
			invocacion := evidence.InvocationID
			finalize := func(class, detail string) {
				if finalizer != nil && evidence.RunID != "" && evidence.InvocationID != "" {
					_ = finalizer(evidence.RunID, evidence.InvocationID, class, detail)
				}
			}
			var respuesta respuestaRefutador
			if err := json.Unmarshal([]byte(salida), &respuesta); err != nil || !respuesta.Refuted || strings.TrimSpace(respuesta.Reason) == "" {
				finalize("invalid_output", "refuter response did not satisfy the contract")
				continue
			}
			rango, ok := validarEvidenciaRefutacion(leerSnapshot, sha, *finding, respuesta)
			if !ok {
				finalize("invalid_output", "refuter evidence did not match the immutable snapshot")
				continue
			}
			finding.Status = StatusRefuted
			finding.RefutationReason = respuesta.Reason
			finding.RefutationEvidence = respuesta.Evidence
			finding.RefutationLineStart = respuesta.LineStart
			finding.RefutationLineEnd = respuesta.LineEnd
			finding.RefutationRangeHash = rango
			dimension.Resultado.RefutedCritical = true
			// Ticket 13 hardening pool (R10 L1): the admitted refutation is a
			// distinct durable invocation that flipped a verdict, so its
			// identity travels into the downgraded persisted finding exactly
			// like the dimension transports stamp theirs (ticket 07 slice
			// 2b). Prune provenance scanning then naturally protects the
			// refutation stream.
			refutarHallazgoV2(dimension.Resultado.Hallazgos, *finding, respuesta, invocacion)
			finalize("", "")
		}
		if puedeDegradarBloque(*dimension.Resultado) {
			dimension.Resultado.Verdict = VerdictWarn
		}
	}
}

func validarEvidenciaRefutacion(leerSnapshot SnapshotReader, sha string, finding ReviewFinding, respuesta respuestaRefutador) (string, bool) {
	rutasSeguras := rutasRevisionSeguras([]string{finding.File})
	if len(rutasSeguras) != 1 || respuesta.SHA != sha || respuesta.File != rutasSeguras[0] || respuesta.LineStart <= 0 || respuesta.LineEnd < respuesta.LineStart || respuesta.LineEnd-respuesta.LineStart >= 20 || int(finding.Line) < respuesta.LineStart || int(finding.Line) > respuesta.LineEnd {
		return "", false
	}
	contenido, err := leerSnapshot(sha, rutasSeguras[0])
	if err != nil {
		return "", false
	}
	lineas := strings.Split(contenido, "\n")
	if respuesta.LineEnd > len(lineas) {
		return "", false
	}
	extracto := strings.Join(lineas[respuesta.LineStart-1:respuesta.LineEnd], "\n")
	evidencia := strings.TrimSpace(respuesta.Evidence)
	if len(evidencia) < 12 || evidencia != strings.TrimSpace(extracto) {
		return "", false
	}
	suma := sha256.Sum256([]byte(extracto))
	return fmt.Sprintf("%x", suma), true
}

func refutarHallazgoV2(hallazgos []Hallazgo, finding ReviewFinding, respuesta respuestaRefutador, invocacion string) {
	for i := range hallazgos {
		hallazgo := &hallazgos[i]
		if hallazgo.Severity != SevCritical || hallazgo.Location.Archivo != finding.File || hallazgo.Location.LineaInicio != int(finding.Line) || hallazgo.Description != finding.Description {
			continue
		}
		hallazgo.Source = SourceReview
		hallazgo.Status = StatusRefuted
		hallazgo.RefutationReason = respuesta.Reason
		hallazgo.RefutationEvidence = respuesta.Evidence
		hallazgo.RefutationLineStart = respuesta.LineStart
		hallazgo.RefutationLineEnd = respuesta.LineEnd
		hallazgo.RefutationRangeHash = finding.RefutationRangeHash
		// Parity with the dimension transports: the admitted refutation
		// invocation becomes the finding's recorded provenance (ticket 13,
		// R10 L1). An empty identity keeps serialized shapes stable for
		// transports that cannot attribute the call.
		if invocacion != "" {
			hallazgo.InvocationID = invocacion
		}
		return
	}
}

func tieneCriticalConfirmado(findings []ReviewFinding) bool {
	for _, finding := range findings {
		if finding.Severity == SevCritical && finding.Status != StatusRefuted {
			return true
		}
	}
	return false
}

func tieneHallazgoCriticalConfirmado(hallazgos []Hallazgo) bool {
	for _, hallazgo := range hallazgos {
		if hallazgo.Severity == SevCritical && hallazgo.Status != StatusRefuted {
			return true
		}
	}
	return false
}

func puedeDegradarBloque(resultado DimensionResult) bool {
	return resultado.Verdict == VerdictBlock &&
		resultado.RefutedCritical &&
		strings.TrimSpace(resultado.Reason) == "" &&
		!tieneCriticalConfirmado(resultado.Findings) &&
		!tieneHallazgoCriticalConfirmado(resultado.Hallazgos)
}

// RutasRevisionSeguras exposes the engine's reviewer-path sanitizer so
// out-of-engine transports bind the exact same safe list the legacy path
// uses. Ticket 05 Judgment Day JD-A1: binding raw caller lists would let
// dash-prefixed, control-character, absolute, and parent-relative names
// reach reviewers unfiltered on the durable path.
func RutasRevisionSeguras(rutas []string) []string { return rutasRevisionSeguras(rutas) }

func rutasRevisionSeguras(rutas []string) []string {
	seguras := make([]string, 0, len(rutas))
	for _, ruta := range rutas {
		normalizada := strings.ReplaceAll(ruta, "\\", "/")
		limpia := path.Clean(normalizada)
		drive := len(limpia) >= 2 && limpia[1] == ':'
		if ruta == "" || path.IsAbs(limpia) || drive || limpia == "." || limpia == ".." || strings.HasPrefix(limpia, "../") || strings.HasPrefix(ruta, "-") || strings.ContainsAny(ruta, "\x00\r\n*?[]{}!") {
			continue
		}
		seguras = append(seguras, limpia)
	}
	return seguras
}

// auditWithAgent executes the prompt, including the extra --answer round when
// the agent requests clarification, then parses its JSONL. A transport-reported
// invocation identity is added to the dimension result and each finding
// (ticket 07 slice 2b) as provenance metadata, never as fingerprint input.
func auditWithAgent(agent AuditorAgente, bundle ReviewBundle, dimension string, opts OpcionesAuditoria, reviewContext string) (*DimensionResult, error) {
	contract, err := reviewcontract.Lookup(dimension)
	if err != nil {
		return &DimensionResult{Dim: dimension, Verdict: VerdictUnavailable, Reason: err.Error()}, err
	}
	return (DimensionReviewer{}).Review(context.Background(), DimensionReviewRequest{Agent: agent, Bundle: bundle, Contract: contract, Options: opts, Context: reviewContext})
}

// Review executes one resolved contract and preserves raw provider output or a
// typed provider execution failure on the returned result for internal
// diagnosis. A malformed semantic payload gets one corrective retry; a
// settled retryable provider failure gets one bounded retry in a new physical
// run, while every terminal error remains typed and lossless.
type metricsEvidenceError interface {
	MetricsEvidence() (runID, invocationID, class, detail string)
}

func finalizeInvocation(opts OpcionesAuditoria, runID, invocationID, failureClass, detail string) error {
	if opts.FinalizeMetrics == nil || runID == "" || invocationID == "" {
		return nil
	}
	return opts.FinalizeMetrics(runID, invocationID, failureClass, detail)
}

func semanticFailure(err error) (string, string) {
	var semantic *SemanticOutputError
	if !errors.As(err, &semantic) {
		return "", ""
	}
	return "invalid_output", semantic.Error()
}
func (DimensionReviewer) Review(ctx context.Context, request DimensionReviewRequest) (*DimensionResult, error) {
	if err := ctx.Err(); err != nil {
		failure := &ProviderExecutionFailure{Err: err}
		return &DimensionResult{Dim: request.Contract.Name, Verdict: VerdictUnavailable, Reason: failure.Error(), ExecutionFailure: failure}, failure
	}
	agent, bundle, opts, contract := request.Agent, request.Bundle, request.Options, request.Contract
	if _, ok := agent.(policyAwareReviewer); !ok {
		return &DimensionResult{Dim: contract.Name, Verdict: VerdictUnavailable, Reason: ErrRestrictedRequired.Error()}, ErrRestrictedRequired
	}
	ejecutar := func(prompt string) (string, error) {
		policyReviewer := agent.(policyAwareReviewer)
		return policyReviewer.ReviewWithPolicy(prompt, opts.SHA, opts.RutasContexto, contract.ToolPolicy)
	}
	finalizeProvider := func(runID, invocationID string, err error) error {
		class, detail := "", ""
		var evidenceErr metricsEvidenceError
		if errors.As(err, &evidenceErr) {
			reportedRun, reportedInvocation, reportedClass, reportedDetail := evidenceErr.MetricsEvidence()
			if reportedRun != "" {
				runID = reportedRun
			}
			if reportedInvocation != "" {
				invocationID = reportedInvocation
			}
			class, detail = reportedClass, reportedDetail
		}
		if detail == "" && err != nil {
			detail = err.Error()
		}
		return finalizeInvocation(opts, runID, invocationID, class, detail)
	}
	finalizeSemantic := func(runID, invocationID string, err error) error {
		class, detail := semanticFailure(err)
		return finalizeInvocation(opts, runID, invocationID, class, detail)
	}
	prompt := buildPromptWithContext(bundle, contract, opts.Mensaje, opts.Diff, "", request.Context, opts.RutasContexto, opts.NetUnitLabel, opts.NetUnitHistory)
	output, invocation, runID, err := invokeReview(opts, bundle, contract.Name, bindPolicy(agent, contract.ToolPolicy), ejecutar, prompt)
	if err != nil && esFalloTransitorioDeProveedor(err) {
		// The first provider failure is already a settled physical run; fold
		// it before admitting the bounded retry so no attempt disappears from
		// the durable history.
		if finalizeErr := finalizeProvider(runID, invocation, err); finalizeErr != nil {
			err = fmt.Errorf("review metrics finalization failed: %w", finalizeErr)
			failure := &ProviderExecutionFailure{Err: err}
			return &DimensionResult{Dim: contract.Name, Verdict: VerdictUnavailable, Reason: failure.Error(), ExecutionFailure: failure}, failure
		}
		output, invocation, runID, err = invokeReview(opts, bundle, contract.Name, bindPolicy(agent, contract.ToolPolicy), ejecutar, prompt)
	}
	if err != nil {
		finalizeErr := finalizeProvider(runID, invocation, err)
		if finalizeErr != nil {
			err = fmt.Errorf("review metrics finalization failed: %w", finalizeErr)
		}
		failure := &ProviderExecutionFailure{Err: err}
		return &DimensionResult{Dim: contract.Name, Verdict: VerdictUnavailable, Reason: failure.Error(), ExecutionFailure: failure}, failure
	}

	crudo, err := ParseDimensionResultForContract(output, contract)
	if shouldRetryFormat(err) {
		if finalizeErr := finalizeSemantic(runID, invocation, err); finalizeErr != nil {
			return &DimensionResult{Dim: contract.Name, Verdict: VerdictUnavailable, Reason: finalizeErr.Error(), RawProviderOutput: output}, finalizeErr
		}
		output, invocation, runID, err = invokeReview(opts, bundle, contract.Name, bindPolicy(agent, contract.ToolPolicy), ejecutar, prompt+formatRetryInstruction)
		if err != nil {
			if finalizeErr := finalizeProvider(runID, invocation, err); finalizeErr != nil {
				err = fmt.Errorf("review metrics finalization failed: %w", finalizeErr)
			}
		} else {
			crudo, err = ParseDimensionResultForContract(output, contract)
			if err != nil {
				if finalizeErr := finalizeSemantic(runID, invocation, err); finalizeErr != nil {
					err = fmt.Errorf("review metrics finalization failed: %w", finalizeErr)
				}
			}
		}
	} else if err != nil {
		if finalizeErr := finalizeSemantic(runID, invocation, err); finalizeErr != nil {
			err = fmt.Errorf("review metrics finalization failed: %w", finalizeErr)
		}
	}
	if err != nil {
		return &DimensionResult{Dim: contract.Name, Verdict: VerdictUnavailable, Reason: err.Error(), RawProviderOutput: output}, err
	}
	crudo.InvocationID = invocation
	crudo.RawProviderOutput = output

	// Segunda ronda solo si el agente pidió aclaraciones y el usuario respondió.
	if crudo.Verdict == VerdictQuestion && opts.Respuestas != "" {
		if finalizeErr := finalizeInvocation(opts, runID, invocation, "", ""); finalizeErr != nil {
			return &DimensionResult{Dim: contract.Name, Verdict: VerdictUnavailable, Reason: finalizeErr.Error(), RawProviderOutput: output}, finalizeErr
		}
		output, invocation, runID, err = invokeReview(opts, bundle, contract.Name, bindPolicy(agent, contract.ToolPolicy), ejecutar, buildPromptWithContext(bundle, contract, opts.Mensaje, opts.Diff, opts.Respuestas, request.Context, opts.RutasContexto, opts.NetUnitLabel, opts.NetUnitHistory))
		if err != nil {
			if finalizeErr := finalizeProvider(runID, invocation, err); finalizeErr != nil {
				err = fmt.Errorf("review metrics finalization failed: %w", finalizeErr)
			}
			failure := &ProviderExecutionFailure{Err: err}
			return &DimensionResult{Dim: contract.Name, Verdict: VerdictUnavailable, Reason: failure.Error(), ExecutionFailure: failure}, failure
		}
		crudo, err = ParseDimensionResultForContract(output, contract)
		if err != nil {
			if finalizeErr := finalizeSemantic(runID, invocation, err); finalizeErr != nil {
				err = fmt.Errorf("review metrics finalization failed: %w", finalizeErr)
			}
			return &DimensionResult{Dim: contract.Name, Verdict: VerdictUnavailable, Reason: err.Error(), RawProviderOutput: output}, err
		}
		crudo.InvocationID = invocation
		crudo.RawProviderOutput = output
	}
	if finalizeErr := finalizeInvocation(opts, runID, invocation, "", ""); finalizeErr != nil {
		return &DimensionResult{Dim: contract.Name, Verdict: VerdictUnavailable, Reason: finalizeErr.Error(), RawProviderOutput: output}, finalizeErr
	}
	stamparSourceReview(crudo.Hallazgos)
	stamparInvocacion(crudo.Hallazgos, invocation)
	stamparProductorEfectivo(crudo.Hallazgos, agent)
	return crudo, nil
}

// fallosPermanentesDeProveedor son los textos que prueban que repetir la
// llamada no puede cambiar nada. Se comparan por contenido porque vienen del
// stderr del proveedor, que no ofrece códigos estables.
var fallosPermanentesDeProveedor = []string{
	// Modelo inexistente o mal escrito en la configuración. Observado como
	// `"claude-opus" is not a model this version of Claude Code recognizes`.
	"is not a model",
	// La política de herramientas exigida no existe para ese proveedor: lo
	// rechaza reviewCommand antes de lanzar nada.
	"not configured for this provider",
}

// proveedorAsentadoReintentable lo implementa el error que un transporte
// devuelve cuando el run ASENTÓ durablemente con un resultado no exitoso.
//
// La distinción es la que hace seguro el reintento y la señaló la revisión que
// bloqueó la primera versión de este código: un transporte también devuelve
// fallos de ADMISIÓN y errores posteriores al envío, y repetir esos puede
// duplicar invocaciones. Un run asentado, en cambio, ya terminó y está
// registrado, así que reintentar abre un run nuevo y no duplica nada.
//
// Se declara como interfaz aquí, y no se importa el tipo concreto, porque
// internal/reviewexec ya importa este paquete: la dirección de dependencia solo
// admite que sea el transporte quien declare su error reintentable.
type proveedorAsentadoReintentable interface {
	ProviderSettledRetryable() bool
}

// esFalloTransitorioDeProveedor decide si un fallo merece el único reintento.
// Es una LISTA BLANCA: solo un run asentado con un resultado reintentable
// entra. Todo lo demás —admisión, observación, evidencia, o cualquier error del
// camino directo— se rechaza, porque no podemos probar que el proveedor no
// llegara a ejecutar.
//
// Eso también impide que este reintento se componga con el de invokeReview:
// ejecutarConReintento solo actúa cuando no hay transporte, y ese camino nunca
// produce un error asentado, así que el presupuesto sigue siendo una sola
// repetición.
//
// Un fallo de formato NO llega aquí: lo decide shouldRetryFormat, después de
// que el proveedor haya respondido.
func esFalloTransitorioDeProveedor(err error) bool {
	if err == nil {
		return false
	}
	var asentado proveedorAsentadoReintentable
	if !errors.As(err, &asentado) || !asentado.ProviderSettledRetryable() {
		return false
	}
	texto := err.Error()
	for _, permanente := range fallosPermanentesDeProveedor {
		if strings.Contains(texto, permanente) {
			return false
		}
	}
	return true
}

const formatRetryInstruction = "\n\nFORMAT RETRY: Your previous response did not satisfy the required review schema. Return only one complete BEGIN_REVIEW/END_REVIEW payload with verdict set to ok, warn, block, question, or unavailable. EVERY finding must carry a non-empty literal `evidence` quoted from the reviewed code and a `confidence` value; a single finding missing either one discards the whole response."

// shouldRetryFormat decides the ONE corrective retry a malformed semantic
// payload gets. Every SemanticOutputClass that means "the provider answered but
// the payload does not satisfy the contract" qualifies, because that is exactly
// what a corrective instruction can fix.
//
// It used to re-parse the output as a single JSON object and retry only when
// that object carried a non-empty INVALID verdict. That missed the failure that
// actually happens: a JSONL payload with a VALID verdict whose findings breach
// the evidence policy. All six canonical dimensions require literal evidence and
// confidence, and one offending finding discards the whole block, so those
// dimensions went straight to unavailable with no second chance.
//
// tool_denied is deliberately excluded: a denied tool is not a format problem,
// and repeating the prompt cannot grant permissions — it would only spend
// another provider call. Provider execution failures never reach here, because
// they are not SemanticOutputError at all.
func shouldRetryFormat(err error) bool {
	var semantic *SemanticOutputError
	if !errors.As(err, &semantic) {
		return false
	}
	switch semantic.Class {
	case SemanticOutputMissingPayload, SemanticOutputMalformedJSON, SemanticOutputSchemaInvalid:
		return true
	default:
		return false
	}
}

// stamparSourceReview marca todo Hallazgo que sale de la revisión semántica
// con Source: SourceReview, con la misma autoridad de origen que
// stamparProductorEfectivo ya aplica al Producer: el prompt no le pide al
// modelo declarar su propia procedencia (T6.2 la necesita para distinguir
// qué hallazgo puede ser suplantado por uno determinista), así que
// depender de que el JSON la incluya dejaría el campo vacío en la práctica.
func stamparSourceReview(hallazgos []Hallazgo) {
	for i := range hallazgos {
		hallazgos[i].Source = SourceReview
	}
}

func stamparProductorEfectivo(hallazgos []Hallazgo, agente AuditorAgente) {
	reporta, ok := agente.(agentadapter.ReportaAgenteEfectivo)
	if !ok {
		return
	}
	efectivo, ok := reporta.AgenteEfectivo()
	if !ok || efectivo.Vacio() {
		return
	}
	productor := Productor{
		Agente:   efectivo.Binario,
		Binario:  efectivo.Binario,
		Modelo:   efectivo.Modelo,
		Esfuerzo: efectivo.Esfuerzo,
	}
	for i := range hallazgos {
		hallazgos[i].Producer = productor
		if hallazgos[i].EvidenceSet == nil {
			continue
		}
		evidencias := make([]FindingEvidence, len(hallazgos[i].EvidenceSet.Values))
		for j, evidencia := range hallazgos[i].EvidenceSet.Values {
			evidencia.Producer = productor
			evidencias[j] = evidencia
		}
		hallazgos[i].EvidenceSet = &FindingEvidenceSet{Values: evidencias}
	}
}

// stamparInvocacion records the producing durable invocation on every finding
// of a dimension result, only when the transport reported one (ticket 07
// slice 2b). An empty identity leaves findings untouched, so pre-existing
// serialized findings keep their shape.
func stamparInvocacion(hallazgos []Hallazgo, invocacion string) {
	if invocacion == "" {
		return
	}
	for i := range hallazgos {
		hallazgos[i].InvocationID = invocacion
	}
}

func legacyReviewTransport(opts OpcionesAuditoria) ReviewTransport {
	if opts.ReviewTransport != nil {
		return opts.ReviewTransport
	}
	if opts.ReviewTransportWithEvidence == nil {
		return nil
	}
	return func(bundleName, dimension, prompt string, agent AuditorAgente) (string, string, error) {
		output, evidence, err := opts.ReviewTransportWithEvidence(bundleName, dimension, prompt, agent)
		return output, evidence.InvocationID, err
	}
}

// invokeReview routes one reviewer call through the configured durable
// transport when present; nil keeps the direct restricted call with its
// transport retry (engine-level injection seam only: production wiring always
// supplies the admitted transport since ticket 13, R11). Both paths receive
// the identical prompt so parsing stays shared. The middle result is the
// producing invocation identity: whatever the transport reports, or empty on
// the direct path.
func invokeReview(opts OpcionesAuditoria, bundle ReviewBundle, dimension string, agente AuditorAgente, ejecutar func(string) (string, error), prompt string) (string, string, string, error) {
	if opts.ReviewTransportWithEvidence != nil {
		output, evidence, err := opts.ReviewTransportWithEvidence(bundle.Name, dimension, prompt, agente)
		if err != nil {
			var identity metricsEvidenceError
			if errors.As(err, &identity) {
				runID, invocationID, _, _ := identity.MetricsEvidence()
				if runID != "" {
					evidence.RunID = runID
				}
				if invocationID != "" {
					evidence.InvocationID = invocationID
				}
			}
		}
		return output, evidence.InvocationID, evidence.RunID, err
	}
	if opts.ReviewTransport != nil {
		output, invocation, err := opts.ReviewTransport(bundle.Name, dimension, prompt, agente)
		return output, invocation, "", err
	}
	output, err := ejecutarConReintento(ejecutar, prompt)
	return output, "", "", err
}

func ejecutarConReintento(ejecutar func(string) (string, error), prompt string) (string, error) {
	salida, err := ejecutar(prompt)
	if err != nil && esErrorTransporte(err) {
		return ejecutar(prompt)
	}
	return salida, err
}

func esErrorTransporte(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary())
}

type Relation string
type Reason string

const (
	RelationAffectedTest Relation = "affected_test"
	ReasonCodeGraph      Reason   = "codegraph_dependency"
)

type Reference struct {
	Path     string   `json:"path"`
	Relation Relation `json:"relation"`
	Reason   Reason   `json:"reason"`
}

type ContextProvider interface {
	Nombre() string
	Contexto(sha string, rutas []string) ([]Reference, error)
}

func contextoRevisor(proveedor ContextProvider, sha string, rutas []string) string {
	if proveedor == nil {
		return ""
	}
	referencias, err := proveedor.Contexto(sha, rutas)
	if err != nil {
		return ""
	}
	datos, err := json.Marshal(referencias)
	if err != nil || len(referencias) == 0 {
		return ""
	}
	return string(datos)
}

// veredictoGlobal decide el veredicto del commit: block manda, luego question
// (sin resolver), luego unavailable, luego warn, y por último ok.
func veredictoGlobal(dims []ResultadoDimension) (string, []AgentQuestion) {
	peor := VerdictOK
	var preguntas []AgentQuestion
	for _, rd := range dims {
		if rd.Resultado == nil {
			continue
		}
		switch rd.Resultado.Verdict {
		case VerdictBlock:
			peor = VerdictBlock
		case VerdictQuestion:
			preguntas = append(preguntas, rd.Resultado.Questions...)
			if peor != VerdictBlock {
				peor = VerdictQuestion
			}
		case VerdictUnavailable:
			if peor == VerdictOK || peor == VerdictWarn {
				peor = VerdictUnavailable
			}
		case VerdictWarn:
			if peor == VerdictOK {
				peor = VerdictWarn
			}
		}
	}
	return peor, preguntas
}

// String resume la auditoría para la salida en consola.
// Los unavailable siempre muestran su reason y su clase (admission vs
// infrastructure) aunque el veredicto global sea block: sin eso un block
// oculta que otras dimensiones ni siquiera llegaron a auditarse y el operador
// no ve el motivo real.
func (r ResultadoAuditoria) String() string {
	lineas := make([]string, 0, len(r.Dims)+len(r.Skipped)+6)
	shaCorto := r.SHA
	if len(shaCorto) > 8 {
		shaCorto = shaCorto[:8]
	}
	lineas = append(lineas, fmt.Sprintf("🔎 Revisión de %s: %s", shaCorto, r.Veredicto))
	for _, rd := range r.Dims {
		veredicto := "?"
		reason := ""
		if rd.Resultado != nil {
			veredicto = rd.Resultado.Verdict
			reason = rd.Resultado.Reason
		}
		if reason == "" && rd.Error != nil {
			reason = rd.Error.Error()
		}
		if rd.Resultado == nil && rd.Error != nil && veredicto == "?" {
			veredicto = VerdictUnavailable
		}
		lineas = append(lineas, fmt.Sprintf("  %-9s %-11s %s", rd.Dim, veredicto, rd.Perfil))
		if veredicto == VerdictUnavailable && strings.TrimSpace(reason) != "" {
			class := "infrastructure"
			if strings.HasPrefix(reason, "admission: ") {
				class = "admission"
			}
			lineas = append(lineas, fmt.Sprintf("    ↳ reason=%q class=%s", reason, class))
		}
	}
	for _, skipped := range r.Skipped {
		lineas = append(lineas, fmt.Sprintf("  %-9s %-11s %s", skipped.Name, "skipped", skipped.Reason))
	}
	if r.Veredicto == VerdictBlock {
		var unav []string
		for _, rd := range r.Dims {
			isUnav := false
			if rd.Resultado != nil && rd.Resultado.Verdict == VerdictUnavailable {
				isUnav = true
			} else if rd.Resultado == nil && rd.Error != nil {
				isUnav = true
			}
			if isUnav {
				name := rd.Dim
				if name == "" && rd.Resultado != nil {
					name = rd.Resultado.Dim
				}
				if name == "" {
					name = "?"
				}
				unav = append(unav, name)
			}
		}
		if len(unav) > 0 {
			lineas = append(lineas, fmt.Sprintf("  ⚠️  %d dimension(es) unavailable (%s) — ver reason arriba (no oculta el block, pero explica cobertura incompleta)", len(unav), strings.Join(unav, ", ")))
		}
	}
	return "\n" + strings.Join(lineas, "\n")
}
