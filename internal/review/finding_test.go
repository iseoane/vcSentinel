package review

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestParsearDimensionResultLimpio(t *testing.T) {
	salida := `{"dim":"logic","verdict":"warn","findings":[{"dimension":"logic","file":"internal/a.go","line":10,"severity":"WARNING","description":"condición redundante","suggestion":"simplifica"}]}`
	resultado, err := ParsearDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParsearDimensionResult devolvió error: %v", err)
	}
	if resultado.Dim != DimLogic {
		t.Errorf("dim = %q, esperado %q", resultado.Dim, DimLogic)
	}
	if resultado.Verdict != VerdictWarn {
		t.Errorf("verdict = %q, esperado %q", resultado.Verdict, VerdictWarn)
	}
	if len(resultado.Findings) != 1 {
		t.Fatalf("findings = %d, esperado 1", len(resultado.Findings))
	}
	if resultado.Findings[0].Severity != SevWarning {
		t.Errorf("severity = %q, esperado %q", resultado.Findings[0].Severity, SevWarning)
	}
}

func TestParsearDimensionResultConFencesYTexto(t *testing.T) {
	salida := "Analizando el diff...\n```json\nBEGIN_REVIEW\n{\"dim\":\"security\",\"verdict\":\"ok\"}\nEND_REVIEW\n```\nFin"
	resultado, err := ParsearDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParsearDimensionResult devolvió error: %v", err)
	}
	if resultado.Dim != DimSecurity || resultado.Verdict != VerdictOK {
		t.Errorf("esperado security/ok, obtenido %s/%s", resultado.Dim, resultado.Verdict)
	}
}

func TestParsearDimensionResultSinDelimitadores(t *testing.T) {
	salida := "{\"dim\":\"design\",\"verdict\":\"warn\",\"findings\":[]}\n"
	resultado, err := ParsearDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParsearDimensionResult devolvió error: %v", err)
	}
	if resultado.Dim != DimDesign || resultado.Verdict != VerdictWarn {
		t.Errorf("esperado design/warn, obtenido %s/%s", resultado.Dim, resultado.Verdict)
	}
}

func TestParsearDimensionResultVacia(t *testing.T) {
	_, err := ParsearDimensionResult("")
	if !errors.Is(err, ErrSalidaVacia) {
		t.Errorf("se esperaba ErrSalidaVacia, obtenido %v", err)
	}
}

func TestParsearDimensionResultInvalida(t *testing.T) {
	_, err := ParsearDimensionResult("esto no es json\nBEGIN_REVIEW\ntampoco\nEND_REVIEW\n")
	if !errors.Is(err, ErrJSONLInvalido) {
		t.Errorf("se esperaba ErrJSONLInvalido, obtenido %v", err)
	}
}

func TestParsearDimensionResultDesconocida(t *testing.T) {
	_, err := ParsearDimensionResult(`{"dim":"perf","verdict":"ok"}`)
	if !errors.Is(err, ErrDimensionInvalida) {
		t.Errorf("se esperaba ErrDimensionInvalida, obtenido %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "perf") {
		t.Errorf("el error debería nombrar la dimensión desconocida, obtenido %v", err)
	}
}

func TestParsearDimensionResultVeredictoDeFactoConHallazgos(t *testing.T) {
	// El agente real devuelve a veces "issues" como veredicto con hallazgos:
	// con hallazgos se deriva de las severidades, sin hallazgos es un error.
	salida := `{"dim":"logic","verdict":"issues","findings":[{"dimension":"logic","file":"a.go","line":1,"severity":"WARNING","description":"d"}]}`
	resultado, err := ParsearDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParsearDimensionResult devolvió error: %v", err)
	}
	if resultado.Verdict != VerdictWarn {
		t.Errorf("verdict = %q, esperado %q derivado de WARNING", resultado.Verdict, VerdictWarn)
	}
	if len(resultado.Advertencias) == 0 {
		t.Error("se esperaba advertencia por la normalización del veredicto")
	}

	if _, err := ParsearDimensionResult(`{"dim":"logic","verdict":"issues"}`); !errors.Is(err, ErrVeredictoInvalido) {
		t.Errorf("veredicto de facto sin hallazgos debería ser %v, obtenido %v", ErrVeredictoInvalido, err)
	}
}

