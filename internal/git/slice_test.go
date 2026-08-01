package git

import (
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
)

func TestClasificarCapa(t *testing.T) {
	tests := []struct {
		nombre   string
		ruta     string
		esperado string
	}{
		{nombre: "archivo de test en subcarpeta", ruta: "internal/git/slice_test.go", esperado: "test"},
		{nombre: "documento spec en base", ruta: "docs/spec.md", esperado: "test"},
		{nombre: "ruta frontend explicita", ruta: "web/frontend/app.tsx", esperado: "frontend"},
		{nombre: "extension tsx", ruta: "web/app.tsx", esperado: "frontend"},
		{nombre: "extension jsx", ruta: "app.jsx", esperado: "frontend"},
		{nombre: "extension css", ruta: "web/app.css", esperado: "frontend"},
		{nombre: "extension scss", ruta: "web/app.scss", esperado: "frontend"},
		{nombre: "archivo lock", ruta: "package-lock.json", esperado: "config"},
		{nombre: "archivo sum", ruta: "go.sum", esperado: "config"},
		{nombre: "extension yaml", ruta: "config.yaml", esperado: "config"},
		{nombre: "extension json", ruta: "config.json", esperado: "config"},
		{nombre: "extension toml", ruta: "config.toml", esperado: "config"},
		{nombre: "requirements.txt por nombre", ruta: "requirements.txt", esperado: "config"},
		{nombre: "extension yml todavia no reconocida", ruta: "config.yml", esperado: "backend"},
		{nombre: "codigo go en cmd", ruta: "cmd/main.go", esperado: "backend"},
		{nombre: "test en subcarpeta precede a backend", ruta: "internal/mi_test/helper.go", esperado: "test"},
	}

	for _, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			obtenido := clasificarCapa(tt.ruta)
			if obtenido != tt.esperado {
				t.Errorf("clasificarCapa(%q) = %q, esperado %q", tt.ruta, obtenido, tt.esperado)
			}
		})
	}
}

