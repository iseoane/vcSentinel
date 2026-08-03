package review

import (
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
