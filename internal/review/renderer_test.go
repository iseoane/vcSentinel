package review

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// fichaAyuda construye una ficha realista para los tests del renderer.
func fichaAyuda(sha, mensaje, modelo string, revs ...Revision) Ficha {
	return Ficha{SHA: sha, Message: mensaje, Model: modelo, Revisions: revs}
}

// revisionAyuda construye una revisión con el veredicto y hallazgos dados.
func revisionAyuda(resultado string, dims ...DimensionResult) Revision {
	return Revision{At: time.Now().UTC(), Result: resultado, Dims: dims}
}

func TestRenderMatrizBasica(t *testing.T) {
	fichas := []Ficha{
		fichaAyuda("6b127cd", "docs(review): concepto fase 2", "opencode.cheap",
			revisionAyuda("ok",
				DimensionResult{Dim: DimSpec, Verdict: VerdictOK},
				DimensionResult{Dim: DimSecurity, Verdict: VerdictBlock},
				DimensionResult{Dim: DimLogic, Verdict: VerdictOK},
			)),
		fichaAyuda("945b5b5", "feat(config): comandos de verificacion", "deepseek-v4-flash-free",
			revisionAyuda("warn",
				DimensionResult{Dim: DimSpec, Verdict: VerdictOK},
				DimensionResult{Dim: DimTests, Verdict: VerdictWarn},
			)),
	}

	salida := RenderMatriz(fichas)

	// Aserción exacta: cabecera completa canónica, filas en orden y celdas
	// con el veredicto de la última revisión (— para dimensiones ausentes).
	esperado := "| Commit | logic | style | design | tests | security | spec |\n" +
		"|---|---|---|---|---|---|---|\n" +
		"| `6b127cd` docs(review): concepto fase 2 | ✅ | — | — | — | 🚨 | ✅ |\n" +
		"| `945b5b5` feat(config): comandos de verificacion | — | — | — | ⚠️ | — | ✅ |\n"
	if salida != esperado {
		t.Errorf("matriz no coincide:\ngot:\n%s\nwant:\n%s", salida, esperado)
	}
}

func TestRenderMatrizVacia(t *testing.T) {
	if salida := RenderMatriz(nil); !strings.Contains(salida, "No hay commits auditados") {
		t.Errorf("matriz vacía = %q, esperado aviso de sin commits", salida)
	}
}

func TestRenderMatrizRevisionPasada(t *testing.T) {
	ficha := fichaAyuda("945b5b5", "feat(config): comandos", "opencode.cheap",
		revisionAyuda("block",
			DimensionResult{Dim: DimSpec, Verdict: VerdictBlock,
				Findings: []ReviewFinding{{Dimension: DimSpec, File: "a.go", Line: 1, Severity: SevCritical}}},
		),
		revisionAyuda("ok",
			DimensionResult{Dim: DimSpec, Verdict: VerdictOK},
		),
	)

	salida := RenderMatriz([]Ficha{ficha})

	// La última revisión manda y la pasada tras un block se marca.
	if !strings.Contains(salida, "✅ (2ª rev — CRITICAL superado)") {
		t.Errorf("falta la marca de revisión pasada:\n%s", salida)
	}
	if strings.Contains(salida, "🚨") {
		t.Errorf("la matriz no debe mostrar el block de la primera revisión:\n%s", salida)
	}
}

func TestRenderResumenRiesgos(t *testing.T) {
	fichas := []Ficha{
		fichaAyuda("6b127cd", "docs(review): concepto fase 2", "opencode.cheap",
			revisionAyuda("block",
				DimensionResult{Dim: DimSecurity, Verdict: VerdictBlock,
					Findings: []ReviewFinding{
						{Dimension: DimSecurity, File: "a.go", Line: 42, Severity: SevCritical, Description: "dato expuesto"},
						{Dimension: DimSecurity, File: "a.go", Line: 10, Severity: SevAdvisory, Description: "sugerencia menor"},
					}},
			)),
		fichaAyuda("945b5b5", "feat(config): comandos", "deepseek-v4-flash-free",
			revisionAyuda("warn",
				DimensionResult{Dim: DimTests, Verdict: VerdictWarn,
					Findings: []ReviewFinding{
						{Dimension: DimTests, File: "z.go", Line: 10, Severity: SevWarning, Description: "test frágil"},
					}},
			)),
	}

	salida := RenderResumen(fichas)

	// Conteo global.
	if !strings.Contains(salida, "🟢 ok: 0 · 🟡 warn: 1 · 🚨 block: 1") {
		t.Errorf("conteo global incorrecto:\n%s", salida)
	}
	// Riesgos: CRITICAL y WARNING sí; ADVISORY no.
	if !strings.Contains(salida, "🚨 `6b127cd` [security] CRITICAL — dato expuesto (a.go:42)") {
		t.Errorf("falta el riesgo CRITICAL:\n%s", salida)
	}
	if !strings.Contains(salida, "⚠️ `945b5b5` [tests] WARNING — test frágil (z.go:10)") {
		t.Errorf("falta el riesgo WARNING:\n%s", salida)
	}
	if strings.Contains(salida, "sugerencia menor") {
		t.Errorf("los ADVISORY no son riesgos y no deben aparecer en el resumen:\n%s", salida)
	}
}

