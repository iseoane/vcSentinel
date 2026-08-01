package agentadapter

import (
	"os"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
)

func NewAgentAdapter(worktreePath string) AgentAdapter {
	agenteActivo := os.Getenv("MY_SUB_AGENT")
	if agenteActivo == "" {
		agenteActivo = "claude"
	}

	tablaConfiguraciones := config.CargarConfiguracionLocal(worktreePath)
	configDelAgente := tablaConfiguraciones[agenteActivo]

	switch agenteActivo {
	case "claude":
		return &CLIAdapter{BinaryName: "claude", Config: configDelAgente}
	case "opencode":
		return &CLIAdapter{BinaryName: "opencode", Config: configDelAgente}
	default:
		return &CLIAdapter{BinaryName: "claude", Config: configDelAgente}
	}
}
