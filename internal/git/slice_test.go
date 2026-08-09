package git

import (
	"os"
	"os/exec"
	"path/filepath"
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
		{nombre: "extension yml reconocida como config", ruta: "config.yml", esperado: "config"},
		{nombre: "codigo go en cmd", ruta: "cmd/main.go", esperado: "backend"},
		{nombre: "test en subcarpeta precede a backend", ruta: "internal/mi_test/helper.go", esperado: "test"},
	}

	for _, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			obtenido := ClasificarCapa(tt.ruta)
			if obtenido != tt.esperado {
				t.Errorf("ClasificarCapa(%q) = %q, esperado %q", tt.ruta, obtenido, tt.esperado)
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

func TestParsearNumstat(t *testing.T) {
	tests := []struct {
		nombre   string
		salida   string
		esperado []ArchivoModificado
	}{
		{
			nombre:   "salida vacia",
			salida:   "",
			esperado: nil,
		},
		{
			nombre: "ruta simple",
			salida: "10\t2\tcmd/main.go\n",
			esperado: []ArchivoModificado{
				{Ruta: "cmd/main.go", Lineas: 10, Capa: "backend"},
			},
		},
		{
			nombre: "ruta con espacios se conserva integra",
			salida: "1\t0\tarchivo con espacios.go\n",
			esperado: []ArchivoModificado{
				{Ruta: "archivo con espacios.go", Lineas: 1, Capa: "backend"},
			},
		},
		{
			// Forma plana de un renombrado. La cadena completa "viejo => nuevo"
			// NO es una ruta válida para "git add"; solo el destino lo es.
			nombre: "renombrado en forma plana devuelve solo el destino",
			salida: "0\t0\tviejo archivo.go => nuevo archivo.go\n",
			esperado: []ArchivoModificado{
				{Ruta: "nuevo archivo.go", Lineas: 0, Capa: "backend"},
			},
		},
		{
			// Forma abreviada con llaves: git sustituye solo el tramo que
			// cambia. Hay que reconstruir la ruta de destino sustituyendo el
			// bloque "{viejo => nuevo}" por su mitad derecha.
			nombre: "renombrado con llaves reconstruye la ruta de destino",
			salida: "0\t0\tdir/{viejo => sub1/nuevo}.go\n",
			esperado: []ArchivoModificado{
				{Ruta: "dir/sub1/nuevo.go", Lineas: 0, Capa: "backend"},
			},
		},
		{
			nombre:   "binario marcado con guion se ignora",
			salida:   "-\t-\timagen.png\n",
			esperado: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			obtenido := parsearNumstat(tt.salida)
			if !reflect.DeepEqual(obtenido, tt.esperado) {
				t.Errorf("parsearNumstat(%q) = %+v, esperado %+v", tt.salida, obtenido, tt.esperado)
			}
		})
	}
}

func TestRutasNoRastreadas(t *testing.T) {
	tests := []struct {
		nombre   string
		salida   string
		esperado []string
	}{
		{
			nombre:   "salida vacia",
			salida:   "",
			esperado: nil,
		},
		{
			nombre:   "un archivo simple",
			salida:   "?? a.go\x00",
			esperado: []string{"a.go"},
		},
		{
			// git status --porcelain -z emite la ruta en crudo, sin comillas
			// ni escapes: la ruta con espacios se conserva íntegra sin
			// necesidad de desentrecomillar nada.
			nombre:   "ruta con espacios se conserva integra",
			salida:   "?? archivo con espacios.go\x00",
			esperado: []string{"archivo con espacios.go"},
		},
		{
			// Con --short (sin -z), esta misma ruta llegaría entrecomillada
			// y con escapes octales para la "ó" (B3 tras el arreglo de B1).
			// Con -z llega en UTF-8 puro, sin comillas ni escapes.
			nombre:   "ruta con tilde llega sin comillas ni escapes octales",
			salida:   "?? configuración.go\x00",
			esperado: []string{"configuración.go"},
		},
		{
			nombre:   "entradas rastreadas se ignoran",
			salida:   " M archivo.go\x00A  otro.go\x00",
			esperado: nil,
		},
		{
			nombre:   "mezcla de rastreados y no rastreados",
			salida:   " M archivo.go\x00?? nuevo.go\x00?? otro con espacios.go\x00",
			esperado: []string{"nuevo.go", "otro con espacios.go"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			obtenido := rutasNoRastreadas(tt.salida)
			if !reflect.DeepEqual(obtenido, tt.esperado) {
				t.Errorf("rutasNoRastreadas(%q) = %+v, esperado %+v", tt.salida, obtenido, tt.esperado)
			}
		})
	}
}

