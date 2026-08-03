package agentadapter

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// compilarSleeper compila el helper de testdata/sleeper a un ejecutable
// temporal y devuelve su ruta. Los tests que lo usan se saltan en -short.
func compilarSleeper(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("salta la compilación del helper en modo -short")
	}

	exe := filepath.Join(t.TempDir(), "sleeper")
	if filepath.Ext(exe) == "" && os.PathSeparator == '\\' {
		exe += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", exe, ".")
	cmd.Dir = filepath.Join("testdata", "sleeper")
	if salida, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("no se pudo compilar el helper: %v\n%s", err, salida)
	}
	return exe
}

func TestEjecutarComandoDevuelveSalida(t *testing.T) {
	sleeper := compilarSleeper(t)
	adapter := CLIAdapter{BinaryName: sleeper}

	salida, err := adapter.ejecutarComandoConTimeout("prompt de prueba", 10*time.Second)
	if err != nil {
		t.Fatalf("ejecutarComandoConTimeout devolvió error: %v", err)
	}
	if salida != "prompt de prueba" {
		t.Errorf("salida = %q, esperado %q", salida, "prompt de prueba")
	}
}

func TestEjecutarComandoCortaPorTimeout(t *testing.T) {
	sleeper := compilarSleeper(t)
	adapter := CLIAdapter{BinaryName: sleeper}

	inicio := time.Now()
	// El helper interpreta el primer argumento numérico como segundos de sueño:
	// "3" duerme 3 s, muy por encima del timeout de 150 ms.
	_, err := adapter.ejecutarComandoConTimeout("3", 150*time.Millisecond)
	duracion := time.Since(inicio)

	if err == nil {
		t.Fatal("se esperaba un error por timeout, no se devolvió ninguno")
	}
	if duracion > 2*time.Second {
		t.Errorf("el proceso no se cortó: tardó %v en devolver", duracion)
	}
	if duracion < 100*time.Millisecond {
		t.Errorf("el error llegó demasiado pronto (%v), parece un fallo previo al timeout", duracion)
	}
}
