package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// AgentConfig define el modelo y esfuerzo por defecto de un agente (binario).
type AgentConfig struct {
	Model           string
	ReasoningEffort string
}

// ProfileConfig es una receta con nombre: agente + modelo + esfuerzo. El
// agente es opcional: si está vacío se usa el binario activo (active_agent).
type ProfileConfig struct {
	Agent           string
	Model           string
	ReasoningEffort string
}

// ReviewConfig agrupa la configuración del motor de auditoría.
type ReviewConfig struct {
	Timeout  time.Duration
	Parallel int
	// Dims asigna cada dimensión canónica a un perfil de agente.
	Dims map[string]string
}

// Config es la configuración completa de VAS Sentinel con precedencia
// defaults -> global -> per-proyecto.
type Config struct {
	ActiveAgent  string
	Agents       map[string]AgentConfig
	Profiles     map[string]ProfileConfig
	Review       ReviewConfig
	LintCommands []string
}

// DimensionesPorDefecto son las seis dimensiones canónicas de auditoría.
var DimensionesPorDefecto = []string{"logic", "style", "design", "tests", "security", "spec"}

func configuracionPorDefecto() Config {
	return Config{
		ActiveAgent: "auto",
		Agents: map[string]AgentConfig{
			"claude":   {Model: "claude-3-5-sonnet", ReasoningEffort: "high"},
			"opencode": {Model: "deepseek-v4-flash", ReasoningEffort: "max"},
		},
		Profiles: map[string]ProfileConfig{
			"cheap":  {Model: "deepseek-v4-flash", ReasoningEffort: "low"},
			"normal": {},
			"deep":   {Model: "claude-3-5-sonnet", ReasoningEffort: "max"},
		},
		Review: ReviewConfig{
			Timeout:  300 * time.Second,
			Parallel: 2,
			Dims: map[string]string{
				"spec":     "cheap",
				"style":    "cheap",
				"tests":    "normal",
				"logic":    "normal",
				"design":   "deep",
				"security": "deep",
			},
		},
		LintCommands: []string{},
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
// sobreescribiendo los campos presentes. El parser es por niveles de
// indentación relativos (robusto a 2 o 4 espacios por nivel) y soporta las
// secciones active_agent, agents, profiles, review (con dims) y
// lint_commands.
func aplicarDesdeRuta(cfg *Config, ruta string) {
	file, err := os.Open(ruta)
	if err != nil {
		return
	}
	defer file.Close()

	seccion := ""
	entidad := ""
	indentEntidad := -1
	subseccion := ""
	indentSubseccion := -1

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		linea := strings.TrimRight(scanner.Text(), " \t")
		texto := strings.TrimSpace(linea)
		if texto == "" || strings.HasPrefix(texto, "#") {
			continue
		}
		indent := contarIndent(linea)
		clave, valor := dividirClaveValor(texto)
		cierraBloque := strings.HasSuffix(texto, ":") && valor == ""

		switch {
		case indent == 0:
			entidad, subseccion = "", ""
			indentEntidad, indentSubseccion = -1, -1
			switch clave {
			case "active_agent":
				cfg.ActiveAgent = limpiarValor(valor)
			case "agents":
				seccion = "agents"
			case "profiles":
				seccion = "profiles"
			case "review":
				seccion = "review"
			case "lint_commands":
				seccion = "lint"
			default:
				seccion = ""
			}
		case seccion == "agents" || seccion == "profiles":
			if (entidad == "" || indent <= indentEntidad) && cierraBloque {
				entidad = clave
				indentEntidad = indent
				continue
			}
			if entidad != "" && indent > indentEntidad && clave != "" {
				aplicarCampoEntidad(cfg, seccion, entidad, clave, limpiarValor(valor))
			}
		case seccion == "review":
			if (subseccion == "" || indent <= indentSubseccion) && cierraBloque {
				subseccion = clave
				indentSubseccion = indent
				continue
			}
			if subseccion == "dims" && indent > indentSubseccion && clave != "" && valor != "" {
				cfg.Review.Dims[clave] = limpiarValor(valor)
			}
			if subseccion == "" && clave != "" {
				switch clave {
				case "timeout":
					if n, err := strconv.Atoi(limpiarValor(valor)); err == nil && n > 0 {
						cfg.Review.Timeout = time.Duration(n) * time.Second
					}
				case "parallel":
					if n, err := strconv.Atoi(limpiarValor(valor)); err == nil && n > 0 {
						cfg.Review.Parallel = n
					}
				}
			}
		case seccion == "lint":
			if indent >= 1 && strings.HasPrefix(texto, "-") {
				if cmd := limpiarValor(strings.TrimSpace(strings.TrimPrefix(texto, "-"))); cmd != "" {
					cfg.LintCommands = append(cfg.LintCommands, cmd)
				}
			}
		}
	}
}

// aplicarCampoEntidad aplica un campo (model, reasoning_effort, agent) a un
// agente o perfil según la sección activa.
func aplicarCampoEntidad(cfg *Config, seccion, entidad, clave, valor string) {
	switch {
	case seccion == "agents":
		agente := cfg.Agents[entidad]
		switch clave {
		case "model":
			agente.Model = valor
		case "reasoning_effort":
			agente.ReasoningEffort = valor
		}
		cfg.Agents[entidad] = agente
	case seccion == "profiles":
		perfil := cfg.Profiles[entidad]
		switch clave {
		case "agent":
			perfil.Agent = valor
		case "model":
			perfil.Model = valor
		case "reasoning_effort":
			perfil.ReasoningEffort = valor
		}
		cfg.Profiles[entidad] = perfil
	}
}

// contarIndent cuenta los espacios iniciales de una línea.
func contarIndent(linea string) int {
	n := 0
	for n < len(linea) && linea[n] == ' ' {
		n++
	}
	return n
}

// dividirClaveValor separa "clave: valor" en sus dos partes. Una línea que
// termina en ":" sin valor devuelve clave con valor vacío.
func dividirClaveValor(texto string) (string, string) {
	partes := strings.SplitN(texto, ":", 2)
	if len(partes) == 1 {
		return strings.TrimSpace(partes[0]), ""
	}
	return strings.TrimSpace(partes[0]), strings.TrimSpace(partes[1])
}

// limpiarValor elimina comillas dobles y simples alrededor de un valor.
func limpiarValor(valor string) string {
	return strings.Trim(valor, `"'`)
}
