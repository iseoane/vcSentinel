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

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
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
}

// EjecutarGate aplica el orden fijo de T1.7: valida primero y, SOLO si la
// validación pasa, ejecuta la revisión semántica.
// Si la validación falla, ni siquiera se llama a FabricaAuditor: la revisión
// semántica ni se intenta (regla central de la ficha, verificada en los
// tests con un contador de invocaciones).
func EjecutarGate(opts Opciones) Resultado {
	ejecutarValidacion := opts.EjecutarValidacion
	if ejecutarValidacion == nil {
		ejecutarValidacion = validation.EjecutarPerfilSobreCandidato
	}

	runs, err := ejecutarValidacion(opts.Perfil, opts.RutasCambiadas, opts.OpcionesValidacion)
	if err != nil {
		// Un fallo al ORQUESTAR la validación (candidato obsoleto, snapshot no
		// creado, capability mal referenciada en el perfil...) es
		// infraestructura, no un hallazgo del código: nunca se inventa un
		// VALIDATION_FAILED para algo que ni llegó a ejecutarse.
		return Resultado{
			Estado:   EstadoReviewInfrastructureError,
			Mensajes: []string{fmt.Sprintf("No se pudo ejecutar la validación: %v", err)},
		}
	}

	hallazgos := validation.Hallazgos(runs, opts.OpcionesValidacion.Cfg.Validation.Capabilities)
	if len(hallazgos) > 0 {
		return Resultado{Estado: EstadoValidationFailed, Mensajes: mensajesValidacionFallida(hallazgos)}
	}

	opcionesRevision := opts.OpcionesRevision
	opcionesRevision.FabricaRefutador = opts.FabricaRefutador
	resultado := review.AuditarCommit(opts.FabricaAuditor, opts.Parallel, opcionesRevision)
	return traducirVeredicto(resultado)
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
			Mensajes: mensajesRevisionNoDisponible(resultado),
		}
	case review.VerdictQuestion:
		mensajes := []string{"❓ La revisión semántica requiere atención humana explícita:"}
		for _, pregunta := range resultado.Preguntas {
			mensajes = append(mensajes, fmt.Sprintf("  ? %s", pregunta.Text))
		}
		return Resultado{Estado: EstadoNeedsUserReview, Mensajes: mensajes}
	case review.VerdictBlock:
		mensajes := []string{
			"❌ La revisión semántica confirmó hallazgos CRITICAL.",
			resultado.String(),
		}
		mensajes = append(mensajes, mensajesHallazgosCriticos(resultado)...)
		return Resultado{Estado: EstadoCodeReviewFailed, Mensajes: mensajes}
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

func mensajesRevisionNoDisponible(resultado review.ResultadoAuditoria) []string {
	mensajes := []string{"❌ La revisión semántica no pudo ejecutarse (agente no disponible o error de infraestructura)."}
	dimensiones := dimensionesNoDisponibles(resultado)
	if len(dimensiones) == 0 {
		return append(mensajes, "  No unavailable dimension evidence was retained.")
	}

	mensajes = append(mensajes, "Unavailable dimensions:")
	for _, dimension := range dimensiones {
		nombre := dimension.Dim
		razon := ""
		if dimension.Resultado != nil {
			if nombre == "" {
				nombre = dimension.Resultado.Dim
			}
			razon = dimension.Resultado.Reason
		}
		if razon == "" && dimension.Error != nil {
			razon = dimension.Error.Error()
		}
		mensajes = append(mensajes, fmt.Sprintf("  - dimension=%q reason=%q", nombre, razon))
	}
	return mensajes
}

func dimensionesNoDisponibles(resultado review.ResultadoAuditoria) []review.ResultadoDimension {
	var dimensiones []review.ResultadoDimension
	for _, dimension := range resultado.Dims {
		if dimension.Resultado != nil && dimension.Resultado.Verdict == review.VerdictUnavailable {
			dimensiones = append(dimensiones, dimension)
			continue
		}
		if dimension.Resultado == nil && dimension.Error != nil {
			dimensiones = append(dimensiones, dimension)
		}
	}
	return dimensiones
}

