// Package gate orquesta el subcomando "gate" (T1.7): un único punto de
// entrada consolidado que exige validación en verde (T1.6) ANTES de invocar
// la revisión semántica (internal/review), en vez de que cada gancho del
// ciclo de vida (pre-commit, pre-push, pr) reimplemente su propio orden.
//
// La lógica de negocio vive aquí (nunca en cmd/, ver nota de arquitectura de
// la ficha): cmd/sentinel/comandos_gate.go solo parsea flags, resuelve las
// costuras reales (git, agentadapter) y imprime Resultado.
package gate

import (
	"fmt"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewexec"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

// Estados terminales de gate: vocabulario cerrado de la ficha de T1.7.
const (
	EstadoPass                      = "PASS"
	EstadoValidationFailed          = "VALIDATION_FAILED"
	EstadoCodeReviewFailed          = "CODE_REVIEW_FAILED"
	EstadoNeedsUserReview           = "NEEDS_USER_REVIEW"
	EstadoReviewInfrastructureError = "REVIEW_INFRASTRUCTURE_ERROR"
)

// CodigoSalida traduce Resultado.Estado al exit code exacto pedido por la
// ficha: 0 PASS, 1 VALIDATION_FAILED, 2 NEEDS_USER_REVIEW,
// 4 REVIEW_INFRASTRUCTURE_ERROR. Un estado desconocido NUNCA se trata como
// PASS: un gate cuyo propósito es bloquear no puede fallar abierto ante un
// valor que este mismo paquete no reconoce (p. ej. un estado nuevo añadido en
// EjecutarGate y olvidado aquí), así que se traduce al mismo código que
// REVIEW_INFRASTRUCTURE_ERROR (4): un estado no reconocido es un problema de
// la propia infraestructura del gate, no una validación superada.
func CodigoSalida(estado string) int {
	switch estado {
	case EstadoPass:
		return 0
	case EstadoValidationFailed:
		return 1
	case EstadoCodeReviewFailed:
		return 1
	case EstadoNeedsUserReview:
		return 2
	case EstadoReviewInfrastructureError:
		return 4
	default:
		return 4
	}
}

// Resultado es la salida de EjecutarGate: el estado final y los mensajes ya
// redactados para que el comando los imprima tal cual, sin añadir lógica de
// presentación en cmd/.
type Resultado struct {
	Estado   string
	Mensajes []string
	// Err carries a typed error produced by the durable orchestration when a
	// plan, admission, or settlement seam fails (R9 slice 1). Facade text
	// travels in Estado/Mensajes; Err exists for callers that need the typed
	// cause.
	Err error
}

// Opciones configura una ejecución de gate. Las costuras EjecutarValidacion y
// FabricaAuditor son las que T0.x/T1.6 y el motor de review.AuditarCommit ya
// exponen como inyectables: gate no añade una capa de indirección nueva,
// reutiliza la que ya existe para poder testear sin procesos ni agentes
// reales.
type Opciones struct {
	// Perfil es el perfil de validation.profiles a ejecutar (--profile del
	// comando). No confundir con Stage: Stage identifica el punto del ciclo de
	// vida (mensajes/registro), Perfil identifica QUÉ se valida — son ejes
	// independientes (decisión de diseño de T1.7, documentada también en
	// cmd/sentinel/comandos_gate.go).
	Perfil         string
	RutasCambiadas []string

	OpcionesValidacion validation.OpcionesEjecucion
	// EjecutarValidacion es la función de orquestación de T1.6
	// (validation.EjecutarPerfilSobreCandidato); nil usa esa misma función.
	// Inyectable para simular fallos de infraestructura (candidato obsoleto,
	// snapshot que no se pudo crear...) sin depender de git real.
	EjecutarValidacion func(perfil string, alcance []string, opts validation.OpcionesEjecucion) ([]validation.ValidationRun, error)

	// FabricaAuditor construye el agente de cada dimensión de la revisión
	// semántica (mismo contrato que review.AuditarCommit). gate no construye
	// agentes reales por sí mismo: eso es plumbing de cmd/, igual que hace hoy
	// ejecutarReview.
	FabricaAuditor   review.FabricaAuditor
	FabricaRefutador review.FabricaRefutador
	Parallel         int
	OpcionesRevision review.OpcionesAuditoria

	// Stage is the --stage lifecycle context embedded in the durable root
	// run request. cmd/sentinel's facade messages also read the stage value.
	Stage string
	// CandidateSHA is the candidate HEAD commit SHA embedded in the durable
	// root run request.
	CandidateSHA string
	// DurableStore backs the root gate run and every validation-job
	// settlement. A nil store fails honestly as infrastructure before any
	// phase executes; cmd/sentinel wires it through applyDurableCutover over
	// the repository common-dir store.
	DurableStore *store.Store
	// DurableReviewTransportFactory constructs the review-side transport used
	// by the durable orchestration's review phase. It receives the gate's
	// ROOT run ID so production wiring can thread the parent linkage into the
	// review-side durable runs (review runs are constructed at a different
	// site — inside the factory — and cannot be stamped by the gate
	// orchestrator itself). Production wiring must be the same construction
	// path `sentinel review` uses today (durableReviewTransport in
	// cmd/sentinel), so reviewer invocations keep inheriting admission, owned
	// process trees, and cancellation; tests inject substitutes and
	// invocation counters here. When nil, the review phase falls back to
	// OpcionesRevision.ReviewTransport (engine-level injection seam): there
	// is no second review execution path.
	DurableReviewTransportFactory func(rootRunID agentrun.Identity) review.ReviewTransport
	// DurableReviewTransportFactoryWithEvidence is the preferred factory for
	// semantic finalization. It preserves parent linkage while returning the
	// owner callback for the exact physical run.
	DurableReviewTransportFactoryWithEvidence func(rootRunID agentrun.Identity) (review.ReviewTransportWithEvidence, review.MetricsFinalizer)
	// DurableReviewChildren reports the review-side child run identities that
	// were ACTUALLY admitted during the review phase (ticket 11 slice 3).
	// Review candidate identities are process-salted inside the shared
	// durable transport, so the orchestrator cannot derive them from the
	// plan: production wiring records every admission through a
	// concurrency-safe observer sink and hands the drain function here. The
	// orchestrator consumes it when composing the root settlement's
	// "|children=" enumeration so the enumerated set equals the persisted
	// ParentRunID scan even though neither side derives the IDs
	// deterministically. When nil, only planned validation jobs (and any
	// resolvable planned review job) are enumerated.
	DurableReviewChildren func() []agentrun.Identity
}

// mensajeValidacionNoEjecutada is the single facade text for a validation
// ORCHESTRATION failure (infrastructure, never a code finding). The durable
// orchestration renders it so equivalent inputs keep the historical facade
// text byte-identical.
func mensajeValidacionNoEjecutada(err error) string {
	return fmt.Sprintf("No se pudo ejecutar la validación: %v", err)
}

// mensajesValidacionFallida redacta el detalle de qué comandos fallaron y su
// salida real: la ficha exige mostrar la evidencia real, nunca inventar un
// PASS.
func mensajesValidacionFallida(hallazgos []validation.Hallazgo) []string {
	mensajes := []string{"❌ Validación FALLIDA: no se ejecuta la revisión semántica."}
	for _, h := range hallazgos {
		mensajes = append(mensajes,
			fmt.Sprintf("  ✖ %s (%s):\n%s", h.Capability, h.Comando, strings.TrimSpace(h.Evidencia)))
	}
	return mensajes
}

// traducirVeredicto keeps validation and semantic blockers distinct. A refuted
// semantic critical finding remains visible and requires human review.
func traducirVeredicto(resultado review.ResultadoAuditoria) Resultado {
	switch resultado.Veredicto {
	case review.VerdictUnavailable:
		return Resultado{
			Estado:   EstadoReviewInfrastructureError,
			Mensajes: unavailableReviewMessages(resultado),
		}
	case review.VerdictQuestion:
		mensajes := []string{"❓ La revisión semántica requiere atención humana explícita:"}
		for _, pregunta := range resultado.Preguntas {
			mensajes = append(mensajes, fmt.Sprintf("  ? %s", pregunta.Text))
		}
		mensajes = append(mensajes, unavailableDimensionMessages(resultado)...)
		return Resultado{Estado: EstadoNeedsUserReview, Mensajes: mensajes}
	case review.VerdictBlock:
		messages := []string{
			"❌ La revisión semántica confirmó hallazgos CRITICAL.",
			resultado.String(),
		}
		messages = append(messages, criticalFindingMessages(resultado)...)
		messages = append(messages, unavailableDimensionMessages(resultado)...)
		return Resultado{Estado: EstadoCodeReviewFailed, Mensajes: messages}
	default:
		if tieneHallazgoCriticoRefutado(resultado) {
			return Resultado{Estado: EstadoNeedsUserReview, Mensajes: []string{"❓ La revisión semántica refutó un hallazgo CRITICAL y requiere atención humana.", resultado.String()}}
		}
		return Resultado{Estado: EstadoPass, Mensajes: []string{"✅ Validación y revisión semántica en verde.", resultado.String()}}
	}
}

func tieneHallazgoCriticoRefutado(resultado review.ResultadoAuditoria) bool {
	for _, dimension := range resultado.Dims {
		if dimension.Resultado != nil && dimension.Resultado.RefutedCritical {
			return true
		}
	}
	return false
}

func unavailableReviewMessages(auditResult review.ResultadoAuditoria) []string {
	messages := []string{"❌ La revisión semántica no pudo ejecutarse (agente no disponible o error de infraestructura)."}
	dimensionMessages := unavailableDimensionMessages(auditResult)
	if len(dimensionMessages) == 0 {
		return append(messages, "  No unavailable dimension evidence was retained.")
	}
	return append(messages, dimensionMessages...)
}

func unavailableDimensionMessages(auditResult review.ResultadoAuditoria) []string {
	dimensions := unavailableDimensions(auditResult)
	if len(dimensions) == 0 {
		return nil
	}

	messages := []string{"Unavailable dimensions:"}
	for _, dimension := range dimensions {
		dimensionName := dimension.Dim
		reason := ""
		if dimension.Resultado != nil {
			if dimensionName == "" {
				dimensionName = dimension.Resultado.Dim
			}
			reason = dimension.Resultado.Reason
		}
		if reason == "" && dimension.Error != nil {
			reason = dimension.Error.Error()
		}
		// Ticket 07: admission failures surface with their own class label
		// instead of wearing generic infrastructure unavailability. Exit-code
		// contracts are untouched: this marker only classifies evidence.
		messages = append(messages, fmt.Sprintf("  - dimension=%q reason=%q class=%s", dimensionName, reason, failureClass(dimension)))
	}
	return messages
}

// failureClass labels an unavailable dimension as an admission failure or an
// infrastructure failure (ticket 07). The typed transport error wins when the
// engine retained it; once only the persisted reason survives, classification
// goes through the literal admission prefix both share.
func failureClass(dimension review.ResultadoDimension) string {
	if reviewexec.IsAdmissionError(dimension.Error) {
		return "admission"
	}
	if dimension.Resultado != nil && reviewexec.IsAdmissionReason(dimension.Resultado.Reason) {
		return "admission"
	}
	return "infrastructure"
}

func unavailableDimensions(auditResult review.ResultadoAuditoria) []review.ResultadoDimension {
	var dimensions []review.ResultadoDimension
	for _, dimension := range auditResult.Dims {
		if dimension.Resultado != nil && dimension.Resultado.Verdict == review.VerdictUnavailable {
			dimensions = append(dimensions, dimension)
			continue
		}
		if dimension.Resultado == nil && dimension.Error != nil {
			dimensions = append(dimensions, dimension)
		}
	}
	return dimensions
}

func criticalFindingMessages(auditResult review.ResultadoAuditoria) []string {
	messages := []string{"Confirmed CRITICAL finding evidence:"}
	findings := effectiveCriticalFindings(auditResult)
	if len(findings) == 0 {
		return append(messages, "  No structured finding evidence was retained.")
	}

	for _, finding := range findings {
		fingerprint := finding.Fingerprint
		if fingerprint == "" {
			fingerprint = review.Fingerprint(finding)
		}
		identity := finding.ID
		if identity == "" {
			identity = fingerprint
		}
		producer := finding.Producer
		primaryEvidence := finding.Evidence
		if finding.EvidenceSet != nil && len(finding.EvidenceSet.Values) > 0 {
			if primaryEvidence == "" {
				primaryEvidence = finding.EvidenceSet.Values[0].Evidence
			}
			if producer == (review.Productor{}) {
				producer = finding.EvidenceSet.Values[0].Producer
			}
		}

		messages = append(messages,
			fmt.Sprintf("  finding identity=%q", identity),
			fmt.Sprintf("    id: %q", finding.ID),
			fmt.Sprintf("    fingerprint: %q", fingerprint),
			fmt.Sprintf("    dimension: %q", finding.Dimension),
			fmt.Sprintf("    location: file=%q line_start=%d line_end=%d symbol=%q blob=%q", finding.Location.Archivo, finding.Location.LineaInicio, finding.Location.LineaFin, finding.Location.Simbolo, finding.Location.Blob),
			fmt.Sprintf("    description: %q", finding.Description),
			fmt.Sprintf("    evidence: %q", primaryEvidence),
			fmt.Sprintf("    confidence: %g", finding.Confidence),
			fmt.Sprintf("    producer: %s", formatProducer(producer)),
		)
		if finding.EvidenceSet == nil {
			continue
		}
		for index, corroboratingEvidence := range finding.EvidenceSet.Values {
			if index == 0 && corroboratingEvidence.Evidence == primaryEvidence {
				continue
			}
			messages = append(messages, fmt.Sprintf("    corroborating evidence[%d]: dimension=%q confidence=%g producer=%s value=%q", index+1, corroboratingEvidence.Dimension, corroboratingEvidence.Confidence, formatProducer(corroboratingEvidence.Producer), corroboratingEvidence.Evidence))
		}
	}
	return messages
}

func effectiveCriticalFindings(auditResult review.ResultadoAuditoria) []review.Hallazgo {
	findings := append([]review.Hallazgo(nil), auditResult.Findings...)
	hasAggregatedFindings := len(auditResult.Findings) > 0
	for _, dimension := range auditResult.Dims {
		if dimension.Resultado == nil {
			continue
		}
		dimensionResult := dimension.Resultado
		if !hasAggregatedFindings {
			findings = append(findings, dimensionResult.Hallazgos...)
		}
		for _, legacyFinding := range dimensionResult.Findings {
			if isLegacyFindingRepresented(legacyFinding, dimension.Dim, dimensionResult.Hallazgos) {
				continue
			}
			findings = append(findings, findingFromLegacy(dimension.Dim, legacyFinding))
		}
	}

	var effective []review.Hallazgo
	for _, finding := range findings {
		// Empty status is the legacy default for parsed v2 findings; every status
		// other than refuted is still effective under the existing gate rules.
		if finding.Severity == review.SevCritical && finding.Status != review.StatusRefuted {
			effective = append(effective, finding)
		}
	}
	return effective
}

func isLegacyFindingRepresented(legacy review.ReviewFinding, dimension string, v2Findings []review.Hallazgo) bool {
	for _, v2Finding := range v2Findings {
		if v2Finding.Dimension == dimension &&
			v2Finding.Severity == legacy.Severity &&
			v2Finding.Description == legacy.Description &&
			v2Finding.Location.Archivo == legacy.File &&
			v2Finding.Location.LineaInicio == int(legacy.Line) {
			return true
		}
	}
	return false
}

func findingFromLegacy(dimension string, finding review.ReviewFinding) review.Hallazgo {
	status := finding.Status
	if status == "" {
		status = review.StatusConfirmed
	}
	convertedFinding := review.Hallazgo{
		Dimension:   dimension,
		Severity:    finding.Severity,
		Status:      status,
		Description: finding.Description,
		Location: review.Ubicacion{
			Archivo:     finding.File,
			LineaInicio: int(finding.Line),
		},
	}
	convertedFinding.Fingerprint = review.Fingerprint(convertedFinding)
	return convertedFinding
}

func formatProducer(producer review.Productor) string {
	return fmt.Sprintf("agent=%q binary=%q model=%q reasoning_effort=%q model_verified=%t", producer.Agente, producer.Binario, producer.Modelo, producer.Esfuerzo, producer.ModeloVerificado)
}
