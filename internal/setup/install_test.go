package setup

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func setHome(t *testing.T, home string) {
	t.Helper()
	// os.UserHomeDir() usa HOME en Unix y USERPROFILE en Windows.
	claves := []string{"HOME"}
	if runtime.GOOS == "windows" {
		claves = append(claves, "USERPROFILE")
	}
	originales := make(map[string]string, len(claves))
	for _, clave := range claves {
		originales[clave] = os.Getenv(clave)
	}
	for _, clave := range claves {
		if err := os.Setenv(clave, home); err != nil {
			t.Fatalf("no se pudo fijar %s: %v", clave, err)
		}
	}
	t.Cleanup(func() {
		for clave, valor := range originales {
			os.Setenv(clave, valor)
		}
	})
}

func TestCrearConfiguracionGlobal(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)

	if err := crearConfiguracionGlobal(); err != nil {
		t.Fatalf("crearConfiguracionGlobal devolvió error: %v", err)
	}
	ruta := filepath.Join(home, ".vas_sentinel", "vassentinel.yml")
	contenido, err := os.ReadFile(ruta)
	if err != nil {
		t.Fatalf("no se creó la config global en %s: %v", ruta, err)
	}
	if len(contenido) == 0 {
		t.Error("la config global quedó vacía")
	}

	t.Run("no sobreescribe una config existente", func(t *testing.T) {
		previo := "contenido-previo\n"
		if err := os.WriteFile(ruta, []byte(previo), 0644); err != nil {
			t.Fatal(err)
		}
		if err := crearConfiguracionGlobal(); err != nil {
			t.Fatalf("segunda llamada devolvió error: %v", err)
		}
		despues, err := os.ReadFile(ruta)
		if err != nil {
			t.Fatal(err)
		}
		if string(despues) != previo {
			t.Errorf("la config global fue sobreescrita: %q, esperado %q", despues, previo)
		}
	})
}

func TestCrearConfiguracionPerProyecto(t *testing.T) {
	worktree := t.TempDir()

	if err := CrearConfiguracionPerProyecto(worktree); err != nil {
		t.Fatalf("CrearConfiguracionPerProyecto devolvió error: %v", err)
	}
	ruta := filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml")
	contenido, err := os.ReadFile(ruta)
	if err != nil {
		t.Fatalf("no se creó la config per-proyecto en %s: %v", ruta, err)
	}
	if len(contenido) == 0 {
		t.Error("la config per-proyecto quedó vacía")
	}

	t.Run("no sobreescribe una config existente", func(t *testing.T) {
		previo := "contenido-previo\n"
		if err := os.WriteFile(ruta, []byte(previo), 0644); err != nil {
			t.Fatal(err)
		}
		if err := CrearConfiguracionPerProyecto(worktree); err != nil {
			t.Fatal(err)
		}
		despues, err := os.ReadFile(ruta)
		if err != nil {
			t.Fatal(err)
		}
		if string(despues) != previo {
			t.Errorf("la config per-proyecto fue sobreescrita: %q, esperado %q", despues, previo)
		}
	})
}

func TestPlantillaSolicitaDiffExternoDesactivadoPorDefecto(t *testing.T) {
	if !strings.Contains(archivoConfiguracionPerProyectoBase, "request_external_agent_diff: false") {
		t.Fatal("la plantilla debe declarar la solicitud externa en false")
	}
	if strings.Contains(archivoConfiguracionPerProyectoBase, "allow_external_agent_diff") {
		t.Fatal("la plantilla conserva el antiguo nombre ambiguo de consentimiento")
	}
}

// TestPlantillaPerProyectoDocumentaVerificacionDeterminista: la plantilla que
// init escribe debe mostrar al usuario (comentadas) las claves que activan la
// verificación determinista sin agente — lint_commands, test_commands y
// build_commands — con su efecto y un ejemplo ejecutable.
func TestPlantillaPerProyectoDocumentaVerificacionDeterminista(t *testing.T) {
	contenido := archivoConfiguracionPerProyectoBase
	for _, clave := range []string{"lint_commands", "test_commands", "build_commands"} {
		if !strings.Contains(contenido, clave) {
			t.Errorf("la plantilla per-proyecto no documenta %q", clave)
		}
	}
	if !strings.Contains(contenido, "Verificación determinista SIN agente") {
		t.Error("la plantilla debe explicar que estas claves activan la verificación determinista")
	}
	if !strings.Contains(contenido, `go test ./...`) {
		t.Error("la plantilla debe mostrar un ejemplo de test_commands")
	}
	if !strings.Contains(contenido, `go build ./...`) {
		t.Error("la plantilla debe mostrar un ejemplo de build_commands")
	}
	if !strings.Contains(contenido, `supports_scope: true`) || !strings.Contains(contenido, `scoped_command: "go test {packages}"`) {
		t.Error("la plantilla debe documentar el scope real de unit_test")
	}
}