func TestConstruirLotes(t *testing.T) {
	archivo := func(ruta string, lineas int) ArchivoModificado {
		return ArchivoModificado{Ruta: ruta, Lineas: lineas}
	}
	aplanar := func(lotes [][]ArchivoModificado) [][]string {
		var obtenido [][]string
		for _, lote := range lotes {
			rutas := make([]string, 0, len(lote))
			for _, f := range lote {
				rutas = append(rutas, f.Ruta)
			}
			obtenido = append(obtenido, rutas)
		}
		return obtenido
	}

	tests := []struct {
		nombre   string
		archivos []ArchivoModificado
		esperado [][]string
	}{
		{
			nombre:   "sin archivos genera cero lotes",
			archivos: nil,
			esperado: nil,
		},
		{
			nombre:   "un solo archivo dentro del limite",
			archivos: []ArchivoModificado{archivo("a.go", 300)},
			esperado: [][]string{{"a.go"}},
		},
		{
			nombre:   "un solo archivo que supera el limite se mantiene completo",
			archivos: []ArchivoModificado{archivo("a.go", 450)},
			esperado: [][]string{{"a.go"}},
		},
		{
			nombre:   "tres archivos de 200 generan dos lotes",
			archivos: []ArchivoModificado{archivo("a.go", 200), archivo("b.go", 200), archivo("c.go", 200)},
			esperado: [][]string{{"a.go", "b.go"}, {"c.go"}},
		},
		{
			nombre:   "100 350 50 cortan entre el primero y el segundo",
			archivos: []ArchivoModificado{archivo("a.go", 100), archivo("b.go", 350), archivo("c.go", 50)},
			esperado: [][]string{{"a.go"}, {"b.go", "c.go"}},
		},
		{
			nombre:   "200 450 cortan y el grande queda solo",
			archivos: []ArchivoModificado{archivo("a.go", 200), archivo("b.go", 450)},
			esperado: [][]string{{"a.go"}, {"b.go"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			obtenido := aplanar(construirLotes(tt.archivos))
			if !reflect.DeepEqual(obtenido, tt.esperado) {
				t.Errorf("construirLotes() = %v, esperado %v", obtenido, tt.esperado)
			}
		})
	}
}

func TestConstruirSecuenciaLotesRespetaOrdenDeCapas(t *testing.T) {
	porCapas := map[string][]ArchivoModificado{
		"test":     {{Ruta: "internal/git/slice_test.go", Lineas: 10}},
		"backend":  {{Ruta: "cmd/main.go", Lineas: 10}},
		"config":   {{Ruta: "config.yaml", Lineas: 10}},
		"frontend": {{Ruta: "web/app.tsx", Lineas: 10}},
	}

	secuencia := construirSecuenciaLotes(porCapas)
	capasObtenidas := make([]string, 0, len(secuencia))
	for _, lote := range secuencia {
		capasObtenidas = append(capasObtenidas, lote.Capa)
	}
	esperado := []string{"config", "backend", "frontend", "test"}
	if !reflect.DeepEqual(capasObtenidas, esperado) {
		t.Errorf("orden de capas = %v, esperado %v", capasObtenidas, esperado)
	}
}

func TestConstruirSecuenciaLotesRespetaAgrupacionYLimites(t *testing.T) {
	porCapas := map[string][]ArchivoModificado{
		"config": {
			{Ruta: "config.yaml", Lineas: 300},
			{Ruta: "config2.yaml", Lineas: 300},
		},
		"backend": {
			{Ruta: "cmd/main.go", Lineas: 10},
		},
	}

	secuencia := construirSecuenciaLotes(porCapas)
	if len(secuencia) != 3 {
		t.Fatalf("se esperaban 3 lotes, obtuve %d", len(secuencia))
	}
	esperado := []loteConCapa{
		{Capa: "config", Rutas: []string{"config.yaml"}},
		{Capa: "config", Rutas: []string{"config2.yaml"}},
		{Capa: "backend", Rutas: []string{"cmd/main.go"}},
	}
	if !reflect.DeepEqual(secuencia, esperado) {
		t.Errorf("secuencia = %+v, esperado %+v", secuencia, esperado)
	}
}

type adaptadorPrueba struct{}

func (adaptadorPrueba) ObtenerMensajeCommit(rutasArchivos []string, capa string, batchNum int) (string, error) {
	return "chore(slice): prueba", nil
}

func TestObtenerArchivosModificadosEnRepositorioReal(t *testing.T) {
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

	agregarLineas(t, "a.go", 300)
	if err := os.WriteFile("b.go", []byte("package b\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ejecutarGit(t, dir, "add", "b.go")

	archivos, err := ObtenerArchivosModificados()
	if err != nil {
		t.Fatalf("ObtenerArchivosModificados devolvió error: %v", err)
	}
	if len(archivos) != 2 {
		t.Fatalf("se esperaban 2 archivos, obtuve %d: %+v", len(archivos), archivos)
	}

	var a, b *ArchivoModificado
	for i := range archivos {
		if archivos[i].Ruta == "a.go" {
			a = &archivos[i]
		}
		if archivos[i].Ruta == "b.go" {
			b = &archivos[i]
		}
	}
	if a == nil || b == nil {
		t.Fatalf("no se encontraron a.go y b.go: %+v", archivos)
	}
	if a.Lineas != 300 {
		t.Errorf("a.go líneas = %d, esperado 300", a.Lineas)
	}
	if a.Capa != "backend" || b.Capa != "backend" {
		t.Errorf("capas esperadas backend, obtuve a=%q b=%q", a.Capa, b.Capa)
	}
}

func TestFragmentarYCommitearEnRepositorioReal(t *testing.T) {
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

	if err := FragmentarYCommitear(archivos, adaptadorPrueba{}); err != nil {
		t.Fatalf("FragmentarYCommitear devolvió error: %v", err)
	}

	totalCommits := ejecutarGit(t, dir, "rev-list", "--count", "HEAD")
	if totalCommits != "3" {
		t.Errorf("se esperaban 3 commits (inicial + 2 lotes), obtuve %s", totalCommits)
	}

	estado := ejecutarGit(t, dir, "status", "--porcelain")
	if estado != "" {
		t.Errorf("el worktree debería quedar limpio tras fragmentar, obtuve: %s", estado)
	}
}

func TestFragmentarYCommitearUsaFallbackCuandoAdapterFalla(t *testing.T) {
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

	archivos := []ArchivoModificado{
		{Ruta: "b.go", Lineas: 10, Capa: "backend"},
	}
	if err := os.WriteFile("b.go", []byte("package b\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ejecutarGit(t, dir, "add", "b.go")

	adapterFallido := agentadapterFunc(func(rutas []string, capa string, num int) (string, error) {
		return "", os.ErrPermission
	})

	if err := FragmentarYCommitear(archivos, adapterFallido); err != nil {
		t.Fatalf("FragmentarYCommitear debería usar el mensaje de respaldo, devolvió error: %v", err)
	}

	mensaje := ejecutarGit(t, dir, "log", "-1", "--pretty=%s")
	if !strings.Contains(mensaje, "auto-fragmented") {
		t.Errorf("el commit debería usar el mensaje de respaldo, obtuve: %q", mensaje)
	}
}

type agentadapterFunc func(rutasArchivos []string, capa string, batchNum int) (string, error)

func (f agentadapterFunc) ObtenerMensajeCommit(rutasArchivos []string, capa string, batchNum int) (string, error) {
	return f(rutasArchivos, capa, batchNum)
}

var _ agentadapter.AgentAdapter = agentadapterFunc(nil)

func TestEsConfigGigante(t *testing.T) {
	tests := []struct {
		nombre   string
		archivo  ArchivoModificado
		esperado bool
	}{
		{nombre: "config dentro del limite", archivo: ArchivoModificado{Ruta: "config.yaml", Lineas: 400, Capa: "config"}, esperado: false},
		{nombre: "config sobre el limite", archivo: ArchivoModificado{Ruta: "config.yaml", Lineas: 401, Capa: "config"}, esperado: true},
		{nombre: "backend sobre 400 no es config gigante", archivo: ArchivoModificado{Ruta: "a.go", Lineas: 450, Capa: "backend"}, esperado: false},
	}

	for _, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			if obtenido := esConfigGigante(tt.archivo); obtenido != tt.esperado {
				t.Errorf("esConfigGigante(%+v) = %v, esperado %v", tt.archivo, obtenido, tt.esperado)
			}
		})
	}
}

func TestEsCodigoGigante(t *testing.T) {
	tests := []struct {
		nombre   string
		archivo  ArchivoModificado
		esperado bool
	}{
		{nombre: "codigo dentro del limite", archivo: ArchivoModificado{Ruta: "a.go", Lineas: 500, Capa: "backend"}, esperado: false},
		{nombre: "codigo sobre el limite", archivo: ArchivoModificado{Ruta: "a.go", Lineas: 501, Capa: "backend"}, esperado: true},
		{nombre: "config sobre 500 no es codigo gigante", archivo: ArchivoModificado{Ruta: "config.yaml", Lineas: 600, Capa: "config"}, esperado: false},
	}

	for _, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			if obtenido := esCodigoGigante(tt.archivo); obtenido != tt.esperado {
				t.Errorf("esCodigoGigante(%+v) = %v, esperado %v", tt.archivo, obtenido, tt.esperado)
			}
		})
	}
}

