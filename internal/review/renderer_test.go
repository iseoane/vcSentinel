package review

import (
	"strings"
	"testing"
	"time"
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

	// Cabecera: las seis dimensiones canónicas siempre, en orden fijo.
	if !strings.Contains(salida, "| Commit | logic | style | design | tests | security | spec |") {
		t.Errorf("cabecera de matriz incorrecta:\n%s", salida)
	}
	// Celdas por commit: block en security, warn en tests, dim ausente como —.
	if !strings.Contains(salida, "`6b127cd`") || !strings.Contains(salida, "🚨") {
		t.Errorf("falta la fila de 6b127cd con block:\n%s", salida)
	}
	if !strings.Contains(salida, "`945b5b5`") || !strings.Contains(salida, "⚠️") {
		t.Errorf("falta la fila de 945b5b5 con warn:\n%s", salida)
	}
	if !strings.Contains(salida, "| — |") {
		t.Errorf("faltan celdas de dimensión ausente (—):\n%s", salida)
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
