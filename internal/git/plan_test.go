package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestConstruirPlanFragmentacionAgrupaPorCapasEnOrden(t *testing.T) {
	archivos := []ArchivoModificado{
		{Ruta: "web/app.tsx", Lineas: 100, Capa: "frontend"},
		{Ruta: "cmd/main.go", Lineas: 100, Capa: "backend"},
		{Ruta: "config.yaml", Lineas: 100, Capa: "config"},
		{Ruta: "internal/git/slice_test.go", Lineas: 100, Capa: "test"},
	}
	plan, err := ConstruirPlanFragmentacion(archivos, func(ArchivoModificado) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("ConstruirPlanFragmentacion devolvió error: %v", err)
	}
	if len(plan.Lotes) != 4 {
		t.Fatalf("se esperaban 4 lotes, obtuve %d", len(plan.Lotes))
	}
	var capas []string
	for _, lote := range plan.Lotes {
		capas = append(capas, lote.Capa)
	}
	esperado := []string{"config", "backend", "frontend", "test"}
	if !reflect.DeepEqual(capas, esperado) {
		t.Errorf("orden de capas = %v, esperado %v", capas, esperado)
	}
	for i, lote := range plan.Lotes {
		if lote.Numero != i+1 {
			t.Errorf("lote %d: número %d, esperado %d", i, lote.Numero, i+1)
		}
	}
}

// TestConstruirPlanFragmentacionNoMezclaClases cubre T0.12: un .go y un .md
// que caen en la misma capa ("backend", el caso por defecto de
// ClasificarCapa para un .md) no deben terminar en el mismo lote. Antes de
// esta tarea, agruparPorCapas solo miraba la capa y los mezclaba, como pasó
// de verdad en el commit 3160133 (código y doc de diseño en un solo commit).
func TestConstruirPlanFragmentacionNoMezclaClases(t *testing.T) {
	archivos := []ArchivoModificado{
		{Ruta: "cmd/main.go", Lineas: 50, Capa: "backend"},
		{Ruta: "docs/guia.md", Lineas: 50, Capa: "backend"},
	}
	plan, err := ConstruirPlanFragmentacion(archivos, func(ArchivoModificado) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("ConstruirPlanFragmentacion devolvió error: %v", err)
	}
	if len(plan.Lotes) != 2 {
		t.Fatalf("se esperaban 2 lotes (uno por clase), obtuve %d", len(plan.Lotes))
	}
	for _, lote := range plan.Lotes {
		clases := make(map[string]bool)
		for _, ruta := range lote.Rutas {
			clases[ClaseArchivo(ruta)] = true
		}
		if len(clases) > 1 {
			t.Errorf("lote #%d mezcla clases: rutas %v", lote.Numero, lote.Rutas)
		}
	}
}

func TestConstruirPlanFragmentacionMantieneAgrupacionPorLimites(t *testing.T) {
	archivos := []ArchivoModificado{
		{Ruta: "a.go", Lineas: 200, Capa: "backend"},
		{Ruta: "b.go", Lineas: 200, Capa: "backend"},
		{Ruta: "c.go", Lineas: 200, Capa: "backend"},
	}
	plan, err := ConstruirPlanFragmentacion(archivos, func(ArchivoModificado) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("ConstruirPlanFragmentacion devolvió error: %v", err)
	}
	if len(plan.Lotes) != 2 {
		t.Fatalf("se esperaban 2 lotes, obtuve %d", len(plan.Lotes))
	}
	if !reflect.DeepEqual(plan.Lotes[0].Rutas, []string{"a.go", "b.go"}) {
		t.Errorf("lote 1 rutas = %v", plan.Lotes[0].Rutas)
	}
	if plan.Lotes[0].LineasTotales != 400 {
		t.Errorf("lote 1 líneas = %d, esperado 400", plan.Lotes[0].LineasTotales)
	}
	if plan.Lotes[0].Numero != 1 || plan.Lotes[1].Numero != 2 {
		t.Errorf("números de lote = %d, %d; esperado 1, 2", plan.Lotes[0].Numero, plan.Lotes[1].Numero)
	}
	if !reflect.DeepEqual(plan.Lotes[1].Rutas, []string{"c.go"}) {
		t.Errorf("lote 2 rutas = %v", plan.Lotes[1].Rutas)
	}
}

