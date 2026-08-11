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
	Parallel         int
	OpcionesRevision review.OpcionesAuditoria
}

// EjecutarGate aplica el orden fijo de T1.7: valida primero y, SOLO si la
// validación pasa, ejecuta la revisión semántica en modo advisory (hasta F5).
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

	resultado := review.AuditarCommit(opts.FabricaAuditor, opts.Parallel, opts.OpcionesRevision)
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

// traducirVeredicto aplica el modo advisory de esta fase (hasta F5, decisión
// explícita de la ficha): block (CRITICAL) NO bloquea, solo se avisa de forma
// destacada y el gate sigue en PASS; question exige atención humana explícita
// (NEEDS_USER_REVIEW); unavailable es infraestructura (agente no disponible),
// nunca un hallazgo del código.
func traducirVeredicto(resultado review.ResultadoAuditoria) Resultado {
	switch resultado.Veredicto {
	case review.VerdictUnavailable:
		return Resultado{
			Estado:   EstadoReviewInfrastructureError,
			Mensajes: []string{"❌ La revisión semántica no pudo ejecutarse (agente no disponible o error de infraestructura)."},
		}
	case review.VerdictQuestion:
		mensajes := []string{"❓ La revisión semántica requiere atención humana explícita:"}
		for _, pregunta := range resultado.Preguntas {
			mensajes = append(mensajes, fmt.Sprintf("  ? %s", pregunta.Text))
		}
		return Resultado{Estado: EstadoNeedsUserReview, Mensajes: mensajes}
	case review.VerdictBlock:
		return Resultado{
			Estado: EstadoPass,
			Mensajes: []string{
				"⚠️  AVISO: la revisión semántica encontró hallazgos CRITICAL (no bloquea en esta fase, advisory hasta F5).",
				resultado.String(),
			},
		}
	default:
		return Resultado{Estado: EstadoPass, Mensajes: []string{"✅ Validación y revisión semántica en verde.", resultado.String()}}
	}
}
