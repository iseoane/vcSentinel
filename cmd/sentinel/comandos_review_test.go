package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

// TestEjecutarReview_ClaveDesconocidaEnYml_Exit1ConLinea cubre el Fix 1 (F1,
// hallazgo del orquestador): ejecutarReview usaba CargarConfiguracionLocal
// (sin error). Con un yml roto, antes seguía adelante en silencio hasta
// fallar más tarde con un error de git ajeno al problema real (el worktree de
// este test no es un repo); con CargarConfiguracionLocalEstricta debe cortar
// aquí mismo con exit 1 y el error del yml visible.
func TestEjecutarReview_ClaveDesconocidaEnYml_Exit1ConLinea(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	escribirYmlConClaveDesconocida(t, worktree)

	salida, exit := ejecutarComoSubproceso(t, "ejecutarReview", worktree, home)

	if exit != 1 {
		t.Errorf("exit esperado 1, obtuve %d (salida: %q)", exit, salida)
	}
	if !strings.Contains(salida, "line") {
		t.Errorf("la salida debe incluir la línea del error del yml, obtuve: %q", salida)
	}
}

func TestCodigoSalidaVeredicto(t *testing.T) {
	pruebas := []struct {
		veredicto string
		esperado  int
	}{
		{review.VerdictOK, 0},
		{review.VerdictWarn, 0},
		{review.VerdictBlock, 1},
		{review.VerdictQuestion, 3},
		{review.VerdictUnavailable, 4},
	}
	for _, prueba := range pruebas {
		if got := codigoSalidaVeredicto(prueba.veredicto); got != prueba.esperado {
			t.Errorf("codigoSalidaVeredicto(%q) = %d, esperado %d", prueba.veredicto, got, prueba.esperado)
		}
	}
}

func TestCalcularBucket(t *testing.T) {
	pruebas := []struct {
		nombre   string
		archivos []string
		esperado string
	}{
		{"un archivo backend", []string{"cmd/main.go"}, "backend"},
		{"config", []string{"vassentinel.yml"}, "config"},
		{"varias capas", []string{"internal/a.go", "internal/a_test.go"}, "mixto"},
		{"sin archivos", nil, "backend"},
	}
	for _, prueba := range pruebas {
		if got := calcularBucket(prueba.archivos); got != prueba.esperado {
			t.Errorf("%s: calcularBucket = %q, esperado %q", prueba.nombre, got, prueba.esperado)
		}
	}
}

func TestDimsResultadosParaFicha(t *testing.T) {
	rd := []review.ResultadoDimension{
		{Dim: review.DimLogic, Resultado: &review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictOK}},
		{Dim: review.DimSecurity, Error: errors.New("fallo")},
		{Dim: review.DimSpec, Resultado: &review.DimensionResult{Dim: review.DimSpec, Verdict: review.VerdictBlock}},
	}
	resultados := review.DimsResultadosParaFicha(rd)
	if len(resultados) != 2 {
		t.Fatalf("DimsResultadosParaFicha = %d resultados, esperado 2 (filtra nil)", len(resultados))
	}
	if resultados[0].Verdict != review.VerdictOK || resultados[1].Verdict != review.VerdictBlock {
		t.Errorf("verdicts = %q/%q, esperado ok/block", resultados[0].Verdict, resultados[1].Verdict)
	}
}

func TestTieneHallazgosCriticos(t *testing.T) {
	construir := func(severidad string) review.ResultadoAuditoria {
		return review.ResultadoAuditoria{
			Dims: []review.ResultadoDimension{{
				Dim: review.DimSecurity,
				Resultado: &review.DimensionResult{
					Dim:     review.DimSecurity,
					Verdict: review.VerdictWarn,
					Findings: []review.ReviewFinding{{
						Severity: severidad,
						File:     "a.go",
					}},
				},
			}},
		}
	}

	if !tieneHallazgosCriticos(construir(review.SevCritical)) {
		t.Error("debería detectar CRITICAL")
	}
	if tieneHallazgosCriticos(construir(review.SevWarning)) {
		t.Error("WARNING no debería contar como crítico")
	}
	if tieneHallazgosCriticos(review.ResultadoAuditoria{}) {
		t.Error("sin hallazgos no debería haber críticos")
	}
}

