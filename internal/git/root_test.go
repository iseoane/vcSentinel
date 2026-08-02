package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEsMismaRuta(t *testing.T) {
	base := filepath.Join("repos", "demo")
	conBarraFinal := base + string(filepath.Separator)
	separadorContrario := filepath.ToSlash(base)

	tests := []struct {
		nombre   string
		a        string
		b        string
		esperado bool
	}{
		{nombre: "idénticas", a: base, b: base, esperado: true},
		{nombre: "con barra final", a: base, b: conBarraFinal, esperado: true},
		{nombre: "con separador contrario", a: base, b: separadorContrario, esperado: true},
		{nombre: "rutas distintas", a: base, b: filepath.Join("repos", "otro"), esperado: false},
	}

	for _, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			obtenido := EsMismaRuta(tt.a, tt.b)
			if obtenido != tt.esperado {
				t.Errorf("EsMismaRuta(%q, %q) = %v, esperado %v", tt.a, tt.b, obtenido, tt.esperado)
			}
		})
	}
}

func TestEsMismaRutaToleranteMayusculas(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("la comparación insensible a mayúsculas solo aplica en Windows")
	}
	if !EsMismaRuta(`C:\Repos\Demo`, `c:\repos\demo`) {
		t.Error("EsMismaRuta debería ignorar mayúsculas en Windows")
	}
}

func TestObtenerRaizWorktreeEnRaiz(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioPrueba(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	raiz, err := ObtenerRaizWorktree()
	if err != nil {
		t.Fatalf("ObtenerRaizWorktree devolvió error: %v", err)
	}
	if !EsMismaRuta(raiz, dir) {
		t.Errorf("ObtenerRaizWorktree() = %q, esperado %q", raiz, dir)
	}
}

func TestObtenerRaizWorktreeDesdeSubdirectorio(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioPrueba(t, map[string]string{"a.go": "package a\n"})
	sub := filepath.Join(dir, "src", "paquete")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatalf("no se pudo crear el subdirectorio: %v", err)
	}
	t.Chdir(sub)

	raiz, err := ObtenerRaizWorktree()
	if err != nil {
		t.Fatalf("ObtenerRaizWorktree devolvió error: %v", err)
	}
	if !EsMismaRuta(raiz, dir) {
		t.Errorf("ObtenerRaizWorktree() = %q, esperado %q", raiz, dir)
	}
}

func TestObtenerRaizWorktreeFueraDeRepositorio(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	t.Chdir(t.TempDir())

	if _, err := ObtenerRaizWorktree(); err == nil {
		t.Error("se esperaba error al consultar la raíz fuera de un repositorio Git")
	}
}
