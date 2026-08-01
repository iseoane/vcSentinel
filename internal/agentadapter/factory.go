package agentadapter

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
)

func NewAgentAdapter(worktreePath string) (AgentAdapter, error) {
	cfg := config.CargarConfiguracionLocal(worktreePath)

	nombre := os.Getenv("MY_SUB_AGENT")
	if nombre == "" {
		nombre = cfg.ActiveAgent
	}

	if nombre != "auto" {
		if _, existe := cfg.Agents[nombre]; existe {
			return &CLIAdapter{BinaryName: nombre, Config: cfg.Agents[nombre]}, nil
		}
	}

	nombres := make([]string, 0, len(cfg.Agents))
	for clave := range cfg.Agents {
		nombres = append(nombres, clave)
	}
	sort.Strings(nombres)

	for _, clave := range nombres {
		if _, err := exec.LookPath(clave); err == nil {
			return &CLIAdapter{BinaryName: clave, Config: cfg.Agents[clave]}, nil
		}
	}

	return nil, fmt.Errorf("ningun agente configurado en vassentinel.yml esta disponible en el PATH: %s", strings.Join(nombres, ", "))
}