func TestRenderResumenCorregida(t *testing.T) {
	ficha := fichaAyuda("6b127cd", "docs(review): concepto", "opencode.cheap",
		revisionAyuda("block", DimensionResult{Dim: DimSpec, Verdict: VerdictBlock}),
	)
	ficha.FixedIn = "a1b2c3d"

	salida := RenderResumen([]Ficha{ficha})
	if !strings.Contains(salida, "🔧 corregida en `a1b2c3d`") {
		t.Errorf("falta la marca de corrección:\n%s", salida)
	}
}

func TestTruncarCuerpo(t *testing.T) {
	corto := "texto breve"
	if got := TruncarCuerpo(corto, 1024); got != corto {
		t.Errorf("TruncarCuerpo(corto) = %q, esperado sin cambios", got)
	}

	largo := strings.Repeat("x", 100)
	got := TruncarCuerpo(largo, 50)
	if len(got) > 50 {
		t.Errorf("TruncarCuerpo = %d bytes, esperado ≤50", len(got))
	}
	if !strings.Contains(got, "truncado") || !strings.Contains(got, "omitieron") {
		t.Errorf("el truncamiento debe estar marcado explícitamente, got: %q", got)
	}
}

// TestTruncarCuerpoLimiteNulo: sin límite (0 o negativo) el texto no cambia.
func TestTruncarCuerpoLimiteNulo(t *testing.T) {
	texto := "abc"
	if got := TruncarCuerpo(texto, 0); got != texto {
		t.Errorf("TruncarCuerpo(0) = %q, esperado sin cambios", got)
	}
	if got := TruncarCuerpo(texto, -5); got != texto {
		t.Errorf("TruncarCuerpo(-5) = %q, esperado sin cambios", got)
	}
}

// TestTruncarCuerpoMarcadorMayorQueLimite: cuando ni el marcador cabe, el
// resultado es solo el marcador recortado al límite, sin contenido del texto.
func TestTruncarCuerpoMarcadorMayorQueLimite(t *testing.T) {
	got := TruncarCuerpo(strings.Repeat("x", 500), 10)
	if len(got) > 10 {
		t.Errorf("TruncarCuerpo = %d bytes, esperado ≤10", len(got))
	}
	if strings.Contains(got, "x") {
		t.Errorf("el resultado no debe contener contenido del texto, got: %q", got)
	}
}

// TestTruncarCuerpoNoParteRunas: el corte nunca parte una runa UTF-8 y el
// resultado siempre es texto válido.
func TestTruncarCuerpoNoParteRunas(t *testing.T) {
	texto := "áéíóúüñ " + strings.Repeat("ñ", 200)
	got := TruncarCuerpo(texto, 57)
	if len(got) > 57 {
		t.Errorf("TruncarCuerpo = %d bytes, esperado ≤57", len(got))
	}
	if !utf8.ValidString(got) {
		t.Errorf("TruncarCuerpo partió una runa UTF-8: %q", got)
	}
}

// TestRenderResumenVacio: sin fichas, el resumen avisa en lugar de inventar.
func TestRenderResumenVacio(t *testing.T) {
	if salida := RenderResumen(nil); !strings.Contains(salida, "No hay commits auditados") {
		t.Errorf("resumen vacío = %q, esperado aviso de sin commits", salida)
	}
}

// TestConteoQuestionUnavailable: el conteo global incluye question y
// unavailable solo cuando existen.
func TestConteoQuestionUnavailable(t *testing.T) {
	fichas := []Ficha{
		fichaAyuda("aaaaaaa", "feat(a): a", "m",
			revisionAyuda(VerdictQuestion, DimensionResult{Dim: DimSpec, Verdict: VerdictQuestion})),
		fichaAyuda("bbbbbbb", "feat(b): b", "m",
			revisionAyuda(VerdictUnavailable, DimensionResult{Dim: DimSpec, Verdict: VerdictUnavailable})),
	}

	salida := RenderResumen(fichas)
	if !strings.Contains(salida, "❓ question: 1") || !strings.Contains(salida, "⛔ unavailable: 1") {
		t.Errorf("faltan los conteos de question/unavailable:\n%s", salida)
	}
	if strings.Contains(salida, "🔧") {
		t.Errorf("no debe haber correcciones en este fixture:\n%s", salida)
	}
}
