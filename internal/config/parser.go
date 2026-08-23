package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ISeoane-Quental/vas.sentinel/internal/change"
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
	Timeout          time.Duration
	Parallel         int
	CodeGraphContext bool
	// DurableRuns enables routing every dimension reviewer call through the
	// durable run controller (R4). False keeps the legacy scheduler; this
	// flag is the construction-time rollback seam of ticket 05.
	DurableRuns bool
	// EvidenceAdmission enables evidence admission over durable transport
	// output (ticket 07): snapshot binding and output-hash verification run
	// before a completion may influence verdicts, and admission failures are
	// surfaced as first-class evidence. It defaults to true (cutover
	// default-on); setting it false restores the pre-R6 observe-but-admit
	// lenient behavior while runs stay inspectable via `sentinel runs`.
	EvidenceAdmission bool
	// CancellationEscalation enables bounded whole-tree escalation after the
	// cooperative grace budget expires when a routed review owns its provider
	// process tree (ticket 08). It defaults to true; setting it false keeps
	// cooperative cancellation and orphan detection while never issuing a
	// kill signal beyond the direct child.
	CancellationEscalation bool
	// Dims asigna cada dimensión canónica a un perfil de agente.
	Dims map[string]string
}

// GateConfig groups the gate subcommand configuration (R9, ticket 11).
type GateConfig struct {
	// DurableRuns routes `sentinel gate` through ONE root durable run with
	// validation and review logical jobs under it (ticket 11). TRUE is the
	// cutover default: this unit IS the switch-over. Setting it false is the
	// rollback seam — gate returns to the legacy orchestration byte-for-byte
	// while durable history already written stays fully inspectable via
	// `sentinel runs`.
	DurableRuns bool
}

// Valores posibles de CapabilityConfig.FailsWhen: cuándo se considera que una
// capability de validación falló. exit_code es el default histórico (el
// mismo criterio que ya usan lint_commands/test_commands/build_commands).
const (
	FailsWhenExitCode       = "exit_code"
	FailsWhenOutputNotEmpty = "output_not_empty"
)

// Valores posibles de ValidationConfig.Mode: dónde se ejecutan las
// capabilities de validación.
const (
	ModeWorktree = "worktree"
	ModeInplace  = "inplace"
)

// marcadorPaquetes es el marcador literal que todo scoped_command debe
// contener: el ejecutor (fuera del alcance de esta tarea) lo sustituye por
// los paquetes a los que se acota la validación.
const marcadorPaquetes = "{packages}"

// CapabilityConfig describe un chequeo de validación configurable por el
// usuario (T1.2): comando a ejecutar, criterio de fallo y, opcionalmente, una
// variante acotada a un subconjunto de paquetes.
type CapabilityConfig struct {
	Command   string
	FailsWhen string
	// SupportsScope y ScopedCommand habilitan una variante del comando
	// acotada a los paquetes afectados (p. ej. tras un slice parcial).
	SupportsScope bool
	ScopedCommand string
	// Timeout en segundos; 0 significa "sin timeout explícito propio", el
	// ejecutor decide su default.
	Timeout int
}

// ValidationConfig agrupa las capabilities configurables por el usuario, los
// perfiles que las combinan por nombre y el modo de ejecución (T1.2).
type ValidationConfig struct {
	Capabilities map[string]CapabilityConfig
	// Profiles asigna un nombre de perfil de validación a la lista ordenada
	// de capabilities que agrupa (deben existir en Capabilities).
	Profiles map[string][]string
	Mode     string
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
	Validation ValidationConfig
	// Gate groups the gate subcommand configuration (ticket 11).
	Gate GateConfig
	// Change son las reglas de change.classes (T3.1) que consume
	// change.ClasificarPorRuta: el orden ES la precedencia. Sin struct
	// envoltorio porque no agrupa nada más que esto (revisión de T3.1).
	Change []change.Regla
	// CommitLanguage fija el idioma de los mensajes de commit que genera el
	// agente (T0.13). Por defecto, el del historial del repositorio.
	CommitLanguage string
	// RequestExternalAgentDiff permite al repositorio solicitar generación
	// semántica externa. No representa consentimiento personal.
	RequestExternalAgentDiff bool
	LintCommands             []string
	TestCommands             []string
	BuildCommands            []string
}