func TestParsearDimensionResultOkConCriticoSubeABlock(t *testing.T) {
	salida := `{"dim":"security","verdict":"ok","findings":[{"dimension":"security","file":"a.go","line":2,"severity":"CRITICAL","description":"secreto expuesto"}]}`
	resultado, err := ParsearDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParsearDimensionResult devolvió error: %v", err)
	}
	if resultado.Verdict != VerdictBlock {
		t.Errorf("verdict = %q, esperado %q (los hallazgos mandan)", resultado.Verdict, VerdictBlock)
	}
}

func TestParsearDimensionResultOkConWarningSubeAWarn(t *testing.T) {
	salida := `{"dim":"style","verdict":"ok","findings":[{"dimension":"style","file":"a.go","line":3,"severity":"ADVISORY","description":"nombre confuso"}]}`
	resultado, err := ParsearDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParsearDimensionResult devolvió error: %v", err)
	}
	if resultado.Verdict != VerdictWarn {
		t.Errorf("verdict = %q, esperado %q (ADVISORY presente)", resultado.Verdict, VerdictWarn)
	}
}

func TestParsearDimensionResultQuestionSeRespeta(t *testing.T) {
	salida := `{"dim":"logic","verdict":"question","questions":[{"id":"Q1","text":"¿X?"}]}`
	resultado, err := ParsearDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParsearDimensionResult devolvió error: %v", err)
	}
	if resultado.Verdict != VerdictQuestion {
		t.Errorf("verdict = %q, esperado %q", resultado.Verdict, VerdictQuestion)
	}
}

func TestParsearDimensionResultSeveridadDesconocida(t *testing.T) {
	salida := `{"dim":"logic","verdict":"warn","findings":[{"dimension":"logic","file":"a.go","line":1,"severity":"FATAL","description":"d"}]}`
	resultado, err := ParsearDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParsearDimensionResult devolvió error: %v", err)
	}
	if len(resultado.Findings) != 1 {
		t.Fatalf("findings = %d, esperado 1", len(resultado.Findings))
	}
	if resultado.Findings[0].Severity != SevAdvisory {
		t.Errorf("severity = %q, esperado normalizada a %q", resultado.Findings[0].Severity, SevAdvisory)
	}
	if len(resultado.Advertencias) == 0 {
		t.Error("se esperaba una advertencia por la normalización")
	}
}

func TestParsearDimensionResultDescartaLineasBasura(t *testing.T) {
	// Las líneas que ni siquiera decodifican como JSON se descartan; la línea
	// válida gana. (Una línea JSON válida con dimensión desconocida, en cambio,
	// aborta con error explícito: TestParsearDimensionResultDesconocida.)
	salida := "texto del agente sin sentido\n```\n{\"dim\":\"tests\",\"verdict\":\"ok\"}\n```\n"
	resultado, err := ParsearDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParsearDimensionResult devolvió error: %v", err)
	}
	if resultado.Dim != DimTests || resultado.Verdict != VerdictOK {
		t.Errorf("esperado tests/ok, obtenido %s/%s", resultado.Dim, resultado.Verdict)
	}
	// La línea de texto y el fence no decodifican: deben quedar registrados.
	encontroAdvertencia := false
	for _, adv := range resultado.Advertencias {
		if len(adv) > 0 {
			encontroAdvertencia = true
		}
	}
	if !encontroAdvertencia {
		t.Errorf("se esperaba advertencia por líneas descartadas, advertencias = %v", resultado.Advertencias)
	}
}

