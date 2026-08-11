package agentadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
)

func TestPrepararComandoCommitOpenCodeAislaLaEjecucion(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("no se pudo obtener el directorio actual: %v", err)
	}
	adapter := CLIAdapter{
		BinaryName: "opencode",
		Config:     config.AgentConfig{Model: "openai/gpt-5.6-sol", ReasoningEffort: "high"},
	}

	cmd, limpiar, err := adapter.prepararComandoCommit(context.Background(), "feat(test): mensaje")
	if err != nil {
		t.Fatalf("prepararComandoCommit devolvió error: %v", err)
	}

	esperados := []string{"opencode", "run", "--pure", "--agent", "title", "--format", "json", "--model", "openai/gpt-5.6-sol", "--variant", "high", "--dir", cmd.Dir}
	if !reflect.DeepEqual(cmd.Args, esperados) {
		t.Fatalf("argumentos = %v, esperados %v", cmd.Args, esperados)
	}
	if cmd.Dir == "" {
		t.Fatal("OpenCode debe ejecutarse en un directorio neutral")
	}
	if mismaRuta(cmd.Dir, cwd) {
		t.Fatalf("cmd.Dir = %q, debe ser distinto del cwd del repositorio %q", cmd.Dir, cwd)
	}
	datos, err := io.ReadAll(cmd.Stdin)
	if err != nil {
		t.Fatalf("no se pudo leer stdin: %v", err)
	}
	if string(datos) != "feat(test): mensaje" {
		t.Fatalf("stdin = %q, esperado el prompt completo", datos)
	}

	limpiar()
	if _, err := os.Stat(cmd.Dir); !os.IsNotExist(err) {
		t.Fatalf("el directorio aislado sigue existiendo tras limpiar: %v", err)
	}
}

type capturaAgente struct {
	Args  []string `json:"args"`
	Dir   string   `json:"dir"`
	Stdin string   `json:"stdin"`
}

func mismaRuta(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	return errA == nil && errB == nil && filepath.Clean(absA) == filepath.Clean(absB)
}

func leerCapturaAgente(t *testing.T, ruta string) capturaAgente {
	t.Helper()
	datos, err := os.ReadFile(ruta)
	if err != nil {
		t.Fatalf("no se pudo leer la captura del agente: %v", err)
	}
	var captura capturaAgente
	if err := json.Unmarshal(datos, &captura); err != nil {
		t.Fatalf("captura inválida: %v", err)
	}
	return captura
}

func TestObtenerMensajeCommitOpenCodeSinDiffFallaSinInvocarProceso(t *testing.T) {
	capturaRuta := filepath.Join(t.TempDir(), "captura.json")
	t.Setenv("VAS_SENTINEL_TEST_CAPTURE", capturaRuta)
	t.Setenv("VAS_SENTINEL_TEST_OUTPUT", "feat(adapter): conservar contexto del repositorio")
	adapter := CLIAdapter{BinaryName: compilarAgenteConNombre(t, "opencode"), Timeout: 10 * time.Second}

	if _, err := adapter.ObtenerMensajeCommit([]string{"internal/git/plan.go"}, "backend", 1); err == nil {
		t.Fatal("OpenCode sin micro-diff consentido debe fallar cerrado")
	}
	if _, err := os.Stat(capturaRuta); !os.IsNotExist(err) {
		t.Fatalf("OpenCode fue invocado sin micro-diff consentido: %v", err)
	}
}

func TestObtenerMensajeCommitConDiffOpenCodeEjecutaAislado(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("no se pudo obtener el directorio actual: %v", err)
	}
	capturaRuta := filepath.Join(t.TempDir(), "captura.json")
	t.Setenv("VAS_SENTINEL_TEST_CAPTURE", capturaRuta)
	t.Setenv("VAS_SENTINEL_TEST_OUTPUT", "{\"type\":\"step_start\"}\n{\"type\":\"text\",\"part\":{\"text\":\"fix(adapter): usar el micro diff aislado\"}}\n{\"type\":\"step_finish\"}\n")
	adapter := CLIAdapter{
		BinaryName: compilarAgenteConNombre(t, "opencode"),
		Config:     config.AgentConfig{Model: "openai/gpt-5.6-sol", ReasoningEffort: "high"},
		Timeout:    10 * time.Second,
	}
	microDiff := "diff --git a/x.go b/x.go\n+func corregida() {}"

	mensaje, err := adapter.ObtenerMensajeCommitConDiff([]string{"x.go"}, "backend", 1, microDiff)
	if err != nil {
		t.Fatalf("ObtenerMensajeCommitConDiff devolvió error: %v", err)
	}
	if mensaje != "fix(adapter): usar el micro diff aislado" {
		t.Fatalf("mensaje = %q", mensaje)
	}
	captura := leerCapturaAgente(t, capturaRuta)
	if mismaRuta(captura.Dir, cwd) {
		t.Fatalf("cwd aislado = %q, no debe ser el repositorio", captura.Dir)
	}
	esperados := []string{"run", "--pure", "--agent", "title", "--format", "json", "--model", "openai/gpt-5.6-sol", "--variant", "high", "--dir", captura.Dir}
	if !reflect.DeepEqual(captura.Args, esperados) {
		t.Fatalf("argumentos = %v, esperados %v", captura.Args, esperados)
	}
	if !strings.Contains(captura.Stdin, microDiff) {
		t.Fatalf("stdin no contiene el micro-diff real: %q", captura.Stdin)
	}
	if _, err := os.Stat(captura.Dir); !os.IsNotExist(err) {
		t.Fatalf("el cwd aislado sigue existiendo tras la ejecución: %v", err)
	}
}

