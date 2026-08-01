package agentadapter

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
)

type CLIAdapter struct {
	BinaryName string
	Config     config.AgentConfig
}

func (c *CLIAdapter) ObtenerMensajeCommit(rutasArchivos []string, capa string, batchNum int) (string, error) {
	archivosStr := strings.Join(rutasArchivos, " ")
	promptAgente := fmt.Sprintf(
		"Analizá estos archivos modificados de la capa [%s] (Lote #%d): %s. Generá un mensaje de commit semántico bajo el estándar Conventional Commits. Devolvé ÚNICAMENTE la línea del mensaje, sin marcas de markdown ni comillas.",
		capa, batchNum, archivosStr,
	)

	cmd := exec.Command(c.BinaryName, "-p", promptAgente)
	env := os.Environ()

	if c.BinaryName == "claude" {
		env = append(env, fmt.Sprintf("CLAUDE_CODE_MODEL=%s", c.Config.Model))
		env = append(env, fmt.Sprintf("CLAUDE_CODE_REASONING=%s", c.Config.ReasoningEffort))
	} else if c.BinaryName == "opencode" {
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