// DimensionesPorDefecto son las seis dimensiones canónicas de auditoría.
var DimensionesPorDefecto = []string{"logic", "style", "design", "tests", "security", "spec"}

func configuracionPorDefecto() Config {
	return Config{
		ActiveAgent: "auto",
		// "es" y no agentadapter.IdiomaPorDefecto: agentadapter ya importa
		// config, así que referenciarlo aquí crearía un ciclo. Los tests de
		// agentadapter fijan que ambos valores coinciden.
		CommitLanguage:           "es",
		RequestExternalAgentDiff: false,
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
			// Evidence admission is default-on at cutover (ticket 07): the
			// rollback seam is setting it to false, never leaving it unset.
			EvidenceAdmission: true,
			// Bounded cancellation escalation is default-on (ticket 08): the
			// rollback seam is setting it to false, which restricts every
			// kill to the direct child.
			CancellationEscalation: true,
			Dims: map[string]string{
				"spec":     "cheap",
				"style":    "cheap",
				"tests":    "normal",
				"logic":    "normal",
				"design":   "deep",
				"security": "deep",
			},
		},
		Validation: ValidationConfig{
			Capabilities: map[string]CapabilityConfig{},
			Profiles:     map[string][]string{},
			Mode:         ModeWorktree,
		},
		// Gate durable runs are default-on at cutover (ticket 11): the
		// rollback seam is an explicit false, never leaving it unset.
		Gate:          GateConfig{DurableRuns: true},
		LintCommands:  []string{},
		TestCommands:  []string{},
		BuildCommands: []string{},
		Change:        change.ReglasPorDefecto(),
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

	traducirComandosLegadoACapabilities(&cfg)

	return cfg
}

// CargarConfiguracionLocalEstricta carga la configuración con la misma
// precedencia que CargarConfiguracionLocal (defaults -> global ->
// per-proyecto) pero SIN descartar en silencio el error de aplicarDesdeRuta:
// una clave desconocida en el yml (global o per-proyecto) se propaga con
// archivo y línea (T1.1), en vez de ignorarse.
//
// Requisito añadido por el orquestador para el criterio de salida #4 de la
// fase F1 ("una clave desconocida en vassentinel.yml produce error explícito
// con la línea, no silencio"): 'gate' (T1.7) es el primer punto de entrada
// donde esto debe ser visible, por ser el comando consolidado nuevo.
// CargarConfiguracionLocal NO cambia (mismo contrato sin error para no
// romper a sus llamadores actuales); esta función es la variante estricta
// para quien pueda propagar el error al operador.
func CargarConfiguracionLocalEstricta(worktreePath string) (Config, error) {
	cfg := configuracionPorDefecto()

	if ruta, err := rutaConfigGlobal(); err == nil {
		if err := aplicarDesdeRuta(&cfg, ruta); err != nil {
			return Config{}, err
		}
	}

	if err := aplicarDesdeRuta(&cfg, rutaConfigPerProyecto(worktreePath)); err != nil {
		return Config{}, err
	}

	traducirComandosLegadoACapabilities(&cfg)

	return cfg, nil
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
	Timeout                yaml.Node         `yaml:"timeout"`
	Parallel               yaml.Node         `yaml:"parallel"`
	CodeGraphContext       *bool             `yaml:"codegraph_context"`
	DurableRuns            *bool             `yaml:"durable_runs"`
	EvidenceAdmission      *bool             `yaml:"evidence_admission"`
	CancellationEscalation *bool             `yaml:"cancellation_escalation"`
	Dims                   map[string]string `yaml:"dims"`
}

