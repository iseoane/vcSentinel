package graph

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/tools/go/packages"
)

func TestProveedorNativoCargaGrafoBasico(t *testing.T) {
	dir := crearModulo(t, map[string]string{
		"go.mod":                         "module example.test/snapshot\n\ngo 1.26\n",
		"internal/git/diff.go":           "package git\n",
		"internal/git/diff_test.go":      "package git\nimport \"testing\"\nfunc TestDiff(t *testing.T) {}\n",
		"internal/review/engine.go":      "package review\nimport _ \"example.test/snapshot/internal/git\"\n",
		"internal/review/engine_test.go": "package review\nimport \"testing\"\nfunc TestEngine(t *testing.T) {}\n",
		"cmd/sentinel/main.go":           "package main\nimport _ \"example.test/snapshot/internal/review\"\nfunc main() {}\n",
		"cmd/sentinel/main_test.go":      "package main\nimport \"testing\"\nfunc TestMain(t *testing.T) {}\n",
	})
	p := proveedorDelModulo(t, dir)
	resultado, err := p.Analizar([]string{"internal/git/diff.go", "internal/git/diff.go"})
	if err != nil || !resultado.Completo() {
		t.Fatalf("análisis = %+v, error = %v, motivo = %q", resultado, err, resultado.MotivoIncompleto())
	}
	alcance := resultado.Alcance()
	if !reflect.DeepEqual(alcance.Paquetes(), []string{
		"example.test/snapshot/cmd/sentinel", "example.test/snapshot/internal/git", "example.test/snapshot/internal/review",
	}) || !reflect.DeepEqual(alcance.Tests(), []string{"./cmd/sentinel", "./internal/git", "./internal/review"}) {
		t.Fatalf("alcance = paquetes %v, tests %v", alcance.Paquetes(), alcance.Tests())
	}
	explicacion := strings.Join(alcance.Explicacion(), "\n")
	for _, parte := range []string{"internal/git/diff.go", "internal/review", "cmd/sentinel", "importa"} {
		if !strings.Contains(explicacion, parte) {
			t.Fatalf("explicación sin %q:\n%s", parte, explicacion)
		}
	}
}

func TestProveedorNativoCacheaCargaPorTreeOID(t *testing.T) {
	dir := crearModulo(t, map[string]string{"go.mod": "module example.test/s\n\ngo 1.26\n", "a/a.go": "package a\n", "b/b.go": "package b\nimport _ \"example.test/s/a\"\n"})
	cargas := 0
	analizar := func(oid, ruta, goos string) ResultadoAnalisis {
		p := NuevoProveedorNativo(dir, oid)
		if goos != "" {
			p.contexto.GOOS = goos
		}
		cargar := p.cargar
		p.cargar = func(config *packages.Config, patrones ...string) ([]*packages.Package, error) {
			cargas++
			return cargar(config, patrones...)
		}
		resultado, _ := p.Analizar([]string{ruta})
		return resultado
	}

	oid := treeOID(t, dir)
	analizar(oid, "a/a.go", "")
	segundo := analizar(oid, "b/b.go", "")
	cargasMismoArbol := cargas
	goos := "windows"
	if contextoCargaActual().GOOS == goos {
		goos = "linux"
	}
	analizar(oid, "a/a.go", goos)
	cargasOtroContexto := cargas
	analizar(oid, "a/a.go", "")
	cargasContextoOriginal := cargas
	os.WriteFile(filepath.Join(dir, "b", "b.go"), []byte("package b\n"), 0644)
	git(t, dir, "commit", "-qam", "segundo árbol")
	analizar(treeOID(t, dir), "b/b.go", "")
	if cargasMismoArbol != 1 || cargasOtroContexto != 2 || cargasContextoOriginal != 2 || cargas != 3 || !reflect.DeepEqual(segundo.Alcance().Paquetes(), []string{"example.test/s/b"}) {
		t.Fatalf("cargas mismo/otro/original/árbol=%d/%d/%d/%d; alcance=%v", cargasMismoArbol, cargasOtroContexto, cargasContextoOriginal, cargas, segundo.Alcance().Paquetes())
	}
}

