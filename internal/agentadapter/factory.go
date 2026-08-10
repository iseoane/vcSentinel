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
			return nuevoAdaptador(cfg, nombre, "")
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

	// Camino auto: cadena con un adaptador por cada agente disponible, en el
	// orden de configuración del yml (fallback en cadena por petición).
	return construirCadena(cfg, nombres, ""), nil
}

// NewAgentAdapterParaMensaje construye el adaptador para generar los mensajes
// de commit de 'sentinel slice'. Igual que NewAgentAdapter, pero cada agente
// resuelve su modelo/esfuerzo con el perfil anidado "commit" (razonamiento bajo)
// en lugar del modelo/esfuerzo base: nombrar un commit no necesita el mismo
// razonamiento que el resto de la tarea. Cuando el agente no define el perfil,
// ResolverPerfilAgente cae al modelo/esfuerzo base y la config queda igual.
func NewAgentAdapterParaMensaje(worktreePath string) (AgentAdapter, error) {
	cfg := config.CargarConfiguracionLocal(worktreePath)

	nombre := os.Getenv("MY_SUB_AGENT")
	if nombre == "" {
		nombre = cfg.ActiveAgent
	}

	if nombre != "auto" {
		if _, existe := cfg.Agents[nombre]; existe {
			return nuevoAdaptador(cfg, nombre, "commit")
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

	// Camino auto: cada adaptador de la cadena resuelve SU perfil commit.
	return construirCadena(cfg, nombres, "commit"), nil
}

// NewAgentAdapterNamed construye un adaptador CLI para un nombre de agente
// explícito, sin resolución automática. Devuelve error si el nombre no está
// configurado en vassentinel.yml.
func NewAgentAdapterNamed(worktreePath string, nombre string) (AgentAdapter, error) {
	cfg := config.CargarConfiguracionLocal(worktreePath)
	return nuevoAdaptador(cfg, nombre, "")
}

// NewAgentAdapterNamedParaMensaje construye un adaptador CLI para un nombre de
// agente explícito, con el perfil "commit" para los mensajes de slice (aplica
// razonamiento bajo y cae al modelo base del agente si el perfil no existe).
// Devuelve error si el nombre no está configurado en vassentinel.yml.
func NewAgentAdapterNamedParaMensaje(worktreePath string, nombre string) (AgentAdapter, error) {
	cfg := config.CargarConfiguracionLocal(worktreePath)
	return nuevoAdaptador(cfg, nombre, "commit")
}

// nuevoAdaptador construye el adaptador CLI de un agente concreto, resolviendo
// el binario y la configuración de modelo/esfuerzo. Si perfil es no vacío, la
// configuración se resuelve con el perfil anidado del agente (con fallback
// al modelo/esfuerzo base cuando el perfil no los define); si perfil es vacío
// se usa la configuración base del agente tal cual (compatibilidad con
// NewAgentAdapter/NewAgentAdapterNamed).
func nuevoAdaptador(cfg config.Config, nombre, perfil string) (AgentAdapter, error) {
	if _, existe := cfg.Agents[nombre]; !existe {
		return nil, fmt.Errorf("el agente %q no está configurado en vassentinel.yml", nombre)
	}
	return &CLIAdapter{
		BinaryName:     resolverBinarioReal(nombre),
		Config:         configAgente(cfg, nombre, perfil),
		CommitLanguage: cfg.CommitLanguage,
	}, nil
}

// construirCadena crea la cadena de adaptadores del camino auto: un CLIAdapter
// por cada agente disponible, en el orden recibido, cada uno con su
// configuración (su perfil commit si perfil es no vacío).
func construirCadena(cfg config.Config, disponibles []string, perfil string) *CadenaAdaptador {
	cadena := &CadenaAdaptador{}
	for _, agente := range disponibles {
		cadena.adaptadores = append(cadena.adaptadores, &CLIAdapter{
			BinaryName:     resolverBinarioReal(agente),
			Config:         configAgente(cfg, agente, perfil),
			CommitLanguage: cfg.CommitLanguage,
		})
	}
	return cadena
}

// configAgente resuelve la configuración de modelo/esfuerzo de un agente. Si
// perfil es "" devuelve la config base del agente; si no, aplica
// config.ResolverPerfilAgente (perfil commit con fallback al agente).
func configAgente(cfg config.Config, nombre, perfil string) config.AgentConfig {
	if perfil == "" {
		return cfg.Agents[nombre]
	}
	modelo, esfuerzo := config.ResolverPerfilAgente(cfg, nombre, perfil)
	return config.AgentConfig{Model: modelo, ReasoningEffort: esfuerzo}
}

// NombresAdaptadoresDisponibles devuelve los nombres de agentes configurados en
// vassentinel.yml cuyo binario está disponible en el PATH, en el orden en que
// aparecen en el yml (alfabético si no hay orden declarado).
func NombresAdaptadoresDisponibles(worktreePath string) []string {
	cfg := config.CargarConfiguracionLocal(worktreePath)
	return nombresAgentesEnPATH(cfg)
}

// NuevoAdaptadorConPerfil construye el adaptador que corresponde a un perfil
// resuelto: resuelve el binario (perfil -> active_agent -> auto), completa el
// modelo y esfuerzo con los del agente cuando el perfil no los define y fija
// el timeout de auditoría. En el camino auto devuelve un CadenaAdaptador con
// un adaptador por agente disponible, cada uno con el perfil de SU agente.
func NuevoAdaptadorConPerfil(cfg config.Config, perfil config.PerfilResuelto) (AdaptadorPrompt, error) {
	nombre := perfil.Binario
	if nombre == "" {
		nombre = cfg.ActiveAgent
	}
	if nombre == "" || nombre == "auto" {
		disponibles := nombresAgentesEnPATH(cfg)
		if len(disponibles) == 0 {
			return nil, fmt.Errorf("ningun agente configurado en vassentinel.yml esta disponible en el PATH")
		}
		return construirCadenaPerfil(cfg, disponibles, perfil), nil
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
		BinaryName:     binario,
		Config:         config.AgentConfig{Model: modelo, ReasoningEffort: esfuerzo},
		CommitLanguage: cfg.CommitLanguage,
		Timeout:        cfg.Review.Timeout,
	}, nil
}

// construirCadenaPerfil crea la cadena de adaptadores del camino auto: un
// CLIAdapter por cada agente disponible (en el orden recibido), cada uno con
// el modelo/esfuerzo de SU perfil anidado, no el del perfil resuelto.
func construirCadenaPerfil(cfg config.Config, disponibles []string, perfil config.PerfilResuelto) *CadenaAdaptador {
	cadena := &CadenaAdaptador{}
	for _, agente := range disponibles {
		modelo, esfuerzo := config.ResolverPerfilAgente(cfg, agente, perfil.Nombre)
		cadena.adaptadores = append(cadena.adaptadores, &CLIAdapter{
			BinaryName:     resolverBinarioReal(agente),
			Config:         config.AgentConfig{Model: modelo, ReasoningEffort: esfuerzo},
			CommitLanguage: cfg.CommitLanguage,
			Timeout:        cfg.Review.Timeout,
		})
	}
	return cadena
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

// nombresAgentesEnPATH filtra los agentes configurados que existen en el PATH.
// Si cfg.AgentOrder tiene elementos, conserva ese orden; si está vacío (por
// ejemplo, una Config construida a mano en tests) cae al orden alfabético.
// Es la lógica compartida por la resolución automática de NewAgentAdapter, por
// NuevoAdaptadorConPerfil y por NombresAdaptadoresDisponibles.
func nombresAgentesEnPATH(cfg config.Config) []string {
	orden := cfg.AgentOrder
	if len(orden) == 0 {
		orden = make([]string, 0, len(cfg.Agents))
		for clave := range cfg.Agents {
			orden = append(orden, clave)
		}
		sort.Strings(orden)
	}
	nombres := make([]string, 0, len(orden))
	for _, clave := range orden {
		if _, err := exec.LookPath(clave); err == nil {
			nombres = append(nombres, clave)
		}
	}
	return nombres
}
