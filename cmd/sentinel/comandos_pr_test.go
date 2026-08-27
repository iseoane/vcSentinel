package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/modelprobe"
	"github.com/ISeoane-Quental/vas.sentinel/internal/ops"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

// TestEjecutarPrReview_ClaveDesconocidaEnYml_Exit1ConLinea cubre el Fix 1
// (F1, hallazgo del orquestador): ejecutarPrReview usaba
// CargarConfiguracionLocal (sin error). Con un yml roto, antes seguía
// adelante en silencio hasta fallar más tarde con un error de git ajeno al
// problema real (el worktree de este test no es un repo); con
// CargarConfiguracionLocalEstricta debe cortar aquí mismo con exit 1 y el
// error del yml visible.
func TestEjecutarPrReview_ClaveDesconocidaEnYml_Exit1ConLinea(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	escribirYmlConClaveDesconocida(t, worktree)

	salida, exit := ejecutarComoSubproceso(t, "ejecutarPrReview", worktree, home)

	if exit != 1 {
		t.Errorf("exit esperado 1, obtuve %d (salida: %q)", exit, salida)
	}
	if !strings.Contains(salida, "line") {
		t.Errorf("la salida debe incluir la línea del error del yml, obtuve: %q", salida)
	}
}

// TestVerboPr verifies review/create dispatch and retired passthrough handling.
func TestVerboPr(t *testing.T) {
	casos := []struct {
		args []string
		want string
	}{
		{[]string{}, ""},
		{[]string{"review"}, "review"},
		{[]string{"review", "--base", "dev"}, "review"},
		{[]string{"create"}, "create"},
		{[]string{"create", "--draft"}, "create"},
		{[]string{"--title", "hola"}, ""},
		{[]string{"-t", "hola"}, ""},
	}
	for _, caso := range casos {
		if got := verboPr(caso.args); got != caso.want {
			t.Errorf("verboPr(%v) = %q, want %q", caso.args, got, caso.want)
		}
	}
	message, exitCode := retiredPassthroughDisposition()
	if exitCode != 1 ||
		!strings.Contains(message, "was removed") ||
		!strings.Contains(message, "sentinel pr create") ||
		!strings.Contains(message, "sentinel pr review") {
		t.Errorf("retired passthrough = (%d, %q), want exit 1 and removal/create-or-review guidance", exitCode, message)
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
	rf, err := parsearFlagsPrReview([]string{"--parent", "layer-a"})
	if err != nil || rf.parent != "layer-a" {
		t.Errorf("--parent accept: (%q,%v)", rf.parent, err)
	}
	for _, args := range [][]string{{"--parent"}, {"--parent", ""}, {"--parent", "--chain-pr"}} {
		_, errR := parsearFlagsPrReview(args)
		_, errC := parsearFlagsPrCreate(args)
		if errR == nil || errC == nil {
			t.Errorf("%v: want an explicit failure, got %v/%v", args, errR, errC)
		}
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

// TestAvisoSemanticoSinBlockNoAvisa: sin veredicto block, no hay nada que
// avisar (antes gateBlock devolvía permitido=true; ahora ni siquiera decide
// si se publica, T1.8 lo dejó fuera de ese camino).
func TestAvisoSemanticoSinBlockNoAvisa(t *testing.T) {
	fichas := []review.Ficha{
		fichaCreateAyuda("abc1234", review.VerdictOK,
			review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictOK}),
	}
	avisar, bloqueantes := avisoSemantico(fichas)
	if avisar {
		t.Fatal("sin block no hay nada que avisar")
	}
	if len(bloqueantes) != 0 {
		t.Fatalf("sin bloqueantes, lista = %v", bloqueantes)
	}
}

// TestAvisoSemanticoConBlockAvisaYListaCriticos: el veredicto block ya NO
// impide publicar (T1.8 lo pasa a advisory); avisoSemantico solo señala el
// aviso destacado y devuelve los CRITICAL estructurados para que el CLI los
// muestre.
func TestAvisoSemanticoConBlockAvisaYListaCriticos(t *testing.T) {
	fichas := []review.Ficha{
		fichaCreateAyuda("abc1234", review.VerdictBlock,
			review.DimensionResult{
				Dim:      review.DimSecurity,
				Verdict:  review.VerdictBlock,
				Findings: []review.ReviewFinding{hallazgoCritico()},
			}),
	}
	avisar, bloqueantes := avisoSemantico(fichas)
	if !avisar {
		t.Fatal("block debe disparar el aviso destacado")
	}
	if len(bloqueantes) != 1 {
		t.Fatalf("debe listar el CRITICAL, lista = %v", bloqueantes)
	}
	h := bloqueantes[0]
	if h.Severity != review.SevCritical || h.Description != "secreto en el log" {
		t.Errorf("el bloqueante debe conservar severidad y descripción: %+v", h)
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
	plantilla := verificarParaPlantillaCon("worktree", "gitdir", config.Config{}, nil,
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
	plantilla := verificarParaPlantillaCon("worktree", "gitdir", config.Config{}, nil,
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

// TestVerificarParaPlantillaNilUsaLaRutaReal: el guard defensivo (verificar
// nil -> ops.Verificar) no se puede quitar sin romper el test: sin comandos
// configurados, la ruta real pasa por el aviso interactivo y, sin respuesta
// en stdin, degrada a omitido sin panic.
func TestVerificarParaPlantillaNilUsaLaRutaReal(t *testing.T) {
	plantilla := verificarParaPlantillaCon("worktree", "", config.Config{}, nil, nil)
	if plantilla.Modo != ops.ModoOmitido {
		t.Errorf("con nil la ruta real debe degradar a omitido, got %q", plantilla.Modo)
	}
	if plantilla.Motivo != "aviso_no_respondio" {
		t.Errorf("sin respuesta en el aviso el motivo debe ser aviso_no_respondio, got %q", plantilla.Motivo)
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
	url, fallback, err := publicarPRCon("worktree", ruta, "", opcionesPublicarPR{
		ghDisponible: func(string) bool { return false },
		copiar:       func(texto string) error { copiado = texto; return nil },
	})
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
	_, fallback, err := publicarPRCon("worktree", ruta, "", opcionesPublicarPR{
		ghDisponible: func(string) bool { return false },
		copiar:       func(string) error { t.Fatal("sin contenido no debe copiar nada"); return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "releer") {
		t.Fatalf("el archivo ilegible debe fallar con aviso de relectura: %v", err)
	}
	if !fallback {
		t.Fatal("el fallo de relectura sigue siendo fallback")
	}
}

// TestPublicarPRConUsaGhConArgumentosExactos: con gh disponible se usa gh con
// los argumentos del contrato (pr create --draft -F) y el worktree como cwd;
// la URL sale de la salida de gh, sin portapapeles.
func TestPublicarPRConUsaGhConArgumentosExactos(t *testing.T) {
	ruta := filepath.Join(t.TempDir(), "plantilla.md")
	if err := os.WriteFile(ruta, []byte("cuerpo"), 0o644); err != nil {
		t.Fatal(err)
	}
	var worktreeVisto string
	var argsVistos []string
	url, fallback, err := publicarPRCon("el-worktree", ruta, "", opcionesPublicarPR{
		ghDisponible: func(string) bool { return true },
		ejecutarGh: func(worktree string, args ...string) ([]byte, error) {
			worktreeVisto = worktree
			argsVistos = args
			return []byte("https://github.com/ejemplo/repo/pull/9\n"), nil
		},
		copiar: func(string) error { t.Fatal("con gh no debe usar portapapeles"); return nil },
	})
	if err != nil {
		t.Fatalf("gh simulado no debería fallar: %v", err)
	}
	if fallback {
		t.Fatal("con gh no debe activarse el fallback")
	}
	if url != "https://github.com/ejemplo/repo/pull/9" {
		t.Errorf("la URL debe salir de gh (trimmed), got %q", url)
	}
	if worktreeVisto != "el-worktree" {
		t.Errorf("gh debe ejecutarse con el worktree como cwd, got %q", worktreeVisto)
	}
	esperados := []string{"pr", "create", "--draft", "-F", ruta}
	if !reflect.DeepEqual(argsVistos, esperados) {
		t.Errorf("argumentos de gh = %v, esperados %v", argsVistos, esperados)
	}
}

// TestPublicarPRConGhFallidoSePropaga: si gh termina con error, su stderr se
// propaga en el mensaje y no se cae al portapapeles.
func TestPublicarPRConGhFallidoSePropaga(t *testing.T) {
	ruta := filepath.Join(t.TempDir(), "plantilla.md")
	if err := os.WriteFile(ruta, []byte("cuerpo"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, fallback, err := publicarPRCon("el-worktree", ruta, "", opcionesPublicarPR{
		ghDisponible: func(string) bool { return true },
		ejecutarGh:   func(string, ...string) ([]byte, error) { return nil, errors.New("gh: repo no configurado") },
		copiar:       func(string) error { t.Fatal("con gh fallido no debe copiar"); return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "gh: repo no configurado") {
		t.Fatalf("el error de gh debe propagarse: %v", err)
	}
	if fallback {
		t.Fatal("un fallo de gh no es fallback (el fallback solo aplica sin gh)")
	}
}

// TestPublicarPRConBaseExplícita: cuando el usuario da --base, esa misma base
// debe propagarse a gh pr create (la revisión y la PR no pueden divergir).
func TestPublicarPRConBaseExplícita(t *testing.T) {
	ruta := filepath.Join(t.TempDir(), "plantilla.md")
	if err := os.WriteFile(ruta, []byte("cuerpo"), 0o644); err != nil {
		t.Fatal(err)
	}
	var argsVistos []string
	_, _, err := publicarPRCon("el-worktree", ruta, "develop", opcionesPublicarPR{
		ghDisponible: func(string) bool { return true },
		ejecutarGh: func(worktree string, args ...string) ([]byte, error) {
			argsVistos = args
			return []byte("https://github.com/ejemplo/repo/pull/11\n"), nil
		},
		copiar: func(string) error { t.Fatal("con gh no debe usar portapapeles"); return nil },
	})
	if err != nil {
		t.Fatalf("gh simulado no debería fallar: %v", err)
	}
	esperados := []string{"pr", "create", "--draft", "--base", "develop", "-F", ruta}
	if !reflect.DeepEqual(argsVistos, esperados) {
		t.Errorf("con --base los argumentos de gh = %v, esperados %v", argsVistos, esperados)
	}
}

// TestPublicarPRConBaseVacíaNoAñadeFlag: sin --base, gh usa el default
// upstream y no recibe --base.
func TestPublicarPRConBaseVacíaNoAñadeFlag(t *testing.T) {
	ruta := filepath.Join(t.TempDir(), "plantilla.md")
	if err := os.WriteFile(ruta, []byte("cuerpo"), 0o644); err != nil {
		t.Fatal(err)
	}
	var argsVistos []string
	_, _, err := publicarPRCon("el-worktree", ruta, "", opcionesPublicarPR{
		ghDisponible: func(string) bool { return true },
		ejecutarGh: func(worktree string, args ...string) ([]byte, error) {
			argsVistos = args
			return []byte("https://github.com/PR/pull/12\n"), nil
		},
		copiar: func(string) error { t.Fatal("con gh no debe usar portapapeles"); return nil },
	})
	if err != nil {
		t.Fatalf("gh simulado no debería fallar: %v", err)
	}
	esperados := []string{"pr", "create", "--draft", "-F", ruta}
	if !reflect.DeepEqual(argsVistos, esperados) {
		t.Errorf("sin --base los argumentos de gh = %v, esperados %v", argsVistos, esperados)
	}
}

func TestDetalleEventoPrCreate(t *testing.T) {
	detalle, err := detalleEventoPrCreate("https://github.com/x/pr/1", false, false, false, "")
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
	if crudo["fallback"] != false || crudo["chain_pr"] != false || crudo["force"] != false {
		t.Errorf("fallback/chain_pr/force = %v/%v/%v", crudo["fallback"], crudo["chain_pr"], crudo["force"])
	}
	if _, hay := crudo["motivo"]; hay {
		t.Errorf("sin force no debe haber motivo: %v", crudo["motivo"])
	}
}

func TestDetalleEventoPrCreateFallbackYChain(t *testing.T) {
	detalle, err := detalleEventoPrCreate("", true, true, false, "")
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

// TestDetalleEventoPrCreateForceConMotivo: la excepción de --force queda
// registrada en el evento con el motivo explícito (T1.8).
func TestDetalleEventoPrCreateForceConMotivo(t *testing.T) {
	detalle, err := detalleEventoPrCreate("https://github.com/x/pr/2", false, false, true, "motivo real")
	if err != nil {
		t.Fatalf("detalle no debería fallar: %v", err)
	}
	var crudo map[string]any
	if err := json.Unmarshal([]byte(detalle), &crudo); err != nil {
		t.Fatalf("detail debe ser JSON válido: %v", err)
	}
	if crudo["force"] != true || crudo["motivo"] != "motivo real" {
		t.Errorf("force/motivo = %v/%v", crudo["force"], crudo["motivo"])
	}
}

func TestParsearFlagsPrCreate(t *testing.T) {
	flags, err := parsearFlagsPrCreate([]string{"--base", "develop", "--chain-pr", "--force", "--reason", "motivo real"})
	if err != nil {
		t.Fatalf("parseo no debería fallar: %v", err)
	}
	if flags.base != "develop" || !flags.chainPR || !flags.force || flags.reason != "motivo real" {
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

// TestParsearFlagsPrCreateForceSinReason: --force sin --reason es un error
// explícito (T1.8): la validación es el único gate real y forzarla sin motivo
// no puede quedar en silencio.
func TestParsearFlagsPrCreateForceSinReason(t *testing.T) {
	_, err := parsearFlagsPrCreate([]string{"--force"})
	if err == nil {
		t.Fatal("--force sin --reason debe fallar")
	}
	if !strings.Contains(err.Error(), "reason") && !strings.Contains(err.Error(), "motivo") {
		t.Errorf("el error debe pedir el motivo, got: %v", err)
	}
}

// TestParsearFlagsPrCreateReasonSinValor: --reason sin valor falla igual que
// el resto de flags de valor.
func TestParsearFlagsPrCreateReasonSinValor(t *testing.T) {
	if _, err := parsearFlagsPrCreate([]string{"--force", "--reason"}); err == nil {
		t.Fatal("--reason sin valor debe fallar")
	}
}

// TestEjecutarPrCreateCon_ValidacionRojaSinForce_NoPublicaNiAuditaRama cubre
// la regla central de T1.8: si la validación falla sin --force, ni se publica
// ni se gasta un token en la revisión semántica (AnalizarRama nunca se llama).
func TestEjecutarPrCreateCon_ValidacionRojaSinForce_NoPublicaNiAuditaRama(t *testing.T) {
	var analizarRamaLlamado, publicarLlamado bool
	var salida bytes.Buffer
	codigo := ejecutarPrCreateCon(&salida, "worktree", nil, depsPrCreate{
		cargarConfig:  func(string) (config.Config, error) { return config.Config{}, nil },
		obtenerGitDir: func() (string, error) { return "gitdir", nil },
		ejecutarValidacion: func(string, []string, validation.OpcionesEjecucion) ([]validation.ValidationRun, error) {
			return []validation.ValidationRun{{Capability: "test", Comando: "go test ./...", Exit: 1, Salida: "FAIL"}}, nil
		},
		analizarRama: func(string, review.OpcionesRama) (*review.ResultadoRama, error) {
			analizarRamaLlamado = true
			return nil, nil
		},
		publicar: func(string, string, string) (string, bool, error) {
			publicarLlamado = true
			return "", false, nil
		},
	})
	if codigo != 1 {
		t.Fatalf("codigo = %d, esperado 1", codigo)
	}
	if analizarRamaLlamado {
		t.Fatal("la validación en rojo sin --force no debe invocar AnalizarRama: cero tokens")
	}
	if publicarLlamado {
		t.Fatal("la validación en rojo sin --force no debe publicar")
	}
	if !strings.Contains(salida.String(), "go test ./...") || !strings.Contains(salida.String(), "FAIL") {
		t.Errorf("debe listar el comando en rojo con su salida real: %s", salida.String())
	}
}

func TestEjecutarPrCreateCon_ComparteElVerificadorModeloConLaPlantilla(t *testing.T) {
	anterior := nuevoVerificadorModelo
	t.Cleanup(func() { nuevoVerificadorModelo = anterior })
	var construcciones int
	nuevoVerificadorModelo = func(string) *modelprobe.Verificador {
		construcciones++
		return modelprobe.NuevoVerificador(nil)
	}

	var salida bytes.Buffer
	// verificar delega en verificarParaPlantillaCon con verificar=nil, que a su
	// vez cae en la ruta real ops.Verificar: ese camino llama de verdad a
	// ops.RegistrarEvento(gitDir, "pr-verify", ...). Un "gitdir" literal aquí
	// escribiría fuera de un directorio temporal, en <cwd>/gitdir/vas-sentinel
	// (cwd = el paquete durante `go test`), contaminando el árbol del
	// repositorio en cada ejecución. t.TempDir() mantiene la escritura real
	// que este test necesita para cubrir la ruta, sin tocar el repositorio.
	gitDir := t.TempDir()
	codigo := ejecutarPrCreateCon(&salida, "worktree", nil, depsPrCreate{
		cargarConfig:  func(string) (config.Config, error) { return config.Config{}, nil },
		obtenerGitDir: func() (string, error) { return gitDir, nil },
		ejecutarValidacion: func(string, []string, validation.OpcionesEjecucion) ([]validation.ValidationRun, error) {
			return nil, nil
		},
		analizarRama: func(string, review.OpcionesRama) (*review.ResultadoRama, error) {
			return &review.ResultadoRama{Fichas: []review.Ficha{fichaCreateAyuda("abc1234", review.VerdictOK)}, SHAs: []string{"abc1234"}}, nil
		},
		verificar: func(worktree, gitDir string, cfg config.Config, verificadorModelo *modelprobe.Verificador) review.VerificacionPlantilla {
			return verificarParaPlantillaCon(worktree, gitDir, cfg, verificadorModelo, nil)
		},
		publicar:        func(string, string, string) (string, bool, error) { return "https://github.com/x/pr/1", false, nil },
		registrarEvento: func(string, string, int, []string, string, string) error { return nil },
	})
	if codigo != 0 {
		t.Fatalf("codigo = %d, esperado 0: %s", codigo, salida.String())
	}
	if construcciones != 1 {
		t.Fatalf("construcciones de verificador = %d, esperado 1 por invocación de pr create", construcciones)
	}
}

func TestFabricaRefutadorResuelvePerfilCheap(t *testing.T) {
	cfg := configConPerfilCheap()
	agente, perfil, err := fabricaRefutador(cfg, modelprobe.NuevoVerificador(nil))()
	if err != nil {
		t.Fatalf("fabricaRefutador() error = %v", err)
	}
	if perfil != "cheap" {
		t.Fatalf("perfil = %q, want cheap", perfil)
	}
	adapter, ok := agente.(*agentadapter.CLIAdapter)
	if !ok {
		t.Fatalf("agente = %T, want *agentadapter.CLIAdapter", agente)
	}
	if adapter.Config.Model != "cheap-model" || adapter.Config.ReasoningEffort != "low" {
		t.Fatalf("configuracion del refutador = %+v, want cheap profile", adapter.Config)
	}
}

func TestOpcionesAuditoriaConRefutadorLlevaPerfilCheap(t *testing.T) {
	opciones := opcionesAuditoriaConRefutador(review.OpcionesAuditoria{}, configConPerfilCheap(), modelprobe.NuevoVerificador(nil))
	if opciones.FabricaRefutador == nil {
		t.Fatal("OpcionesAuditoria must carry a refuter factory")
	}
	_, perfil, err := opciones.FabricaRefutador()
	if err != nil {
		t.Fatalf("FabricaRefutador() error = %v", err)
	}
	if perfil != "cheap" {
		t.Fatalf("refuter profile = %q, want cheap", perfil)
	}
}

func TestOpcionesRamaConRefutadorLlevaPerfilCheap(t *testing.T) {
	opciones := opcionesRamaConRefutador(configConPerfilCheap(), modelprobe.NuevoVerificador(nil), review.OpcionesRama{})
	if opciones.FabricaRefutador == nil {
		t.Fatal("OpcionesRama must carry a refuter factory")
	}
	_, perfil, err := opciones.FabricaRefutador()
	if err != nil {
		t.Fatalf("FabricaRefutador() error = %v", err)
	}
	if perfil != "cheap" {
		t.Fatalf("refuter profile = %q, want cheap", perfil)
	}
}

func TestEjecutarPrCreateConPasaFabricaRefutadorCheap(t *testing.T) {
	var salida bytes.Buffer
	codigo := ejecutarPrCreateCon(&salida, "worktree", nil, depsPrCreate{
		cargarConfig:  func(string) (config.Config, error) { return configConPerfilCheap(), nil },
		obtenerGitDir: func() (string, error) { return "gitdir", nil },
		ejecutarValidacion: func(string, []string, validation.OpcionesEjecucion) ([]validation.ValidationRun, error) {
			return nil, nil
		},
		analizarRama: func(_ string, opts review.OpcionesRama) (*review.ResultadoRama, error) {
			if opts.FabricaRefutador == nil {
				t.Fatal("OpcionesRama must carry a refuter factory")
			}
			_, perfil, err := opts.FabricaRefutador()
			if err != nil {
				t.Fatalf("FabricaRefutador() error = %v", err)
			}
			if perfil != "cheap" {
				t.Fatalf("refuter profile = %q, want cheap", perfil)
			}
			return &review.ResultadoRama{Fichas: []review.Ficha{fichaCreateAyuda("abc1234", review.VerdictOK)}, SHAs: []string{"abc1234"}}, nil
		},
		verificar: func(string, string, config.Config, *modelprobe.Verificador) review.VerificacionPlantilla {
			return review.VerificacionPlantilla{Modo: "omitido"}
		},
		publicar:        func(string, string, string) (string, bool, error) { return "https://github.com/x/pr/1", false, nil },
		registrarEvento: func(string, string, int, []string, string, string) error { return nil },
	})
	if codigo != 0 {
		t.Fatalf("codigo = %d, want 0: %s", codigo, salida.String())
	}
}

func configConPerfilCheap() config.Config {
	return config.Config{
		ActiveAgent: "stub",
		Agents: map[string]config.AgentConfig{
			"stub": {
				Model: "normal-model",
				Profiles: map[string]config.ProfileConfig{
					"cheap": {Model: "cheap-model", ReasoningEffort: "low"},
				},
			},
		},
	}
}

// TestEjecutarPrCreateCon_ForceSinReason_ErrorSinTocarNada: --force sin
// --reason falla en el parseo, antes de tocar config/git/validación.
func TestEjecutarPrCreateCon_ForceSinReason_ErrorSinTocarNada(t *testing.T) {
	var salida bytes.Buffer
	codigo := ejecutarPrCreateCon(&salida, "worktree", []string{"--force"}, depsPrCreate{
		cargarConfig: func(string) (config.Config, error) {
			t.Fatal("no debe cargar configuración sin --reason: el error es de parseo")
			return config.Config{}, nil
		},
	})
	if codigo != 1 {
		t.Fatalf("codigo = %d, esperado 1", codigo)
	}
	if !strings.Contains(salida.String(), "motivo") {
		t.Errorf("debe pedir el motivo explícitamente: %s", salida.String())
	}
}

// TestEjecutarPrCreateCon_ForceConReason_PublicaYRegistraExcepcion cubre el
// tercer escenario de aceptación: con --force --reason, la validación en rojo
// se supera, se publica igual y el evento registra force+motivo.
func TestEjecutarPrCreateCon_ForceConReason_PublicaYRegistraExcepcion(t *testing.T) {
	fichaOK := fichaCreateAyuda("abc1234", review.VerdictOK,
		review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictOK})
	var detalleRegistrado string
	var salida bytes.Buffer
	codigo := ejecutarPrCreateCon(&salida, "worktree", []string{"--force", "--reason", "motivo real"}, depsPrCreate{
		cargarConfig:   func(string) (config.Config, error) { return config.Config{}, nil },
		obtenerGitDir:  func() (string, error) { return "gitdir", nil },
		obtenerSHAHead: func() (string, error) { return "abc1234", nil },
		ejecutarValidacion: func(string, []string, validation.OpcionesEjecucion) ([]validation.ValidationRun, error) {
			return []validation.ValidationRun{{Capability: "test", Comando: "go test ./...", Exit: 1}}, nil
		},
		analizarRama: func(string, review.OpcionesRama) (*review.ResultadoRama, error) {
			return &review.ResultadoRama{Fichas: []review.Ficha{fichaOK}, SHAs: []string{"abc1234"}, Decision: "single"}, nil
		},
		verificar: func(string, string, config.Config, *modelprobe.Verificador) review.VerificacionPlantilla {
			return review.VerificacionPlantilla{Modo: "omitido"}
		},
		publicar: func(string, string, string) (string, bool, error) { return "https://github.com/x/pr/9", false, nil },
		registrarEvento: func(gitDir, tipo string, exit int, shas []string, detalle, worktree string) error {
			detalleRegistrado = detalle
			return nil
		},
		obtenerGitCommonDir: func(string) (string, error) { return "commondir", nil },
		registrarDecision:   func(string, *store.Decision) error { return nil },
		resolverActor:       func(string) string { return "actor-de-prueba" },
	})
	if codigo != 0 {
		t.Fatalf("codigo = %d, esperado 0 (--force publica igual)", codigo)
	}
	var crudo map[string]any
	if err := json.Unmarshal([]byte(detalleRegistrado), &crudo); err != nil {
		t.Fatalf("el detalle del evento debe ser JSON válido: %v\n%s", err, detalleRegistrado)
	}
	if crudo["force"] != true || crudo["motivo"] != "motivo real" {
		t.Errorf("el evento debe registrar force y motivo, got %+v", crudo)
	}
}

// TestEjecutarPrCreateCon_ForceConValidacionRoja_PropagaHallazgosDeterministas
// covers the wiring that activates T6.2 in production: with --force and a
// red validation, the deterministic findings projected from that validation
// must reach review.AnalizarRama via OpcionesRama.HallazgosDeterministas, so
// AuditarCommit can supersede the equivalent semantic finding.
func TestEjecutarPrCreateCon_ForceConValidacionRoja_PropagaHallazgosDeterministas(t *testing.T) {
	fichaOK := fichaCreateAyuda("abc1234", review.VerdictOK,
		review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictOK})
	var opcionesRecibidas review.OpcionesRama
	var salida bytes.Buffer
	codigo := ejecutarPrCreateCon(&salida, "worktree", []string{"--force", "--reason", "motivo real"}, depsPrCreate{
		cargarConfig:   func(string) (config.Config, error) { return config.Config{}, nil },
		obtenerGitDir:  func() (string, error) { return "gitdir", nil },
		obtenerSHAHead: func() (string, error) { return "abc1234", nil },
		ejecutarValidacion: func(string, []string, validation.OpcionesEjecucion) ([]validation.ValidationRun, error) {
			return []validation.ValidationRun{{Capability: "lint", Comando: "go vet ./...", Exit: 1}}, nil
		},
		analizarRama: func(_ string, opts review.OpcionesRama) (*review.ResultadoRama, error) {
			opcionesRecibidas = opts
			return &review.ResultadoRama{Fichas: []review.Ficha{fichaOK}, SHAs: []string{"abc1234"}, Decision: "single"}, nil
		},
		verificar: func(string, string, config.Config, *modelprobe.Verificador) review.VerificacionPlantilla {
			return review.VerificacionPlantilla{Modo: "omitido"}
		},
		publicar: func(string, string, string) (string, bool, error) { return "https://github.com/x/pr/12", false, nil },
		registrarEvento: func(gitDir, tipo string, exit int, shas []string, detalle, worktree string) error {
			return nil
		},
		obtenerGitCommonDir: func(string) (string, error) { return "commondir", nil },
		registrarDecision:   func(string, *store.Decision) error { return nil },
		resolverActor:       func(string) string { return "actor-de-prueba" },
	})
	if codigo != 0 {
		t.Fatalf("codigo = %d, esperado 0 (--force publica igual)", codigo)
	}
	if opcionesRecibidas.HallazgosDeterministasSHA != "abc1234" {
		t.Errorf("HallazgosDeterministasSHA = %q, expected the validated HEAD sha", opcionesRecibidas.HallazgosDeterministasSHA)
	}
	if len(opcionesRecibidas.HallazgosDeterministas) != 1 {
		t.Fatalf("HallazgosDeterministas = %#v, expected 1 projected finding", opcionesRecibidas.HallazgosDeterministas)
	}
	got := opcionesRecibidas.HallazgosDeterministas[0]
	if got.Source != review.SourceValidation {
		t.Errorf("Source = %q, expected %q", got.Source, review.SourceValidation)
	}
	if got.Dimension != review.DimStyle {
		t.Errorf("Dimension = %q, expected %q for capability 'lint'", got.Dimension, review.DimStyle)
	}
}

// TestEjecutarPrCreateCon_ForceConValidacionRoja_HEADIrresolubleAvisaYSigue
// is a regression test: when obtenerSHAHead fails, the command must not
// silently drop the deterministic findings without a trace. It still
// publishes (this failure is unrelated to --force's own decision to
// continue), but PublicaCon must print an explicit warning and must not
// bind the projected findings to any commit (HallazgosDeterministasSHA
// stays empty, so AnalizarRama can never mismatch them to the wrong SHA).
func TestEjecutarPrCreateCon_ForceConValidacionRoja_HEADIrresolubleAvisaYSigue(t *testing.T) {
	fichaOK := fichaCreateAyuda("abc1234", review.VerdictOK,
		review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictOK})
	var opcionesRecibidas review.OpcionesRama
	var sePublico bool
	var salida bytes.Buffer
	codigo := ejecutarPrCreateCon(&salida, "worktree", []string{"--force", "--reason", "motivo real"}, depsPrCreate{
		cargarConfig:   func(string) (config.Config, error) { return config.Config{}, nil },
		obtenerGitDir:  func() (string, error) { return "gitdir", nil },
		obtenerSHAHead: func() (string, error) { return "", errors.New("HEAD irresoluble") },
		ejecutarValidacion: func(string, []string, validation.OpcionesEjecucion) ([]validation.ValidationRun, error) {
			return []validation.ValidationRun{{Capability: "lint", Comando: "go vet ./...", Exit: 1}}, nil
		},
		analizarRama: func(_ string, opts review.OpcionesRama) (*review.ResultadoRama, error) {
			opcionesRecibidas = opts
			return &review.ResultadoRama{Fichas: []review.Ficha{fichaOK}, SHAs: []string{"abc1234"}, Decision: "single"}, nil
		},
		verificar: func(string, string, config.Config, *modelprobe.Verificador) review.VerificacionPlantilla {
			return review.VerificacionPlantilla{Modo: "omitido"}
		},
		publicar: func(string, string, string) (string, bool, error) {
			sePublico = true
			return "https://github.com/x/pr/14", false, nil
		},
		registrarEvento: func(gitDir, tipo string, exit int, shas []string, detalle, worktree string) error {
			return nil
		},
		obtenerGitCommonDir: func(string) (string, error) { return "commondir", nil },
		registrarDecision:   func(string, *store.Decision) error { return nil },
		resolverActor:       func(string) string { return "actor-de-prueba" },
	})
	if codigo != 0 {
		t.Fatalf("codigo = %d, esperado 0 (el fallo de HEAD no bloquea --force)", codigo)
	}
	if !sePublico {
		t.Error("se esperaba que la PR se publicara pese al fallo de HEAD")
	}
	if !strings.Contains(salida.String(), "no se pudo resolver el commit validado") {
		t.Errorf("se esperaba un aviso explícito del fallo de HEAD, got: %s", salida.String())
	}
	if opcionesRecibidas.HallazgosDeterministasSHA != "" {
		t.Errorf("HallazgosDeterministasSHA = %q, expected empty when HEAD couldn't be resolved", opcionesRecibidas.HallazgosDeterministasSHA)
	}
	if len(opcionesRecibidas.HallazgosDeterministas) != 1 {
		t.Fatalf("HallazgosDeterministas = %#v, expected the projected finding to be preserved even without a bound SHA", opcionesRecibidas.HallazgosDeterministas)
	}
	if got := opcionesRecibidas.HallazgosDeterministas[0]; got.Source != review.SourceValidation || got.Dimension != review.DimStyle {
		t.Errorf("hallazgo preservado = %#v, expected Source=%q Dimension=%q (proyectado desde la capability 'lint')", got, review.SourceValidation, review.DimStyle)
	}
}

// TestEjecutarPrCreateCon_ForceConValidacionVerde_NoPropagaHallazgosDeterministas
// is the complement: with no red validation to force through, there is
// nothing deterministic to supersede with, so the collection must stay
// empty rather than accidentally leaking stale state.
func TestEjecutarPrCreateCon_ForceConValidacionVerde_NoPropagaHallazgosDeterministas(t *testing.T) {
	fichaOK := fichaCreateAyuda("abc1234", review.VerdictOK,
		review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictOK})
	var opcionesRecibidas review.OpcionesRama
	var salida bytes.Buffer
	codigo := ejecutarPrCreateCon(&salida, "worktree", []string{"--force", "--reason", "motivo real"}, depsPrCreate{
		cargarConfig:  func(string) (config.Config, error) { return config.Config{}, nil },
		obtenerGitDir: func() (string, error) { return "gitdir", nil },
		ejecutarValidacion: func(string, []string, validation.OpcionesEjecucion) ([]validation.ValidationRun, error) {
			return nil, nil // validación en verde: sin runs, sin hallazgos
		},
		analizarRama: func(_ string, opts review.OpcionesRama) (*review.ResultadoRama, error) {
			opcionesRecibidas = opts
			return &review.ResultadoRama{Fichas: []review.Ficha{fichaOK}, SHAs: []string{"abc1234"}, Decision: "single"}, nil
		},
		verificar: func(string, string, config.Config, *modelprobe.Verificador) review.VerificacionPlantilla {
			return review.VerificacionPlantilla{Modo: "omitido"}
		},
		publicar: func(string, string, string) (string, bool, error) { return "https://github.com/x/pr/13", false, nil },
		registrarEvento: func(gitDir, tipo string, exit int, shas []string, detalle, worktree string) error {
			return nil
		},
	})
	if codigo != 0 {
		t.Fatalf("codigo = %d, esperado 0", codigo)
	}
	if len(opcionesRecibidas.HallazgosDeterministas) != 0 {
		t.Errorf("HallazgosDeterministas = %#v, expected empty without a forced red validation", opcionesRecibidas.HallazgosDeterministas)
	}
}

// TestEjecutarPrCreateCon_ForceConValidacionVerde_NoRegistraExcepcionQueNoOcurrio
// cubre el Fix 2 (hallazgo del orquestador): --force --reason con la
// validación YA en verde (sin hallazgos) no tiene ningún efecto real que
// superar, así que no debe avisar de una "validación superada" que no
// ocurrió, y el evento debe registrar force:false sin motivo — el flag
// existió en la invocación pero no ejerció ningún efecto.
func TestEjecutarPrCreateCon_ForceConValidacionVerde_NoRegistraExcepcionQueNoOcurrio(t *testing.T) {
	fichaOK := fichaCreateAyuda("abc1234", review.VerdictOK,
		review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictOK})
	var detalleRegistrado string
	var salida bytes.Buffer
	codigo := ejecutarPrCreateCon(&salida, "worktree", []string{"--force", "--reason", "motivo real"}, depsPrCreate{
		cargarConfig:  func(string) (config.Config, error) { return config.Config{}, nil },
		obtenerGitDir: func() (string, error) { return "gitdir", nil },
		ejecutarValidacion: func(string, []string, validation.OpcionesEjecucion) ([]validation.ValidationRun, error) {
			return nil, nil // validación en verde: sin runs, sin hallazgos
		},
		analizarRama: func(string, review.OpcionesRama) (*review.ResultadoRama, error) {
			return &review.ResultadoRama{Fichas: []review.Ficha{fichaOK}, SHAs: []string{"abc1234"}, Decision: "single"}, nil
		},
		verificar: func(string, string, config.Config, *modelprobe.Verificador) review.VerificacionPlantilla {
			return review.VerificacionPlantilla{Modo: "omitido"}
		},
		publicar: func(string, string, string) (string, bool, error) { return "https://github.com/x/pr/11", false, nil },
		registrarEvento: func(gitDir, tipo string, exit int, shas []string, detalle, worktree string) error {
			detalleRegistrado = detalle
			return nil
		},
	})
	if codigo != 0 {
		t.Fatalf("codigo = %d, esperado 0 (sin hallazgos, publica igual)", codigo)
	}
	if strings.Contains(salida.String(), "superada") {
		t.Errorf("sin hallazgos que superar no debe avisar de una validación superada: %s", salida.String())
	}
	var crudo map[string]any
	if err := json.Unmarshal([]byte(detalleRegistrado), &crudo); err != nil {
		t.Fatalf("el detalle del evento debe ser JSON válido: %v\n%s", err, detalleRegistrado)
	}
	if crudo["force"] != false {
		t.Errorf("force debe registrar false: no había nada que forzar, got %+v", crudo)
	}
	if _, hay := crudo["motivo"]; hay {
		t.Errorf("sin efecto real de --force no debe quedar motivo en el evento: %v", crudo)
	}
}

// TestEjecutarPrCreateCon_ForceConValidacionRoja_RegistraDecisionForceBypass
// cubre T7.5 (informe M3): --force que de verdad supera una validación en
// rojo debe registrar una store.Decision con Decision="force_bypass" y
// Motivo=el --reason, exactamente una vez.
func TestEjecutarPrCreateCon_ForceConValidacionRoja_RegistraDecisionForceBypass(t *testing.T) {
	fichaOK := fichaCreateAyuda("abc1234", review.VerdictOK,
		review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictOK})
	var llamadas int
	var decisionRegistrada *store.Decision
	var salida bytes.Buffer
	codigo := ejecutarPrCreateCon(&salida, "worktree", []string{"--force", "--reason", "x"}, depsPrCreate{
		cargarConfig:   func(string) (config.Config, error) { return config.Config{}, nil },
		obtenerGitDir:  func() (string, error) { return "gitdir", nil },
		obtenerSHAHead: func() (string, error) { return "abc1234", nil },
		ejecutarValidacion: func(string, []string, validation.OpcionesEjecucion) ([]validation.ValidationRun, error) {
			return []validation.ValidationRun{{Capability: "test", Comando: "go test ./...", Exit: 1}}, nil
		},
		analizarRama: func(string, review.OpcionesRama) (*review.ResultadoRama, error) {
			return &review.ResultadoRama{Fichas: []review.Ficha{fichaOK}, SHAs: []string{"abc1234"}, Decision: "single"}, nil
		},
		verificar: func(string, string, config.Config, *modelprobe.Verificador) review.VerificacionPlantilla {
			return review.VerificacionPlantilla{Modo: "omitido"}
		},
		publicar:        func(string, string, string) (string, bool, error) { return "https://github.com/x/pr/20", false, nil },
		registrarEvento: func(string, string, int, []string, string, string) error { return nil },
		obtenerGitCommonDir: func(worktree string) (string, error) {
			if worktree != "worktree" {
				t.Errorf("obtenerGitCommonDir worktree = %q, esperado %q", worktree, "worktree")
			}
			return "commondir", nil
		},
		registrarDecision: func(commonDir string, d *store.Decision) error {
			llamadas++
			if commonDir != "commondir" {
				t.Errorf("commonDir = %q, esperado %q", commonDir, "commondir")
			}
			decisionRegistrada = d
			return nil
		},
		resolverActor: func(string) string { return "actor-de-prueba" },
	})
	if codigo != 0 {
		t.Fatalf("codigo = %d, esperado 0 (--force publica igual)", codigo)
	}
	if llamadas != 1 {
		t.Fatalf("registrarDecision se llamó %d veces, esperado exactamente 1", llamadas)
	}
	if decisionRegistrada == nil {
		t.Fatal("no se registró ninguna decisión")
	}
	if decisionRegistrada.Decision != "force_bypass" {
		t.Errorf("Decision = %q, esperado %q", decisionRegistrada.Decision, "force_bypass")
	}
	if decisionRegistrada.Motivo != "x" {
		t.Errorf("Motivo = %q, esperado %q", decisionRegistrada.Motivo, "x")
	}
	if decisionRegistrada.Actor != "actor-de-prueba" {
		t.Errorf("Actor = %q, esperado %q (debe venir de deps.resolverActor, no de un git real)", decisionRegistrada.Actor, "actor-de-prueba")
	}
}

// TestEjecutarPrCreateCon_ForceConValidacionVerde_NoRegistraDecision cubre el
// complemento: --force presente pero SIN efecto real (validación ya en
// verde, igual que forzoValidacionEnRojo distingue arriba) no debe registrar
// ninguna decisión de bypass, porque no hubo ningún bypass que registrar.
func TestEjecutarPrCreateCon_ForceConValidacionVerde_NoRegistraDecision(t *testing.T) {
	fichaOK := fichaCreateAyuda("abc1234", review.VerdictOK,
		review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictOK})
	var registrarDecisionLlamado bool
	var salida bytes.Buffer
	codigo := ejecutarPrCreateCon(&salida, "worktree", []string{"--force", "--reason", "x"}, depsPrCreate{
		cargarConfig:  func(string) (config.Config, error) { return config.Config{}, nil },
		obtenerGitDir: func() (string, error) { return "gitdir", nil },
		ejecutarValidacion: func(string, []string, validation.OpcionesEjecucion) ([]validation.ValidationRun, error) {
			return nil, nil // validación en verde: --force no tiene nada que superar
		},
		analizarRama: func(string, review.OpcionesRama) (*review.ResultadoRama, error) {
			return &review.ResultadoRama{Fichas: []review.Ficha{fichaOK}, SHAs: []string{"abc1234"}, Decision: "single"}, nil
		},
		verificar: func(string, string, config.Config, *modelprobe.Verificador) review.VerificacionPlantilla {
			return review.VerificacionPlantilla{Modo: "omitido"}
		},
		publicar:        func(string, string, string) (string, bool, error) { return "https://github.com/x/pr/21", false, nil },
		registrarEvento: func(string, string, int, []string, string, string) error { return nil },
		obtenerGitCommonDir: func(string) (string, error) {
			t.Fatal("obtenerGitCommonDir no debe llamarse: --force no tuvo ningún efecto real que registrar")
			return "", nil
		},
		registrarDecision: func(string, *store.Decision) error {
			registrarDecisionLlamado = true
			return nil
		},
	})
	if codigo != 0 {
		t.Fatalf("codigo = %d, esperado 0", codigo)
	}
	if registrarDecisionLlamado {
		t.Error("registrarDecision no debe llamarse: la validación ya estaba en verde, --force no ejerció ningún efecto")
	}
}

// TestEjecutarPrCreateCon_ForceConValidacionRoja_GitCommonDirFallaAvisaYSigue
// cubre el aviso-y-continúa cuando obtenerGitCommonDir falla: --force ya
// decidió seguir pese a la validación en rojo, así que un fallo al resolver
// dónde registrar la decisión no debe abortar la publicación, solo avisar.
func TestEjecutarPrCreateCon_ForceConValidacionRoja_GitCommonDirFallaAvisaYSigue(t *testing.T) {
	fichaOK := fichaCreateAyuda("abc1234", review.VerdictOK,
		review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictOK})
	var salida bytes.Buffer
	codigo := ejecutarPrCreateCon(&salida, "worktree", []string{"--force", "--reason", "x"}, depsPrCreate{
		cargarConfig:   func(string) (config.Config, error) { return config.Config{}, nil },
		obtenerGitDir:  func() (string, error) { return "gitdir", nil },
		obtenerSHAHead: func() (string, error) { return "abc1234", nil },
		ejecutarValidacion: func(string, []string, validation.OpcionesEjecucion) ([]validation.ValidationRun, error) {
			return []validation.ValidationRun{{Capability: "test", Comando: "go test ./...", Exit: 1}}, nil
		},
		analizarRama: func(string, review.OpcionesRama) (*review.ResultadoRama, error) {
			return &review.ResultadoRama{Fichas: []review.Ficha{fichaOK}, SHAs: []string{"abc1234"}, Decision: "single"}, nil
		},
		verificar: func(string, string, config.Config, *modelprobe.Verificador) review.VerificacionPlantilla {
			return review.VerificacionPlantilla{Modo: "omitido"}
		},
		publicar:        func(string, string, string) (string, bool, error) { return "https://github.com/x/pr/22", false, nil },
		registrarEvento: func(string, string, int, []string, string, string) error { return nil },
		obtenerGitCommonDir: func(string) (string, error) {
			return "", errors.New("boom")
		},
		registrarDecision: func(string, *store.Decision) error {
			t.Fatal("registrarDecision no debe llamarse: no se pudo resolver el commonDir")
			return nil
		},
		resolverActor: func(string) string { return "actor-de-prueba" },
	})
	if codigo != 0 {
		t.Fatalf("codigo = %d, esperado 0 (el fallo al resolver commonDir no bloquea --force)", codigo)
	}
	if !strings.Contains(salida.String(), "no se pudo resolver el git-common-dir") {
		t.Errorf("la salida debe avisar del fallo al resolver commonDir, got: %s", salida.String())
	}
}

// TestEjecutarPrCreateCon_ForceConValidacionRoja_RegistrarDecisionFallaAvisaYSigue
// cubre el aviso-y-continúa cuando registrarDecision falla (p.ej. no se pudo
// escribir en decisions.jsonl): mismo criterio, no aborta la publicación.
func TestEjecutarPrCreateCon_ForceConValidacionRoja_RegistrarDecisionFallaAvisaYSigue(t *testing.T) {
	fichaOK := fichaCreateAyuda("abc1234", review.VerdictOK,
		review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictOK})
	var salida bytes.Buffer
	codigo := ejecutarPrCreateCon(&salida, "worktree", []string{"--force", "--reason", "x"}, depsPrCreate{
		cargarConfig:   func(string) (config.Config, error) { return config.Config{}, nil },
		obtenerGitDir:  func() (string, error) { return "gitdir", nil },
		obtenerSHAHead: func() (string, error) { return "abc1234", nil },
		ejecutarValidacion: func(string, []string, validation.OpcionesEjecucion) ([]validation.ValidationRun, error) {
			return []validation.ValidationRun{{Capability: "test", Comando: "go test ./...", Exit: 1}}, nil
		},
		analizarRama: func(string, review.OpcionesRama) (*review.ResultadoRama, error) {
			return &review.ResultadoRama{Fichas: []review.Ficha{fichaOK}, SHAs: []string{"abc1234"}, Decision: "single"}, nil
		},
		verificar: func(string, string, config.Config, *modelprobe.Verificador) review.VerificacionPlantilla {
			return review.VerificacionPlantilla{Modo: "omitido"}
		},
		publicar:            func(string, string, string) (string, bool, error) { return "https://github.com/x/pr/23", false, nil },
		registrarEvento:     func(string, string, int, []string, string, string) error { return nil },
		obtenerGitCommonDir: func(string) (string, error) { return "commondir", nil },
		registrarDecision: func(string, *store.Decision) error {
			return errors.New("boom")
		},
		resolverActor: func(string) string { return "actor-de-prueba" },
	})
	if codigo != 0 {
		t.Fatalf("codigo = %d, esperado 0 (el fallo al escribir la decisión no bloquea --force)", codigo)
	}
	if !strings.Contains(salida.String(), "no se pudo escribir la decisión de --force") {
		t.Errorf("la salida debe avisar del fallo al escribir la decisión, got: %s", salida.String())
	}
}

// TestResolverActor_UsaGitConfigLocalDelWorktree confirma que resolverActor
// usa cmd.Dir=worktree (no el cwd del proceso que ejecuta el test): un
// user.name local del worktree debe ganar, aunque el proceso de test corra
// en otro directorio (este mismo repositorio).
func TestResolverActor_UsaGitConfigLocalDelWorktree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("aislar HOME/GIT_CONFIG_NOSYSTEM de forma determinista en Windows requiere más que este helper")
	}
	worktree := t.TempDir()
	if out, err := exec.Command("git", "-C", worktree, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", worktree, "config", "user.name", "actor-local-del-worktree").CombinedOutput(); err != nil {
		t.Fatalf("git config: %v\n%s", err, out)
	}
	if got := resolverActor(worktree); got != "actor-local-del-worktree" {
		t.Errorf("resolverActor(worktree) = %q, esperado el user.name LOCAL del worktree, no el del cwd del proceso", got)
	}
}

// TestResolverActor_SinGitCaeAUserYLuegoAUsername confirma la cadena de
// fallback completa cuando git config no puede resolver ningún nombre (ni
// local ni global ni de sistema): $USER primero, $USERNAME si $USER está
// vacío, y "desconocido" si ambas lo están. HOME se redirige a un directorio
// vacío y GIT_CONFIG_NOSYSTEM=1 para que el resultado no dependa de la
// configuración git real de la máquina que ejecuta el test.
func TestResolverActor_SinGitCaeAUserYLuegoAUsername(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("aislar HOME/GIT_CONFIG_NOSYSTEM de forma determinista en Windows requiere más que este helper")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	worktreeSinGit := t.TempDir() // no es un repo git: sin config local posible

	t.Run("cae a USER", func(t *testing.T) {
		t.Setenv("USER", "usuario-env")
		t.Setenv("USERNAME", "")
		if got := resolverActor(worktreeSinGit); got != "usuario-env" {
			t.Errorf("resolverActor = %q, esperado %q ($USER)", got, "usuario-env")
		}
	})
	t.Run("sin USER cae a USERNAME", func(t *testing.T) {
		t.Setenv("USER", "")
		t.Setenv("USERNAME", "usuario-windows-env")
		if got := resolverActor(worktreeSinGit); got != "usuario-windows-env" {
			t.Errorf("resolverActor = %q, esperado %q ($USERNAME)", got, "usuario-windows-env")
		}
	})
	t.Run("sin ninguna variable usa el placeholder", func(t *testing.T) {
		t.Setenv("USER", "")
		t.Setenv("USERNAME", "")
		if got := resolverActor(worktreeSinGit); got != "desconocido" {
			t.Errorf("resolverActor = %q, esperado %q", got, "desconocido")
		}
	})
}

// TestEjecutarPrCreateCon_CargaConfigConError_Exit1SinValidarNiPublicar cubre
// el Fix 3 (hallazgo del orquestador): pr create debe usar la carga ESTRICTA
// de configuración (config.CargarConfiguracionLocalEstricta en producción,
// ver ejecutarPrCreate). Con un yml roto, debe cortar aquí mismo con exit 1 y
// el error visible, sin llegar a ejecutar la validación ni a publicar nada.
func TestEjecutarPrCreateCon_CargaConfigConError_Exit1SinValidarNiPublicar(t *testing.T) {
	var validacionLlamada, publicarLlamado bool
	var salida bytes.Buffer
	codigo := ejecutarPrCreateCon(&salida, "worktree", nil, depsPrCreate{
		cargarConfig: func(string) (config.Config, error) {
			return config.Config{}, errors.New("vassentinel.yml: line 2: field clave_inexistente not found in type config.Config")
		},
		ejecutarValidacion: func(string, []string, validation.OpcionesEjecucion) ([]validation.ValidationRun, error) {
			validacionLlamada = true
			return nil, nil
		},
		publicar: func(string, string, string) (string, bool, error) {
			publicarLlamado = true
			return "", false, nil
		},
	})
	if codigo != 1 {
		t.Fatalf("codigo = %d, esperado 1", codigo)
	}
	if !strings.Contains(salida.String(), "line") {
		t.Errorf("el error del yml roto debe quedar visible en la salida, got: %s", salida.String())
	}
	if validacionLlamada {
		t.Fatal("con el yml roto no debe llegar a ejecutar la validación")
	}
	if publicarLlamado {
		t.Fatal("con el yml roto no debe publicar nada")
	}
}

func TestExecutePrCreateWith_StackAndNetAuthority(t *testing.T) {
	res := &review.ResultadoRama{
		Fichas: []review.Ficha{fichaCreateAyuda("abc1234", review.VerdictOK)}, SHAs: []string{"abc1234"}, Decision: "single",
		Net: &review.NetReview{Audit: review.ResultadoAuditoria{Veredicto: review.VerdictBlock,
			Findings: []review.Hallazgo{{Dimension: review.DimSecurity, Severity: review.SevCritical, Description: "secret logged"}}}},
		Heredados: []review.HallazgoHeredado{{SHA: "deadbeefcafe", Hallazgo: review.Hallazgo{Dimension: review.DimLogic, Severity: review.SevCritical}}},
	}
	var opts review.OpcionesRama
	pubBase, body := "", ""
	output := &bytes.Buffer{}
	deps := depsPrCreate{
		cargarConfig:  func(string) (config.Config, error) { return config.Config{}, nil },
		obtenerGitDir: func() (string, error) { return "gd", nil },
		ejecutarValidacion: func(string, []string, validation.OpcionesEjecucion) ([]validation.ValidationRun, error) {
			return nil, nil
		},
		analizarRama: func(_ string, o review.OpcionesRama) (*review.ResultadoRama, error) { opts = o; return res, nil },
		verificar: func(string, string, config.Config, *modelprobe.Verificador) review.VerificacionPlantilla {
			return review.VerificacionPlantilla{Modo: "omitido"}
		},
		publicar: func(_, path, base string) (string, bool, error) {
			pubBase = base
			data, _ := os.ReadFile(path)
			body = string(data)
			return "https://x/pr/1", false, nil
		},
		registrarEvento: func(string, string, int, []string, string, string) error { return nil },
	}
	res.Propio = &review.RangoPropio{Parent: "layer-a", PublicationBranch: "layer-a"}
	code := ejecutarPrCreateCon(output, "wt", []string{"--parent", "layer-a", "--chain-pr"}, deps)
	if code != 0 || pubBase != "layer-a" || *opts.OwnDiff != (review.OwnDiffOptions{Parent: "layer-a"}) ||
		opts.NetReview == nil || opts.NetReview.Intention != honestNetIntention {
		t.Errorf("stacked: exitCode=%d base=%q own=%v net=%v", code, pubBase, opts.OwnDiff, opts.NetReview)
	}
	for _, want := range []string{"Net audit verdict: block", "secret logged", "OWN (per-commit audit)", "INHERITED (non-blocking)", "deadbee"} {
		if !strings.Contains(body, want) {
			t.Errorf("published body lacks %q: %s", want, body)
		}
	}
	if strings.Contains(body, "No pending risks") {
		t.Errorf("a net BLOCK must not claim a risk-free body: %s", body)
	}
	pubBase, output = "", new(bytes.Buffer)
	code = ejecutarPrCreateCon(output, "wt", []string{"--chain-pr"}, deps)
	if code != 0 ||
		pubBase != "layer-a" ||
		opts.OwnDiff == nil ||
		*opts.OwnDiff != (review.OwnDiffOptions{ResolveParent: true}) {
		t.Errorf("chain-only: base=%q own=%v", pubBase, opts.OwnDiff)
	}
	res.Propio = &review.RangoPropio{Parent: "layer-a"}
	pubBase, output = "", new(bytes.Buffer)
	plantillaCreated, publishCalled := false, false
	deps.escribirPlantilla = func(string) (string, error) { plantillaCreated = true; return "/tmp/sentinel_pr_fake.md", nil }
	origPublicar := deps.publicar
	deps.publicar = func(wt, path, base string) (string, bool, error) {
		publishCalled = true
		return origPublicar(wt, path, base)
	}
	code = ejecutarPrCreateCon(output, "wt", []string{"--parent", "layer-a"}, deps)
	if code != 1 ||
		pubBase != "" ||
		opts.OwnDiff == nil ||
		*opts.OwnDiff != (review.OwnDiffOptions{Parent: "layer-a"}) ||
		plantillaCreated ||
		publishCalled {
		t.Errorf("missing publication branch: base=%q own=%v templateCreated=%v publishCalled=%v output=%q", pubBase, opts.OwnDiff, plantillaCreated, publishCalled, output.String())
	}
	deps.escribirPlantilla = nil
	deps.publicar = origPublicar
	res.Net.Audit.Veredicto = review.VerdictOK
	res.Net.Audit.Findings, res.Fichas, res.Propio = nil, []review.Ficha{fichaCreateAyuda("abc1234", review.VerdictBlock)}, nil
	pubBase, output = "", new(bytes.Buffer)
	code = ejecutarPrCreateCon(output, "wt", nil, deps)
	if code != 0 || strings.Contains(output.String(), "AVISO") || !strings.Contains(body, "Net audit verdict: ok") {
		t.Errorf("net OK over historical BLOCK must not warn: %s / %s", output.String(), body)
	}
	res.Propio, res.Net, res.Heredados = nil, nil, nil
	pubBase = ""
	code = ejecutarPrCreateCon(output, "wt", nil, deps)
	if code != 0 || pubBase != "main" {
		t.Errorf("legacy: exitCode=%d base=%q, want 0/main", code, pubBase)
	}
}

// TestResolveBlobStoreResolvesTheCommonDir covers the F8 criterion 2 wiring at its
// only nil-able point: blob reuse in AnalizarRama is gated on Store != nil, so
// a helper that silently returned nil inside a real repository would make the
// criterion unreachable from the PR commands without any test noticing.
func TestResolveBlobStoreResolvesTheCommonDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}
	repo := t.TempDir()
	for _, args := range [][]string{
		{"-C", repo, "init", "-q", "-b", "main"},
		{"-C", repo, "config", "user.email", "test@vas.sentinel"},
		{"-C", repo, "config", "user.name", "VAS Sentinel Test"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-C", repo, "add", "base.txt"},
		{"-C", repo, "commit", "-q", "-m", "feat(base): base"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	principal, err := resolveBlobStore(repo)
	if err != nil {
		t.Fatalf("resolveBlobStore inside a real repository: %v", err)
	}
	if principal == nil {
		t.Fatal("resolveBlobStore returned a nil store inside a real repository: blob reuse would never fire and a base rebase would re-audit everything")
	}

	// The contract the doc comment declares mandatory: linked worktrees share
	// ONE store. A plain repository cannot prove it, because there the
	// per-worktree git dir and the common dir are the same path; only a linked
	// worktree distinguishes ObtenerGitCommonDir from ObtenerGitDir.
	enlazado := filepath.Join(t.TempDir(), "linked")
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-q", "-b", "linked", enlazado).CombinedOutput(); err != nil {
		t.Skipf("git worktree add is unavailable: %v\n%s", err, out)
	}
	deEnlazado, err := resolveBlobStore(enlazado)
	if err != nil {
		t.Fatalf("resolveBlobStore inside a linked worktree: %v", err)
	}
	if !reflect.DeepEqual(principal, deEnlazado) {
		t.Errorf("the linked worktree resolved a different store (%v) than the main worktree (%v): reviews would not be shared across worktrees", deEnlazado, principal)
	}

	// Outside a repository the reuse optimization must report the failure and
	// return no store, so the caller degrades instead of publishing with a
	// half-built one.
	if st, err := resolveBlobStore(t.TempDir()); err == nil || st != nil {
		t.Errorf("resolveBlobStore outside a repository = (%v, %v), expected (nil, error)", st, err)
	}
}

// TestExecutePrCreateWiresTheBlobStore is the pr create half of F8 criterion 2:
// the command must hand AnalizarRama the shared blob store, otherwise rebasing
// the stack base re-audits every commit.
func TestExecutePrCreateWiresTheBlobStore(t *testing.T) {
	var opts review.OpcionesRama
	deps := depsPrCreate{
		cargarConfig:  func(string) (config.Config, error) { return config.Config{}, nil },
		obtenerGitDir: func() (string, error) { return "gd", nil },
		ejecutarValidacion: func(string, []string, validation.OpcionesEjecucion) ([]validation.ValidationRun, error) {
			return nil, nil
		},
		blobStore: func(string) (review.StoreBlobs, error) {
			return store.NuevoStore(t.TempDir()), nil
		},
		analizarRama: func(_ string, o review.OpcionesRama) (*review.ResultadoRama, error) {
			opts = o
			return nil, errors.New("cut the flow right after AnalizarRama: this test only observes its options")
		},
		verificar: func(string, string, config.Config, *modelprobe.Verificador) review.VerificacionPlantilla {
			return review.VerificacionPlantilla{Modo: "omitido"}
		},
		registrarEvento: func(string, string, int, []string, string, string) error { return nil },
		resolverActor:   func(string) string { return "actor" },
	}
	ejecutarPrCreateCon(&bytes.Buffer{}, "wt", nil, deps)
	if opts.Store == nil {
		t.Error("OpcionesRama.Store is nil: pr create never reaches blob reuse, so a base rebase re-audits the whole stack")
	}
}