func TestProveedorNativoCargaVendorHermetico(t *testing.T) {
	dir := crearModulo(t, map[string]string{
		"go.mod":                         "module example.test/s\n\ngo 1.26\n\nrequire example.test/dep v1.0.0\n",
		"main.go":                        "package s\nimport _ \"example.test/dep\"\n",
		"vendor/modules.txt":             "# example.test/dep v1.0.0\n## explicit; go 1.26\nexample.test/dep\n",
		"vendor/example.test/dep/dep.go": "package dep\n",
	})
	p := proveedorDelModulo(t, dir)
	p.contexto.GOPATH, p.contexto.GOMODCACHE, p.contexto.GOCACHE = t.TempDir(), t.TempDir(), t.TempDir()
	resultado, _ := p.Analizar([]string{"main.go"})
	if !resultado.Completo() || !reflect.DeepEqual(p.contexto.BuildFlags, []string{"-mod=vendor", "-tags="}) {
		t.Fatalf("completo=%v flags=%v motivo=%q", resultado.Completo(), p.contexto.BuildFlags, resultado.MotivoIncompleto())
	}
}

func TestProveedorNativoVendorIncompletoUsaReadonly(t *testing.T) {
	for _, caso := range []string{"ausente", "symlink"} {
		t.Run(caso, func(t *testing.T) {
			dir := crearModulo(t, map[string]string{"go.mod": "module example.test/s\n\ngo 1.26\n", "main.go": "package s\n"})
			if err := os.Mkdir(filepath.Join(dir, "vendor"), 0755); err != nil {
				t.Fatal(err)
			}
			if caso == "symlink" {
				if err := os.Symlink(filepath.Join(dir, "go.mod"), filepath.Join(dir, "vendor", "modules.txt")); err != nil {
					t.Skipf("symlinks no disponibles: %v", err)
				}
			}
			p := proveedorDelModulo(t, dir)
			if !reflect.DeepEqual(p.contexto.BuildFlags, []string{"-mod=readonly", "-tags="}) {
				t.Fatalf("flags=%v", p.contexto.BuildFlags)
			}
		})
	}
}

func TestProveedorNativoCacheNoAfectaCompletitud(t *testing.T) {
	dir := crearModulo(t, map[string]string{"go.mod": "module example.test/s\n\ngo 1.26\n", "main.go": "package s\n"})
	graphDir := filepath.Join(dir, ".git", "vas-sentinel", "graph")
	if err := os.MkdirAll(graphDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(graphDir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(graphDir, 0700) })
	resultado, err := proveedorDelModulo(t, dir).Analizar([]string{"main.go"})
	if err != nil || !resultado.Completo() || resultado.MotivoIncompleto() != "" {
		t.Fatalf("completo=%v error=%v motivo=%q", resultado.Completo(), err, resultado.MotivoIncompleto())
	}
}

func TestProveedorNativoCacheEvictaNovenoContexto(t *testing.T) {
	dir := crearModulo(t, map[string]string{"go.mod": "module example.test/s\n\ngo 1.26\n", "main.go": "package s\n"})
	oid := treeOID(t, dir)
	fingerprints := make([]string, 0, maxEntradasCache+1)
	for i := range maxEntradasCache + 1 {
		p := NuevoProveedorNativo(dir, oid)
		p.contexto.GoVersion = fmt.Sprintf("test-version-%d", i)
		fingerprints = append(fingerprints, p.contexto.fingerprint())
		resultado, _ := p.Analizar([]string{"main.go"})
		if !resultado.Completo() {
			t.Fatalf("contexto %d incompleto: %s", i, resultado.MotivoIncompleto())
		}
	}
	cache, ok := NuevoProveedorNativo(dir, oid).leerCacheValida()
	if !ok || len(cache.Entries) != maxEntradasCache {
		t.Fatalf("cache válida=%v entradas=%d", ok, len(cache.Entries))
	}
	sort.Strings(fingerprints[:maxEntradasCache])
	if _, existe := cache.Entries[fingerprints[0]]; existe || cache.Entries[fingerprints[maxEntradasCache]].TreeOID != oid {
		t.Fatalf("evicción no determinista: %#v", cache.Entries)
	}
}

