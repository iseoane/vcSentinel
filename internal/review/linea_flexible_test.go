package review

import (
	"errors"
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

// TestParsearDimensionResultLineaNoNumerica: un string que no es número, o un
// JSON de otro tipo, sigue siendo una línea inválida (se descarta) — nunca un
// panic ni un número inventado.
func TestParsearDimensionResultLineaNoNumerica(t *testing.T) {
	_, err := ParseDimensionResult(`{"dim":"spec","verdict":"FAIL","findings":[{"dimension":"spec","file":"a.go","line":"L126-130","severity":"CRITICAL","description":"d"}]}`)
	if !errors.Is(err, ErrJSONLInvalido) {
		t.Errorf("se esperaba ErrJSONLInvalido (línea descartada), obtenido %v", err)
	}
}
