// Package validation ejecuta perfiles de validación (T1.3): capabilities
// configurables por el usuario (config.ValidationConfig, T1.2), agrupadas en
// perfiles, con una variante opcional acotada a un subconjunto de paquetes.
// Es el sucesor, para el nuevo modelo de capabilities/perfiles, de la
// verificación dual de internal/ops.Verificar; internal/ops queda intacto en
// esta tarea (F2 traerá el finding v2 completo).
package validation

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/agentshell"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/graph"
)

// Alcance de una ValidationRun: completo (command sin acotar) o parcial
// (scoped_command acotado a los paquetes afectados).
const (
	AlcanceCompleto = "completo"
	AlcanceParcial  = "parcial"
)

// capabilityDelegada identifica, dentro de ValidationRun.Capability, los runs
// que vienen del contrato tested delegado al agente (perfil sin capabilities
// configuradas), no de una capability real del yml.
const capabilityDelegada = "delegado"

// marcadorPaquetes es el marcador literal que scoped_command debe contener;
// coincide con el que documenta config.CapabilityConfig.ScopedCommand.
const marcadorPaquetes = "{packages}"

// ValidationRun es el resultado de ejecutar una capability. Forma exacta
// pedida por la ficha de T1.3: la evidencia de un finding sale de aquí.
type ValidationRun struct {
	Capability    string
	Comando       string
	Alcance       string // completo | parcial
	MotivoAlcance string
	Exit          int
	DuracionMs    int64
	Salida        string // recortada, para la evidencia del finding
}

// Hallazgo es el finding mínimo de validación de esta fase: el tipo v2
// completo (evidencia estructurada, severidad tipada) llega en F2. Aquí basta
// con no inventar nunca un PASS y mostrar la salida real del comando fallido.
type Hallazgo struct {
	Source     string // fijo "validation"
	Severity   string // fijo "CRITICAL": sin grados de severidad en esta fase
	Capability string
	Comando    string
	Evidencia  string
}

// EjecutorComando ejecuta un comando y devuelve su exit code y la salida
// combinada (stdout+stderr): fails_when=output_not_empty necesita la salida
// real, no solo el exit code (caso "gofmt -l .", que sale 0 con archivos
// listados).
type EjecutorComando func(comando string) (exit int, salida string, err error)

// OpcionesEjecucion configura EjecutarPerfil. Ejecutar y Agente son
// inyectables (misma costura que internal/ops.OpcionesVerificar) para poder
// testear sin lanzar procesos reales ni depender de un agente de verdad.
type OpcionesEjecucion struct {
	Worktree string
	Cfg      config.Config
	// ProveedorGraph se invoca únicamente sobre el snapshot congelado. Nil
	// conserva validación completa, igual que un error o análisis incompleto.
	ProveedorGraph func(snapshot, treeOID string) graph.GraphProvider
	autorizacion   graph.AutorizacionAlcance
	// Ejecutar (nil = shell real) corre el comando y devuelve exit + salida.
	Ejecutar EjecutorComando
	// Agente es la vía de delegación (contrato tested), usada solo cuando el
	// perfil no tiene capabilities configuradas; nil = no delegable.
	Agente agentadapter.AdaptadorPrompt
}

