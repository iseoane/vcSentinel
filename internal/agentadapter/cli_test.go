package agentadapter

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
)

// compilarSleeper compila el helper de testdata/sleeper a un ejecutable
// temporal y devuelve su ruta. Los tests que lo usan se saltan en -short.
func compilarSleeper(t *testing.T) string {
	t.Helper()
	return compilarAgenteConNombre(t, "sleeper")
}

// compilarAgenteConNombre compila el helper de testdata/sleeper a un binario
// temporal con el nombre dado (p. ej. "opencode" o "claude") y devuelve su
// ruta. Nombrar el binario como el agente active el transporte por stdin en
// el adaptador sin depender de un agente real. Los tests que lo usan se
// saltan en -short.
func compilarAgenteConNombre(t *testing.T, nombre string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("salta la compilación del helper en modo -short")
	}

	exe := filepath.Join(t.TempDir(), nombre)
	if filepath.Ext(exe) == "" && os.PathSeparator == '\\' {
		exe += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", exe, ".")
	cmd.Dir = filepath.Join("testdata", "sleeper")
	if salida, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("no se pudo compilar el helper %s: %v\n%s", nombre, err, salida)
	}
	return exe
}

// promptLargo devuelve un prompt de más de 40.000 caracteres, por encima del
// límite de 32.767 de la línea de comandos de Windows, para verificar que el
// transporte por stdin no depende de la longitud.
func promptLargo() string {
	return strings.Repeat("[1]", 15_000)
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

// TestEjecutarComandoStdinOpenCode verifica que opencode recibe el prompt por
// stdin (el helper se nombra "opencode" para activar ese camino) y que un
// prompt por encima del límite de línea de Windows llega completo, sin truncar.
func TestEjecutarComandoStdinOpenCode(t *testing.T) {
	binario := compilarAgenteConNombre(t, "opencode")
	adapter := CLIAdapter{
		BinaryName: binario,
		Config:     config.AgentConfig{Model: "deepseek-v4-flash-free", ReasoningEffort: "default"},
	}

	prompt := promptLargo()
	salida, err := adapter.ejecutarComandoConTimeout(prompt, 10*time.Second)
	if err != nil {
		t.Fatalf("ejecutarComandoConTimeout devolvió error: %v", err)
	}
	if salida != prompt {
		t.Errorf("salida de longitud %d, esperado %d (el prompt viaja por stdin sin truncar)", len(salida), len(prompt))
	}
}

// TestEjecutarComandoStdinClaude verifica el mismo transporte por stdin para
// claude, con su configuración de modelo/esfuerzo habitual.
func TestEjecutarComandoStdinClaude(t *testing.T) {
	binario := compilarAgenteConNombre(t, "claude")
	adapter := CLIAdapter{
		BinaryName: binario,
		Config:     config.AgentConfig{Model: "claude-3-5-sonnet", ReasoningEffort: "high"},
	}

	prompt := promptLargo()
	salida, err := adapter.ejecutarComandoConTimeout(prompt, 10*time.Second)
	if err != nil {
		t.Fatalf("ejecutarComandoConTimeout devolvió error: %v", err)
	}
	if salida != prompt {
		t.Errorf("salida de longitud %d, esperado %d (el prompt viaja por stdin sin truncar)", len(salida), len(prompt))
	}
}

// TestEjecutarComandoArgumentoDesconocido verifica que un binario fuera de los
// conocidos (claude/opencode) mantiene el transporte por argumento: el helper
// "sleeper" recibe el prompt como "-p <prompt>" y lo devuelve tal cual.
func TestEjecutarComandoArgumentoDesconocido(t *testing.T) {
	sleeper := compilarSleeper(t)
	adapter := CLIAdapter{BinaryName: sleeper}

	// Prompt largo pero por debajo del límite de 32.767 de Windows: el caso
	// desconocido sigue viajando por argumento y no debe truncarse.
	prompt := strings.Repeat("x", 20_000)
	salida, err := adapter.ejecutarComandoConTimeout(prompt, 10*time.Second)
	if err != nil {
		t.Fatalf("ejecutarComandoConTimeout devolvió error: %v", err)
	}
	if salida != prompt {
		t.Errorf("salida de longitud %d, esperado %d (el transporte por argumento se mantiene)", len(salida), len(prompt))
	}
}

// TestComandoPromptDecideStdin verifica la decisión de transporte sin ejecutar
// ningún binario: opencode/claude usan stdin; cualquier otro pasa el prompt
// como argumento de "-p".
func TestComandoPromptDecideStdin(t *testing.T) {
	casos := []struct {
		nombre        string
		binario       string
		prompt        string
		argsEsperados []string
		viaStdin      bool
	}{
		{nombre: "opencode usa run y stdin", binario: "opencode", argsEsperados: []string{"run"}, viaStdin: true},
		{nombre: "opencode con extensión se detecta", binario: "opencode.exe", argsEsperados: []string{"run"}, viaStdin: true},
		{nombre: "claude usa -p y stdin", binario: "claude", argsEsperados: []string{"-p"}, viaStdin: true},
		{nombre: "desconocido usa -p con el prompt en el argumento", binario: "sleeper", prompt: "prompt de prueba", argsEsperados: []string{"-p", "prompt de prueba"}, viaStdin: false},
	}
	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			adapter := CLIAdapter{BinaryName: caso.binario}
			args, viaStdin := adapter.comandoPrompt(caso.prompt)
			if !reflect.DeepEqual(args, caso.argsEsperados) {
				t.Errorf("comandoPrompt(%q) args = %v, esperado %v", caso.prompt, args, caso.argsEsperados)
			}
			if viaStdin != caso.viaStdin {
				t.Errorf("comandoPrompt(%q) viaStdin = %v, esperado %v", caso.prompt, viaStdin, caso.viaStdin)
			}
		})
	}
}