// capabilityYAML es una entrada de validation.capabilities.<nombre> (T1.2).
// El nombre de la capability es una etiqueta libre elegida por el usuario
// (no un enum cerrado en Go); lo único fijo es esta forma. Command es
// obligatorio en la práctica (sin él la capability no ejecuta nada), pero
// esta tarea solo exige las tres validaciones de forma listadas en el
// diseño: quien declare una capability sin command se queda con la cadena
// vacía, sin fallar la carga.
type capabilityYAML struct {
	Command       *string `yaml:"command"`
	FailsWhen     *string `yaml:"fails_when"`
	SupportsScope *bool   `yaml:"supports_scope"`
	ScopedCommand *string `yaml:"scoped_command"`
	Timeout       *int    `yaml:"timeout"`
}

// validationYAML es la sección validation completa (T1.2): capabilities
// configurables por el usuario, perfiles que las agrupan por nombre y modo
// de ejecución.
type validationYAML struct {
	Capabilities map[string]capabilityYAML `yaml:"capabilities"`
	Profiles     map[string][]string       `yaml:"profiles"`
	Mode         *string                   `yaml:"mode"`
}

// gateYAML is the gate section (ticket 11): today only the durable switch,
// shaped with the same *bool pattern as evidence_admission (absent = the
// default is untouched; an explicit false is always honored).
type gateYAML struct {
	DurableRuns *bool `yaml:"durable_runs"`
}

// changeYAML es la sección change.classes: cada clase a su lista de globs.
// El mapa no preserva el orden textual; aplicarOrdenClases lo recupera.
type changeYAML struct {
	Classes map[string][]string `yaml:"classes"`
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
	Version                  *string                     `yaml:"version"`
	ActiveAgent              *string                     `yaml:"active_agent"`
	Agents                   map[string]agenteYAML       `yaml:"agents"`
	Profiles                 map[string]perfilGlobalYAML `yaml:"profiles"`
	Review                   *reviewYAML                 `yaml:"review"`
	Validation               *validationYAML             `yaml:"validation"`
	Gate                     *gateYAML                   `yaml:"gate"`
	CommitLanguage           *string                     `yaml:"commit_language"`
	RequestExternalAgentDiff *bool                       `yaml:"request_external_agent_diff"`
	LintCommands             []string                    `yaml:"lint_commands"`
	TestCommands             []string                    `yaml:"test_commands"`
	BuildCommands            []string                    `yaml:"build_commands"`
	Change                   *changeYAML                 `yaml:"change"`
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

	if err := aplicarValoresYAML(cfg, &raw); err != nil {
		return fmt.Errorf("%s: %w", ruta, err)
	}

	var orden ordenAgentesYAML
	if err := yaml.Unmarshal(datos, &orden); err == nil {
		aplicarOrdenAgentes(cfg, clavesEnOrden(&orden.Agents))
	}

	if raw.Change != nil {
		aplicarOrdenClases(cfg, datos, raw.Change.Classes)
	}

	return nil
}

// aplicarValoresYAML aplica sobre cfg los campos presentes en raw, campo a
// campo (solo sobreescribe lo que el archivo declara explícitamente).
// Devuelve error cuando validation.capabilities/profiles no respeta la forma
// exigida (ver aplicarValidacion): a diferencia del resto de secciones, aquí
// un valor inválido no puede ignorarse en silencio.
func aplicarValoresYAML(cfg *Config, raw *configYAML) error {
	if raw.ActiveAgent != nil {
		cfg.ActiveAgent = *raw.ActiveAgent
	}
	if raw.CommitLanguage != nil {
		cfg.CommitLanguage = *raw.CommitLanguage
	}
	if raw.RequestExternalAgentDiff != nil {
		cfg.RequestExternalAgentDiff = *raw.RequestExternalAgentDiff
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
		if raw.Review.CodeGraphContext != nil {
			cfg.Review.CodeGraphContext = *raw.Review.CodeGraphContext
		}
		if n, ok := decodificarEnteroPositivo(&raw.Review.Timeout); ok {
			cfg.Review.Timeout = time.Duration(n) * time.Second
		}
		if n, ok := decodificarEnteroPositivo(&raw.Review.Parallel); ok {
			cfg.Review.Parallel = n
		}
		for dim, perfilDim := range raw.Review.Dims {
			cfg.Review.Dims[dim] = perfilDim
		}
		if raw.Review.DurableRuns != nil {
			cfg.Review.DurableRuns = *raw.Review.DurableRuns
		}
		if raw.Review.EvidenceAdmission != nil {
			cfg.Review.EvidenceAdmission = *raw.Review.EvidenceAdmission
		}
		if raw.Review.CancellationEscalation != nil {
			cfg.Review.CancellationEscalation = *raw.Review.CancellationEscalation
		}
	}
	cfg.LintCommands = append(cfg.LintCommands, raw.LintCommands...)
	cfg.TestCommands = append(cfg.TestCommands, raw.TestCommands...)
	cfg.BuildCommands = append(cfg.BuildCommands, raw.BuildCommands...)

	if raw.Validation != nil {
		if err := aplicarValidacion(cfg, raw.Validation); err != nil {
			return err
		}
	}
	if raw.Gate != nil && raw.Gate.DurableRuns != nil {
		cfg.Gate.DurableRuns = *raw.Gate.DurableRuns
	}
	return nil
}

