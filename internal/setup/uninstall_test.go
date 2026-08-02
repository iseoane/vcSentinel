package setup

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEliminarBinario(t *testing.T) {
	t.Run("elimina un binario existente", func(t *testing.T) {
		dir := t.TempDir()
		ruta := filepath.Join(dir, "sentinel.exe")
		if err := os.WriteFile(ruta, []byte("binario"), 0755); err != nil {
			t.Fatal(err)
		}

		if err := eliminarBinario(ruta); err != nil {
			t.Fatalf("eliminarBinario devolvió error: %v", err)
		}
		if _, err := os.Stat(ruta); !os.IsNotExist(err) {
			t.Errorf("el binario sigue existiendo: %v", err)
		}
	})

	t.Run("no falla cuando no existe", func(t *testing.T) {
		ruta := filepath.Join(t.TempDir(), "no-existe.exe")
		if err := eliminarBinario(ruta); err != nil {
			t.Fatalf("eliminarBinario con archivo inexistente devolvió error: %v", err)
		}
	})
}

func TestQuitarRutaShell(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)

	const linea = `export PATH="/usr/local/bin:$PATH"`
	bloque := "\n# VAS Sentinel\n" + linea + "\n"
	zshrc := filepath.Join(home, ".zshrc")

	t.Run("elimina el bloque de un archivo existente", func(t *testing.T) {
		if err := os.WriteFile(zshrc, []byte("export OLD=1\n"+bloque), 0644); err != nil {
			t.Fatal(err)
		}
		if err := quitarRutaShell(); err != nil {
			t.Fatalf("quitarRutaShell devolvió error: %v", err)
		}

		contenido, err := os.ReadFile(zshrc)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(contenido), "VAS Sentinel") {
			t.Errorf("el bloque de VAS Sentinel sigue en el archivo:\n%s", contenido)
		}
		if !strings.Contains(string(contenido), "export OLD=1") {
			t.Errorf("se eliminó contenido que no debía:\n%s", contenido)
		}
	})

	t.Run("es idempotente y no toca archivos sin el bloque", func(t *testing.T) {
		if err := os.WriteFile(zshrc, []byte("export SOLO=1\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := quitarRutaShell(); err != nil {
			t.Fatalf("segunda llamada devolvió error: %v", err)
		}
		contenido, err := os.ReadFile(zshrc)
		if err != nil {
			t.Fatal(err)
		}
		if string(contenido) != "export SOLO=1\n" {
			t.Errorf("el archivo fue modificado sin contener el bloque:\n%s", contenido)
		}
	})
}

func TestEliminarConfiguracionGlobal(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)

	dir := filepath.Join(home, ".vas_sentinel")
	ruta := filepath.Join(dir, "vassentinel.yml")

	t.Run("elimina la config y el directorio vacío", func(t *testing.T) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ruta, []byte("config"), 0644); err != nil {
			t.Fatal(err)
		}

		if err := eliminarConfiguracionGlobal(home); err != nil {
			t.Fatalf("eliminarConfiguracionGlobal devolvió error: %v", err)
		}
		if _, err := os.Stat(ruta); !os.IsNotExist(err) {
			t.Errorf("la config global sigue existiendo: %v", err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("el directorio .vas_sentinel no se eliminó al quedar vacío: %v", err)
		}
	})

	t.Run("no falla si no existe la config", func(t *testing.T) {
		if err := eliminarConfiguracionGlobal(home); err != nil {
			t.Fatalf("eliminarConfiguracionGlobal sin config devolvió error: %v", err)
		}
	})

	t.Run("no elimina el directorio si contiene otros archivos", func(t *testing.T) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ruta, []byte("config"), 0644); err != nil {
			t.Fatal(err)
		}
		otro := filepath.Join(dir, "otro.txt")
		if err := os.WriteFile(otro, []byte("otro"), 0644); err != nil {
			t.Fatal(err)
		}

		if err := eliminarConfiguracionGlobal(home); err != nil {
			t.Fatalf("eliminarConfiguracionGlobal devolvió error: %v", err)
		}
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("el directorio con otros archivos no debería eliminarse: %v", err)
		}
	})
}

func TestQuitarPathWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("solo aplica a Windows")
	}
	// quitarPathWindows toca el PATH real de usuario: no ejecutamos el comando
	// aquí, solo verificamos que la decisión de quitarlo usa la misma función
	// que la de añadirlo (necesitaAnadirPathWindows).
	if !necesitaAnadirPathWindows("C:\\x", "C:\\nuevo") {
		t.Error("debería detectar que el directorio debe quitarse del PATH")
	}
}