func TestMoverYReemplazar(t *testing.T) {
	dir := t.TempDir()
	origen := filepath.Join(dir, "origen")
	destino := filepath.Join(dir, "destino")

	if err := os.WriteFile(origen, []byte("contenido-nuevo"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destino, []byte("contenido-viejo"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := moverYReemplazar(origen, destino); err != nil {
		t.Fatalf("moverYReemplazar devolvió error: %v", err)
	}

	contenido, err := os.ReadFile(destino)
	if err != nil {
		t.Fatal(err)
	}
	if string(contenido) != "contenido-nuevo" {
		t.Errorf("el destino no fue reemplazado: %q", contenido)
	}
}

func TestAnadirRutaShell(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)

	if err := anadirRutaShell(); err != nil {
		t.Fatalf("anadirRutaShell devolvió error: %v", err)
	}

	zshrc := filepath.Join(home, ".zshrc")
	contenido, err := os.ReadFile(zshrc)
	if err != nil {
		t.Fatalf("no se creó ~/.zshrc: %v", err)
	}
	bloque := `export PATH="/usr/local/bin:$PATH"`
	if !strings.Contains(string(contenido), bloque) {
		t.Errorf("~/.zshrc no contiene el bloque esperado:\n%s", contenido)
	}

	t.Run("es idempotente", func(t *testing.T) {
		if err := anadirRutaShell(); err != nil {
			t.Fatalf("segunda llamada devolvió error: %v", err)
		}
		despues, err := os.ReadFile(zshrc)
		if err != nil {
			t.Fatal(err)
		}
		apariciones := strings.Count(string(despues), bloque)
		if apariciones != 1 {
			t.Errorf("el bloque debería aparecer una sola vez, obtuve %d", apariciones)
		}
	})

	t.Run("anade a bashrc cuando no existe zshrc", func(t *testing.T) {
		bashrc := filepath.Join(home, ".bashrc")
		if err := os.WriteFile(bashrc, []byte("export OLD=1\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := anadirRutaShell(); err != nil {
			t.Fatalf("anadirRutaShell devolvió error: %v", err)
		}
		contenido, err := os.ReadFile(bashrc)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(contenido), bloque) {
			t.Errorf("~/.bashrc no contiene el bloque esperado:\n%s", contenido)
		}
	})

	t.Run("anade a ambos cuando existen", func(t *testing.T) {
		zshrc := filepath.Join(home, ".zshrc")
		bashrc := filepath.Join(home, ".bashrc")
		if err := os.WriteFile(zshrc, []byte("export OLD=1\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bashrc, []byte("export OLD=1\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := anadirRutaShell(); err != nil {
			t.Fatalf("anadirRutaShell devolvió error: %v", err)
		}
		for _, ruta := range []string{zshrc, bashrc} {
			contenido, err := os.ReadFile(ruta)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(contenido), bloque) {
				t.Errorf("%s no contiene el bloque esperado", ruta)
			}
		}
	})
}

func TestRutasBinario(t *testing.T) {
	home := t.TempDir()
	if ruta := rutaBinarioWindows(home); ruta != filepath.Join(home, ".vas_sentinel", "bin", "sentinel.exe") {
		t.Errorf("rutaBinarioWindows = %q, esperado en .vas_sentinel/bin/sentinel.exe", ruta)
	}
	if ruta := rutaBinarioLinux(); ruta != "/usr/local/bin/sentinel" {
		t.Errorf("rutaBinarioLinux = %q, esperado /usr/local/bin/sentinel", ruta)
	}
}

func TestNecesitaAnadirPathWindows(t *testing.T) {
	if necesitaAnadirPathWindows("C:\\x;C:\\y", "C:\\x") {
		t.Error("no debería añadir un directorio ya presente en el PATH")
	}
	if !necesitaAnadirPathWindows("C:\\x", "C:\\nuevo") {
		t.Error("debería añadir un directorio ausente del PATH")
	}
}

func TestObtenerPathUsuarioWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("solo aplica a Windows")
	}
	if _, err := obtenerPathUsuarioWindows(); err != nil {
		t.Fatalf("obtenerPathUsuarioWindows devolvió error: %v", err)
	}
}