func mensajesHallazgosCriticos(resultado review.ResultadoAuditoria) []string {
	mensajes := []string{"Confirmed CRITICAL finding evidence:"}
	hallazgos := hallazgosCriticosEfectivos(resultado)
	if len(hallazgos) == 0 {
		return append(mensajes, "  No structured finding evidence was retained.")
	}

	for _, hallazgo := range hallazgos {
		fingerprint := hallazgo.Fingerprint
		if fingerprint == "" {
			fingerprint = review.Fingerprint(hallazgo)
		}
		identity := hallazgo.ID
		if identity == "" {
			identity = fingerprint
		}
		productor := hallazgo.Producer
		evidencia := hallazgo.Evidence
		if hallazgo.EvidenceSet != nil && len(hallazgo.EvidenceSet.Values) > 0 {
			if evidencia == "" {
				evidencia = hallazgo.EvidenceSet.Values[0].Evidence
			}
			if productor == (review.Productor{}) {
				productor = hallazgo.EvidenceSet.Values[0].Producer
			}
		}

		mensajes = append(mensajes,
			fmt.Sprintf("  finding identity=%q", identity),
			fmt.Sprintf("    id: %q", hallazgo.ID),
			fmt.Sprintf("    fingerprint: %q", fingerprint),
			fmt.Sprintf("    dimension: %q", hallazgo.Dimension),
			fmt.Sprintf("    location: file=%q line_start=%d line_end=%d symbol=%q blob=%q", hallazgo.Location.Archivo, hallazgo.Location.LineaInicio, hallazgo.Location.LineaFin, hallazgo.Location.Simbolo, hallazgo.Location.Blob),
			fmt.Sprintf("    description: %q", hallazgo.Description),
			fmt.Sprintf("    evidence: %q", evidencia),
			fmt.Sprintf("    confidence: %g", hallazgo.Confidence),
			fmt.Sprintf("    producer: %s", formatoProductor(productor)),
		)
		if hallazgo.EvidenceSet == nil {
			continue
		}
		for index, evidence := range hallazgo.EvidenceSet.Values {
			if index == 0 && evidence.Evidence == evidencia {
				continue
			}
			mensajes = append(mensajes, fmt.Sprintf("    corroborating evidence[%d]: dimension=%q confidence=%g producer=%s value=%q", index+1, evidence.Dimension, evidence.Confidence, formatoProductor(evidence.Producer), evidence.Evidence))
		}
	}
	return mensajes
}

func hallazgosCriticosEfectivos(resultado review.ResultadoAuditoria) []review.Hallazgo {
	candidatos := resultado.Findings
	if len(candidatos) == 0 {
		for _, dimension := range resultado.Dims {
			if dimension.Resultado == nil {
				continue
			}
			if len(dimension.Resultado.Hallazgos) > 0 {
				candidatos = append(candidatos, dimension.Resultado.Hallazgos...)
				continue
			}
			for _, legacy := range dimension.Resultado.Findings {
				candidatos = append(candidatos, hallazgoDesdeLegacy(dimension.Dim, legacy))
			}
		}
	}

	var efectivos []review.Hallazgo
	for _, hallazgo := range candidatos {
		// Empty status is the legacy default for parsed v2 findings; every status
		// other than refuted is still effective under the existing gate rules.
		if hallazgo.Severity == review.SevCritical && hallazgo.Status != review.StatusRefuted {
			efectivos = append(efectivos, hallazgo)
		}
	}
	return efectivos
}

func hallazgoDesdeLegacy(dimension string, finding review.ReviewFinding) review.Hallazgo {
	status := finding.Status
	if status == "" {
		status = review.StatusConfirmed
	}
	hallazgo := review.Hallazgo{
		Dimension:   dimension,
		Severity:    finding.Severity,
		Status:      status,
		Description: finding.Description,
		Location: review.Ubicacion{
			Archivo:     finding.File,
			LineaInicio: int(finding.Line),
		},
	}
	hallazgo.Fingerprint = review.Fingerprint(hallazgo)
	return hallazgo
}

func formatoProductor(productor review.Productor) string {
	return fmt.Sprintf("agent=%q binary=%q model=%q reasoning_effort=%q model_verified=%t", productor.Agente, productor.Binario, productor.Modelo, productor.Esfuerzo, productor.ModeloVerificado)
}