func TestGestionarArchivosGigantesDevuelveRestantes(t *testing.T) {
	porCapas := map[string][]ArchivoModificado{
		"config":  {{Ruta: "config.yaml", Lineas: 100, Capa: "config"}},
		"backend": {{Ruta: "a.go", Lineas: 50, Capa: "backend"}},
	}

	restantes, err := gestionarArchivosGigantes(porCapas, func(ArchivoModificado) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("gestionarArchivosGigantes devolvió error: %v", err)
	}
	if !reflect.DeepEqual(restantes["config"], porCapas["config"]) {
		t.Errorf("config restante = %+v, esperado %+v", restantes["config"], porCapas["config"])
	}
	if !reflect.DeepEqual(restantes["backend"], porCapas["backend"]) {
		t.Errorf("backend restante = %+v, esperado %+v", restantes["backend"], porCapas["backend"])
	}
}

func TestObtenerArchivosModificadosIncluyeNoRastreados(t *testing.T) {
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

	// Archivo no rastreado: no se hace git add, debe detectarse igual.
	contenido := "package nuevo\n\nfunc Hola() {}\n"
	if err := os.WriteFile("nuevo.go", []byte(contenido), 0644); err != nil {
		t.Fatal(err)
	}

	archivos, err := ObtenerArchivosModificados()
	if err != nil {
		t.Fatalf("ObtenerArchivosModificados devolvió error: %v", err)
	}
	if len(archivos) != 1 {
		t.Fatalf("se esperaba 1 archivo no rastreado, obtuve %d: %+v", len(archivos), archivos)
	}
	if archivos[0].Ruta != "nuevo.go" {
		t.Errorf("ruta = %q, esperado nuevo.go", archivos[0].Ruta)
	}
	if archivos[0].Lineas != 3 {
		t.Errorf("líneas = %d, esperado 3", archivos[0].Lineas)
	}
	if archivos[0].Capa != "backend" {
		t.Errorf("capa = %q, esperado backend", archivos[0].Capa)
	}
}