func TestCacheCombinaEscritoresIndependientes(t *testing.T) {
	dir := crearModulo(t, map[string]string{"go.mod": "module example.test/s\n\ngo 1.26\n", "main.go": "package s\n"})
	oid := treeOID(t, dir)
	p1, p2 := NuevoProveedorNativo(dir, oid), NuevoProveedorNativo(dir, oid)
	p1.contexto.Temp, p2.contexto.Temp = "writer-1", "writer-2"
	for _, p := range []*proveedorNativo{p1, p2} {
		p.imports, p.archivos, p.tests = map[string][]string{}, map[string]string{"main.go": "example.test/s"}, map[string]string{}
	}
	var wg sync.WaitGroup
	for _, p := range []*proveedorNativo{p1, p2} {
		wg.Add(1)
		go func() { defer wg.Done(); _ = p.guardarCacheSinMutex([]string{filepath.Join(dir, "main.go")}) }()
	}
	wg.Wait()
	cache, ok := p1.leerCacheValida()
	if !ok || cache.Entries[p1.contexto.fingerprint()].TreeOID != oid || cache.Entries[p2.contexto.fingerprint()].TreeOID != oid {
		t.Fatalf("se perdió un escritor: %#v", cache.Entries)
	}
}

func TestProveedorNativoRecuperaCacheNoConfiable(t *testing.T) {
	dir := crearModulo(t, map[string]string{"go.mod": "module example.test/s\n\ngo 1.26\n", "main.go": "package s\n"})
	oid := treeOID(t, dir)
	if resultado, _ := NuevoProveedorNativo(dir, oid).Analizar([]string{"main.go"}); !resultado.Completo() {
		t.Fatal(resultado.MotivoIncompleto())
	}
	ruta := filepath.Join(dir, ".git", "vas-sentinel", "graph", oid+".json")
	valida, err := os.ReadFile(ruta)
	if err != nil {
		t.Fatal(err)
	}
	casos := []struct {
		nombre   string
		preparar func()
	}{
		{"malformada", func() { os.WriteFile(ruta, []byte("{"), 0600) }},
		{"versión distinta", func() {
			os.WriteFile(ruta, []byte(strings.Replace(string(valida), `"version": 3`, `"version": 1`, 1)), 0600)
		}},
		{"tree distinto", func() {
			os.WriteFile(ruta, []byte(strings.Replace(string(valida), oid, strings.Repeat("0", len(oid)), 1)), 0600)
		}},
		{"fingerprint distinto", func() {
			fingerprint := NuevoProveedorNativo(dir, oid).contexto.fingerprint()
			os.WriteFile(ruta, []byte(strings.Replace(string(valida), `"`+fingerprint+`":`, `"`+strings.Repeat("0", 64)+`":`, 1)), 0600)
		}},
		{"sobredimensionada", func() { os.WriteFile(ruta, make([]byte, maxCacheGrafo+1), 0600) }},
		{"symlink", func() {
			os.Remove(ruta)
			if err := os.Symlink(filepath.Join(t.TempDir(), "destino"), ruta); err != nil {
				t.Skipf("symlinks no disponibles: %v", err)
			}
		}},
	}
	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			caso.preparar()
			p := NuevoProveedorNativo(dir, oid)
			cargas := 0
			cargar := p.cargar
			p.cargar = func(c *packages.Config, patrones ...string) ([]*packages.Package, error) {
				cargas++
				return cargar(c, patrones...)
			}
			resultado, _ := p.Analizar([]string{"main.go"})
			if !resultado.Completo() || cargas != 1 {
				t.Fatalf("completo=%v cargas=%d motivo=%q", resultado.Completo(), cargas, resultado.MotivoIncompleto())
			}
			if caso.nombre == "symlink" {
				return
			}
			valida, err = os.ReadFile(ruta)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProveedorNativoSoloPersisteTrasVerificacion(t *testing.T) {
	dir := crearModulo(t, map[string]string{"go.mod": "module example.test/s\n\ngo 1.26\n", "main.go": "package s\n"})
	oid := treeOID(t, dir)
	p := NuevoProveedorNativo(dir, oid)
	p.verificar = func(s solicitudVerificacion) error {
		if s.fase == verificarConsumidos {
			return errors.New("fallo final")
		}
		return verificarSnapshotGit(s)
	}
	resultado, _ := p.Analizar([]string{"main.go"})
	ruta := filepath.Join(dir, ".git", "vas-sentinel", "graph", oid+".json")
	if resultado.Completo() || !errors.Is(func() error { _, err := os.Stat(ruta); return err }(), os.ErrNotExist) {
		t.Fatalf("resultado=%q cache persistida=%v", resultado.MotivoIncompleto(), ruta)
	}
}

func TestProveedorNativoCacheConcurrenteEsSegura(t *testing.T) {
	dir := crearModulo(t, map[string]string{"go.mod": "module example.test/s\n\ngo 1.26\n", "main.go": "package s\n"})
	oid := treeOID(t, dir)
	var cargas atomic.Int32
	var wg sync.WaitGroup
	errores := make(chan string, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := NuevoProveedorNativo(dir, oid)
			cargar := p.cargar
			p.cargar = func(c *packages.Config, patrones ...string) ([]*packages.Package, error) {
				cargas.Add(1)
				return cargar(c, patrones...)
			}
			resultado, _ := p.Analizar([]string{"main.go"})
			if !resultado.Completo() {
				errores <- resultado.MotivoIncompleto()
			}
		}()
	}
	wg.Wait()
	close(errores)
	for err := range errores {
		t.Error(err)
	}
	if cargas.Load() == 0 {
		t.Fatal("ningún lector construyó la cache")
	}
	p := NuevoProveedorNativo(dir, oid)
	p.cargar = func(*packages.Config, ...string) ([]*packages.Package, error) {
		return nil, errors.New("loader no debe ejecutarse")
	}
	resultado, _ := p.Analizar([]string{"main.go"})
	if !resultado.Completo() {
		t.Fatal(resultado.MotivoIncompleto())
	}
	ruta := filepath.Join(dir, ".git", "vas-sentinel", "graph", oid+".json")
	info, err := os.Lstat(ruta)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("entrada cache no regular: %v %v", info, err)
	}
	var cache cacheGrafo
	datos, _ := os.ReadFile(ruta)
	if json.Unmarshal(datos, &cache) != nil || !cache.valida(oid) || cache.Entries[p.contexto.fingerprint()].TreeOID != oid {
		t.Fatalf("cache final inválida: %s", datos)
	}
}