// RepositorioSolicitaDiffAgenteExterno lee solo la configuración versionada
// per-proyecto: la configuración global nunca puede activar esta capacidad.
func RepositorioSolicitaDiffAgenteExterno(worktreePath string) bool {
	cfg := configuracionPorDefecto()
	if err := aplicarDesdeRuta(&cfg, rutaConfigPerProyecto(worktreePath)); err != nil {
		return false
	}
	return cfg.RequestExternalAgentDiff
}

// aplicarValidacion aplica sobre cfg.Validation los campos presentes en raw y
// valida su forma. A diferencia del resto del parser (donde un valor
// inválido se ignora y queda el default), aquí una capability o un perfil mal
// formado producen un error explícito: un perfil que promete una capability
// que no existe, o una capability scoped sin scoped_command o sin el
// marcador {packages}, son errores de configuración que el operador debe
// corregir, no defaults silenciosos que oculten el problema.
func aplicarValidacion(cfg *Config, raw *validationYAML) error {
	for nombre, capRaw := range raw.Capabilities {
		capacidad := cfg.Validation.Capabilities[nombre]
		if capRaw.Command != nil {
			capacidad.Command = *capRaw.Command
		}
		if capRaw.FailsWhen != nil {
			// fails_when tiene un dominio cerrado (T1.1/T1.2 lo definen como
			// exit_code/output_not_empty): un typo como "exit-cede" no puede
			// aceptarse en silencio, degradaría el criterio de fallo en tiempo
			// de ejecución sin que el operador se entere (hallazgo del
			// orquestador, fuera del texto original de la ficha).
			if *capRaw.FailsWhen != FailsWhenExitCode && *capRaw.FailsWhen != FailsWhenOutputNotEmpty {
				return fmt.Errorf("validation.capabilities.%s: fails_when %q inválido (valores válidos: %q, %q)",
					nombre, *capRaw.FailsWhen, FailsWhenExitCode, FailsWhenOutputNotEmpty)
			}
			capacidad.FailsWhen = *capRaw.FailsWhen
		} else if capacidad.FailsWhen == "" {
			capacidad.FailsWhen = FailsWhenExitCode
		}
		if capRaw.SupportsScope != nil {
			capacidad.SupportsScope = *capRaw.SupportsScope
		}
		if capRaw.ScopedCommand != nil {
			capacidad.ScopedCommand = *capRaw.ScopedCommand
		}
		if capRaw.Timeout != nil {
			capacidad.Timeout = *capRaw.Timeout
		}
		if capacidad.SupportsScope && capacidad.ScopedCommand == "" {
			return fmt.Errorf("validation.capabilities.%s: supports_scope=true requiere scoped_command", nombre)
		}
		if capacidad.ScopedCommand != "" && !strings.Contains(capacidad.ScopedCommand, marcadorPaquetes) {
			return fmt.Errorf("validation.capabilities.%s: scoped_command debe contener el marcador %s", nombre, marcadorPaquetes)
		}
		cfg.Validation.Capabilities[nombre] = capacidad
	}
	for perfil, nombresCapabilities := range raw.Profiles {
		for _, nombreCap := range nombresCapabilities {
			if _, existe := cfg.Validation.Capabilities[nombreCap]; !existe {
				return fmt.Errorf("validation.profiles.%s: la capability %q no está declarada en validation.capabilities", perfil, nombreCap)
			}
		}
		cfg.Validation.Profiles[perfil] = nombresCapabilities
	}
	if raw.Mode != nil {
		// mode también tiene un dominio cerrado (worktree/inplace): mismo
		// motivo que fails_when, arriba.
		if *raw.Mode != ModeWorktree && *raw.Mode != ModeInplace {
			return fmt.Errorf("validation.mode: %q inválido (valores válidos: %q, %q)",
				*raw.Mode, ModeWorktree, ModeInplace)
		}
		cfg.Validation.Mode = *raw.Mode
	}
	return nil
}

