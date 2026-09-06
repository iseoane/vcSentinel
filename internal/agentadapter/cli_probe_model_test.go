package agentadapter

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
)

// Tests de comportamiento para la vía de sondeo de modelo (EjecutarPrompt):
// la invocación del probe debe solicitar el modelo configurado con --model,
// igual que reviewCommand, porque las variables de entorno (OPENCODE_MODEL)
// no bastan con OpenCode real. Se usa el binario falso de testdata/sleeper
// para capturar los argumentos sin invocar un agente real.

func argsConModelo(t *testing.T, captura capturaAgente, modeloEsperado string) {
	indice := -1
	t.Helper()
	for i, arg := range captura.Args {
		if arg == "--model" {
			indice = i
			break
		}
	}
	if indice == -1 {
		t.Fatalf("la invocación del probe no pide modelo: args = %v, esperado --model %s", captura.Args, modeloEsperado)
	}
	if indice+1 >= len(captura.Args) {
		t.Fatalf("--model va sin valor: args = %v", captura.Args)
	}
	if captura.Args[indice+1] != modeloEsperado {
		t.Fatalf("modelo solicitado = %q, esperado %q (args = %v)", captura.Args[indice+1], modeloEsperado, captura.Args)
	}
}

func TestEjecutarPromptProbePideModeloConfiguradoOpenCode(t *testing.T) {
	const modelo = "opencode-go/glm-5.3-flash"
	capturaRuta := filepath.Join(t.TempDir(), "captura.json")
	t.Setenv("VAS_SENTINEL_TEST_CAPTURE", capturaRuta)
	adapter := CLIAdapter{
		BinaryName: compilarAgenteConNombre(t, "opencode"),
		Config:     config.AgentConfig{Model: modelo, ReasoningEffort: "low"},
		Timeout:    10 * time.Second,
	}

	salida, err := adapter.EjecutarPrompt("¿qué modelo eres?")
	if err != nil {
		t.Fatalf("EjecutarPrompt devolvió error: %v", err)
	}
	if salida != "¿qué modelo eres?" {
		t.Fatalf("salida = %q, esperado el eco del prompt por stdin", salida)
	}

	captura := leerCapturaAgente(t, capturaRuta)
	if len(captura.Args) == 0 || captura.Args[0] != "run" {
		t.Fatalf("args = %v, esperado que empiece por el subcomando run", captura.Args)
	}
	argsConModelo(t, captura, modelo)
	if captura.Stdin != "¿qué modelo eres?" {
		t.Fatalf("stdin = %q, esperado el prompt completo (transporte por stdin intacto)", captura.Stdin)
	}
}

func TestEjecutarPromptProbePideModeloConfiguradoClaude(t *testing.T) {
	const modelo = "claude-sonnet-5"
	capturaRuta := filepath.Join(t.TempDir(), "captura.json")
	t.Setenv("VAS_SENTINEL_TEST_CAPTURE", capturaRuta)
	adapter := CLIAdapter{
		BinaryName: compilarAgenteConNombre(t, "claude"),
		Config:     config.AgentConfig{Model: modelo, ReasoningEffort: "high"},
		Timeout:    10 * time.Second,
	}

	salida, err := adapter.EjecutarPrompt("¿qué modelo eres?")
	if err != nil {
		t.Fatalf("EjecutarPrompt devolvió error: %v", err)
	}
	if salida != "¿qué modelo eres?" {
		t.Fatalf("salida = %q, esperado el eco del prompt por stdin", salida)
	}

	captura := leerCapturaAgente(t, capturaRuta)
	if len(captura.Args) == 0 || captura.Args[0] != "-p" {
		t.Fatalf("args = %v, esperado que empiecen por -p", captura.Args)
	}
	argsConModelo(t, captura, modelo)
	if captura.Stdin != "¿qué modelo eres?" {
		t.Fatalf("stdin = %q, esperado el prompt completo (transporte por stdin intacto)", captura.Stdin)
	}
}

func TestEjecutarPromptProbeSinModeloNoAnadeBandera(t *testing.T) {
	capturaRuta := filepath.Join(t.TempDir(), "captura.json")
	t.Setenv("VAS_SENTINEL_TEST_CAPTURE", capturaRuta)
	adapter := CLIAdapter{
		BinaryName: compilarAgenteConNombre(t, "opencode"),
		Timeout:    10 * time.Second,
	}

	if _, err := adapter.EjecutarPrompt("prompt"); err != nil {
		t.Fatalf("EjecutarPrompt devolvió error: %v", err)
	}

	captura := leerCapturaAgente(t, capturaRuta)
	for _, arg := range captura.Args {
		if arg == "--model" {
			t.Fatalf("sin modelo configurado la invocación no debe llevar --model: args = %v", captura.Args)
		}
	}
	if len(captura.Args) == 0 || captura.Args[0] != "run" {
		t.Fatalf("args = %v, esperado que empiece por run", captura.Args)
	}
	if captura.Stdin != "prompt" {
		t.Fatalf("stdin = %q, esperado el prompt completo", captura.Stdin)
	}
}