func TestProveedorNativoFallaCerrado(t *testing.T) {
	casos := []struct {
		nombre, ruta, contenido, motivo string
	}{
		{"error de carga", "bad.go", "package broken\nfunc {", "carga"},
		{"reflect", "main.go", "package sample\nimport \"reflect\"\nvar _ = reflect.TypeOf(1)\n", "reflect"},
		{"plugin", "main.go", "package sample\nimport \"plugin\"\nvar _ = plugin.Open\n", "plugin"},
		{"go linkname", "main.go", "package sample\nimport _ \"unsafe\"\n//go:linkname f x.f\nfunc f()\n", "go:linkname"},
		{"archivo no cubierto", "z.txt", "texto\n", "ningún paquete"},
		{"go mod", "go.mod", "module example.test/changed\n", "configuración global"},
		{"makefile", "Makefile", "all:\n\t@true\n", "configuración global"},
		{"build script", "build.sh", "go build ./...\n", "configuración global"},
		{"ci", filepath.Join(".github", "workflows", "ci.yml"), "name: ci\n", "configuración global"},
		{"sin identidad", "main.go", "package sample\n", "identidad de snapshot"},
	}
	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			archivos := map[string]string{
				"go.mod":  "module example.test/snapshot\n\ngo 1.26\n",
				"base.go": "package sample\n",
			}
			archivos[caso.ruta] = caso.contenido
			dir := crearModulo(t, archivos)
			identidad := "tree-case"
			if caso.nombre == "sin identidad" {
				identidad = ""
			}
			if identidad != "" {
				identidad = treeOID(t, dir)
			}
			resultado, err := NuevoProveedorNativo(dir, identidad).Analizar([]string{caso.ruta})
			if err != nil || resultado.Completo() || !strings.Contains(resultado.MotivoIncompleto(), caso.motivo) {
				t.Fatalf("completo=%v, error=%v, motivo=%q", resultado.Completo(), err, resultado.MotivoIncompleto())
			}
			if !reflect.DeepEqual(resultado.NoCubiertos(), []string{filepath.ToSlash(caso.ruta)}) {
				t.Fatalf("no cubiertos = %v", resultado.NoCubiertos())
			}
		})
	}
}