func TestParsearDimensionResultPreguntas(t *testing.T) {
	salida := `{"dim":"logic","verdict":"question","questions":[{"id":"Q1","text":"¿El rebase debe abortar si hay cambios sin commitear?"}]}`
	resultado, err := ParsearDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParsearDimensionResult devolvió error: %v", err)
	}
	if resultado.Verdict != VerdictQuestion {
		t.Errorf("verdict = %q, esperado %q", resultado.Verdict, VerdictQuestion)
	}
	if len(resultado.Questions) != 1 || resultado.Questions[0].ID != "Q1" {
		t.Errorf("questions = %+v, esperado una con ID Q1", resultado.Questions)
	}
}

func TestHallazgoSerializacionIdaYVuelta(t *testing.T) {
	original := Hallazgo{
		ID:     "h-1",
		Source: SourceReview,
		Producer: Productor{
			Agente:           "claude",
			Binario:          "",
			Modelo:           "claude-sonnet-5",
			Esfuerzo:         "high",
			ModeloVerificado: true,
		},
		Dimension:   DimLogic,
		Severity:    SevCritical,
		Confidence:  0.87,
		Status:      StatusPending,
		Title:       "condición siempre verdadera",
		Description: "el condicional nunca evalúa a falso por el operador usado",
		Location: Ubicacion{
			Archivo:     "internal/a.go",
			Blob:        "deadbeef",
			LineaInicio: 10,
			LineaFin:    12,
			Simbolo:     "FuncionX",
		},
		Evidence:       "if x >= 0 || x < 0 {",
		Impact:         "la rama de error nunca se ejecuta",
		Recommendation: "usa un único operador de comparación",
		Fixable:        FixableNeedsReview,
		IntroducedBy:   "abc123",
		Fingerprint:    "sha256:xyz",
	}

	crudo, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal devolvió error: %v", err)
	}

	var reconstruido Hallazgo
	if err := json.Unmarshal(crudo, &reconstruido); err != nil {
		t.Fatalf("Unmarshal devolvió error: %v", err)
	}

	if reconstruido != original {
		t.Errorf("el hallazgo no sobrevivió el ciclo completo:\noriginal:      %+v\nreconstruido:  %+v", original, reconstruido)
	}
}

func TestHallazgoConviveConReviewFindingV1EnElMismoPaquete(t *testing.T) {
	// Una ficha v1 debe poder seguir deserializándose sin error en el mismo
	// paquete que ya define el nuevo tipo Hallazgo (v2): ambos conviven sin
	// que la existencia de v2 rompa la lectura de v1.
	crudoV1 := `{"dimension":"logic","file":"a.go","line":5,"severity":"WARNING","description":"d","suggestion":"s"}`
	var findingV1 ReviewFinding
	if err := json.Unmarshal([]byte(crudoV1), &findingV1); err != nil {
		t.Fatalf("Unmarshal de ReviewFinding v1 devolvió error: %v", err)
	}
	if findingV1.Dimension != DimLogic || findingV1.Line != 5 {
		t.Errorf("finding v1 mal deserializado: %+v", findingV1)
	}

	crudoV2 := `{"id":"h-2","source":"validation","producer":{"agent":"validation","model_verified":false},"dimension":"tests","severity":"CRITICAL","confidence":1.0,"status":"pending","title":"t","description":"d","location":{"file":"b.go","line_start":1},"evidence":"e","fixable":"manual","fingerprint":"f"}`
	var hallazgoV2 Hallazgo
	if err := json.Unmarshal([]byte(crudoV2), &hallazgoV2); err != nil {
		t.Fatalf("Unmarshal de Hallazgo v2 devolvió error: %v", err)
	}
	if hallazgoV2.Source != SourceValidation || hallazgoV2.Confidence != 1.0 {
		t.Errorf("hallazgo v2 mal deserializado: %+v", hallazgoV2)
	}
}

func TestParsearDimensionResultUnavailable(t *testing.T) {
	salida := `{"dim":"security","verdict":"unavailable","reason":"rate_limit"}`
	resultado, err := ParsearDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParsearDimensionResult devolvió error: %v", err)
	}
	if resultado.Verdict != VerdictUnavailable || resultado.Reason != "rate_limit" {
		t.Errorf("esperado unavailable/rate_limit, obtenido %s/%s", resultado.Verdict, resultado.Reason)
	}
}