// traducirComandosLegadoACapabilities genera capabilities implícitas a
// partir de lint_commands/test_commands/build_commands cuando el yml no
// declara ninguna validation.capabilities explícita. Decisión: una config
// vieja que solo conoce el esquema de comandos anterior a T1.2 debe seguir
// produciendo un resultado utilizable para quien pida "las capabilities
// configuradas" (tarea futura), sin obligar al usuario a reescribir su yml.
// Si el usuario ya declaró validation.capabilities, esa declaración manda:
// no se mezclan dos fuentes de verdad para las mismas capabilities. Los
// comandos de una misma lista se combinan con "&&" en un único Command
// porque CapabilityConfig modela un comando, no una lista.
func traducirComandosLegadoACapabilities(cfg *Config) {
	if len(cfg.Validation.Capabilities) > 0 {
		return
	}
	agregarCapabilityImplicita(cfg, "lint", cfg.LintCommands)
	agregarCapabilityImplicita(cfg, "unit_test", cfg.TestCommands)
	agregarCapabilityImplicita(cfg, "build", cfg.BuildCommands)
}

// agregarCapabilityImplicita añade a cfg.Validation.Capabilities una entrada
// con nombre a partir de comandos, si hay al menos uno.
func agregarCapabilityImplicita(cfg *Config, nombre string, comandos []string) {
	if len(comandos) == 0 {
		return
	}
	if cfg.Validation.Capabilities == nil {
		cfg.Validation.Capabilities = map[string]CapabilityConfig{}
	}
	cfg.Validation.Capabilities[nombre] = CapabilityConfig{
		Command:   strings.Join(comandos, " && "),
		FailsWhen: FailsWhenExitCode,
	}
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

// ordenClasesYAML lee sin KnownFields el orden textual de change.classes
// (mismo motivo que ordenAgentesYAML).
type ordenClasesYAML struct {
	Change struct {
		Classes yaml.Node `yaml:"classes"`
	} `yaml:"change"`
}

// aplicarOrdenClases reconstruye cfg.Change en el orden textual del archivo.
// Al declarar change.classes el usuario reemplaza los defaults (mismo
// criterio que active_agent): no se fusionan dos fuentes de reglas.
//
// classes nil (la clave "classes" no aparece bajo "change:") deja los
// defaults intactos; classes no nil pero vacío ("classes: {}", declarado a
// propósito) vacía cfg.Change: son dos intenciones distintas del usuario y
// antes se confundían (revisión de T3.1, ambas caían en el mismo "return").
func aplicarOrdenClases(cfg *Config, datos []byte, classes map[string][]string) {
	if classes == nil {
		return
	}
	var orden ordenClasesYAML
	if err := yaml.Unmarshal(datos, &orden); err != nil {
		return
	}
	claves := clavesEnOrden(&orden.Change.Classes)
	reglas := make([]change.Regla, 0, len(claves))
	for _, clave := range claves {
		reglas = append(reglas, change.Regla{Clase: clave, Patrones: classes[clave]})
	}
	cfg.Change = reglas
}
