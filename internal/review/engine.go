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

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/change"
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
// (ticket 07 slice 2b): empty when the transport cannot attribute the call,
// and always empty on the legacy direct path, which has no durable invocation.
type ReviewTransport func(bundleName, dimension, prompt string, agente AuditorAgente) (string, string, error)

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
	// an alternative execution path such as the durable run controller. Nil
	// keeps the legacy direct call with its in-process transport retry.
	ReviewTransport ReviewTransport
}

// ResultadoDimension es el veredicto de una dimensión tras la auditoría.
type ResultadoDimension struct {
	Bundle    string
	Dim       string
	Perfil    string
	Resultado *DimensionResult
	Error     error
}

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
	contexto := contextoRevisor(opts.ProveedorContexto, opts.SHA, rutasRevision)
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
				agente, perfil, err := fabrica(bundle, dimension)
				rd.Perfil = perfil
				if err != nil {
					rd.Error = err
					rd.Resultado = &DimensionResult{Dim: dimension, Verdict: VerdictUnavailable, Reason: err.Error()}
				} else {
					opciones := opts
					opciones.RutasContexto = rutasRevision
					rd.Resultado, rd.Error = auditarConAgente(agente, bundle, dimension, opciones, contexto)
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
	refutarHallazgosCriticos(resultado.Dims, opts.FabricaRefutador, opts.SHA, rutasRevision, opts.LeerContenidoSnapshot, opts.ReviewTransport)
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
// it downgrades a confirmed semantic CRITICAL blocker to refuted — so on the
// durable path it must flow through the same admitted invocation envelopes as
// dimension reviews. When transport is non-nil every refuter call is admitted
// through it; nil keeps the legacy direct call byte-identical (the
// review.durable_runs=false rollback path). A transport rejection surfaces as
// an error, which preserves the original blocker exactly like any other
// unavailable refuter answer.
func refutarHallazgosCriticos(dimensiones []ResultadoDimension, fabrica FabricaRefutador, sha string, paths []string, leerSnapshot SnapshotReader, transport ReviewTransport) {
	if fabrica == nil {
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
			revisor, ok := refutador.(auditorConHerramientasRestringidas)
			if !ok {
				continue
			}
			prompt := construirPromptRefutacion(sha, dimension.Dim, *finding)
			var salida string
			var invocacion string
			if transport != nil {
				// Durable path: admitted envelope flow. The refuter answer
				// influences verdicts, so it may never bypass admission.
				salida, invocacion, err = transport("refutation", dimension.Dim, prompt, refutador)
			} else {
				// Legacy rollback path (review.durable_runs=false): direct
				// call, byte-identical to pre-R10 behavior.
				salida, err = revisor.EjecutarRevision(prompt, sha, paths)
			}
			if err != nil {
				continue
			}
			var respuesta respuestaRefutador
			if json.Unmarshal([]byte(salida), &respuesta) != nil || !respuesta.Refuted || strings.TrimSpace(respuesta.Reason) == "" {
				continue
			}
			rango, ok := validarEvidenciaRefutacion(leerSnapshot, sha, *finding, respuesta)
			if !ok {
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
			// refutation stream. The legacy nil-transport path passes an
			// empty identity and findings stay untouched.
			refutarHallazgoV2(dimension.Resultado.Hallazgos, *finding, respuesta, invocacion)
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
		// R10 L1). Empty on the legacy path keeps serialized shapes stable.
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

// auditarConAgente ejecuta el prompt (con la ronda extra de --answer si el
// agente pide aclaraciones) y parsea el JSONL del agente. Cuando la llamada
// pasó por un transporte que reporta su identidad de invocación, esa identidad
// viaja al resultado de la dimensión y a cada hallazgo (ticket 07 slice 2b):
// es metadato aditivo de procedencia, nunca entrada del fingerprint.
func auditarConAgente(agente AuditorAgente, bundle ReviewBundle, dimension string, opts OpcionesAuditoria, contexto string) (*DimensionResult, error) {
	ejecutar := func(prompt string) (string, error) {
		if restringido, ok := agente.(auditorConHerramientasRestringidas); ok {
			return restringido.EjecutarRevision(prompt, opts.SHA, opts.RutasContexto)
		}
		return "", ErrRestrictedRequired
	}
	salida, invocacion, err := invokeReview(opts, bundle, dimension, agente, ejecutar, construirPromptConContexto(bundle, dimension, opts.Mensaje, opts.Diff, "", contexto, opts.RutasContexto))
	if err != nil {
		return &DimensionResult{Dim: dimension, Verdict: VerdictUnavailable, Reason: err.Error()}, err
	}

	crudo, err := ParsearDimensionResult(salida)
	if err != nil {
		return &DimensionResult{Dim: dimension, Verdict: VerdictUnavailable, Reason: err.Error()}, err
	}
	crudo.InvocationID = invocacion

	// Segunda ronda solo si el agente pidió aclaraciones y el usuario respondió.
	if crudo.Verdict == VerdictQuestion && opts.Respuestas != "" {
		salida, invocacion, err = invokeReview(opts, bundle, dimension, agente, ejecutar, construirPromptConContexto(bundle, dimension, opts.Mensaje, opts.Diff, opts.Respuestas, contexto, opts.RutasContexto))
		if err != nil {
			return &DimensionResult{Dim: dimension, Verdict: VerdictUnavailable, Reason: err.Error()}, err
		}
		crudo, err = ParsearDimensionResult(salida)
		if err != nil {
			return &DimensionResult{Dim: dimension, Verdict: VerdictUnavailable, Reason: err.Error()}, err
		}
		crudo.InvocationID = invocacion
	}
	stamparSourceReview(crudo.Hallazgos)
	stamparInvocacion(crudo.Hallazgos, invocacion)
	stamparProductorEfectivo(crudo.Hallazgos, agente)
	return crudo, nil
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
// slice 2b). The legacy direct path passes an empty identity and findings
// stay untouched, so pre-existing serialized findings keep their shape.
func stamparInvocacion(hallazgos []Hallazgo, invocacion string) {
	if invocacion == "" {
		return
	}
	for i := range hallazgos {
		hallazgos[i].InvocationID = invocacion
	}
}

// invokeReview routes one reviewer call through the configured durable
// transport when present; nil keeps the legacy direct call with its transport
// retry. Both paths receive the identical prompt so parsing stays shared.
// The middle result is the producing invocation identity: whatever the
// transport reports, or empty on the legacy path.
func invokeReview(opts OpcionesAuditoria, bundle ReviewBundle, dimension string, agente AuditorAgente, ejecutar func(string) (string, error), prompt string) (string, string, error) {
	if opts.ReviewTransport != nil {
		return opts.ReviewTransport(bundle.Name, dimension, prompt, agente)
	}
	salida, err := ejecutarConReintento(ejecutar, prompt)
	return salida, "", err
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
func (r ResultadoAuditoria) String() string {
	lineas := make([]string, 0, len(r.Dims)+len(r.Skipped)+2)
	lineas = append(lineas, fmt.Sprintf("🔎 Revisión de %s: %s", r.SHA[:8], r.Veredicto))
	for _, rd := range r.Dims {
		veredicto := "?"
		if rd.Resultado != nil {
			veredicto = rd.Resultado.Verdict
		}
		lineas = append(lineas, fmt.Sprintf("  %-9s %-11s %s", rd.Dim, veredicto, rd.Perfil))
	}
	for _, skipped := range r.Skipped {
		lineas = append(lineas, fmt.Sprintf("  %-9s %-11s %s", skipped.Name, "skipped", skipped.Reason))
	}
	return "\n" + strings.Join(lineas, "\n")
}
