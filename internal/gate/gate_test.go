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

func (a *auditorFalso) EjecutarRevision(prompt, _ string, _ []string) (string, error) {
	return a.EjecutarPrompt(prompt)
}

// fabricaContadora construye una review.FabricaAuditor que cuenta cuántas
// veces se invoca (una por dimensión) y devuelve siempre el mismo auditor
// falso: permite comprobar "cero llamadas al motor de revisión semántica"
// cuando la validación falla (regla central de T1.7).
func fabricaContadora(llamadas *int, salida string, err error) review.FabricaAuditor {
	return func(_ review.ReviewBundle, dimension string) (review.AuditorAgente, string, error) {
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
			SHA:     "0123456789abcdef",
			Bundles: []review.ReviewBundle{{Name: "test", Dimensions: []string{review.DimLogic}, Priority: 1, Cost: 1}},
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
	refutadores := 0
	opts.FabricaRefutador = func() (review.AuditorAgente, string, error) {
		refutadores++
		return &auditorFalso{}, "cheap", nil
	}

	resultado := EjecutarGate(opts)

	if resultado.Estado != EstadoValidationFailed {
		t.Fatalf("estado esperado %q, obtuve %q", EstadoValidationFailed, resultado.Estado)
	}
	if llamadas != 0 {
		t.Fatalf("se esperaban 0 llamadas al motor de revisión, hubo %d", llamadas)
	}
	if refutadores != 0 {
		t.Fatalf("se esperaban 0 refutadores para un CRITICAL de validación, hubo %d", refutadores)
	}
	unido := strings.Join(resultado.Mensajes, "\n")
	if !strings.Contains(unido, "salida real del comando fallido") {
		t.Fatalf("el mensaje debe mostrar la salida real del comando fallido, obtuve: %q", unido)
	}
}

func TestValidacionEnVerdeConCriticalConfirmado_Bloquea(t *testing.T) {
	cfg := cfgConPerfil("lint", "echo ok")
	llamadas := 0
	salidaAgente := `{"dim":"logic","verdict":"block","findings":[{"dimension":"logic","file":"a.go","line":1,"severity":"CRITICAL","description":"riesgo"}]}`
	opts := opcionesBase(cfg, func(string) (int, string, error) {
		return 0, "", nil
	}, fabricaContadora(&llamadas, salidaAgente, nil))
	opts.EjecutarValidacion = ejecutarPerfilSinCandidato
	refutadores := 0
	opts.FabricaRefutador = func() (review.AuditorAgente, string, error) {
		refutadores++
		return &auditorFalso{salida: `{"refuted":false,"reason":"the risk remains"}`}, "cheap", nil
	}

	resultado := EjecutarGate(opts)

	if resultado.Estado != EstadoCodeReviewFailed {
		t.Fatalf("estado esperado %q, obtuve %q", EstadoCodeReviewFailed, resultado.Estado)
	}
	if llamadas != 1 {
		t.Fatalf("se esperaba 1 llamada al motor de revisión, hubo %d", llamadas)
	}
	if refutadores != 1 {
		t.Fatalf("se esperaba 1 llamada al refutador cheap, hubo %d", refutadores)
	}
	unido := strings.Join(resultado.Mensajes, "\n")
	if !strings.Contains(unido, "confirmó") {
		t.Fatalf("se esperaba confirmación del CRITICAL, obtuve: %q", unido)
	}
}

func TestCriticalRefuted_NeedsUserReview(t *testing.T) {
	cfg := cfgConPerfil("lint", "echo ok")
	llamadas := 0
	agente := &auditorSecuencial{respuestas: []string{`{"dim":"logic","verdict":"block","findings":[{"dimension":"logic","file":"a.go","line":1,"severity":"CRITICAL","description":"risk"}]}`}, llamadas: &llamadas}
	fabrica := func(_ review.ReviewBundle, _ string) (review.AuditorAgente, string, error) {
		return agente, "perfil-test", nil
	}
	opts := opcionesBase(cfg, func(string) (int, string, error) { return 0, "", nil }, fabrica)
	opts.EjecutarValidacion = ejecutarPerfilSinCandidato
	opts.FabricaRefutador = func() (review.AuditorAgente, string, error) {
		return &auditorFalso{salida: `{"refuted":true,"reason":"the final code already handles this case","sha":"0123456789abcdef","file":"a.go","line_start":1,"line_end":1,"evidence":"final code handles this case"}`}, "cheap", nil
	}
	opts.OpcionesRevision.LeerContenidoSnapshot = func(sha, file string) (string, error) {
		if sha != "0123456789abcdef" || file != "a.go" {
			t.Fatalf("snapshot read sha=%q file=%q", sha, file)
		}
		return "final code handles this case", nil
	}

	resultado := EjecutarGate(opts)

	if resultado.Estado != EstadoNeedsUserReview || CodigoSalida(resultado.Estado) != 2 {
		t.Fatalf("estado=%q exit=%d, expected NEEDS_USER_REVIEW and exit 2", resultado.Estado, CodigoSalida(resultado.Estado))
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
	}, func(_ review.ReviewBundle, dimension string) (review.AuditorAgente, string, error) {
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
		EstadoCodeReviewFailed:          1,
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

type auditorSecuencial struct {
	respuestas []string
	llamadas   *int
}

func (a *auditorSecuencial) EjecutarPrompt(string) (string, error) {
	salida := a.respuestas[*a.llamadas]
	*a.llamadas++
	return salida, nil
}

func (a *auditorSecuencial) EjecutarRevision(prompt, sha string, paths []string) (string, error) {
	return a.EjecutarPrompt(prompt)
}

func TestTraducirVeredictoRendersCurrentEvidence(t *testing.T) {
	confirmed := review.Hallazgo{
		ID:          "finding-1",
		Fingerprint: "fingerprint-1",
		Dimension:   review.DimLogic,
		Severity:    review.SevCritical,
		Status:      review.StatusConfirmed,
		Description: "unsafe fallback is reachable",
		Evidence:    "return fallbackValue",
		Confidence:  0.92,
		Location: review.Ubicacion{
			Archivo:     "internal/service.go",
			LineaInicio: 17,
			LineaFin:    19,
			Simbolo:     "loadValue",
		},
		Producer: review.Productor{
			Agente:           "reviewer-cli",
			Binario:          "reviewer-cli",
			Modelo:           "model-a",
			Esfuerzo:         "high",
			ModeloVerificado: true,
		},
	}

	cases := []struct {
		name      string
		resultado review.ResultadoAuditoria
		estado    string
		exit      int
		contains  []string
		excludes  []string
	}{
		{
			name: "blocked findings include all identifying evidence",
			resultado: review.ResultadoAuditoria{
				SHA:       "0123456789abcdef",
				Veredicto: review.VerdictBlock,
				Findings:  []review.Hallazgo{confirmed},
			},
			estado: EstadoCodeReviewFailed,
			exit:   1,
			contains: []string{
				"finding-1",
				"fingerprint-1",
				"logic",
				"internal/service.go",
				"unsafe fallback is reachable",
				"return fallbackValue",
				"0.92",
				"reviewer-cli",
				"model-a",
			},
		},
		{
			name: "unavailable dimensions retain their reasons",
			resultado: review.ResultadoAuditoria{
				SHA:       "0123456789abcdef",
				Veredicto: review.VerdictUnavailable,
				Dims: []review.ResultadoDimension{
					{Dim: review.DimLogic, Resultado: &review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictUnavailable, Reason: "provider rate limit"}},
					{Dim: review.DimSecurity, Error: errAgenteNoDisponibleTest},
				},
			},
			estado: EstadoReviewInfrastructureError,
			exit:   4,
			contains: []string{
				"Unavailable dimensions:",
				`dimension="logic" reason="provider rate limit"`,
				`dimension="security" reason="agente no disponible"`,
			},
		},
		{
			name: "refuted critical remains human review",
			resultado: review.ResultadoAuditoria{
				SHA:       "0123456789abcdef",
				Veredicto: review.VerdictWarn,
				Dims:      []review.ResultadoDimension{{Dim: review.DimLogic, Resultado: &review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictWarn, RefutedCritical: true}}},
			},
			estado:   EstadoNeedsUserReview,
			exit:     2,
			contains: []string{"refutó un hallazgo CRITICAL"},
		},
		{
			name: "mixed dimensions render only effective blockers",
			resultado: review.ResultadoAuditoria{
				SHA:       "0123456789abcdef",
				Veredicto: review.VerdictBlock,
				Findings: []review.Hallazgo{
					confirmed,
					{ID: "refuted-1", Fingerprint: "refuted-fingerprint", Dimension: review.DimSecurity, Severity: review.SevCritical, Status: review.StatusRefuted, Description: "already disproved", Evidence: "safe path"},
				},
			},
			estado:   EstadoCodeReviewFailed,
			exit:     1,
			contains: []string{"finding-1", "fingerprint-1"},
			excludes: []string{"refuted-1", "refuted-fingerprint", "safe path"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resultado := traducirVeredicto(tc.resultado)
			if resultado.Estado != tc.estado {
				t.Fatalf("state = %q, expected %q", resultado.Estado, tc.estado)
			}
			if got := CodigoSalida(resultado.Estado); got != tc.exit {
				t.Fatalf("exit = %d, expected %d", got, tc.exit)
			}
			output := strings.Join(resultado.Mensajes, "\n")
			for _, value := range tc.contains {
				if !strings.Contains(output, value) {
					t.Errorf("output does not contain %q: %s", value, output)
				}
			}
			for _, value := range tc.excludes {
				if strings.Contains(output, value) {
					t.Errorf("output unexpectedly contains %q: %s", value, output)
				}
			}
		})
	}
}
