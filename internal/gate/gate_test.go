package gate

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

// auditorFalso implementa review.AuditorAgente devolviendo una salida fija;
// costura mínima para no depender de ningún agente real en los tests de gate.
type auditorFalso struct {
	salida string
	err    error
}

func (a *auditorFalso) EjecutarPrompt(prompt string) (string, error) {
	return completeTestContract(a.salida), a.err
}

func (a *auditorFalso) EjecutarRevision(prompt, _ string, _ []string) (string, error) {
	return a.EjecutarPrompt(prompt)
}

func (a *auditorFalso) ReviewWithPolicy(prompt, sha string, paths []string, _ reviewcontract.ToolPolicy) (string, error) {
	return a.EjecutarRevision(prompt, sha, paths)
}

func completeTestContract(output string) string {
	var result map[string]any
	if json.Unmarshal([]byte(output), &result) != nil {
		return output
	}
	findings, ok := result["findings"].([]any)
	if !ok {
		return output
	}
	for _, item := range findings {
		finding, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if _, ok := finding["evidence"]; !ok {
			finding["evidence"] = "test evidence"
		}
		if _, ok := finding["confidence"]; !ok {
			finding["confidence"] = "high"
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return output
	}
	return string(encoded)
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

func opcionesBase(t *testing.T, cfg config.Config, ejecutar validation.EjecutorComando, fabrica review.FabricaAuditor) Opciones {
	opts := Opciones{
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
	// Ticket 13 (R11): EjecutarGate IS the durable orchestration, so every
	// fixture exercises the only execution path with its full seam set:
	// stage/candidate identity, a temp-dir backed durable store, and a REAL
	// transport factory mirroring cmd/sentinel's construction. Tests that
	// need their own counter or transport override these fields afterwards.
	opts.Stage = "pre-push"
	opts.CandidateSHA = opts.OpcionesRevision.SHA
	opts.DurableStore = store.NuevoStore(filepath.Join(t.TempDir(), "gate-common"))
	opts.DurableReviewTransportFactory = countedTransportFactory(t, opts.OpcionesRevision.SHA, new(int))
	return opts
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
	opts := opcionesBase(t, cfg, func(string) (int, string, error) {
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
	opts := opcionesBase(t, cfg, func(string) (int, string, error) {
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
	opts := opcionesBase(t, cfg, func(string) (int, string, error) { return 0, "", nil }, fabrica)
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
	opts := opcionesBase(t, cfg, func(string) (int, string, error) {
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
	opts := opcionesBase(t, cfg, func(string) (int, string, error) {
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
	opts := opcionesBase(t, cfg, nil, fabricaContadora(&llamadas, "", nil))
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
	return completeTestContract(salida), nil
}

func (a *auditorSecuencial) EjecutarRevision(prompt, sha string, paths []string) (string, error) {
	return a.EjecutarPrompt(prompt)
}

func (a *auditorSecuencial) ReviewWithPolicy(prompt, sha string, paths []string, _ reviewcontract.ToolPolicy) (string, error) {
	return a.EjecutarRevision(prompt, sha, paths)
}

func TestTranslateVerdictRendersCurrentEvidence(t *testing.T) {
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
	refuted := review.Hallazgo{
		ID:          "refuted-1",
		Fingerprint: "refuted-fingerprint",
		Dimension:   review.DimLogic,
		Severity:    review.SevCritical,
		Status:      review.StatusRefuted,
		Description: "already disproved",
		Evidence:    "safe path",
		Location: review.Ubicacion{
			Archivo:     "internal/service.go",
			LineaInicio: 23,
		},
	}

	cases := []struct {
		name          string
		auditResult   review.ResultadoAuditoria
		expectedState string
		expectedExit  int
		findingCount  int
		contains      []string
		excludes      []string
	}{
		{
			name: "blocked findings include all identifying evidence",
			auditResult: review.ResultadoAuditoria{
				SHA:       "0123456789abcdef",
				Veredicto: review.VerdictBlock,
				Findings:  []review.Hallazgo{confirmed},
			},
			expectedState: EstadoCodeReviewFailed,
			expectedExit:  1,
			findingCount:  1,
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
			auditResult: review.ResultadoAuditoria{
				SHA:       "0123456789abcdef",
				Veredicto: review.VerdictUnavailable,
				Dims: []review.ResultadoDimension{
					{Dim: review.DimLogic, Resultado: &review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictUnavailable, Reason: "provider rate limit"}},
					{Dim: review.DimSecurity, Error: errAgenteNoDisponibleTest},
				},
			},
			expectedState: EstadoReviewInfrastructureError,
			expectedExit:  4,
			contains: []string{
				"Unavailable dimensions:",
				`dimension="logic" reason="provider rate limit"`,
				`dimension="security" reason="agente no disponible"`,
			},
		},
		{
			name: "block precedence retains unavailable dimension reasons",
			auditResult: review.ResultadoAuditoria{
				SHA:       "0123456789abcdef",
				Veredicto: review.VerdictBlock,
				Findings:  []review.Hallazgo{confirmed},
				Dims: []review.ResultadoDimension{
					{Dim: review.DimLogic, Resultado: &review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictBlock}},
					{Dim: review.DimSecurity, Resultado: &review.DimensionResult{Dim: review.DimSecurity, Verdict: review.VerdictUnavailable, Reason: "security reviewer timed out"}},
				},
			},
			expectedState: EstadoCodeReviewFailed,
			expectedExit:  1,
			findingCount:  1,
			contains: []string{
				"finding-1",
				`dimension="security" reason="security reviewer timed out"`,
			},
		},
		{
			name: "question precedence retains unavailable dimension reasons",
			auditResult: review.ResultadoAuditoria{
				SHA:       "0123456789abcdef",
				Veredicto: review.VerdictQuestion,
				Preguntas: []review.AgentQuestion{{ID: "q1", Text: "which behavior is expected?"}},
				Dims: []review.ResultadoDimension{
					{Dim: review.DimLogic, Resultado: &review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictQuestion}},
					{Dim: review.DimSecurity, Resultado: &review.DimensionResult{Dim: review.DimSecurity, Verdict: review.VerdictUnavailable, Reason: "security reviewer timed out"}},
				},
			},
			expectedState: EstadoNeedsUserReview,
			expectedExit:  2,
			contains: []string{
				"which behavior is expected?",
				`dimension="security" reason="security reviewer timed out"`,
			},
		},
		{
			name: "refuted critical remains human review",
			auditResult: review.ResultadoAuditoria{
				SHA:       "0123456789abcdef",
				Veredicto: review.VerdictWarn,
				Dims:      []review.ResultadoDimension{{Dim: review.DimLogic, Resultado: &review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictWarn, RefutedCritical: true}}},
			},
			expectedState: EstadoNeedsUserReview,
			expectedExit:  2,
			contains:      []string{"refutó un hallazgo CRITICAL"},
		},
		{
			name: "mixed v1 and v2 dimensions render distinct effective blockers",
			auditResult: review.ResultadoAuditoria{
				SHA:       "0123456789abcdef",
				Veredicto: review.VerdictBlock,
				Findings: []review.Hallazgo{
					confirmed,
					refuted,
				},
				Dims: []review.ResultadoDimension{
					{
						Dim: review.DimLogic,
						Resultado: &review.DimensionResult{
							Dim:       review.DimLogic,
							Verdict:   review.VerdictBlock,
							Hallazgos: []review.Hallazgo{confirmed, refuted},
							Findings: []review.ReviewFinding{
								{Dimension: review.DimLogic, File: confirmed.Location.Archivo, Line: 17, Severity: confirmed.Severity, Description: confirmed.Description, Status: review.StatusConfirmed},
								{Dimension: review.DimLogic, File: refuted.Location.Archivo, Line: 23, Severity: refuted.Severity, Description: refuted.Description, Status: review.StatusRefuted},
							},
						},
					},
					{
						Dim: review.DimSecurity,
						Resultado: &review.DimensionResult{
							Dim:     review.DimSecurity,
							Verdict: review.VerdictBlock,
							Findings: []review.ReviewFinding{
								{Dimension: review.DimSecurity, File: "internal/legacy.go", Line: 42, Severity: review.SevCritical, Description: "legacy blocker remains effective", Status: review.StatusConfirmed},
								{Dimension: review.DimSecurity, File: "internal/refuted.go", Line: 8, Severity: review.SevCritical, Description: "legacy blocker was refuted", Status: review.StatusRefuted},
							},
						},
					},
				},
			},
			expectedState: EstadoCodeReviewFailed,
			expectedExit:  1,
			findingCount:  2,
			contains:      []string{"finding-1", "fingerprint-1", "legacy blocker remains effective", "internal/legacy.go"},
			excludes:      []string{"refuted-1", "refuted-fingerprint", "safe path", "legacy blocker was refuted", "internal/refuted.go"},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			translated := traducirVeredicto(testCase.auditResult)
			if translated.Estado != testCase.expectedState {
				t.Fatalf("state = %q, expected %q", translated.Estado, testCase.expectedState)
			}
			if got := CodigoSalida(translated.Estado); got != testCase.expectedExit {
				t.Fatalf("exit = %d, expected %d", got, testCase.expectedExit)
			}
			output := strings.Join(translated.Mensajes, "\n")
			if got := strings.Count(output, "finding identity="); got != testCase.findingCount {
				t.Errorf("finding count = %d, expected %d: %s", got, testCase.findingCount, output)
			}
			for _, value := range testCase.contains {
				if !strings.Contains(output, value) {
					t.Errorf("output does not contain %q: %s", value, output)
				}
			}
			for _, value := range testCase.excludes {
				if strings.Contains(output, value) {
					t.Errorf("output unexpectedly contains %q: %s", value, output)
				}
			}
		})
	}
}

func TestTranslateVerdictCarriesCompactReviewerFailureMetadata(t *testing.T) {
	translated := traducirVeredicto(review.ResultadoAuditoria{
		ContextSkipReason: "dirty_worktree",
		Veredicto:         review.VerdictUnavailable,
		Dims: []review.ResultadoDimension{{
			Bundle: review.BundleCorrectness,
			Dim:    review.DimLogic,
			Resultado: &review.DimensionResult{
				Dim:     review.DimLogic,
				Verdict: review.VerdictUnavailable,
				Reason:  "provider reported: ripgrep execution failed | run restricted reviewer timed out after 10m0s",
			},
		}},
	})
	if translated.ContextSkipReason != "dirty_worktree" {
		t.Fatalf("context skip reason = %q, want it exposed on the gate result", translated.ContextSkipReason)
	}
	if len(translated.ReviewerFailures) != 1 {
		t.Fatalf("reviewer failures = %#v, want one compact failure", translated.ReviewerFailures)
	}
	failure := translated.ReviewerFailures[0]
	if failure.Bundle != review.BundleCorrectness || failure.Dimension != review.DimLogic {
		t.Fatalf("failure identity = %#v, want correctness/logic", failure)
	}
	if want := "provider reported: ripgrep execution failed"; failure.Reason != want {
		t.Fatalf("failure reason = %q, want %q", failure.Reason, want)
	}
	if strings.Contains(failure.Reason, "timed out") {
		t.Fatalf("failure reason = %q, want no timeout trace", failure.Reason)
	}
}
