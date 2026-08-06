package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/ops"
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
	h := bloqueantes[0]
	if h.Severity != review.SevCritical || h.Description != "secreto en el log" {
		t.Errorf("el bloqueante debe conservar severidad y descripción: %+v", h)
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

// TestVerificarParaPlantillaErrorNuncaSilencioso: un fallo de la verificación
// se refleja como motivo en la plantilla (contrato "nunca en silencio").
func TestVerificarParaPlantillaErrorNuncaSilencioso(t *testing.T) {
	plantilla := verificarParaPlantillaCon("worktree", "gitdir", config.Config{},
		func(ops.OpcionesVerificar) (ops.ResultadoVerificacion, error) {
			return ops.ResultadoVerificacion{}, errors.New("build roto")
		})
	if plantilla.Modo != ops.ModoOmitido {
		t.Errorf("con error el modo debe ser omitido, got %q", plantilla.Modo)
	}
	if !strings.Contains(plantilla.Motivo, "error_de_verificacion") ||
		!strings.Contains(plantilla.Motivo, "build roto") {
		t.Errorf("el motivo debe reflejar el error real, got %q", plantilla.Motivo)
	}
}

// TestVerificarParaPlantillaTraduceComandos: un resultado determinista se
// traduce a ComandoVerificado con su exit code real.
func TestVerificarParaPlantillaTraduceComandos(t *testing.T) {
	plantilla := verificarParaPlantillaCon("worktree", "gitdir", config.Config{},
		func(ops.OpcionesVerificar) (ops.ResultadoVerificacion, error) {
			return ops.ResultadoVerificacion{
				Modo: ops.ModoDeterminista,
				Comandos: []ops.ResultadoComando{
					{Comando: "go test ./...", Exit: 0},
					{Comando: "go vet ./...", Exit: 1},
				},
			}, nil
		})
	if plantilla.Modo != ops.ModoDeterminista {
		t.Errorf("modo = %q, esperado determinista", plantilla.Modo)
	}
	if len(plantilla.Comandos) != 2 {
		t.Fatalf("debe traducir los 2 comandos, got %d", len(plantilla.Comandos))
	}
	if plantilla.Comandos[0].Comando != "go test ./..." || plantilla.Comandos[0].Exit != 0 {
		t.Errorf("comando 1 mal traducido: %+v", plantilla.Comandos[0])
	}
	if plantilla.Comandos[1].Exit != 1 {
		t.Errorf("el exit code 1 debe conservarse: %+v", plantilla.Comandos[1])
	}
	if plantilla.Motivo != "" {
		t.Errorf("sin error no debe haber motivo, got %q", plantilla.Motivo)
	}
}

// TestPublicarPRFallbackReleeElArchivo: sin gh, el cuerpo del fallback se
// re-lee del archivo recién escrito (no del parámetro perdido).
func TestPublicarPRFallbackReleeElArchivo(t *testing.T) {
	ruta := filepath.Join(t.TempDir(), "plantilla.md")
	if err := os.WriteFile(ruta, []byte("cuerpo del PR"), 0o644); err != nil {
		t.Fatal(err)
	}
	var copiado string
	url, fallback, err := publicarPRCon("worktree", ruta,
		func(string) bool { return false },
		func(texto string) error { copiado = texto; return nil })
	if err != nil {
		t.Fatalf("fallback no debería fallar: %v", err)
	}
	if !fallback {
		t.Fatal("sin gh debe activarse el fallback")
	}
	if url != "" {
		t.Errorf("en fallback la URL debe quedar vacía, got %q", url)
	}
	if copiado != "cuerpo del PR" {
		t.Errorf("el cuerpo copiado debe releerse del archivo, got %q", copiado)
	}
}

// TestPublicarPRFallbackArchivoIlegible: si el archivo desaparece entre la
// escritura y la re-lectura, el error es explícito y el fallback se marca.
func TestPublicarPRFallbackArchivoIlegible(t *testing.T) {
	ruta := filepath.Join(t.TempDir(), "fantasma.md")
	_, fallback, err := publicarPRCon("worktree", ruta,
		func(string) bool { return false },
		func(string) error { t.Fatal("sin contenido no debe copiar nada"); return nil })
	if err == nil || !strings.Contains(err.Error(), "releer") {
		t.Fatalf("el archivo ilegible debe fallar con aviso de relectura: %v", err)
	}
	if !fallback {
		t.Fatal("el fallo de relectura sigue siendo fallback")
	}
}

// TestPublicarPRConGhSoloCuandoExiste: con gh disponible se usa gh (la URL
// sale del propio gh); sin gh nunca se invoca.
func TestPublicarPRConUsaGhCuandoExiste(t *testing.T) {
	ruta := filepath.Join(t.TempDir(), "plantilla.md")
	if err := os.WriteFile(ruta, []byte("cuerpo"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := publicarPRCon("worktree", ruta,
		func(string) bool { return true },
		func(string) error { t.Fatal("con gh no debe usar portapapeles"); return nil })
	if err == nil {
		t.Fatal("con gh real en el PATH y worktree no-Git debe fallar (gh devuelve error), no copiar")
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
