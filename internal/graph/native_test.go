package graph

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestProveedorNativoCargaGrafoBasico(t *testing.T) {
	dir := crearModulo(t, map[string]string{
		"go.mod":               "module example.test/snapshot\n\ngo 1.26\n",
		"internal/b/b.go":      "package b\n",
		"internal/a/a.go":      "package a\nimport _ \"example.test/snapshot/internal/b\"\n",
		"internal/a/a_test.go": "package a\nimport \"testing\"\nfunc TestA(t *testing.T) {}\n",
	})
	p := NuevoProveedorNativo(dir, "tree-123")
	resultado, err := p.Analizar([]string{"internal/a/a.go"})
	if err != nil || !resultado.Completo() {
		t.Fatalf("análisis = %+v, error = %v, motivo = %q", resultado, err, resultado.MotivoIncompleto())
	}
	if resultado.IdentidadSnapshot() != "tree-123" ||
		!reflect.DeepEqual(p.imports["example.test/snapshot/internal/a"], []string{"example.test/snapshot/internal/b"}) {
		t.Fatalf("identidad/imports = %q / %v", resultado.IdentidadSnapshot(), p.imports)
	}
	alcance := resultado.Alcance()
	if !reflect.DeepEqual(alcance.Paquetes(), []string{"example.test/snapshot/internal/a"}) ||
		!reflect.DeepEqual(alcance.Tests(), []string{"./internal/a"}) {
		t.Fatalf("alcance = paquetes %v, tests %v", alcance.Paquetes(), alcance.Tests())
	}
}

func TestProveedorNativoCargaImportsDelRepositorio(t *testing.T) {
	p := NuevoProveedorNativo(filepath.Join("..", ".."), "tree-repo")
	resultado, err := p.Analizar([]string{"internal/review/engine.go"})
	if err != nil || !resultado.Completo() {
		t.Fatalf("repositorio incompleto: %v (%s)", err, resultado.MotivoIncompleto())
	}
	imports := p.imports["github.com/ISeoane-Quental/vas.sentinel/internal/review"]
	if !contiene(imports, "github.com/ISeoane-Quental/vas.sentinel/internal/git") {
		t.Fatalf("internal/review imports = %v", imports)
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
		{"build config", "build.sh", "go build ./...\n", "configuración global"},
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
	return dir
}

func contiene(valores []string, buscado string) bool {
	for _, valor := range valores {
		if valor == buscado {
			return true
		}
	}
	return false
}
