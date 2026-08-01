package config

import (
	"bufio"
	"os"
	"strings"
)

type AgentConfig struct {
	Model           string
	ReasoningEffort string
}

func CargarConfiguracionLocal(ruta string) map[string]AgentConfig {
	configs := map[string]AgentConfig{
		"claude":   {Model: "claude-3-5-sonnet", ReasoningEffort: "high"},
		"opencode": {Model: "deepseek-v4-flash", ReasoningEffort: "max"},
	}

	file, err := os.Open(ruta + "/.vassentinel.yml")
	if err != nil {
		return configs
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	agenteActual := ""

	for scanner.Scan() {
		linea := strings.TrimSpace(scanner.Text())
		if linea == "" || strings.HasPrefix(linea, "#") {
			continue
		}
		if strings.HasSuffix(linea, ":") && !strings.Contains(linea, "version") && !strings.Contains(linea, "agents") {
			agenteActual = strings.TrimSuffix(linea, ":")
			continue
		}
		if agenteActual != "" && strings.Contains(linea, ":") {
			partes := strings.SplitN(linea, ":", 2)
			clave := strings.TrimSpace(partes[0])
			valor := strings.TrimSpace(strings.ReplaceAll(partes[1], "\"", ""))

			cfg := configs[agenteActual]
			if clave == "model" {
				cfg.Model = valor
			} else if clave == "reasoning_effort" {
				cfg.ReasoningEffort = valor
			}
			configs[agenteActual] = cfg
		}
	}
	return configs
}