func TestFragmentarYCommitearAislaConfigGigante(t *testing.T) {
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

	// Lock grande (>400 líneas) que queda sin rastrear.
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

	if err := FragmentarYCommitear(archivos, adaptadorPrueba{}); err != nil {
		t.Fatalf("FragmentarYCommitear devolvió error: %v", err)
	}

	mensaje := ejecutarGit(t, dir, "log", "-1", "--pretty=%s")
	if mensaje != "chore(deps): track lock and auto-generated files" {
		t.Errorf("mensaje = %q, esperado chore(deps)", mensaje)
	}
	estado := ejecutarGit(t, dir, "status", "--porcelain")
	if estado != "" {
		t.Errorf("el worktree debería quedar limpio tras aislar el lock, obtuve: %s", estado)
	}
}

func TestGestionarArchivosGigantesAislaCodigoConfirmado(t *testing.T) {
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

	porCapas := map[string][]ArchivoModificado{
		"backend": {{Ruta: "big.go", Lineas: 600, Capa: "backend"}},
	}

	restantes, err := gestionarArchivosGigantes(porCapas, func(ArchivoModificado) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("gestionarArchivosGigantes devolvió error: %v", err)
	}
	if len(restantes["backend"]) != 0 {
		t.Errorf("big.go no debería quedar entre los restantes: %+v", restantes["backend"])
	}

	mensaje := ejecutarGit(t, dir, "log", "-1", "--pretty=%s")
	if mensaje != "chore(slice): bypass IA for massive file big.go" {
		t.Errorf("mensaje = %q, esperado bypass", mensaje)
	}
}

func TestGestionarArchivosGigantesAbortaSinConfirmacion(t *testing.T) {
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

	porCapas := map[string][]ArchivoModificado{
		"backend": {{Ruta: "big.go", Lineas: 600, Capa: "backend"}},
	}

	_, err := gestionarArchivosGigantes(porCapas, func(ArchivoModificado) (bool, error) { return false, nil })
	if err == nil {
		t.Fatal("se esperaba error al abortar la fragmentación")
	}
	if !strings.Contains(err.Error(), "abortada") {
		t.Errorf("error = %q, esperado mención de aborto", err)
	}

	estado := ejecutarGit(t, dir, "status", "--porcelain")
	if !strings.Contains(estado, "big.go") {
		t.Errorf("big.go debería seguir pendiente tras abortar, obtuve: %q", estado)
	}
}

type adaptadorConDiffPrueba struct {
	diffRecibido string
}

func (a *adaptadorConDiffPrueba) ObtenerMensajeCommit(rutasArchivos []string, capa string, batchNum int) (string, error) {
	return "chore(slice): base", nil
}

func (a *adaptadorConDiffPrueba) ObtenerMensajeCommitConDiff(rutasArchivos []string, capa string, batchNum int, diff string) (string, error) {
	a.diffRecibido = diff
	return "chore(slice): con diff", nil
}

var _ agentadapter.AdapterConDiff = (*adaptadorConDiffPrueba)(nil)

func TestConsolidarCommitLocalEntregaDiffAlAdaptador(t *testing.T) {
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

	adapter := &adaptadorConDiffPrueba{}
	if err := FragmentarYCommitear(archivos, adapter); err != nil {
		t.Fatalf("FragmentarYCommitear devolvió error: %v", err)
	}

	if !strings.Contains(adapter.diffRecibido, "+// linea generada") {
		t.Errorf("el diff staged debería contener la línea añadida, obtuve: %q", adapter.diffRecibido)
	}
	mensaje := ejecutarGit(t, dir, "log", "-1", "--pretty=%s")
	if mensaje != "chore(slice): con diff" {
		t.Errorf("mensaje = %q, esperado el mensaje del adaptador con diff", mensaje)
	}
}
