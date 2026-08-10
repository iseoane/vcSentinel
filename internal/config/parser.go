package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// AgentConfig define el modelo y esfuerzo por defecto de un agente (binario)
// y los perfiles anidados que ese agente ofrece (esquema v2).
type AgentConfig struct {
	Model           string
	ReasoningEffort string
	Profiles        map[string]ProfileConfig
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
	ActiveAgent string
	Agents      map[string]AgentConfig
	// AgentOrder preserva el orden de declaración de los agentes en el yml
	// (el archivo más específico manda); alimenta la resolución automática.
	AgentOrder []string
	Profiles   map[string]ProfileConfig
	Review     ReviewConfig
	// CommitLanguage fija el idioma de los mensajes de commit que genera el
	// agente (T0.13). Por defecto, el del historial del repositorio.
	CommitLanguage string
	LintCommands   []string
	TestCommands   []string
	BuildCommands  []string
}

// DimensionesPorDefecto son las seis dimensiones canónicas de auditoría.
var DimensionesPorDefecto = []string{"logic", "style", "design", "tests", "security", "spec"}

func configuracionPorDefecto() Config {
	return Config{
		ActiveAgent: "auto",
		// "es" y no agentadapter.IdiomaPorDefecto: agentadapter ya importa
		// config, así que referenciarlo aquí crearía un ciclo. Los tests de
		// agentadapter fijan que ambos valores coinciden.
		CommitLanguage: "es",
		Agents: map[string]AgentConfig{
			"claude": {
				Model:           "claude-5-sonnet",
				ReasoningEffort: "high",
				Profiles: map[string]ProfileConfig{
					"cheap":  {Model: "claude-5-sonnet", ReasoningEffort: "low"},
					"normal": {Model: "claude-5-sonnet", ReasoningEffort: "high"},
					"deep":   {Model: "claude-opus", ReasoningEffort: "high"},
				},
			},
			"opencode": {
				Model:           "deepseek-v4-flash-free",
				ReasoningEffort: "max",
				Profiles: map[string]ProfileConfig{
					"cheap":  {Model: "deepseek-v4-flash-free", ReasoningEffort: "default"},
					"normal": {Model: "deepseek-v4-flash-free", ReasoningEffort: "high"},
					"deep":   {Model: "deepseek-v4-flash-free", ReasoningEffort: "max"},
				},
			},
		},
		AgentOrder: []string{"claude", "opencode"},
		Profiles:   map[string]ProfileConfig{}, // compat v1: perfiles globales
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
		LintCommands:  []string{},
		TestCommands:  []string{},
		BuildCommands: []string{},
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

	// CargarConfiguracionLocal conserva su firma sin error (mismo contrato de
	// hoy para sus llamadores); el error estricto de aplicarDesdeRuta queda
	// disponible para quien lo invoque directamente (ver tests), pendiente de
	// decidir en otra tarea cómo se hace visible al operador del CLI.
	if ruta, err := rutaConfigGlobal(); err == nil {
		_ = aplicarDesdeRuta(&cfg, ruta)
	}

	_ = aplicarDesdeRuta(&cfg, rutaConfigPerProyecto(worktreePath))

	return cfg
}

// perfilAgenteYAML es un perfil anidado dentro de un agente
// (agents.<agente>.profiles.<perfil>, esquema v2): solo model/reasoning_effort,
// el agente lo da la clave exterior.
type perfilAgenteYAML struct {
	Model           *string `yaml:"model"`
	ReasoningEffort *string `yaml:"reasoning_effort"`
}

// perfilGlobalYAML es un perfil de nivel superior (sección profiles, compat
// v1): agent es opcional, si falta se usa el active_agent.
type perfilGlobalYAML struct {
	Agent           *string `yaml:"agent"`
	Model           *string `yaml:"model"`
	ReasoningEffort *string `yaml:"reasoning_effort"`
}

// agenteYAML es la entrada de un agente en agents.<nombre>.
type agenteYAML struct {
	Model           *string                     `yaml:"model"`
	ReasoningEffort *string                     `yaml:"reasoning_effort"`
	Profiles        map[string]perfilAgenteYAML `yaml:"profiles"`
}

// reviewYAML es la sección review. Timeout/Parallel se decodifican como
// yaml.Node (no int directo) para conservar la tolerancia histórica a
// valores no numéricos (se ignoran y queda el default), igual que hacía
// strconv.Atoi en el parser artesanal.
type reviewYAML struct {
	Timeout  yaml.Node         `yaml:"timeout"`
	Parallel yaml.Node         `yaml:"parallel"`
	Dims     map[string]string `yaml:"dims"`
}

// configYAML es el esquema completo tal cual lo consume yaml.v3 con
// KnownFields(true): una clave fuera de esta lista (p. ej. "comand" en vez de
// "command") hace fallar la decodificación con archivo y línea, en vez de
// ignorarse en silencio como el parser artesanal anterior.
//
// Version no tiene campo equivalente en Config: no se usa en ningún cálculo
// hoy, pero los ymls reales de este repo la declaran (ver
// .vas_sentinel/vassentinel.yml), así que debe aceptarse para no romper la
// decodificación estricta de configuración existente. Añadir esa sección a
// Config es otra tarea.
type configYAML struct {
	Version        *string                     `yaml:"version"`
	ActiveAgent    *string                     `yaml:"active_agent"`
	Agents         map[string]agenteYAML       `yaml:"agents"`
	Profiles       map[string]perfilGlobalYAML `yaml:"profiles"`
	Review         *reviewYAML                 `yaml:"review"`
	CommitLanguage *string                     `yaml:"commit_language"`
	LintCommands   []string                    `yaml:"lint_commands"`
	TestCommands   []string                    `yaml:"test_commands"`
	BuildCommands  []string                    `yaml:"build_commands"`
}

// ordenAgentesYAML se decodifica SIN KnownFields, solo para leer el orden
// textual de las claves de "agents" a través de su yaml.Node: los mapas de Go
// no preservan orden de declaración, así que es el único punto donde se
// puede recuperar. La validación estricta de esas mismas claves ya la hizo el
// decode de configYAML antes de llegar aquí.
type ordenAgentesYAML struct {
	Agents yaml.Node `yaml:"agents"`
}

// aplicarDesdeRuta decodifica el archivo en ruta (si existe) con validación
// estricta de claves y aplica sobre cfg los campos presentes, sobreescribiendo
// solo esos. Devuelve un error (con archivo y línea) cuando el archivo existe
// pero tiene una clave fuera del esquema o está mal formado.
func aplicarDesdeRuta(cfg *Config, ruta string) error {
	datos, err := os.ReadFile(ruta)
	if err != nil {
		// Archivo ausente: global y per-proyecto son opcionales.
		return nil
	}

	var raw configYAML
	decoder := yaml.NewDecoder(bytes.NewReader(datos))
	decoder.KnownFields(true)
	if err := decoder.Decode(&raw); err != nil {
		if err == io.EOF {
			return nil // archivo vacío: nada que aplicar.
		}
		return fmt.Errorf("%s: %w", ruta, err)
	}

	aplicarValoresYAML(cfg, &raw)

	var orden ordenAgentesYAML
	if err := yaml.Unmarshal(datos, &orden); err == nil {
		aplicarOrdenAgentes(cfg, clavesEnOrden(&orden.Agents))
	}

	return nil
}

// aplicarValoresYAML aplica sobre cfg los campos presentes en raw, campo a
// campo (solo sobreescribe lo que el archivo declara explícitamente).
func aplicarValoresYAML(cfg *Config, raw *configYAML) {
	if raw.ActiveAgent != nil {
		cfg.ActiveAgent = *raw.ActiveAgent
	}
	if raw.CommitLanguage != nil {
		cfg.CommitLanguage = *raw.CommitLanguage
	}
	for nombre, agenteRaw := range raw.Agents {
		agente := cfg.Agents[nombre]
		if agenteRaw.Model != nil {
			agente.Model = *agenteRaw.Model
		}
		if agenteRaw.ReasoningEffort != nil {
			agente.ReasoningEffort = *agenteRaw.ReasoningEffort
		}
		for perfilNombre, perfilRaw := range agenteRaw.Profiles {
			if agente.Profiles == nil {
				agente.Profiles = map[string]ProfileConfig{}
			}
			perfil := agente.Profiles[perfilNombre]
			if perfilRaw.Model != nil {
				perfil.Model = *perfilRaw.Model
			}
			if perfilRaw.ReasoningEffort != nil {
				perfil.ReasoningEffort = *perfilRaw.ReasoningEffort
			}
			agente.Profiles[perfilNombre] = perfil
		}
		cfg.Agents[nombre] = agente
	}
	for nombre, perfilRaw := range raw.Profiles {
		perfil := cfg.Profiles[nombre]
		if perfilRaw.Agent != nil {
			perfil.Agent = *perfilRaw.Agent
		}
		if perfilRaw.Model != nil {
			perfil.Model = *perfilRaw.Model
		}
		if perfilRaw.ReasoningEffort != nil {
			perfil.ReasoningEffort = *perfilRaw.ReasoningEffort
		}
		cfg.Profiles[nombre] = perfil
	}
	if raw.Review != nil {
		if n, ok := decodificarEnteroPositivo(&raw.Review.Timeout); ok {
			cfg.Review.Timeout = time.Duration(n) * time.Second
		}
		if n, ok := decodificarEnteroPositivo(&raw.Review.Parallel); ok {
			cfg.Review.Parallel = n
		}
		for dim, perfilDim := range raw.Review.Dims {
			cfg.Review.Dims[dim] = perfilDim
		}
	}
	cfg.LintCommands = append(cfg.LintCommands, raw.LintCommands...)
	cfg.TestCommands = append(cfg.TestCommands, raw.TestCommands...)
	cfg.BuildCommands = append(cfg.BuildCommands, raw.BuildCommands...)
}

// decodificarEnteroPositivo intenta leer nodo como entero positivo. Devuelve
// ok=false si el nodo está ausente (Kind cero), no es numérico o no es
// positivo, igual que hacía strconv.Atoi + "n > 0" en el parser artesanal:
// un valor inválido se ignora en silencio y queda el default.
func decodificarEnteroPositivo(nodo *yaml.Node) (int, bool) {
	if nodo.Kind == 0 {
		return 0, false
	}
	var n int
	if err := nodo.Decode(&n); err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// clavesEnOrden devuelve las claves de un yaml.Node de tipo mapping en el
// orden textual en que aparecen en el archivo.
func clavesEnOrden(nodo *yaml.Node) []string {
	if nodo == nil || nodo.Kind != yaml.MappingNode {
		return nil
	}
	claves := make([]string, 0, len(nodo.Content)/2)
	for i := 0; i < len(nodo.Content); i += 2 {
		claves = append(claves, nodo.Content[i].Value)
	}
	return claves
}

// aplicarOrdenAgentes reconstruye cfg.AgentOrder con ordenArchivo (el orden de
// declaración en el archivo procesado) a la cabeza, seguido de los agentes ya
// conocidos que ese archivo no declara, en su orden relativo anterior.
func aplicarOrdenAgentes(cfg *Config, ordenArchivo []string) {
	if len(ordenArchivo) == 0 {
		return
	}
	enArchivo := make(map[string]bool, len(ordenArchivo))
	for _, nombre := range ordenArchivo {
		enArchivo[nombre] = true
	}
	nuevoOrden := make([]string, 0, len(ordenArchivo)+len(cfg.AgentOrder))
	nuevoOrden = append(nuevoOrden, ordenArchivo...)
	for _, nombre := range cfg.AgentOrder {
		if !enArchivo[nombre] {
			nuevoOrden = append(nuevoOrden, nombre)
		}
	}
	cfg.AgentOrder = nuevoOrden
}
