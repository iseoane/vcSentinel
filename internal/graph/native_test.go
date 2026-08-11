package graph

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
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