func TestProveedorNativoVerificaSnapshotAntesDeConfiar(t *testing.T) {
	dir := crearModulo(t, map[string]string{"go.mod": "module example.test/s\n\ngo 1.26\n", "main.go": "package s\n"})
	casos := []struct {
		nombre, oid string
		mutar       func()
	}{
		{"tree distinto", strings.Repeat("0", 40), func() {}},
		{"archivo rastreado mutado", treeOID(t, dir), func() { os.WriteFile(filepath.Join(dir, "main.go"), []byte("package s\n// dirty\n"), 0644) }},
		{"archivo no rastreado", treeOID(t, dir), func() { os.WriteFile(filepath.Join(dir, "extra.txt"), []byte("dirty"), 0644) }},
	}
	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			caso.mutar()
			resultado, _ := NuevoProveedorNativo(dir, caso.oid).Analizar([]string{"main.go"})
			if resultado.Completo() || !strings.Contains(resultado.MotivoIncompleto(), "snapshot") {
				t.Fatalf("snapshot no verificado: completo=%v motivo=%q", resultado.Completo(), resultado.MotivoIncompleto())
			}
			git(t, dir, "reset", "--hard", "-q")
			os.Remove(filepath.Join(dir, "extra.txt"))
		})
	}
}

func TestProveedorNativoAtestaArchivosConsumidos(t *testing.T) {
	casos := []struct {
		nombre string
		mutar  func(*testing.T, string)
	}{
		{"go ignorado", func(t *testing.T, dir string) {
			os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignored.go\n"), 0644)
			git(t, dir, "add", ".gitignore")
			git(t, dir, "commit", "-qm", "ignore")
			os.WriteFile(filepath.Join(dir, "ignored.go"), []byte("package s\n"), 0644)
		}},
		{"rastreado assume unchanged", func(t *testing.T, dir string) {
			os.WriteFile(filepath.Join(dir, "main.go"), []byte("package s\n// alterado\n"), 0644)
			git(t, dir, "update-index", "--assume-unchanged", "main.go")
			if estado := strings.TrimSpace(git(t, dir, "status", "--porcelain")); estado != "" {
				t.Fatalf("el fixture debe ocultar el cambio a status: %q", estado)
			}
		}},
		{"metadata de módulo", func(t *testing.T, dir string) {
			os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module hostile.test/s\n\ngo 1.26\n"), 0644)
			git(t, dir, "update-index", "--assume-unchanged", "go.mod")
		}},
	}
	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			dir := crearModulo(t, map[string]string{"go.mod": "module example.test/s\n\ngo 1.26\n", "main.go": "package s\n"})
			caso.mutar(t, dir)
			resultado, _ := proveedorDelModulo(t, dir).Analizar([]string{"main.go"})
			if resultado.Completo() || !strings.Contains(resultado.MotivoIncompleto(), "blob") {
				t.Fatalf("bytes no atestados: completo=%v motivo=%q", resultado.Completo(), resultado.MotivoIncompleto())
			}
		})
	}
}

func TestProveedorNativoIgnoraGoWorkDesactivado(t *testing.T) {
	dir := crearModulo(t, map[string]string{
		"go.mod":  "module example.test/s\n\ngo 1.26\n",
		"go.work": "go 1.26\n\nuse .\n",
		"main.go": "package s\n",
	})
	oid := treeOID(t, dir)
	os.WriteFile(filepath.Join(dir, "go.work"), []byte("go 1.26\n\nuse ./missing\n"), 0644)
	git(t, dir, "update-index", "--assume-unchanged", "go.work")
	resultado, _ := NuevoProveedorNativo(dir, oid).Analizar([]string{"main.go"})
	if !resultado.Completo() {
		t.Fatalf("go.work consumido pese a GOWORK=off: %q", resultado.MotivoIncompleto())
	}
}

