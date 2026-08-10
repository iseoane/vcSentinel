package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/ops"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
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

// TestVerificarParaPlantillaNilUsaLaRutaReal: el guard defensivo (verificar
// nil -> ops.Verificar) no se puede quitar sin romper el test: sin comandos
// configurados, la ruta real pasa por el aviso interactivo y, sin respuesta
// en stdin, degrada a omitido sin panic.
func TestVerificarParaPlantillaNilUsaLaRutaReal(t *testing.T) {
	plantilla := verificarParaPlantillaCon("worktree", "", config.Config{}, nil)
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
		cargarConfig:  func(string) (config.Config, error) { return config.Config{}, nil },
		obtenerGitDir: func() (string, error) { return "gitdir", nil },
		ejecutarValidacion: func(string, []string, validation.OpcionesEjecucion) ([]validation.ValidationRun, error) {
			return []validation.ValidationRun{{Capability: "test", Comando: "go test ./...", Exit: 1}}, nil
		},
		analizarRama: func(string, review.OpcionesRama) (*review.ResultadoRama, error) {
			return &review.ResultadoRama{Fichas: []review.Ficha{fichaOK}, SHAs: []string{"abc1234"}, Decision: "single"}, nil
		},
		verificar: func(string, string, config.Config) review.VerificacionPlantilla {
			return review.VerificacionPlantilla{Modo: "omitido"}
		},
		publicar: func(string, string, string) (string, bool, error) { return "https://github.com/x/pr/9", false, nil },
		registrarEvento: func(gitDir, tipo string, exit int, shas []string, detalle, worktree string) error {
			detalleRegistrado = detalle
			return nil
		},
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
		verificar: func(string, string, config.Config) review.VerificacionPlantilla {
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

// TestEjecutarPrCreateCon_ValidacionVerdeVeredictoBlock_PublicaConAvisoDestacado
// cubre el cuarto escenario: con validación en verde, un veredicto semántico
// block ya no bloquea (advisory) — publica igual con el aviso destacado
// visible tanto en la salida como en la plantilla generada.
func TestEjecutarPrCreateCon_ValidacionVerdeVeredictoBlock_PublicaConAvisoDestacado(t *testing.T) {
	fichaBlock := fichaCreateAyuda("abc1234", review.VerdictBlock,
		review.DimensionResult{Dim: review.DimSecurity, Verdict: review.VerdictBlock,
			Findings: []review.ReviewFinding{hallazgoCritico()}})
	var cuerpoPublicado string
	var salida bytes.Buffer
	codigo := ejecutarPrCreateCon(&salida, "worktree", nil, depsPrCreate{
		cargarConfig:  func(string) (config.Config, error) { return config.Config{}, nil },
		obtenerGitDir: func() (string, error) { return "gitdir", nil },
		ejecutarValidacion: func(string, []string, validation.OpcionesEjecucion) ([]validation.ValidationRun, error) {
			return nil, nil // validación en verde: sin runs, sin hallazgos
		},
		analizarRama: func(string, review.OpcionesRama) (*review.ResultadoRama, error) {
			return &review.ResultadoRama{Fichas: []review.Ficha{fichaBlock}, SHAs: []string{"abc1234"}, Decision: "single"}, nil
		},
		verificar: func(string, string, config.Config) review.VerificacionPlantilla {
			return review.VerificacionPlantilla{Modo: "omitido"}
		},
		publicar: func(worktree, ruta, base string) (string, bool, error) {
			datos, err := os.ReadFile(ruta)
			if err != nil {
				t.Fatalf("no se pudo leer la plantilla publicada: %v", err)
			}
			cuerpoPublicado = string(datos)
			return "https://github.com/x/pr/10", false, nil
		},
		registrarEvento: func(string, string, int, []string, string, string) error { return nil },
	})
	if codigo != 0 {
		t.Fatalf("codigo = %d, esperado 0 (el veredicto semántico ya no bloquea)", codigo)
	}
	if !strings.Contains(salida.String(), "AVISO") {
		t.Errorf("la salida debe mostrar el aviso destacado del veredicto block: %s", salida.String())
	}
	if !strings.Contains(cuerpoPublicado, "block") {
		t.Errorf("la plantilla publicada debe mostrar el veredicto block: %s", cuerpoPublicado)
	}
}
