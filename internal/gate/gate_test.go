package gate

import (
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

// auditorFalso implementa review.AuditorAgente devolviendo una salida fija;
// costura mínima para no depender de ningún agente real en los tests de gate.
type auditorFalso struct {
	salida string
	err    error
}

func (a *auditorFalso) EjecutarPrompt(prompt string) (string, error) {
	return a.salida, a.err
}

// fabricaContadora construye una review.FabricaAuditor que cuenta cuántas
// veces se invoca (una por dimensión) y devuelve siempre el mismo auditor
// falso: permite comprobar "cero llamadas al motor de revisión semántica"
// cuando la validación falla (regla central de T1.7).
func fabricaContadora(llamadas *int, salida string, err error) review.FabricaAuditor {
	return func(dimension string) (review.AuditorAgente, string, error) {
		*llamadas++
		return &auditorFalso{salida: salida, err: err}, "perfil-test", nil
	}
}

func cfgConPerfil(nombreCapability string, comando string) config.Config {
	cfg := config.Config{
		Validation: config.ValidationConfig{
			Capabilities: map[string]config.CapabilityConfig{
				nombreCapability: {Command: comando, FailsWhen: config.FailsWhenExitCode},
			},
			Profiles: map[string][]string{
				"testperfil": {nombreCapability},
			},
			Mode: config.ModeInplace,
		},
	}
	return cfg
}

func opcionesBase(cfg config.Config, ejecutar validation.EjecutorComando, fabrica review.FabricaAuditor) Opciones {
	return Opciones{
		Perfil: "testperfil",
		OpcionesValidacion: validation.OpcionesEjecucion{
			Worktree: "/no-usado",
			Cfg:      cfg,
			Ejecutar: ejecutar,
		},
		FabricaAuditor: fabrica,
		Parallel:       1,
		OpcionesRevision: review.OpcionesAuditoria{
			SHA:  "0123456789abcdef",
			Dims: []string{review.DimLogic},
		},
		// EjecutarValidacion inyectado en cada test: no depende de git real.
	}
}

func ejecutarPerfilSinCandidato(perfil string, _ []string, opts validation.OpcionesEjecucion) ([]validation.ValidationRun, error) {
	return validation.EjecutarPerfil(perfil, opts)
}

// TestValidacionEnRojo_NoLanzaRevision cubre la aceptación #1: si la
// validación falla, el motor de revisión semántica NUNCA se invoca (orden
// fijo: validación primero) y el estado es VALIDATION_FAILED.
func TestValidacionEnRojo_NoLanzaRevision(t *testing.T) {
	cfg := cfgConPerfil("lint", "echo boom")
	llamadas := 0
	opts := opcionesBase(cfg, func(string) (int, string, error) {
		return 1, "salida real del comando fallido", nil
	}, fabricaContadora(&llamadas, "", nil))
	opts.EjecutarValidacion = ejecutarPerfilSinCandidato

	resultado := EjecutarGate(opts)

	if resultado.Estado != EstadoValidationFailed {
		t.Fatalf("estado esperado %q, obtuve %q", EstadoValidationFailed, resultado.Estado)
	}
	if llamadas != 0 {
		t.Fatalf("se esperaban 0 llamadas al motor de revisión, hubo %d", llamadas)
	}
	unido := strings.Join(resultado.Mensajes, "\n")
	if !strings.Contains(unido, "salida real del comando fallido") {
		t.Fatalf("el mensaje debe mostrar la salida real del comando fallido, obtuve: %q", unido)
	}
}

// TestValidacionEnVerdeConCritical_NoBloquea cubre la aceptación #2: modo
// advisory hasta F5 — un veredicto CRITICAL semántico no bloquea, solo se
// avisa de forma destacada, y el estado final es PASS.
func TestValidacionEnVerdeConCritical_NoBloquea(t *testing.T) {
	cfg := cfgConPerfil("lint", "echo ok")
	llamadas := 0
	salidaAgente := `{"dim":"logic","verdict":"block","findings":[{"dimension":"logic","file":"a.go","line":1,"severity":"CRITICAL","description":"riesgo"}]}`
	opts := opcionesBase(cfg, func(string) (int, string, error) {
		return 0, "", nil
	}, fabricaContadora(&llamadas, salidaAgente, nil))
	opts.EjecutarValidacion = ejecutarPerfilSinCandidato

	resultado := EjecutarGate(opts)

	if resultado.Estado != EstadoPass {
		t.Fatalf("estado esperado %q (advisory, no bloquea), obtuve %q", EstadoPass, resultado.Estado)
	}
	if llamadas != 1 {
		t.Fatalf("se esperaba 1 llamada al motor de revisión, hubo %d", llamadas)
	}
	unido := strings.Join(resultado.Mensajes, "\n")
	if !strings.Contains(unido, "AVISO") {
		t.Fatalf("se esperaba un aviso destacado del CRITICAL, obtuve: %q", unido)
	}
}

// TestRevisionConPreguntas_NecesitaRevisionHumana cubre el estado
// NEEDS_USER_REVIEW: veredicto question (agente pide aclaraciones sin
// resolver) exige atención humana explícita.
func TestRevisionConPreguntas_NecesitaRevisionHumana(t *testing.T) {
	cfg := cfgConPerfil("lint", "echo ok")
	llamadas := 0
	salidaAgente := `{"dim":"logic","verdict":"question","questions":[{"id":"q1","text":"¿por qué este cambio?"}]}`
	opts := opcionesBase(cfg, func(string) (int, string, error) {
		return 0, "", nil
	}, fabricaContadora(&llamadas, salidaAgente, nil))
	opts.EjecutarValidacion = ejecutarPerfilSinCandidato

	resultado := EjecutarGate(opts)

	if resultado.Estado != EstadoNeedsUserReview {
		t.Fatalf("estado esperado %q, obtuve %q", EstadoNeedsUserReview, resultado.Estado)
	}
}

// TestRevisionSinAgenteDisponible_ErrorInfraestructura cubre
// REVIEW_INFRASTRUCTURE_ERROR cuando el agente de revisión no responde: no es
// un hallazgo del código, es infraestructura.
func TestRevisionSinAgenteDisponible_ErrorInfraestructura(t *testing.T) {
	cfg := cfgConPerfil("lint", "echo ok")
	opts := opcionesBase(cfg, func(string) (int, string, error) {
		return 0, "", nil
	}, func(dimension string) (review.AuditorAgente, string, error) {
		return nil, "perfil-test", errAgenteNoDisponibleTest
	})
	opts.EjecutarValidacion = ejecutarPerfilSinCandidato

	resultado := EjecutarGate(opts)

	if resultado.Estado != EstadoReviewInfrastructureError {
		t.Fatalf("estado esperado %q, obtuve %q", EstadoReviewInfrastructureError, resultado.Estado)
	}
}

// TestEjecutarValidacionFalla_ErrorInfraestructura cubre el caso en el que la
// propia orquestación de validación (T1.6) falla (p. ej. candidato obsoleto):
// es infraestructura, no un hallazgo, y tampoco lanza la revisión.
func TestEjecutarValidacionFalla_ErrorInfraestructura(t *testing.T) {
	cfg := cfgConPerfil("lint", "echo ok")
	llamadas := 0
	opts := opcionesBase(cfg, nil, fabricaContadora(&llamadas, "", nil))
	opts.EjecutarValidacion = func(perfil string, alcance []string, o validation.OpcionesEjecucion) ([]validation.ValidationRun, error) {
		return nil, errAgenteNoDisponibleTest
	}

	resultado := EjecutarGate(opts)

	if resultado.Estado != EstadoReviewInfrastructureError {
		t.Fatalf("estado esperado %q, obtuve %q", EstadoReviewInfrastructureError, resultado.Estado)
	}
	if llamadas != 0 {
		t.Fatalf("se esperaban 0 llamadas al motor de revisión, hubo %d", llamadas)
	}
}

// TestCodigoSalida fija la tabla exacta de exit codes de la ficha. Un estado
// desconocido (que EjecutarGate nunca debería producir, pero que este
// paquete tampoco puede reconocer) NUNCA falla abierto en PASS: se traduce
// al mismo código que REVIEW_INFRASTRUCTURE_ERROR (corrección sobre el
// defecto de diseño original, ver comentario de CodigoSalida).
func TestCodigoSalida(t *testing.T) {
	casos := map[string]int{
		EstadoPass:                      0,
		EstadoValidationFailed:          1,
		EstadoNeedsUserReview:           2,
		EstadoReviewInfrastructureError: 4,
		"ESTADO_DESCONOCIDO":            4,
	}
	for estado, esperado := range casos {
		if got := CodigoSalida(estado); got != esperado {
			t.Errorf("CodigoSalida(%q) = %d, esperado %d", estado, got, esperado)
		}
	}
}

var errAgenteNoDisponibleTest = &errorFijo{"agente no disponible"}

type errorFijo struct{ msg string }

func (e *errorFijo) Error() string { return e.msg }