func TestRevisionCorrigeBlockPrevio(t *testing.T) {
	dir := t.TempDir()
	ledger := review.NuevoLedger(dir)

	// Sin ficha previa: no corrige.
	if review.RevisionCorrigeBlockPrevio(ledger, "abc123", review.VerdictOK) {
		t.Error("sin ficha previa no debería marcar corrección")
	}

	// Previa en block y nueva sin block: corrige.
	rev := review.Revision{At: time.Now().UTC(), Result: review.VerdictBlock}
	if err := ledger.GuardarRevision("abc123", "msg", "backend", "m", rev); err != nil {
		t.Fatal(err)
	}
	if !review.RevisionCorrigeBlockPrevio(ledger, "abc123", review.VerdictOK) {
		t.Error("previa en block y nueva ok debería marcar corrección")
	}
	if review.RevisionCorrigeBlockPrevio(ledger, "abc123", review.VerdictBlock) {
		t.Error("nueva en block no corrige nada")
	}
}

func TestRegistrarCorreccionesMarcaFicha(t *testing.T) {
	dir := t.TempDir()
	ledger := review.NuevoLedger(dir)

	// Ficha previa en block con hallazgo en internal/a.go.
	rev := review.Revision{
		At: time.Now().UTC(), Result: review.VerdictBlock,
		Dims: []review.DimensionResult{{
			Dim: review.DimLogic, Verdict: review.VerdictBlock,
			Findings: []review.ReviewFinding{{
				Severity: review.SevCritical, File: "internal/a.go", Line: 10,
				Description: "bug real",
			}},
		}},
	}
	if err := ledger.GuardarRevision("aaa111", "feat(x): con bug", "backend", "m", rev); err != nil {
		t.Fatal(err)
	}

	// Un fix que toca internal/a.go sale sin críticos: debe marcar la ficha.
	registrarCorrecciones(ledger, dir, "bbb222", []string{"internal/a.go"}, "fix(x): arregla", 0, "worktree")
	ficha, err := ledger.LeerFicha("aaa111")
	if err != nil {
		t.Fatal(err)
	}
	if ficha.FixedIn != "bbb222" {
		t.Errorf("FixedIn = %q, esperado bbb222", ficha.FixedIn)
	}

	// Un fix que NO toca los archivos del hallazgo no marca nada.
	if err := ledger.GuardarRevision("ccc333", "feat(y): otro", "backend", "m",
		review.Revision{At: time.Now().UTC(), Result: review.VerdictBlock,
			Dims: []review.DimensionResult{{Dim: review.DimLogic, Verdict: review.VerdictBlock,
				Findings: []review.ReviewFinding{{Severity: review.SevCritical, File: "internal/b.go", Line: 1, Description: "otro"}}}}},
	); err != nil {
		t.Fatal(err)
	}
	registrarCorrecciones(ledger, dir, "ddd444", []string{"internal/a.go"}, "fix(y): arregla", 0, "worktree")
	ficha, err = ledger.LeerFicha("ccc333")
	if err != nil {
		t.Fatal(err)
	}
	if ficha.FixedIn != "" {
		t.Errorf("FixedIn = %q, esperado vacío (el fix no toca b.go)", ficha.FixedIn)
	}

	// Un commit que sale en block nunca registra correcciones.
	registrarCorrecciones(ledger, dir, "eee555", []string{"internal/a.go"}, "fix(z): intento", 1, "worktree")
	ficha, err = ledger.LeerFicha("ccc333")
	if err != nil {
		t.Fatal(err)
	}
	if ficha.FixedIn != "" {
		t.Errorf("FixedIn = %q, esperado vacío (el fix salió en block)", ficha.FixedIn)
	}
}
