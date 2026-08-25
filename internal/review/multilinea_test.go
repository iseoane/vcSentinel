package review

import (
	"strings"
	"testing"
)

// TestParsearDimensionResultJSONMultilinea: el perfil cheap puede devolver el
// objeto JSON formateado en varias líneas (pretty-printed) dentro de los
// delimitadores. El parser debe aceptarlo: ninguna línea individual es JSONL
// válido, pero el bloque completo sí es un objeto JSON.
func TestParsearDimensionResultJSONMultilinea(t *testing.T) {
	salida := `BEGIN_REVIEW
{
  "dim": "spec",
  "verdict": "ok",
  "reason": "todo en orden"
}
END_REVIEW`

	res, err := ParseDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParsearDimensionResult devolvió error con JSON pretty-printed: %v", err)
	}
	if res.Dim != "spec" {
		t.Errorf("Dim = %q, esperado spec", res.Dim)
	}
	if res.Verdict != VerdictOK {
		t.Errorf("Verdict = %q, esperado ok", res.Verdict)
	}
}

// TestParsearDimensionResultJSONMultilineaConHallazgos: mismo caso con la
// estructura completa de hallazgos en pretty-printed (severidad, línea como
// string, etc. — lo que el modelo cheap emite de facto).
func TestParsearDimensionResultJSONMultilineaConHallazgos(t *testing.T) {
	salida := `BEGIN_REVIEW
{
  "dim": "spec",
  "verdict": "FAIL",
  "findings": [
    {
      "dimension": "spec",
      "file": ".claudecode.md",
      "line": 1,
      "severity": "CRITICAL",
      "description": "Out-of-scope",
      "suggestion": "Separate commit"
    }
  ],
  "reason": "Motive"
}
END_REVIEW`

	res, err := ParseDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParsearDimensionResult devolvió error con JSON multilínea + hallazgos: %v", err)
	}
	if res.Dim != "spec" {
		t.Errorf("Dim = %q, esperado spec", res.Dim)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("len(Findings) = %d, esperado 1", len(res.Findings))
	}
	h := res.Findings[0]
	if h.File != ".claudecode.md" {
		t.Errorf("File = %q, esperado .claudecode.md", h.File)
	}
	if int(h.Line) != 1 {
		t.Errorf("Line = %d, esperado 1", h.Line)
	}
	if h.Severity != SevCritical {
		t.Errorf("Severity = %q, esperado CRITICAL", h.Severity)
	}
	if res.Verdict != VerdictBlock {
		t.Errorf("Verdict = %q, esperado block (derivado de CRITICAL)", res.Verdict)
	}
}

// TestParsearDimensionResultJSONMultilineaSinDelimitadores: el bloque completo
// también debe funcionar cuando el modelo omite BEGIN_REVIEW/END_REVIEW.
func TestParsearDimensionResultJSONMultilineaSinDelimitadores(t *testing.T) {
	salida := `{
  "dim": "design",
  "verdict": "warn",
  "findings": []
}`

	res, err := ParseDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParsearDimensionResult devolvió error sin delimitadores: %v", err)
	}
	if res.Dim != "design" {
		t.Errorf("Dim = %q, esperado design", res.Dim)
	}
}

// TestParseJSONMultilineaEsFallbackNoSustituto: un JSONL válido de una línea
// debe seguir teniendo prioridad sobre el objeto multilínea (no romper los
// contratos existentes).
func TestParseJSONMultilineaEsFallback(t *testing.T) {
	salida := `BEGIN_REVIEW
{"dim":"logic","verdict":"ok"}
{
  "dim": "spec",
  "verdict": "block"
}
END_REVIEW`

	res, err := ParseDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParsearDimensionResult devolvió error: %v", err)
	}
	if res.Dim != "logic" {
		t.Errorf("Dim = %q, esperado logic (primera línea JSONL gana)", res.Dim)
	}
	if res.Verdict != VerdictOK {
		t.Errorf("Verdict = %q, esperado ok", res.Verdict)
	}
}

// TestParsearDimensionResultJSONMultilineaConCamposV2 verifies that the
// multiline fallback (parseMultilineObject) recognizes v2-only Hallazgo
// fields inside a finding, matching the one-line JSONL path.
func TestParsearDimensionResultJSONMultilineaConCamposV2(t *testing.T) {
	salida := `BEGIN_REVIEW
{
  "dim": "design",
  "verdict": "warn",
  "findings": [
    {
      "file": "b.go",
      "line": 3,
      "severity": "WARNING",
      "description": "acoplamiento innecesario",
      "evidence": "import legacy",
      "title": "acoplamiento a módulo legacy"
    }
  ]
}
END_REVIEW`

	res, err := ParseDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParsearDimensionResult devolvió error con campos v2 en multilínea: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("len(Findings) = %d, esperado 1", len(res.Findings))
	}
	if len(res.Hallazgos) != 1 {
		t.Fatalf("len(Hallazgos) = %d, esperado 1 (campos v2 detectados en pretty-printed)", len(res.Hallazgos))
	}
	if res.Hallazgos[0].Title != "acoplamiento a módulo legacy" {
		t.Errorf("Hallazgos[0].Title = %q, esperado el título del finding", res.Hallazgos[0].Title)
	}
	if res.Hallazgos[0].Dimension != "design" {
		t.Errorf("Hallazgos[0].Dimension = %q, esperado design (dimensión de la línea)", res.Hallazgos[0].Dimension)
	}
}

// TestParseJSONMultilineaConBasuraAlrededor: texto de salida alrededor del
// objeto (razonamiento del agente fuera del bloque) no debe romper el fallback.
func TestParseJSONMultilineaConBasuraAlrededor(t *testing.T) {
	salida := `Analizando el diff:
BEGIN_REVIEW
{
  "dim": "tests",
  "verdict": "ok"
}
END_REVIEW
Fin de la auditoría.`

	res, err := ParseDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParsearDimensionResult devolvió error con texto alrededor: %v", err)
	}
	if res.Dim != "tests" {
		t.Errorf("Dim = %q, esperado tests", res.Dim)
	}
	if !strings.Contains(salida, "BEGIN_REVIEW") {
		t.Fatal("test desconfigurado: el ejemplo debe incluir delimitadores")
	}
}