func TestConstruirPlanFragmentacionAislaConfigGigante(t *testing.T) {
	archivos := []ArchivoModificado{
		{Ruta: "package-lock.json", Lineas: 450, Capa: "config"},
		{Ruta: "cmd/main.go", Lineas: 100, Capa: "backend"},
	}
	plan, err := ConstruirPlanFragmentacion(archivos, func(ArchivoModificado) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("ConstruirPlanFragmentacion devolvió error: %v", err)
	}
	if len(plan.Lotes) != 2 {
		t.Fatalf("se esperaban 2 lotes (gigante + normal), obtuve %d", len(plan.Lotes))
	}
	// Desde T0.12 los lotes se agrupan primero por clase de archivo
	// (ClaseArchivo) en el orden config → source → test → docs → generated.
	// "package-lock.json" es clase "generated" (esGenerado por sufijo
	// "-lock.json"), aunque su capa de negocio sea "config"; por eso el lote
	// normal de código (clase "source") sale antes que el gigante aislado.
	normal := plan.Lotes[0]
	if normal.EsGigante || normal.Numero != 1 {
		t.Errorf("lote normal = %+v", normal)
	}
	if normal.Mensaje != "" {
		t.Errorf("el mensaje del lote normal debe nacer vacío y generarse luego, obtuve %q", normal.Mensaje)
	}
	if normal.MensajeDeterminista {
		t.Errorf("el lote normal no debería nacer determinista")
	}
	gigante := plan.Lotes[1]
	if !gigante.EsGigante {
		t.Errorf("el segundo lote debería ser gigante, obtuve %+v", gigante)
	}
	if gigante.Mensaje != mensajeAisladoDeps {
		t.Errorf("mensaje gigante = %q, esperado %q", gigante.Mensaje, mensajeAisladoDeps)
	}
	if !gigante.MensajeDeterminista {
		t.Errorf("el mensaje del gigante debería ser determinista")
	}
	if !reflect.DeepEqual(gigante.Rutas, []string{"package-lock.json"}) {
		t.Errorf("rutas gigante = %v", gigante.Rutas)
	}
	if gigante.LineasTotales != 450 {
		t.Errorf("líneas gigante = %d, esperado 450", gigante.LineasTotales)
	}
}

