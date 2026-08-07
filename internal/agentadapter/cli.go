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

// TimeoutComando es el límite de una llamada al agente (300 s). La fase 1 lo
// hace configurable vía review.timeout en vassentinel.yml; este es el fallback
// cuando un adaptador no define Timeout.
const TimeoutComando = 300 * time.Second

type CLIAdapter struct {
	BinaryName string
	Config     config.AgentConfig
	// Timeout es el límite por llamada; si es 0 se usa TimeoutComando.
	Timeout time.Duration
}

// EjecutarPrompt ejecuta el binario con un prompt arbitrario y devuelve la
// salida. Es la vía pública del motor de auditoría hacia el agente.
func (c *CLIAdapter) EjecutarPrompt(prompt string) (string, error) {
	return c.ejecutarComando(prompt)
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
// la salida estándar completa (recortada). Para opencode y claude el prompt
// viaja por stdin (ver comandoPrompt); para el resto de binarios se pasa como
// argumento de "-p". El proceso se corta con TimeoutComando si el agente no
// responde: un agente que espera entrada interactiva no debe colgar la
// auditoría.
func (c *CLIAdapter) ejecutarComando(prompt string) (string, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = TimeoutComando
	}
	return c.ejecutarComandoConTimeout(prompt, timeout)
}

// ejecutarComandoConTimeout es la variante parametrizada de ejecutarComando;
// permite a los tests acortar la espera sin tocar la constante de producción.
func (c *CLIAdapter) ejecutarComandoConTimeout(prompt string, timeout time.Duration) (string, error) {
	ctx, cancelar := context.WithTimeout(context.Background(), timeout)
	defer cancelar()

	args, viaStdin := c.comandoPrompt(prompt)
	cmd := exec.CommandContext(ctx, c.BinaryName, args...)
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
	// opencode y claude leen el prompt de stdin; los demás binarios lo reciben
	// como argumento. Pasar el prompt por stdin evita el límite de 32.767
	// caracteres de la línea de comandos de Windows.
	if viaStdin {
		cmd.Stdin = strings.NewReader(prompt)
	}
	if err := cmd.Run(); err != nil {
		return "", err
	}

	return strings.TrimSpace(out.String()), nil
}

// comandoPrompt devuelve los argumentos de invocación según el binario y si el
// prompt viaja por stdin: opencode usa el subcomando "run" y claude "-p", ambos
// leyendo el prompt de stdin (sin límite de longitud); cualquier otro binario
// recibe el prompt como argumento de "-p" (comportamiento anterior).
func (c *CLIAdapter) comandoPrompt(prompt string) ([]string, bool) {
	if c.esOpenCode() {
		return []string{"run"}, true
	}
	if c.esClaude() {
		return []string{"-p"}, true
	}
	return []string{"-p", prompt}, false
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