// EjecutarPerfil corre las capabilities del perfil en orden. Solo una
// graph.AutorizacionAlcance válida puede seleccionar ScopedCommand; cualquier
// otro estado usa Command completo. Si el perfil no tiene capabilities (yml
// sin capabilities configuradas), delega al agente con el contrato tested,
// igual que hacía internal/ops.Verificar cuando no había lint/test/build
// commands: es el mismo hueco de configuración, ahora expresado como perfil
// vacío en vez de listas de comandos sueltas.
func EjecutarPerfil(perfil string, opts OpcionesEjecucion) ([]ValidationRun, error) {
	nombres := opts.Cfg.Validation.Profiles[perfil]
	if len(nombres) == 0 {
		return delegarSinCapabilities(opts)
	}

	ejecutar := opts.Ejecutar
	if ejecutar == nil {
		ejecutar = func(comando string) (int, string, error) {
			return agentshell.Ejecutar(opts.Worktree, comando)
		}
	}

	runs := make([]ValidationRun, 0, len(nombres))
	for _, nombre := range nombres {
		capacidad, ok := opts.Cfg.Validation.Capabilities[nombre]
		if !ok {
			return runs, fmt.Errorf("el perfil %q referencia la capability %q, que no está configurada", perfil, nombre)
		}
		comando, alcanceEtiqueta, motivo := resolverComando(capacidad, opts.autorizacion)

		inicio := time.Now()
		exit, salida, err := ejecutar(comando)
		duracion := time.Since(inicio).Milliseconds()
		if err != nil {
			return runs, fmt.Errorf("no se pudo ejecutar %q (capability %q): %w", comando, nombre, err)
		}
		runs = append(runs, ValidationRun{
			Capability:    nombre,
			Comando:       comando,
			Alcance:       alcanceEtiqueta,
			MotivoAlcance: motivo,
			Exit:          exit,
			DuracionMs:    duracion,
			Salida:        salida,
		})
	}
	return runs, nil
}

// elementoAlcanceValido es la lista blanca de caracteres seguros para un
// elemento de alcance (nombre de paquete/ruta Go): letras, dígitos, /, ., _,
// -. A diferencia de Command/ScopedCommand (literales del vassentinel.yml del
// usuario, de confianza por diseño, ver ejecutarShellCombinado), alcance se
// calcula en tiempo de ejecución y se interpola sin comillas en un comando
// que corre después por sh -c/cmd /c: cualquier otro carácter (;, `, $, "
// etc.) permitiría inyectar un comando arbitrario. No se intenta "escapar"
// el string para el shell de destino (frágil y distinto entre cmd/sh):
// rechazar con error lo que no encaje en la lista blanca es más simple y más
// seguro.
var elementoAlcanceValido = regexp.MustCompile(`^[A-Za-z0-9/._-]+$`)

// resolverComando decide qué variante usar: scoped solo con autorización opaca
// del grafo Y soporte declarado; si no, el comando completo exacto. Antes de
// interpolar alcance
// en scoped_command, valida cada elemento contra elementoAlcanceValido; si
// alguno no encaja, conserva Command exacto (nunca construye un comando a
// partir de un elemento sin validar).
func resolverComando(capacidad config.CapabilityConfig, autorizacion graph.AutorizacionAlcance) (comando, etiqueta, motivo string) {
	if !capacidad.SupportsScope {
		return capacidad.Command, AlcanceCompleto, "comando completo: la capability no declara supports_scope"
	}
	if !autorizacion.Autorizada() {
		return capacidad.Command, AlcanceCompleto, "comando completo: grafo ausente, con error, incompleto o no autorizado"
	}
	if !strings.Contains(capacidad.ScopedCommand, marcadorPaquetes) {
		return capacidad.Command, AlcanceCompleto, "comando completo: scoped_command inválido"
	}
	alcance := autorizacion.Paquetes()
	for _, elemento := range alcance {
		if !elementoAlcanceValido.MatchString(elemento) {
			return capacidad.Command, AlcanceCompleto, fmt.Sprintf("comando completo: paquete autorizado %q no es seguro para interpolación shell", elemento)
		}
	}
	acotado := strings.ReplaceAll(capacidad.ScopedCommand, marcadorPaquetes, strings.Join(alcance, " "))
	return acotado, AlcanceParcial, fmt.Sprintf("grafo completo autorizó %d paquete(s) afectado(s): %s", len(alcance), strings.Join(autorizacion.Explicacion(), "; "))
}