func TestConstruirPlanFragmentacionAislaCodigoGiganteConfirmado(t *testing.T) {
	archivos := []ArchivoModificado{
		{Ruta: "big.go", Lineas: 600, Capa: "backend"},
		{Ruta: "config.yaml", Lineas: 50, Capa: "config"},
	}
	confirmado := false
	plan, err := ConstruirPlanFragmentacion(archivos, func(f ArchivoModificado) (bool, error) {
		confirmado = true
		if f.Ruta != "big.go" {
			t.Errorf("confirmarBypass recibió %q, esperado big.go", f.Ruta)
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("ConstruirPlanFragmentacion devolvió error: %v", err)
	}
	if !confirmado {
		t.Error("confirmarBypass debería haberse invocado para el código gigante")
	}
	if len(plan.Lotes) != 2 {
		t.Fatalf("se esperaban 2 lotes, obtuve %d", len(plan.Lotes))
	}
	gigante := plan.Lotes[1]
	if !gigante.EsGigante || gigante.Capa != "backend" {
		t.Errorf("gigante = %+v", gigante)
	}
	esperado := "chore(slice): bypass IA for massive file big.go"
	if gigante.Mensaje != esperado {
		t.Errorf("mensaje = %q, esperado %q", gigante.Mensaje, esperado)
	}
	if !gigante.MensajeDeterminista {
		t.Errorf("el mensaje del gigante confirmado debería ser determinista")
	}
}

func TestConstruirPlanFragmentacionAbortaSiSeRechazaGigante(t *testing.T) {
	archivos := []ArchivoModificado{
		{Ruta: "big.go", Lineas: 600, Capa: "backend"},
	}
	_, err := ConstruirPlanFragmentacion(archivos, func(ArchivoModificado) (bool, error) { return false, nil })
	if err == nil {
		t.Fatal("se esperaba error al rechazar el código gigante")
	}
	if !strings.Contains(err.Error(), "abortada") {
		t.Errorf("error = %q, esperado mención de aborto", err)
	}
}

func TestConstruirPlanFragmentacionPropagaErrorDeConfirmacion(t *testing.T) {
	archivos := []ArchivoModificado{
		{Ruta: "big.go", Lineas: 600, Capa: "backend"},
	}
	_, err := ConstruirPlanFragmentacion(archivos, func(ArchivoModificado) (bool, error) { return false, os.ErrPermission })
	if err != os.ErrPermission {
		t.Errorf("error = %v, esperado os.ErrPermission", err)
	}
}

func TestConstruirPlanFragmentacionAbortaSinCommitearNada(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioPrueba(t, map[string]string{
		"a.go": "package a\n",
	})
	t.Chdir(dir)

	if err := os.WriteFile("big.go", []byte("package big\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ejecutarGit(t, dir, "add", "big.go")

	archivos := []ArchivoModificado{
		{Ruta: "big.go", Lineas: 600, Capa: "backend"},
	}
	_, err := ConstruirPlanFragmentacion(archivos, func(ArchivoModificado) (bool, error) { return false, nil })
	if err == nil {
		t.Fatal("se esperaba error al rechazar el código gigante")
	}

	totalCommits := ejecutarGit(t, dir, "rev-list", "--count", "HEAD")
	if totalCommits != "1" {
		t.Errorf("no debería crearse ningún commit al abortar, obtuve %s commits", totalCommits)
	}
	estado := ejecutarGit(t, dir, "status", "--porcelain")
	if !strings.Contains(estado, "big.go") {
		t.Errorf("big.go debería seguir pendiente tras abortar, obtuve: %q", estado)
	}
}

func TestGenerarMensajesLotesConAdapter(t *testing.T) {
	archivos := []ArchivoModificado{
		{Ruta: "config.yaml", Lineas: 50, Capa: "config"},
		{Ruta: "cmd/main.go", Lineas: 100, Capa: "backend"},
		{Ruta: "package-lock.json", Lineas: 450, Capa: "config"},
	}
	plan, err := ConstruirPlanFragmentacion(archivos, func(ArchivoModificado) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("ConstruirPlanFragmentacion devolvió error: %v", err)
	}

	fallbacks := GenerarMensajesLotes(plan, adaptadorPrueba{})
	if fallbacks != 0 {
		t.Errorf("se esperaban 0 fallbacks con adaptador correcto, obtuve %d", fallbacks)
	}
	for _, lote := range plan.Lotes {
		if lote.Mensaje == "" {
			t.Errorf("lote %d (capa %s) quedó sin mensaje", lote.Numero, lote.Capa)
		}
		if lote.EsGigante {
			if lote.Mensaje != mensajeAisladoDeps {
				t.Errorf("el mensaje del gigante fue sobreescrito: %q", lote.Mensaje)
			}
			continue
		}
		if lote.Mensaje != "chore(slice): prueba" {
			t.Errorf("lote %d mensaje = %q, esperado el del adaptador", lote.Numero, lote.Mensaje)
		}
		if lote.MensajeDeterminista {
			t.Errorf("lote %d debería marcarse como generado por el adaptador", lote.Numero)
		}
	}
}

func TestGenerarMensajesLotesUsaFallbackCuandoAdapterFalla(t *testing.T) {
	archivos := []ArchivoModificado{
		{Ruta: "a.go", Lineas: 200, Capa: "backend"},
		{Ruta: "b.go", Lineas: 200, Capa: "backend"},
		{Ruta: "c.go", Lineas: 200, Capa: "backend"},
	}
	plan, err := ConstruirPlanFragmentacion(archivos, func(ArchivoModificado) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("ConstruirPlanFragmentacion devolvió error: %v", err)
	}
	if len(plan.Lotes) != 2 {
		t.Fatalf("precondición: se esperaban 2 lotes, obtuve %d", len(plan.Lotes))
	}

	adapterFallido := agentadapterFunc(func(rutas []string, capa string, num int) (string, error) {
		return "", os.ErrPermission
	})
	fallbacks := GenerarMensajesLotes(plan, adapterFallido)
	if fallbacks != 2 {
		t.Errorf("se esperaban 2 fallbacks, obtuve %d", fallbacks)
	}
	for i, lote := range plan.Lotes {
		esperado := "chore(slice): auto-fragmented backend batch #" + strconv.Itoa(i+1)
		if lote.Mensaje != esperado {
			t.Errorf("lote %d mensaje = %q, esperado %q", i, lote.Mensaje, esperado)
		}
		if !lote.MensajeDeterminista {
			t.Errorf("lote %d debería marcarse determinista tras el fallback", i)
		}
	}
}

func TestGenerarMensajesLotesRegeneraInclusoLotesYaCaidos(t *testing.T) {
	archivos := []ArchivoModificado{
		{Ruta: "a.go", Lineas: 50, Capa: "backend"},
	}
	plan, err := ConstruirPlanFragmentacion(archivos, func(ArchivoModificado) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("ConstruirPlanFragmentacion devolvió error: %v", err)
	}

	adapterFallido := agentadapterFunc(func(rutas []string, capa string, num int) (string, error) {
		return "", os.ErrPermission
	})
	GenerarMensajesLotes(plan, adapterFallido)
	if !plan.Lotes[0].MensajeDeterminista {
		t.Fatal("precondición: el lote debería haber caído al respaldo")
	}

	// Un adaptador nuevo debe volver a regenerar el mensaje aunque el lote
	// ya estuviera marcado como determinista por el fallback anterior.
	fallbacks := GenerarMensajesLotes(plan, adaptadorPrueba{})
	if fallbacks != 0 {
		t.Errorf("se esperaban 0 fallbacks con el nuevo adaptador, obtuve %d", fallbacks)
	}
	if plan.Lotes[0].Mensaje != "chore(slice): prueba" || plan.Lotes[0].MensajeDeterminista {
		t.Errorf("el lote debería regenerarse con el nuevo adaptador, obtuve %+v", plan.Lotes[0])
	}
}

func TestAplicarMensajesAutomaticos(t *testing.T) {
	archivos := []ArchivoModificado{
		{Ruta: "a.go", Lineas: 50, Capa: "backend"},
		{Ruta: "package-lock.json", Lineas: 450, Capa: "config"},
	}
	plan, err := ConstruirPlanFragmentacion(archivos, func(ArchivoModificado) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("ConstruirPlanFragmentacion devolvió error: %v", err)
	}
	if len(plan.Lotes) != 2 {
		t.Fatalf("se esperaban 2 lotes (gigante + normal), obtuve %d", len(plan.Lotes))
	}
	// Igual que en TestConstruirPlanFragmentacionAislaConfigGigante: por clase,
	// "a.go" (source) sale antes que "package-lock.json" (generated).
	normal := plan.Lotes[0]
	gigante := plan.Lotes[1]
	GenerarMensajesLotes(plan, adaptadorPrueba{})
	if normal.MensajeDeterminista {
		t.Fatal("precondición: el lote normal debería ser no determinista antes de aplicar automáticos")
	}

	AplicarMensajesAutomaticos(plan)
	for _, lote := range plan.Lotes {
		if !lote.MensajeDeterminista {
			t.Errorf("lote %d debería quedar determinista", lote.Numero)
		}
		if lote.Mensaje != lote.MensajeAutomatico {
			t.Errorf("lote %d mensaje = %q, esperado %q", lote.Numero, lote.Mensaje, lote.MensajeAutomatico)
		}
	}
	if gigante.Mensaje != mensajeAisladoDeps {
		t.Errorf("el gigante debería conservar su mensaje determinista, obtuve %q", gigante.Mensaje)
	}
}

func TestRegenerarMensajeLote(t *testing.T) {
	archivos := []ArchivoModificado{
		{Ruta: "a.go", Lineas: 200, Capa: "backend"},
		{Ruta: "b.go", Lineas: 200, Capa: "backend"},
		{Ruta: "c.go", Lineas: 200, Capa: "backend"},
	}
	plan, err := ConstruirPlanFragmentacion(archivos, func(ArchivoModificado) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("ConstruirPlanFragmentacion devolvió error: %v", err)
	}
	if len(plan.Lotes) != 2 {
		t.Fatalf("precondición: se esperaban 2 lotes, obtuve %d", len(plan.Lotes))
	}

	adapter := agentadapterFunc(func(rutas []string, capa string, num int) (string, error) {
		return "fix: regenerado", nil
	})
	if err := RegenerarMensajeLote(plan, 2, adapter); err != nil {
		t.Fatalf("RegenerarMensajeLote devolvió error: %v", err)
	}
	if plan.Lotes[1].Mensaje != "fix: regenerado" {
		t.Errorf("mensaje = %q, esperado 'fix: regenerado'", plan.Lotes[1].Mensaje)
	}
	if plan.Lotes[1].MensajeDeterminista {
		t.Errorf("lote regenerado no debería marcarse determinista")
	}
	if plan.Lotes[0].Mensaje != "" {
		t.Errorf("el lote 1 no debería tocarse, obtuve %q", plan.Lotes[0].Mensaje)
	}
}

func TestRegenerarMensajeLoteFallaConFallback(t *testing.T) {
	archivos := []ArchivoModificado{{Ruta: "a.go", Lineas: 50, Capa: "backend"}}
	plan, err := ConstruirPlanFragmentacion(archivos, func(ArchivoModificado) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("ConstruirPlanFragmentacion devolvió error: %v", err)
	}
	adapterFallido := agentadapterFunc(func(rutas []string, capa string, num int) (string, error) {
		return "", os.ErrPermission
	})
	if err := RegenerarMensajeLote(plan, 1, adapterFallido); err != nil {
		t.Fatalf("RegenerarMensajeLote devolvió error: %v", err)
	}
	if plan.Lotes[0].Mensaje != "chore(slice): auto-fragmented backend batch #1" {
		t.Errorf("mensaje = %q, esperado el respaldo", plan.Lotes[0].Mensaje)
	}
	if !plan.Lotes[0].MensajeDeterminista {
		t.Errorf("lote debería marcarse determinista tras el fallo")
	}
}

func TestRegenerarMensajeLoteConNumeroDesconocido(t *testing.T) {
	archivos := []ArchivoModificado{{Ruta: "a.go", Lineas: 50, Capa: "backend"}}
	plan, err := ConstruirPlanFragmentacion(archivos, func(ArchivoModificado) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("ConstruirPlanFragmentacion devolvió error: %v", err)
	}
	if err := RegenerarMensajeLote(plan, 99, adaptadorPrueba{}); err == nil {
		t.Fatal("se esperaba error con número de lote desconocido")
	}
}

func TestAplicarMensajeAutomaticoLote(t *testing.T) {
	archivos := []ArchivoModificado{{Ruta: "a.go", Lineas: 50, Capa: "backend"}}
	plan, err := ConstruirPlanFragmentacion(archivos, func(ArchivoModificado) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("ConstruirPlanFragmentacion devolvió error: %v", err)
	}
	GenerarMensajesLotes(plan, adaptadorPrueba{})

	if err := AplicarMensajeAutomaticoLote(plan, 1); err != nil {
		t.Fatalf("AplicarMensajeAutomaticoLote devolvió error: %v", err)
	}
	if plan.Lotes[0].Mensaje != "chore(slice): auto-fragmented backend batch #1" {
		t.Errorf("mensaje = %q, esperado el determinista", plan.Lotes[0].Mensaje)
	}
	if !plan.Lotes[0].MensajeDeterminista {
		t.Errorf("lote debería marcarse determinista")
	}
}

func TestEditarMensajeLote(t *testing.T) {
	archivos := []ArchivoModificado{{Ruta: "a.go", Lineas: 50, Capa: "backend"}}
	plan, err := ConstruirPlanFragmentacion(archivos, func(ArchivoModificado) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("ConstruirPlanFragmentacion devolvió error: %v", err)
	}
	if err := EditarMensajeLote(plan, 1, "feat: manual"); err != nil {
		t.Fatalf("EditarMensajeLote devolvió error: %v", err)
	}
	if plan.Lotes[0].Mensaje != "feat: manual" {
		t.Errorf("mensaje = %q, esperado 'feat: manual'", plan.Lotes[0].Mensaje)
	}
	if plan.Lotes[0].MensajeDeterminista {
		t.Errorf("mensaje editado no debería marcarse determinista")
	}
}

func TestEditarMensajeLoteConNumeroDesconocido(t *testing.T) {
	archivos := []ArchivoModificado{{Ruta: "a.go", Lineas: 50, Capa: "backend"}}
	plan, err := ConstruirPlanFragmentacion(archivos, func(ArchivoModificado) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("ConstruirPlanFragmentacion devolvió error: %v", err)
	}
	if err := EditarMensajeLote(plan, 42, "feat: x"); err == nil {
		t.Fatal("se esperaba error con número de lote desconocido")
	}
}

func TestVerificarAdaptador(t *testing.T) {
	if !VerificarAdaptador(adaptadorPrueba{}) {
		t.Error("adaptadorPrueba debería verificar correctamente")
	}
	adapterFallido := agentadapterFunc(func(rutas []string, capa string, num int) (string, error) {
		return "", os.ErrPermission
	})
	if VerificarAdaptador(adapterFallido) {
		t.Error("un adaptador que falla no debería verificar")
	}
}

func TestEjecutarPlanFragmentacionEnRepositorioReal(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioPrueba(t, map[string]string{
		"a.go": "package a\n",
		"b.go": "package b\n",
		"c.go": "package c\n",
		"d.go": "package d\n",
	})
	t.Chdir(dir)

	agregarLineas(t, "b.go", 200)
	agregarLineas(t, "c.go", 200)
	agregarLineas(t, "d.go", 200)

	archivos, err := ObtenerArchivosModificados()
	if err != nil {
		t.Fatalf("ObtenerArchivosModificados devolvió error: %v", err)
	}
	if len(archivos) != 3 {
		t.Fatalf("se esperaban 3 archivos modificados, obtuve %d", len(archivos))
	}

	plan, err := ConstruirPlanFragmentacion(archivos, func(ArchivoModificado) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("ConstruirPlanFragmentacion devolvió error: %v", err)
	}
	GenerarMensajesLotes(plan, adaptadorPrueba{})

	resultados, err := EjecutarPlanFragmentacion(plan)
	if err != nil {
		t.Fatalf("EjecutarPlanFragmentacion devolvió error: %v", err)
	}
	if len(resultados) != 2 {
		t.Fatalf("se esperaban 2 commits, obtuve %d", len(resultados))
	}
	totalCommits := ejecutarGit(t, dir, "rev-list", "--count", "HEAD")
	if totalCommits != "3" {
		t.Errorf("se esperaban 3 commits (inicial + 2 lotes), obtuve %s", totalCommits)
	}
	for i, r := range resultados {
		if r.Hash == "" {
			t.Errorf("el resultado %d debería incluir un hash corto", i)
		}
		if r.Mensaje != "chore(slice): prueba" {
			t.Errorf("resultado %d mensaje = %q, esperado el aprobado", i, r.Mensaje)
		}
		if r.Capa != "backend" {
			t.Errorf("resultado %d capa = %q, esperado backend", i, r.Capa)
		}
	}
	if resultados[0].Archivos != 2 || resultados[1].Archivos != 1 {
		t.Errorf("archivos por commit = %d, %d; esperado 2, 1", resultados[0].Archivos, resultados[1].Archivos)
	}
	estado := ejecutarGit(t, dir, "status", "--porcelain")
	if estado != "" {
		t.Errorf("el worktree debería quedar limpio tras fragmentar, obtuve: %s", estado)
	}
}

func TestEjecutarPlanFragmentacionOmiteVerificacionDeHooks(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioPrueba(t, map[string]string{
		"a.go": "package a\n",
	})
	t.Chdir(dir)

	// Hook que falla siempre: el flujo de slice (mecanismo de fragmentación del
	// guardián) omite la verificación con --no-verify para todos sus commits.
	dirHooks := t.TempDir()
	hook := filepath.Join(dirHooks, "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	ejecutarGit(t, dir, "config", "core.hooksPath", filepath.ToSlash(dirHooks))

	// Lote gigante confirmado: el commit debe omitir el hook.
	if err := os.WriteFile("big.go", []byte("package big\n"), 0644); err != nil {
		t.Fatal(err)
	}
	archivos := []ArchivoModificado{
		{Ruta: "big.go", Lineas: 600, Capa: "backend"},
	}
	plan, err := ConstruirPlanFragmentacion(archivos, func(ArchivoModificado) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("ConstruirPlanFragmentacion devolvió error: %v", err)
	}
	if len(plan.Lotes) != 1 || !plan.Lotes[0].EsGigante {
		t.Fatalf("precondición: se esperaba 1 lote gigante, obtuve %+v", plan.Lotes)
	}
	if _, err := EjecutarPlanFragmentacion(plan); err != nil {
		t.Fatalf("el lote gigante debería omitir el hook, obtuve error: %v", err)
	}

	// Lote normal con el mismo hook también debe omitirlo: el total pendiente
	// de otros lotes no puede rechazar commits legítimos de slice.
	agregarLineas(t, "a.go", 10)
	planNormal, err := ConstruirPlanFragmentacion([]ArchivoModificado{
		{Ruta: "a.go", Lineas: 10, Capa: "backend"},
	}, func(ArchivoModificado) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("ConstruirPlanFragmentacion devolvió error: %v", err)
	}
	if _, err := EjecutarPlanFragmentacion(planNormal); err != nil {
		t.Fatalf("el lote normal debería omitir el hook, obtuve error: %v", err)
	}
}

func TestEjecutarPlanFragmentacionUsaMensajeDeRespaldoCuandoAdapterFalla(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioPrueba(t, map[string]string{
		"a.go": "package a\n",
	})
	t.Chdir(dir)

	agregarLineas(t, "a.go", 10)
	archivos, err := ObtenerArchivosModificados()
	if err != nil {
		t.Fatalf("ObtenerArchivosModificados devolvió error: %v", err)
	}
	plan, err := ConstruirPlanFragmentacion(archivos, func(ArchivoModificado) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("ConstruirPlanFragmentacion devolvió error: %v", err)
	}

	adapterFallido := agentadapterFunc(func(rutas []string, capa string, num int) (string, error) {
		return "", os.ErrPermission
	})
	GenerarMensajesLotes(plan, adapterFallido)

	if _, err := EjecutarPlanFragmentacion(plan); err != nil {
		t.Fatalf("EjecutarPlanFragmentacion devolvió error: %v", err)
	}
	mensaje := ejecutarGit(t, dir, "log", "-1", "--pretty=%s")
	if !strings.Contains(mensaje, "auto-fragmented") {
		t.Errorf("el commit debería usar el mensaje de respaldo, obtuve: %q", mensaje)
	}
}

func TestEjecutarPlanFragmentacionUsaMensajesAprobados(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioPrueba(t, map[string]string{
		"a.go": "package a\n",
	})
	t.Chdir(dir)

	agregarLineas(t, "a.go", 10)
	archivos, err := ObtenerArchivosModificados()
	if err != nil {
		t.Fatalf("ObtenerArchivosModificados devolvió error: %v", err)
	}
	plan, err := ConstruirPlanFragmentacion(archivos, func(ArchivoModificado) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("ConstruirPlanFragmentacion devolvió error: %v", err)
	}
	plan.Lotes[0].Mensaje = "feat: aprobado por el usuario"

	if _, err := EjecutarPlanFragmentacion(plan); err != nil {
		t.Fatalf("EjecutarPlanFragmentacion devolvió error: %v", err)
	}
	mensaje := ejecutarGit(t, dir, "log", "-1", "--pretty=%s")
	if mensaje != "feat: aprobado por el usuario" {
		t.Errorf("mensaje = %q, esperado el aprobado por el usuario", mensaje)
	}
}

func TestEjecutarPlanFragmentacionAislaConfigGigante(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioPrueba(t, map[string]string{
		"a.go": "package a\n",
	})
	t.Chdir(dir)

	var builder strings.Builder
	builder.WriteString("{\n")
	for i := 0; i < 450; i++ {
		builder.WriteString("  \"dep\": true,\n")
	}
	builder.WriteString("}\n")
	if err := os.WriteFile("package-lock.json", []byte(builder.String()), 0644); err != nil {
		t.Fatal(err)
	}

	archivos, err := ObtenerArchivosModificados()
	if err != nil {
		t.Fatalf("ObtenerArchivosModificados devolvió error: %v", err)
	}
	if len(archivos) != 1 {
		t.Fatalf("se esperaba 1 archivo, obtuve %d: %+v", len(archivos), archivos)
	}
	if archivos[0].Capa != "config" {
		t.Errorf("capa = %q, esperado config", archivos[0].Capa)
	}

	plan, err := ConstruirPlanFragmentacion(archivos, func(ArchivoModificado) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("ConstruirPlanFragmentacion devolvió error: %v", err)
	}
	if _, err := EjecutarPlanFragmentacion(plan); err != nil {
		t.Fatalf("EjecutarPlanFragmentacion devolvió error: %v", err)
	}

	mensaje := ejecutarGit(t, dir, "log", "-1", "--pretty=%s")
	if mensaje != mensajeAisladoDeps {
		t.Errorf("mensaje = %q, esperado %q", mensaje, mensajeAisladoDeps)
	}
	estado := ejecutarGit(t, dir, "status", "--porcelain")
	if estado != "" {
		t.Errorf("el worktree debería quedar limpio tras aislar el lock, obtuve: %s", estado)
	}
}

func TestEjecutarPlanFragmentacionAislaCodigoGiganteConfirmado(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioPrueba(t, map[string]string{
		"a.go": "package a\n",
	})
	t.Chdir(dir)

	if err := os.WriteFile("big.go", []byte("package big\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ejecutarGit(t, dir, "add", "big.go")

	archivos := []ArchivoModificado{
		{Ruta: "big.go", Lineas: 600, Capa: "backend"},
	}
	plan, err := ConstruirPlanFragmentacion(archivos, func(ArchivoModificado) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("ConstruirPlanFragmentacion devolvió error: %v", err)
	}
	if _, err := EjecutarPlanFragmentacion(plan); err != nil {
		t.Fatalf("EjecutarPlanFragmentacion devolvió error: %v", err)
	}

	mensaje := ejecutarGit(t, dir, "log", "-1", "--pretty=%s")
	if mensaje != "chore(slice): bypass IA for massive file big.go" {
		t.Errorf("mensaje = %q, esperado bypass", mensaje)
	}
	estado := ejecutarGit(t, dir, "status", "--porcelain")
	if estado != "" {
		t.Errorf("el worktree debería quedar limpio tras el bypass, obtuve: %s", estado)
	}
}

func TestEjecutarPlanFragmentacionEntregaDiffAlAdaptador(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioPrueba(t, map[string]string{
		"a.go": "package a\n",
	})
	t.Chdir(dir)

	agregarLineas(t, "a.go", 5)
	archivos, err := ObtenerArchivosModificados()
	if err != nil {
		t.Fatalf("ObtenerArchivosModificados devolvió error: %v", err)
	}
	plan, err := ConstruirPlanFragmentacion(archivos, func(ArchivoModificado) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("ConstruirPlanFragmentacion devolvió error: %v", err)
	}

	adapter := &adaptadorConDiffPrueba{}
	GenerarMensajesLotes(plan, adapter)
	if !strings.Contains(adapter.diffRecibido, "+// linea generada") {
		t.Errorf("el diff del lote debería contener la línea añadida, obtuve: %q", adapter.diffRecibido)
	}

	if _, err := EjecutarPlanFragmentacion(plan); err != nil {
		t.Fatalf("EjecutarPlanFragmentacion devolvió error: %v", err)
	}
	mensaje := ejecutarGit(t, dir, "log", "-1", "--pretty=%s")
	if mensaje != "chore(slice): con diff" {
		t.Errorf("mensaje = %q, esperado el mensaje del adaptador con diff", mensaje)
	}
}

func TestWorktreeLimpio(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioPrueba(t, map[string]string{
		"a.go": "package a\n",
	})
	t.Chdir(dir)

	limpio, err := WorktreeLimpio()
	if err != nil {
		t.Fatalf("WorktreeLimpio devolvió error: %v", err)
	}
	if !limpio {
		t.Error("un worktree recién preparado debería estar limpio")
	}

	agregarLineas(t, "a.go", 5)
	limpio, err = WorktreeLimpio()
	if err != nil {
		t.Fatalf("WorktreeLimpio devolvió error: %v", err)
	}
	if limpio {
		t.Error("un worktree con cambios debería reportar no limpio")
	}
}

// TestEjecutarPlanFragmentacionCommiteaArchivoTrackeadoQueGitignoreIgnoraDespues
// reproduce B13: un archivo ya trackeado (como .atl/ en este repo) al que
// .gitignore empieza a afectar después. Sin -f, 'git add' sobre esa ruta
// avisa y sale con código 1 aunque de todos modos deja el archivo en stage,
// y ese error abortaba el lote entero.
func TestEjecutarPlanFragmentacionCommiteaArchivoTrackeadoQueGitignoreIgnoraDespues(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioPrueba(t, map[string]string{
		"registro.txt": "estado inicial\n",
	})
	t.Chdir(dir)

	if err := os.WriteFile(".gitignore", []byte("registro.txt\n"), 0644); err != nil {
		t.Fatalf("no se pudo escribir .gitignore: %v", err)
	}
	ejecutarGit(t, dir, "add", "-f", ".gitignore")
	ejecutarGit(t, dir, "commit", "-q", "-m", "ignora registro.txt")

	agregarLineas(t, "registro.txt", 5)

	plan := &PlanFragmentacion{Lotes: []LotePlanificado{
		{Capa: "backend", Numero: 1, Rutas: []string{"registro.txt"}, Mensaje: "chore(slice): prueba"},
	}}

	resultados, err := EjecutarPlanFragmentacion(plan)
	if err != nil {
		t.Fatalf("EjecutarPlanFragmentacion devolvió error con un archivo trackeado e ignorado: %v", err)
	}
	if len(resultados) != 1 || resultados[0].Hash == "" {
		t.Fatalf("se esperaba 1 commit con hash, obtuve %+v", resultados)
	}
	estado := ejecutarGit(t, dir, "status", "--porcelain")
	if estado != "" {
		t.Errorf("el worktree debería quedar limpio, obtuve: %s", estado)
	}
}

// TestEjecutarPlanFragmentacionErrorIncluyeSalidaDeGit comprueba que un fallo
// real de git ya no se reduce a "exit status 1": debe incluir la salida de
// git (aquí, el aviso de pathspec) para poder diagnosticarlo sin adivinar.
func TestEjecutarPlanFragmentacionErrorIncluyeSalidaDeGit(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioPrueba(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	plan := &PlanFragmentacion{Lotes: []LotePlanificado{
		{Capa: "backend", Numero: 1, Rutas: []string{"no-existe.go"}, Mensaje: "chore(slice): prueba"},
	}}

	_, err := EjecutarPlanFragmentacion(plan)
	if err == nil {
		t.Fatal("se esperaba error: la ruta del lote no existe")
	}
	if !strings.Contains(err.Error(), "pathspec") {
		t.Errorf("el error debería incluir la salida real de git (pathspec), obtuve: %v", err)
	}
}
