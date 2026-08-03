package main

import (
	"errors"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

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
	resultados := dimsResultadosParaFicha(rd)
	if len(resultados) != 2 {
		t.Fatalf("dimsResultadosParaFicha = %d resultados, esperado 2 (filtra nil)", len(resultados))
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
