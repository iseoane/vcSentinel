package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

// fichaCreateAyuda construye una ficha para los tests de pr create.
func fichaCreateAyuda(sha, resultado string, dims ...review.DimensionResult) review.Ficha {
	return review.Ficha{
		SHA:     sha,
		Message: "feat(x): cambio",
		Model:   "test",
		Revisions: []review.Revision{{
			At:     time.Now().UTC(),
			Result: resultado,
			Dims:   dims,
		}},
	}
}

// hallazgoCritico es un hallazgo CRITICAL mínimo para el gate.
func hallazgoCritico() review.ReviewFinding {
	return review.ReviewFinding{
		Dimension:   review.DimSecurity,
		File:        "internal/x/x.go",
		Line:        42,
		Severity:    review.SevCritical,
		Description: "secreto en el log",
	}
}

func TestGateBlockPermiteSinBlock(t *testing.T) {
	fichas := []review.Ficha{
		fichaCreateAyuda("abc1234", review.VerdictOK,
			review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictOK}),
	}
	permitido, bloqueantes := gateBlock(fichas, false)
	if !permitido {
		t.Fatal("una rama sin block debe permitir publicar")
	}
	if len(bloqueantes) != 0 {
		t.Fatalf("sin bloqueantes, lista = %v", bloqueantes)
	}
}

func TestGateBlockListaCriticosYNiega(t *testing.T) {
	fichas := []review.Ficha{
		fichaCreateAyuda("abc1234", review.VerdictBlock,
			review.DimensionResult{
				Dim:      review.DimSecurity,
				Verdict:  review.VerdictBlock,
				Findings: []review.ReviewFinding{hallazgoCritico()},
			}),
	}
	permitido, bloqueantes := gateBlock(fichas, false)
	if permitido {
		t.Fatal("block sin --force debe negar la publicación")
	}
	if len(bloqueantes) != 1 {
		t.Fatalf("debe listar el CRITICAL, lista = %v", bloqueantes)
	}
	if !strings.Contains(bloqueantes[0], "CRITICAL") || !strings.Contains(bloqueantes[0], "secreto en el log") {
		t.Errorf("el bloqueante debe citar severidad y descripción: %s", bloqueantes[0])
	}
}

func TestGateBlockForceSupera(t *testing.T) {
	fichas := []review.Ficha{
		fichaCreateAyuda("abc1234", review.VerdictBlock,
			review.DimensionResult{
				Dim:      review.DimSecurity,
				Verdict:  review.VerdictBlock,
				Findings: []review.ReviewFinding{hallazgoCritico()},
			}),
	}
	permitido, bloqueantes := gateBlock(fichas, true)
	if !permitido {
		t.Fatal("--force debe superar el gate de block")
	}
	if len(bloqueantes) != 0 {
		t.Fatalf("con --force no se lista bloqueantes, lista = %v", bloqueantes)
	}
}

func TestCopiarPortapapelesSinHerramienta(t *testing.T) {
	err := copiarPortapapelesCon("cuerpo",
		func(string) bool { return false },
		func(string, string) error { t.Fatal("no debe ejecutar nada"); return nil })
	if err == nil {
		t.Fatal("sin herramienta disponible debe fallar explícitamente")
	}
}

func TestCopiarPortapapelesPrimeraDisponible(t *testing.T) {
	var ejecutado string
	err := copiarPortapapelesCon("cuerpo",
		func(nombre string) bool { return nombre == "wl-copy" },
		func(nombre, contenido string) error {
			ejecutado = nombre
			if contenido != "cuerpo" {
				t.Errorf("contenido = %q, esperado %q", contenido, "cuerpo")
			}
			return nil
		})
	if err != nil {
		t.Fatalf("fallback no debería fallar: %v", err)
	}
	if ejecutado != "wl-copy" {
		t.Fatalf("debe usar la primera herramienta disponible, usó %q", ejecutado)
	}
}

func TestCopiarPortapapelesFalloSePropaga(t *testing.T) {
	err := copiarPortapapelesCon("cuerpo",
		func(nombre string) bool { return nombre == "clip" },
		func(string, string) error { return errors.New("clip roto") })
	if err == nil || !strings.Contains(err.Error(), "clip") {
		t.Fatalf("el fallo de la herramienta debe propagarse: %v", err)
	}
}

func TestDetalleEventoPrCreate(t *testing.T) {
	detalle, err := detalleEventoPrCreate("https://github.com/x/pr/1", false, false)
	if err != nil {
		t.Fatalf("detalle no debería fallar: %v", err)
	}
	var crudo map[string]any
	if err := json.Unmarshal([]byte(detalle), &crudo); err != nil {
		t.Fatalf("detail debe ser JSON válido: %v", err)
	}
	if crudo["pr_url"] != "https://github.com/x/pr/1" {
		t.Errorf("pr_url = %v", crudo["pr_url"])
	}
	if crudo["fallback"] != false || crudo["chain_pr"] != false {
		t.Errorf("fallback/chain_pr = %v/%v", crudo["fallback"], crudo["chain_pr"])
	}
}

func TestDetalleEventoPrCreateFallbackYChain(t *testing.T) {
	detalle, err := detalleEventoPrCreate("", true, true)
	if err != nil {
		t.Fatalf("detalle no debería fallar: %v", err)
	}
	var crudo map[string]any
	if err := json.Unmarshal([]byte(detalle), &crudo); err != nil {
		t.Fatalf("detail debe ser JSON válido: %v", err)
	}
	if crudo["fallback"] != true || crudo["chain_pr"] != true {
		t.Errorf("fallback/chain_pr = %v/%v", crudo["fallback"], crudo["chain_pr"])
	}
	if crudo["pr_url"] != "" {
		t.Errorf("pr_url debe quedar vacío en fallback, = %v", crudo["pr_url"])
	}
}

func TestParsearFlagsPrCreate(t *testing.T) {
	flags, err := parsearFlagsPrCreate([]string{"--base", "develop", "--chain-pr", "--force"})
	if err != nil {
		t.Fatalf("parseo no debería fallar: %v", err)
	}
	if flags.base != "develop" || !flags.chainPR || !flags.force {
		t.Errorf("flags = %+v", flags)
	}
}

func TestParsearFlagsPrCreateBaseSinValor(t *testing.T) {
	if _, err := parsearFlagsPrCreate([]string{"--base"}); err == nil {
		t.Fatal("--base sin valor debe fallar")
	}
}

func TestParsearFlagsPrCreateDesconocida(t *testing.T) {
	if _, err := parsearFlagsPrCreate([]string{"--nope"}); err == nil {
		t.Fatal("opción desconocida debe fallar")
	}
}