// Fallo determina si una ValidationRun se considera fallida según fails_when
// (config.FailsWhenExitCode es el default, incluido el caso vacío).
//
// Un run delegado (Capability == capabilityDelegada) NUNCA falla por esta
// vía, y esa decisión es explícita, no un efecto colateral de que Exit se
// quede en su valor cero: capabilityDelegada es un marcador interno, no una
// capability real del yml, así que no existe un fails_when del que partir
// para él. El contrato tested del agente es evidencia narrativa (aviso,
// nunca bloqueo, mismo criterio que internal/ops.Verificar), jamás un exit
// code verificado localmente; tratarlo como si lo fuera sería inventar un
// PASS o un FAIL según convenga. Por eso se corta aquí antes de mirar
// capacidad.FailsWhen, en vez de confiar en que Exit valga 0.
func Fallo(run ValidationRun, capacidad config.CapabilityConfig) bool {
	if run.Capability == capabilityDelegada {
		return false
	}
	if capacidad.FailsWhen == config.FailsWhenOutputNotEmpty {
		return strings.TrimSpace(run.Salida) != ""
	}
	return run.Exit != 0
}

// Hallazgos traduce las ValidationRun fallidas (según Fallo) a findings
// mínimos: severidad CRITICAL fija (sin grados en esta fase) y la salida real
// como evidencia, nunca un PASS inventado.
//
// Los runs delegados se excluyen aquí, ANTES de mirar Fallo: capabilityDelegada
// no es una clave real de "capacidades" (viene del contrato tested del
// agente, no del yml), así que no hay fails_when que aplicarles. Son
// evidencia informativa del contrato tested, no verificación local, y por
// diseño explícito nunca pueden producir un Hallazgo por este camino (ver el
// comentario de Fallo).
func Hallazgos(runs []ValidationRun, capacidades map[string]config.CapabilityConfig) []Hallazgo {
	var hallazgos []Hallazgo
	for _, run := range runs {
		if run.Capability == capabilityDelegada {
			continue
		}
		if !Fallo(run, capacidades[run.Capability]) {
			continue
		}
		hallazgos = append(hallazgos, Hallazgo{
			Source:     "validation",
			Severity:   "CRITICAL",
			Capability: run.Capability,
			Comando:    run.Comando,
			Evidencia:  run.Salida,
		})
	}
	return hallazgos
}

// delegarSinCapabilities es el fallback cuando el perfil no tiene
// capabilities configuradas: delega al agente con el mismo contrato tested
// que ya usaba internal/ops.Verificar. La validación nunca bloquea: sin
// agente, o si el agente no responde o rompe el contrato, degrada a una lista
// vacía sin error (mismo criterio de "aviso, nunca bloqueo" de internal/ops).
func delegarSinCapabilities(opts OpcionesEjecucion) ([]ValidationRun, error) {
	if opts.Agente == nil {
		return nil, nil
	}
	salida, err := opts.Agente.EjecutarPrompt(promptDelegacion())
	if err != nil {
		return nil, nil
	}
	tested, err := agentshell.ParsearContratoTested(salida)
	if err != nil {
		return nil, nil
	}
	runs := make([]ValidationRun, 0, len(tested))
	for _, comando := range tested {
		runs = append(runs, ValidationRun{
			Capability:    capabilityDelegada,
			Comando:       comando,
			Alcance:       AlcanceCompleto,
			MotivoAlcance: "delegado al agente (contrato tested): el perfil no tiene capabilities configuradas",
		})
	}
	return runs, nil
}

// promptDelegacion es el mismo contrato tested que internal/ops.Verificar:
// shell libre, una línea final "tested: <comando>; ...", o unavailable.
func promptDelegacion() string {
	return "Eres el paso de validación de VAS Sentinel.\n" +
		"Tienes shell libre: descubre las pruebas del proyecto (Makefile, go.mod, scripts, convenciones del lenguaje) y ejecútalas.\n" +
		"Devuelve SOLO una línea final con el contrato tested, con los comandos ejecutados separados por ;:\n" +
		"tested: <comando>; <comando>\n" +
		"Si no puedes ejecutar las pruebas, devuelve SOLO: unavailable"
}

// El parseo del contrato tested y la ejecución por shell con salida
// combinada viven en internal/agentshell: son la misma lógica, byte a byte
// en el caso del parseo, que ya usaba internal/ops.Verificar. Antes de esta
// extracción cada paquete tenía su propia copia; ahora ambos importan
// internal/agentshell (sin dependencias de ops ni de validation, así que no
// se crea un ciclo).