func TestCheckDiffLimitsConArchivoNoRastreadoConEspacios(t *testing.T) {
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

	// Reproduce la regresión: con --short (sin -z), "archivo con espacios.go"
	// llega entrecomillado; campos[1] de strings.Fields queda en `"archivo`,
	// contarLineasFisicas no puede abrirlo y CheckDiffLimits devuelve ERROR
	// en vez de medir el volumen. Eso bloquearía el hook pre-commit entero.
	contenido := strings.Repeat("// linea generada\n", 450)
	if err := os.WriteFile(filepath.Join(dir, "archivo con espacios.go"), []byte(contenido), 0644); err != nil {
		t.Fatal(err)
	}

	lineas, estado, err := CheckDiffLimits()
	if err != nil {
		t.Fatalf("CheckDiffLimits devolvió error: %v", err)
	}
	if lineas != 450 {
		t.Errorf("líneas = %d, esperado 450", lineas)
	}
	if estado != "CRITICO" {
		t.Errorf("estado = %q, esperado CRITICO", estado)
	}
}

func TestObtenerArchivosModificadosConNoRastreadoConTilde(t *testing.T) {
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

	// Con --short (sin -z), git emite esta ruta con escapes octales para la
	// "ó" (p. ej. \303\263), no solo entrecomillada. La verificación es que
	// la ruta devuelta exista de verdad en disco, no que "parezca" bien.
	if err := os.WriteFile(filepath.Join(dir, "configuración.go"), []byte("package a\n\nfunc Hola() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	archivos, err := ObtenerArchivosModificados()
	if err != nil {
		t.Fatalf("ObtenerArchivosModificados devolvió error: %v", err)
	}
	if len(archivos) != 1 {
		t.Fatalf("se esperaba 1 archivo, obtuve %d: %+v", len(archivos), archivos)
	}
	if _, err := os.Stat(filepath.Join(dir, archivos[0].Ruta)); err != nil {
		t.Errorf("la ruta devuelta %q no existe en disco: %v", archivos[0].Ruta, err)
	}
}

func TestObtenerArchivosModificadosConRutaConEspacios(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioPrueba(t, map[string]string{
		"archivo con espacios.go": "package a\n",
	})
	t.Chdir(dir)

	// Reproduce B3: strings.Fields partía la ruta con espacios y solo
	// conservaba el primer fragmento.
	agregarLineas(t, "archivo con espacios.go", 5)

	archivos, err := ObtenerArchivosModificados()
	if err != nil {
		t.Fatalf("ObtenerArchivosModificados devolvió error: %v", err)
	}
	if len(archivos) != 1 {
		t.Fatalf("se esperaba 1 archivo, obtuve %d: %+v", len(archivos), archivos)
	}
	if archivos[0].Ruta != "archivo con espacios.go" {
		t.Errorf("ruta = %q, esperado %q", archivos[0].Ruta, "archivo con espacios.go")
	}
}

// TestObtenerArchivosModificadosConRenombradoEsRutaUtilizablePorGit reproduce
// un renombrado real (git mv) con espacios y comprueba que la ruta devuelta
// no es la cadena cruda del numstat ("viejo => nuevo"), sino una ruta que
// existe de verdad en el worktree y que "git add" acepta. Antes del arreglo,
// slice pasaba la cadena cruda a "git add" y fallaba con exit 128 justo al
// intentar desbloquear el guardián.
func TestObtenerArchivosModificadosConRenombradoEsRutaUtilizablePorGit(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioPrueba(t, map[string]string{
		"archivo con espacios.go": "package a\n",
	})
	t.Chdir(dir)

	ejecutarGit(t, dir, "mv", "archivo con espacios.go", "renombrado con espacios.go")
	ejecutarGit(t, dir, "add", "-A")

	archivos, err := ObtenerArchivosModificados()
	if err != nil {
		t.Fatalf("ObtenerArchivosModificados devolvió error: %v", err)
	}
	if len(archivos) != 1 {
		t.Fatalf("se esperaba 1 archivo renombrado, obtuve %d: %+v", len(archivos), archivos)
	}
	ruta := archivos[0].Ruta

	// La ruta debe existir de verdad en el worktree...
	if _, err := os.Stat(filepath.Join(dir, ruta)); err != nil {
		t.Errorf("la ruta devuelta %q no existe en disco: %v", ruta, err)
	}
	// ...y "git add" debe aceptarla sin fallar (es justo lo que hace slice).
	cmd := exec.Command("git", "-C", dir, "add", "--", ruta)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("git add %q falló: %v\n%s", ruta, err, out)
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
