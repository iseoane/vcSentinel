package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

type AgentConfig struct {
	Model           string
	ReasoningEffort string
}

type Config struct {
	ActiveAgent string
	Agents      map[string]AgentConfig
}

func configuracionPorDefecto() Config {
	return Config{
		ActiveAgent: "auto",
		Agents: map[string]AgentConfig{
			"claude":   {Model: "claude-3-5-sonnet", ReasoningEffort: "high"},
			"opencode": {Model: "deepseek-v4-flash", ReasoningEffort: "max"},
		},
	}
}

// rutaConfigGlobal devuelve la ruta del archivo de configuración global,
// situado junto al directorio base de VAS Sentinel en el home del usuario.
func rutaConfigGlobal() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, ".vas_sentinel", "vassentinel.yml"), nil
}

// rutaConfigPerProyecto devuelve la ruta del archivo de configuración
// per-proyecto, situado en la carpeta .vas_sentinel de la raíz del worktree,
// manteniendo coherencia con el directorio global del usuario.
func rutaConfigPerProyecto(worktreePath string) string {
	return filepath.Join(worktreePath, ".vas_sentinel", "vassentinel.yml")
}

// CargarConfiguracionLocal carga la configuración siguiendo la precedencia:
// defaults -> global (~/.vas_sentinel/vassentinel.yml) -> per-proyecto
// (<worktree>/.vas_sentinel/vassentinel.yml). El per-proyecto predomina y
// sobreescribe solo los campos que define.
func CargarConfiguracionLocal(worktreePath string) Config {
	cfg := configuracionPorDefecto()

	if ruta, err := rutaConfigGlobal(); err == nil {
		aplicarDesdeRuta(&cfg, ruta)
	}

	aplicarDesdeRuta(&cfg, rutaConfigPerProyecto(worktreePath))

	return cfg
}

// aplicarDesdeRuta parsea el archivo en ruta (si existe) sobre cfg,
// sobreescribiendo los campos presentes.
func aplicarDesdeRuta(cfg *Config, ruta string) {
	file, err := os.Open(ruta)
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	agenteActual := ""

	for scanner.Scan() {
		linea := strings.TrimSpace(scanner.Text())
		if linea == "" || strings.HasPrefix(linea, "#") {
			continue
		}
		if strings.HasPrefix(linea, "active_agent:") {
			partes := strings.SplitN(linea, ":", 2)
			cfg.ActiveAgent = strings.TrimSpace(strings.ReplaceAll(partes[1], "\"", ""))
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

			agentCfg := cfg.Agents[agenteActual]
			if clave == "model" {
				agentCfg.Model = valor
			} else if clave == "reasoning_effort" {
				agentCfg.ReasoningEffort = valor
			}
			cfg.Agents[agenteActual] = agentCfg
		}
	}
}
