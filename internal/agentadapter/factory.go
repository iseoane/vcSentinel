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

	todas := make([]string, 0, len(cfg.Agents))
	for clave := range cfg.Agents {
		todas = append(todas, clave)
	}
	sort.Strings(todas)

	nombres := nombresAgentesEnPATH(cfg)
	if len(nombres) == 0 {
		return nil, fmt.Errorf("ningun agente configurado en vassentinel.yml esta disponible en el PATH: %s", strings.Join(todas, ", "))
	}
	return &CLIAdapter{BinaryName: nombres[0], Config: cfg.Agents[nombres[0]]}, nil
}

// NewAgentAdapterNamed construye un adaptador CLI para un nombre de agente
// explícito, sin resolución automática. Devuelve error si el nombre no está
// configurado en vassentinel.yml.
func NewAgentAdapterNamed(worktreePath string, nombre string) (AgentAdapter, error) {
	cfg := config.CargarConfiguracionLocal(worktreePath)
	agente, existe := cfg.Agents[nombre]
	if !existe {
		return nil, fmt.Errorf("el agente %q no está configurado en vassentinel.yml", nombre)
	}
	return &CLIAdapter{BinaryName: nombre, Config: agente}, nil
}

// NombresAdaptadoresDisponibles devuelve los nombres de agentes configurados en
// vassentinel.yml cuyo binario está disponible en el PATH, en orden alfabético.
func NombresAdaptadoresDisponibles(worktreePath string) []string {
	cfg := config.CargarConfiguracionLocal(worktreePath)
	return nombresAgentesEnPATH(cfg)
}

// NuevoAdaptadorConPerfil construye el CLIAdapter que corresponde a un perfil
// resuelto: resuelve el binario (perfil -> active_agent -> auto), completa el
// modelo y esfuerzo con los del agente cuando el perfil no los define y fija
// el timeout de auditoría.
func NuevoAdaptadorConPerfil(cfg config.Config, perfil config.PerfilResuelto) (*CLIAdapter, error) {
	binario := perfil.Binario
	if binario == "" {
		binario = cfg.ActiveAgent
	}
	if binario == "" || binario == "auto" {
		disponibles := nombresAgentesEnPATH(cfg)
		if len(disponibles) == 0 {
			return nil, fmt.Errorf("ningun agente configurado en vassentinel.yml esta disponible en el PATH")
		}
		binario = disponibles[0]
	}

	agente := cfg.Agents[binario]
	modelo := perfil.Modelo
	if modelo == "" {
		modelo = agente.Model
	}
	esfuerzo := perfil.Esfuerzo
	if esfuerzo == "" {
		esfuerzo = agente.ReasoningEffort
	}

	return &CLIAdapter{
		BinaryName: binario,
		Config:     config.AgentConfig{Model: modelo, ReasoningEffort: esfuerzo},
		Timeout:    cfg.Review.Timeout,
	}, nil
}

// nombresAgentesEnPATH filtra los agentes configurados que existen en el PATH,
// en orden alfabético. Es la lógica compartida por la resolución automática de
// NewAgentAdapter y por NombresAdaptadoresDisponibles.
func nombresAgentesEnPATH(cfg config.Config) []string {
	nombres := make([]string, 0, len(cfg.Agents))
	for clave := range cfg.Agents {
		if _, err := exec.LookPath(clave); err == nil {
			nombres = append(nombres, clave)
		}
	}
	sort.Strings(nombres)
	return nombres
}