func TestProveedorNativoIgnoraObjetosDeReemplazo(t *testing.T) {
	dir := crearModulo(t, map[string]string{"go.mod": "module example.test/s\n\ngo 1.26\n", "main.go": "package s\n"})
	original := strings.TrimSpace(git(t, dir, "rev-parse", "HEAD"))
	esperado := treeOID(t, dir)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package hostile\n"), 0644)
	git(t, dir, "commit", "-qam", "replacement")
	reemplazo := strings.TrimSpace(git(t, dir, "rev-parse", "HEAD"))
	git(t, dir, "reset", "--hard", "-q", original)
	git(t, dir, "replace", original, reemplazo)
	if actual := treeOID(t, dir); actual == esperado {
		t.Fatal("refs/replace no alteró HEAD^{tree}; el fixture no prueba la defensa")
	}
	resultado, _ := NuevoProveedorNativo(dir, esperado).Analizar([]string{"main.go"})
	if !resultado.Completo() {
		t.Fatalf("refs/replace alteró la atestación: %q", resultado.MotivoIncompleto())
	}
}

func TestProveedorNativoSaneaEntornoGit(t *testing.T) {
	dir := crearModulo(t, map[string]string{"go.mod": "module example.test/s\n\ngo 1.26\n", "main.go": "package s\n"})
	hostil := crearModulo(t, map[string]string{"go.mod": "module hostile.test/s\n\ngo 1.26\n", "main.go": "package s\n"})
	oid := treeOID(t, dir)
	t.Setenv("GIT_DIR", filepath.Join(hostil, ".git"))
	t.Setenv("GIT_WORK_TREE", hostil)
	configHostil := filepath.Join(hostil, "hostile-config")
	os.WriteFile(configHostil, []byte("configuración inválida\n"), 0644)
	t.Setenv("GIT_CONFIG_GLOBAL", configHostil)
	cmd := exec.Command("git", "-C", dir, "rev-parse", "HEAD")
	cmd.Env = os.Environ()
	if err := cmd.Run(); err == nil {
		t.Fatal("la configuración global hostil no altera Git; el fixture no prueba el saneado")
	}
	resultado, _ := NuevoProveedorNativo(dir, oid).Analizar([]string{"main.go"})
	if !resultado.Completo() {
		t.Fatalf("entorno Git redirigió la atestación: %q", resultado.MotivoIncompleto())
	}
}

func TestProveedorNativoRevalidaTrasCarga(t *testing.T) {
	dir := crearModulo(t, map[string]string{
		"go.mod":   "module example.test/s\n\ngo 1.26\n",
		"main.go":  "package s\n",
		"other.go": "package s\n",
	})
	p := proveedorDelModulo(t, dir)
	cargar := p.cargar
	p.cargar = func(config *packages.Config, patrones ...string) ([]*packages.Package, error) {
		paquetes, err := cargar(config, patrones...)
		os.WriteFile(filepath.Join(dir, "main.go"), []byte("package s\n// mutado durante carga\n"), 0644)
		return paquetes, err
	}
	resultado, _ := p.Analizar([]string{"main.go", "other.go"})
	if resultado.Completo() || !reflect.DeepEqual(resultado.NoCubiertos(), []string{"main.go", "other.go"}) {
		t.Fatalf("mutación temporal autorizada: completo=%v no cubiertos=%v motivo=%q", resultado.Completo(), resultado.NoCubiertos(), resultado.MotivoIncompleto())
	}
}

