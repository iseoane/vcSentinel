package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

// TestVerboPr: el dispatch decide entre review, create y el passthrough
// legacy a gh pr create.
func TestVerboPr(t *testing.T) {
	casos := []struct {
		args []string
		want string
	}{
		{[]string{}, "legacy"},
		{[]string{"review"}, "review"},
		{[]string{"review", "--base", "dev"}, "review"},
		{[]string{"create"}, "create"},
		{[]string{"create", "--draft"}, "create"},
		{[]string{"--title", "hola"}, "legacy"},
		{[]string{"-t", "hola"}, "legacy"},
	}
	for _, caso := range casos {
		if got := verboPr(caso.args); got != caso.want {
			t.Errorf("verboPr(%v) = %q, esperado %q", caso.args, got, caso.want)
		}
	}
}

// TestParsearFlagsPrReview cubre los flags de pr review.
func TestParsearFlagsPrReview(t *testing.T) {
	flags, err := parsearFlagsPrReview([]string{"--base", "dev", "--overview", "--json"})
	if err != nil {
		t.Fatalf("parsearFlagsPrReview falló: %v", err)
	}
	if flags.base != "dev" || !flags.overview || !flags.jsonOut || flags.soloPendientes {
		t.Errorf("flags = %+v, esperado base=dev overview json", flags)
	}

	flags, err = parsearFlagsPrReview([]string{"--only-unaudited"})
	if err != nil {
		t.Fatalf("parsearFlagsPrReview(--only-unaudited) falló: %v", err)
	}
	if !flags.soloPendientes {
		t.Errorf("flags = %+v, esperado soloPendientes", flags)
	}

	if _, err := parsearFlagsPrReview([]string{"--nope"}); err == nil {
		t.Error("flag desconocido debería fallar")
	}
	if _, err := parsearFlagsPrReview([]string{"--base"}); err == nil {
		t.Error("--base sin valor debería fallar")
	}
}

// TestDetalleEventoPrReview: el detail del evento es JSON con los campos del
// esquema de la guía §13.
func TestDetalleEventoPrReview(t *testing.T) {
	res := &review.ResultadoRama{
		Rama:       "feature/x",
		SHAs:       []string{"a1b2c3d4e5f6"},
		Pendientes: []string{"a1b2c3d4e5f6"},
		Fichas:     []review.Ficha{{SHA: "a1b2c3d4e5f6", Message: "feat: algo"}},
		Volumen:    420,
		Decision:   "chain",
	}
	detalle, err := detalleEventoPrReview("main", res, true)
	if err != nil {
		t.Fatalf("detalleEventoPrReview falló: %v", err)
	}

	var crudo map[string]any
	if err := json.Unmarshal([]byte(detalle), &crudo); err != nil {
		t.Fatalf("detail no es JSON válido: %v\n%s", err, detalle)
	}
	for clave, esperado := range map[string]any{
		"base":      "main",
		"rama":      "feature/x",
		"auditadas": float64(1),
		"nuevas":    float64(1),
		"volumen":   float64(420),
		"ci":        true,
		"overview":  false,
		"chain_pr":  true,
	} {
		if crudo[clave] != esperado {
			t.Errorf("detail[%q] = %v, esperado %v", clave, crudo[clave], esperado)
		}
	}
	if _, hay := crudo["overview_error"]; hay {
		t.Errorf("detail[overview_error] presente sin OverviewError: %v", crudo["overview_error"])
	}

	// Con OverviewError, el evento debe llevarlo para que un fallo del
	// overview no quede en silencio.
	res.OverviewError = "el auditor de rama no respondió: boom"
	detalle, err = detalleEventoPrReview("main", res, true)
	if err != nil {
		t.Fatalf("detalleEventoPrReview falló: %v", err)
	}
	if err := json.Unmarshal([]byte(detalle), &crudo); err != nil {
		t.Fatalf("detail no es JSON válido: %v\n%s", err, detalle)
	}
	if crudo["overview_error"] != "el auditor de rama no respondió: boom" {
		t.Errorf("detail[overview_error] = %v", crudo["overview_error"])
	}
}

// TestTextoDecisionPrReview: la salida de decisión distingue single/chain y
// explica el motivo.
func TestTextoDecisionPrReview(t *testing.T) {
	single := textoDecision("single", 100)
	if !strings.Contains(single, "una sola PR") || strings.Contains(single, "cadena") {
		t.Errorf(`textoDecision("single", 100) = %q`, single)
	}
	chain := textoDecision("chain", 450)
	if !strings.Contains(chain, "cadena") {
		t.Errorf(`textoDecision("chain", 450) = %q`, chain)
	}
	singleGrande := textoDecision("single", 450)
	if !strings.Contains(singleGrande, "coherente") {
		t.Errorf(`textoDecision("single", 450) = %q`, singleGrande)
	}
}

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
