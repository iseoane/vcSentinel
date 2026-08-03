package agentadapter

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
)

// TimeoutComando es el límite de una llamada al agente (120 s). La fase 1 lo
// hace configurable vía review.timeout en vassentinel.yml.
const TimeoutComando = 120 * time.Second

type CLIAdapter struct {
	BinaryName string
	Config     config.AgentConfig
}

func (c *CLIAdapter) ObtenerMensajeCommit(rutasArchivos []string, capa string, batchNum int) (string, error) {
	return c.ejecutarComando(construirPromptAgente(capa, batchNum, rutasArchivos))
}

// ProponerPlanRefactor pide al agente un plan de división para un archivo de
// código masivo. Implementa AdapterRefactor.
func (c *CLIAdapter) ProponerPlanRefactor(rutaArchivo string) (string, error) {
	return c.ejecutarComando(construirPromptRefactor(rutaArchivo))
}

// AplicarPlanRefactor ordena al agente ejecutar el plan de refactorización
// directamente sobre el working tree, sin hacer commits. Implementa
// AdapterRefactor.
func (c *CLIAdapter) AplicarPlanRefactor(rutaArchivo string, plan string) (string, error) {
	return c.ejecutarComando(construirPromptAplicarRefactor(rutaArchivo, plan))
}

// ejecutarComando ejecuta el binario del agente con el prompt dado y devuelve
// la salida estándar completa (recortada). Adapta los argumentos al binario:
// claude usa "-p <prompt>"; opencode usa "run <prompt>". El proceso se corta
// con TimeoutComando si el agente no responde: un agente que espera entrada
// interactiva no debe colgar la auditoría.
func (c *CLIAdapter) ejecutarComando(prompt string) (string, error) {
	return c.ejecutarComandoConTimeout(prompt, TimeoutComando)
}

// ejecutarComandoConTimeout es la variante parametrizada de ejecutarComando;
// permite a los tests acortar la espera sin tocar la constante de producción.
func (c *CLIAdapter) ejecutarComandoConTimeout(prompt string, timeout time.Duration) (string, error) {
	ctx, cancelar := context.WithTimeout(context.Background(), timeout)
	defer cancelar()

	cmd := exec.CommandContext(ctx, c.BinaryName, c.comandoArgs(prompt)...)
	env := os.Environ()

	if c.esClaude() {
		env = append(env, fmt.Sprintf("CLAUDE_CODE_MODEL=%s", c.Config.Model))
		env = append(env, fmt.Sprintf("CLAUDE_CODE_REASONING=%s", c.Config.ReasoningEffort))
	} else if c.esOpenCode() {
		env = append(env, fmt.Sprintf("OPENCODE_MODEL=%s", c.Config.Model))
		env = append(env, fmt.Sprintf("OPENCODE_REASONING_EFFORT=%s", c.Config.ReasoningEffort))
	}
	cmd.Env = env

	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}

	return strings.TrimSpace(out.String()), nil
}

// comandoArgs devuelve los argumentos de invocación según el binario: opencode
// usa el subcomando "run" con el prompt como mensaje; el resto usa "-p".
func (c *CLIAdapter) comandoArgs(prompt string) []string {
	if c.esOpenCode() {
		return []string{"run", prompt}
	}
	return []string{"-p", prompt}
}

func (c *CLIAdapter) esClaude() bool {
	return c.nombreBase() == "claude"
}

func (c *CLIAdapter) esOpenCode() bool {
	return c.nombreBase() == "opencode"
}

// nombreBase extrae el nombre del binario sin ruta ni extensión, para tolerar
// rutas completas o sufijos de plataforma (p. ej. "opencode.exe").
func (c *CLIAdapter) nombreBase() string {
	return strings.TrimSuffix(filepath.Base(c.BinaryName), filepath.Ext(c.BinaryName))
}

func construirPromptAgente(capa string, batchNum int, archivos []string) string {
	archivosStr := strings.Join(archivos, " ")
	return fmt.Sprintf(
		"Analiza estos archivos modificados de la capa [%s] (Lote #%d): %s. Genera un mensaje de commit semántico bajo el estándar Conventional Commits. Devuelve ÚNICAMENTE la línea del mensaje, sin marcas de markdown ni comillas.",
		capa, batchNum, archivosStr,
	)
}

func construirPromptRefactor(ruta string) string {
	return fmt.Sprintf(
		"Analiza el archivo %s. Propón un plan detallado para dividirlo en archivos más pequeños y cohesivos, respetando el principio de responsabilidad única (SRP). Devuelve el plan en texto plano: qué archivos crear, qué contenido debería moverse a cada uno y el orden sugerido. Sin marcas de markdown.",
		ruta,
	)
}

func construirPromptAplicarRefactor(ruta string, plan string) string {
	return fmt.Sprintf(
		"Aplica el siguiente plan de refactorización sobre el archivo %s:\n%s\nRealiza los cambios directamente en el working tree: crea, mueve y edita los archivos necesarios. NO hagas commits ni ejecutes git. Devuelve un resumen breve de los archivos creados, modificados o eliminados. Sin marcas de markdown.",
		ruta, plan,
	)
}