func TestValidarMensajeCommit(t *testing.T) {
	casos := []struct {
		nombre string
		salida string
		valida bool
	}{
		{nombre: "convencional", salida: "feat(slice): agrupar por cohesion", valida: true},
		{nombre: "multilinea", salida: "He revisado los cambios\nfeat(slice): agrupar por cohesion", valida: false},
		{nombre: "texto generico", salida: "He revisado los cambios", valida: false},
		{nombre: "vacia", salida: "  ", valida: false},
	}

	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			mensaje, err := validarMensajeCommit(caso.salida)
			if (err == nil) != caso.valida {
				t.Fatalf("validarMensajeCommit(%q) error = %v", caso.salida, err)
			}
			if caso.valida && mensaje != strings.TrimSpace(caso.salida) {
				t.Fatalf("mensaje = %q, esperado %q", mensaje, strings.TrimSpace(caso.salida))
			}
		})
	}
}

func TestExtraerMensajeCommitOpenCode(t *testing.T) {
	casos := []struct {
		nombre   string
		salida   string
		esperado string
		valida   bool
	}{
		{
			nombre:   "un texto entre eventos",
			salida:   "{\"type\":\"step_start\"}\n{\"type\":\"text\",\"part\":{\"text\":\"feat(slice): describir el cambio\"}}\n{\"type\":\"step_finish\"}\n",
			esperado: "feat(slice): describir el cambio",
			valida:   true,
		},
		{nombre: "json malformado", salida: "{\"type\":\"text\"", valida: false},
		{nombre: "objetos concatenados en una linea", salida: "{\"type\":\"step_start\"}{\"type\":\"text\",\"part\":{\"text\":\"feat: cambio\"}}\n", valida: false},
		{nombre: "linea vacia intermedia", salida: "{\"type\":\"step_start\"}\n\n{\"type\":\"text\",\"part\":{\"text\":\"feat: cambio\"}}\n", valida: false},
		{nombre: "contenido en blanco", salida: "   \n", valida: false},
		{nombre: "sin texto", salida: "{\"type\":\"step_finish\"}\n", valida: false},
		{
			nombre: "text sin payload",
			salida: "{\"type\":\"text\",\"part\":{}}\n",
			valida: false,
		},
		{
			nombre: "textos conflictivos",
			salida: "{\"type\":\"text\",\"part\":{\"text\":\"feat: primero\"}}\n{\"type\":\"text\",\"part\":{\"text\":\"fix: segundo\"}}\n",
			valida: false,
		},
		{
			nombre: "payload multilinea",
			salida: "{\"type\":\"text\",\"part\":{\"text\":\"explicacion\\nfeat: cambio\"}}\n",
			valida: false,
		},
		{
			nombre: "payload no convencional",
			salida: "{\"type\":\"text\",\"part\":{\"text\":\"cambio sin formato\"}}\n",
			valida: false,
		},
	}

	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			mensaje, err := extraerMensajeCommitOpenCode(caso.salida)
			if (err == nil) != caso.valida {
				t.Fatalf("extraerMensajeCommitOpenCode error = %v", err)
			}
			if mensaje != caso.esperado {
				t.Fatalf("mensaje = %q, esperado %q", mensaje, caso.esperado)
			}
		})
	}
}

func TestExtraerMensajeCommitOpenCodeAceptaLineaJSONLGrandeAcotada(t *testing.T) {
	salida := fmt.Sprintf("{\"type\":\"step_start\",\"padding\":%q}\n{\"type\":\"text\",\"part\":{\"text\":\"feat(slice): validar jsonl grande\"}}\n", strings.Repeat("x", 70*1024))
	mensaje, err := extraerMensajeCommitOpenCode(salida)
	if err != nil || mensaje != "feat(slice): validar jsonl grande" {
		t.Fatalf("mensaje = %q, err = %v", mensaje, err)
	}
}

func TestExtraerMensajeCommitOpenCodeRechazaLineaJSONLSobreLimite(t *testing.T) {
	salida := fmt.Sprintf("{\"type\":\"step_start\",\"padding\":%q}\n", strings.Repeat("x", 1024*1024))
	if _, err := extraerMensajeCommitOpenCode(salida); err == nil {
		t.Fatal("se aceptó una línea JSONL por encima del límite explícito")
	}
}

func TestObtenerMensajeCommitConDiffIncluyeElDiff(t *testing.T) {
	prompt := construirPromptAgenteConDiff("backend", 2, []string{"internal/git/plan.go"}, "diff --git a/x b/x\n+linea", "es")
	if !strings.Contains(prompt, "diff --git a/x b/x\n+linea") {
		t.Fatalf("el prompt no contiene el micro-diff: %q", prompt)
	}
}

func TestObtenerMensajeCommitOpenCodeRechazaSalidaNoConvencional(t *testing.T) {
	binario := compilarAgenteConNombre(t, "opencode")
	adapter := CLIAdapter{BinaryName: binario, Timeout: 10 * time.Second}

	if _, err := adapter.ObtenerMensajeCommit([]string{"internal/git/plan.go"}, "backend", 1); err == nil {
		t.Fatal("se esperaba error al recibir el prompt completo como salida")
	}
}

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
