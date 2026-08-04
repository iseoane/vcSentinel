package agentadapter

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
			return &CLIAdapter{BinaryName: resolverBinarioReal(nombre), Config: cfg.Agents[nombre]}, nil
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
	return &CLIAdapter{BinaryName: resolverBinarioReal(nombres[0]), Config: cfg.Agents[nombres[0]]}, nil
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
	return &CLIAdapter{BinaryName: resolverBinarioReal(nombre), Config: agente}, nil
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
	nombre := perfil.Binario
	if nombre == "" {
		nombre = cfg.ActiveAgent
	}
	if nombre == "" || nombre == "auto" {
		disponibles := nombresAgentesEnPATH(cfg)
		if len(disponibles) == 0 {
			return nil, fmt.Errorf("ningun agente configurado en vassentinel.yml esta disponible en el PATH")
		}
		nombre = disponibles[0]
	}
	binario := resolverBinarioReal(nombre)

	agente := cfg.Agents[nombre]
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

// resolverBinarioReal convierte un shim npm (.cmd/.bat) en la ruta del
// binario real al que apunta. Ejecutar shims .cmd con exec.Command usa cmd.exe
// y rompe el quoting de prompts largos (límite de línea + comillas), así que
// se extrae el .exe destino de la línea "node_modules\...\bin\X.exe" del shim.
// Si no hay shim o no se puede resolver, devuelve el nombre tal cual.
func resolverBinarioReal(nombre string) string {
	if filepath.IsAbs(nombre) || strings.ContainsAny(nombre, `/\`) {
		return nombre
	}
	ruta, err := exec.LookPath(nombre)
	if err != nil {
		return nombre
	}
	if ext := strings.ToLower(filepath.Ext(ruta)); ext != ".cmd" && ext != ".bat" {
		return ruta
	}

	datos, err := os.ReadFile(ruta)
	if err != nil {
		return ruta
	}
	// Los shims npm usan "%dp0%\node_modules\<paquete>\bin\<binario>.exe" (la
	// variable apunta al directorio del propio shim); algunos usan "%~dp0".
	patron := regexp.MustCompile(`([A-Za-z]:\\[^"\r\n]*node_modules[^"\r\n]*\.exe|(?:%dp0%|%~dp0)\\[^"\r\n]*node_modules[^"\r\n]*\.exe)`)
	coincidencia := patron.FindString(string(datos))
	if coincidencia == "" {
		return ruta
	}
	// El .cmd de npm usa %dp0% (directorio del shim): la ruta extraída es
	// relativa a ese directorio, no al CWD del proceso.
	if strings.HasPrefix(coincidencia, "%dp0%") {
		return filepath.Join(filepath.Dir(ruta), coincidencia[len("%dp0%"):])
	}
	if strings.HasPrefix(coincidencia, "%~dp0") {
		return filepath.Join(filepath.Dir(ruta), coincidencia[len("%~dp0"):])
	}
	return coincidencia
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
