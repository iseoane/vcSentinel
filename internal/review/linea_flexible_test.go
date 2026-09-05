package review

import (
	"testing"
)

// TestParsearDimensionResultLineaComoString cubre el caso real del agente que
// serializa "line" como string ("126" en lugar de 126): json.Unmarshal contra
// int falla, la línea se descarta y la auditoría degrada a unavailable
// (ErrJSONLInvalido). El parseo debe aceptar ambos tipos.
func TestParsearDimensionResultLineaComoString(t *testing.T) {
	salida := `{"dim":"spec","verdict":"FAIL","findings":[{"dimension":"spec","file":"README.md","line":"126","severity":"CRITICAL","description":"d"}]}`
	resultado, err := ParseDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParsearDimensionResult devolvió error con line como string: %v", err)
	}
	if resultado.Dim != DimSpec {
		t.Errorf("dim = %q, esperado %q", resultado.Dim, DimSpec)
	}
	if len(resultado.Findings) != 1 {
		t.Fatalf("findings = %d, esperado 1", len(resultado.Findings))
	}
	if resultado.Findings[0].Line != 126 {
		t.Errorf("line = %d, esperado 126", resultado.Findings[0].Line)
	}
	if resultado.Verdict != VerdictBlock {
		t.Errorf("verdict = %q, esperado %q (CRITICAL eleva a block)", resultado.Verdict, VerdictBlock)
	}
}

// TestParsearDimensionResultLineaNumeroAsegura que la forma numérica sigue
// funcionando tras aceptar la forma string.
func TestParsearDimensionResultLineaNumero(t *testing.T) {
	salida := `{"dim":"spec","verdict":"FAIL","findings":[{"dimension":"spec","file":"a.go","line":42,"severity":"CRITICAL","description":"d"}]}`
	resultado, err := ParseDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParsearDimensionResult devolvió error: %v", err)
	}
	if len(resultado.Findings) != 1 || resultado.Findings[0].Line != 42 {
		t.Fatalf("findings = %+v, esperado line 42", resultado.Findings)
	}
}

// TestParsearDimensionResultLineaNoNumerica: a non-numeric string, or any
// other JSON type, no longer discards the finding (FU-19): the line stays 0
// (unknown, the line-less convention) and the raw text is preserved in
// LineRaw — never a panic nor an invented number.
func TestParsearDimensionResultLineaNoNumerica(t *testing.T) {
	resultado, err := ParseDimensionResult(`{"dim":"spec","verdict":"FAIL","findings":[{"dimension":"spec","file":"a.go","line":"L126-130","severity":"CRITICAL","description":"d"}]}`)
	if err != nil {
		t.Fatalf("a non-numeric line must not discard the whole payload: %v", err)
	}
	if len(resultado.Findings) != 1 || resultado.Findings[0].Line != 0 {
		t.Fatalf("findings = %+v, want 1 finding with unknown line", resultado.Findings)
	}
	if resultado.Findings[0].LineRaw != "L126-130" {
		t.Errorf("line_raw = %q, want the preserved raw text", resultado.Findings[0].LineRaw)
	}
}