func TestProveedorNativoRechazaEscapePorSymlink(t *testing.T) {
	dir := crearModulo(t, map[string]string{"go.mod": "module example.test/s\n\ngo 1.26\n", "main.go": "package s\n"})
	fuera := filepath.Join(t.TempDir(), "fuera.go")
	os.WriteFile(fuera, []byte("package fuera\n"), 0644)
	enlace := filepath.Join(dir, "escape.go")
	if err := os.Symlink(fuera, enlace); err != nil {
		t.Skipf("symlinks no disponibles: %v", err)
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "symlink")
	resultado, _ := proveedorDelModulo(t, dir).Analizar([]string{"escape.go"})
	if resultado.Completo() || !strings.Contains(resultado.MotivoIncompleto(), "fuera del snapshot") {
		t.Fatalf("escape autorizado: %q", resultado.MotivoIncompleto())
	}
	os.Remove(fuera)
	resultado, _ = proveedorDelModulo(t, dir).Analizar([]string{"escape.go"})
	if resultado.Completo() || !strings.Contains(resultado.MotivoIncompleto(), "no resoluble") {
		t.Fatalf("symlink roto autorizado: %q", resultado.MotivoIncompleto())
	}
}

func TestBuildGoEsFuenteOrdinaria(t *testing.T) {
	dir := crearModulo(t, map[string]string{"go.mod": "module example.test/s\n\ngo 1.26\n", "build.go": "package s\n"})
	enlace := filepath.Join(t.TempDir(), "snapshot")
	if err := os.Symlink(dir, enlace); err != nil {
		t.Skipf("symlinks no disponibles: %v", err)
	}
	resultado, _ := NuevoProveedorNativo(enlace, treeOID(t, dir)).Analizar([]string{"build.go"})
	if !resultado.Completo() {
		t.Fatalf("build.go marcado global: %s", resultado.MotivoIncompleto())
	}
}

func TestCierreInversoConCicloEsDeterminista(t *testing.T) {
	p := &proveedorNativo{imports: map[string][]string{"a": {"b"}, "b": {"a"}}, tests: map[string]string{"a": "./a", "b": "./b"}}
	var primero, segundo alcanceAfectado
	p.expandirAlcance(map[string][]string{"a": {"a.go", "a.go"}}, &primero)
	p.expandirAlcance(map[string][]string{"a": {"a.go", "a.go"}}, &segundo)
	if !reflect.DeepEqual(primero, segundo) || !reflect.DeepEqual(primero.paquetes, []string{"a", "b"}) {
		t.Fatalf("cierre no determinista: %+v / %+v", primero, segundo)
	}
}
func TestCargaNoAutorizaArchivosFueraDelSnapshot(t *testing.T) {
	dir := crearModulo(t, map[string]string{"go.mod": "module example.test/s\n\ngo 1.26\n", "main.go": "package s\n"})
	fuera := filepath.Join(t.TempDir(), "fuera.go")
	os.WriteFile(fuera, []byte("package fuera\n"), 0644)
	p := proveedorDelModulo(t, dir)
	p.cargar = func(*packages.Config, ...string) ([]*packages.Package, error) {
		return []*packages.Package{{PkgPath: "example.test/fuera", GoFiles: []string{fuera}}}, nil
	}
	resultado, _ := p.Analizar([]string{"main.go"})
	if resultado.Completo() || !strings.Contains(resultado.MotivoIncompleto(), "archivo de paquete fuera") {
		t.Fatalf("archivo externo autorizado: %q", resultado.MotivoIncompleto())
	}
	if strings.Contains(resultado.MotivoIncompleto(), fuera) {
		t.Fatalf("motivo expone ruta absoluta consumida: %q", resultado.MotivoIncompleto())
	}
}

func crearModulo(t *testing.T, archivos map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for ruta, contenido := range archivos {
		ruta = filepath.Join(dir, filepath.FromSlash(ruta))
		if err := os.MkdirAll(filepath.Dir(ruta), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ruta, []byte(contenido), 0644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, dir, "init", "-q")
	git(t, dir, "config", "user.email", "fixture@example.test")
	git(t, dir, "config", "user.name", "Fixture")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "fixture")
	return dir
}

func proveedorDelModulo(t *testing.T, dir string) *proveedorNativo {
	t.Helper()
	return NuevoProveedorNativo(dir, treeOID(t, dir))
}

func treeOID(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(git(t, dir, "rev-parse", "HEAD^{tree}"))
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	salida, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, salida)
	}
	return string(salida)
}
