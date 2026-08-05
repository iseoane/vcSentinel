package ops

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
)

// Modos de la verificación dual (guía §12.3): determinista cuando hay
// comandos configurados; delegado / omitido / configurar son las salidas del
// aviso que se ofrece cuando no hay test_commands en el yml.
const (
	ModoDeterminista = "determinista"
	ModoDelegado     = "delegado"
	ModoOmitido      = "omitido"
	ModoConfigurar   = "configurar"
)

// errSinAgente es el motivo de degradación cuando el agente de verificación
// no responde: aviso, nunca bloqueo (riesgo declarado de la guía).
var errSinAgente = errors.New("el agente de verificación no respondió")

// ResultadoComando es el exit code real de un comando configurado.
type ResultadoComando struct {
	Comando string
	Exit    int
}

// ResultadoVerificacion describe qué se ejecutó (o por qué no): la plantilla
// PR solo puede mostrar evidencia real, nunca un PASS inventado.
type ResultadoVerificacion struct {
	Modo     string // determinista | delegado | omitido | configurar
	Comandos []ResultadoComando
	// Tested es EVIDENCIA mostrada en la plantilla, nunca una lista para
	// volver a ejecutar: los comandos ya los lanzó el agente con shell libre.
	Tested []string
	Motivo string // no_configurado | omitido | agente_no_respondio | ...
}

// OpcionesVerificar configura la verificación. Ejecutar y Preguntar son
// inyectables para poder testear sin lanzar comandos reales ni leer stdin.
type OpcionesVerificar struct {
	Worktree string
	// GitDir, si no está vacío, registra el evento pr-verify (§13) al
	// terminar: detalle {cmd, exit} por comando, tested o motivo.
	GitDir string
	Cfg    config.Config
	// Ejecutar (nil = shell real) devuelve el exit code de un comando.
	Ejecutar func(comando string) (int, error)
	// Agente es la vía de delegación (contrato tested); nil = no delegable.
	Agente agentadapter.AdaptadorPrompt
	// Preguntar hace el aviso con elección; nil = no interactivo → omitir.
	Preguntar func(aviso string) (string, error)
}

// Verificar implementa la verificación dual: vía 1 determinista si hay
// comandos lint/test/build configurados; si no, aviso con elección
// (configurar | omitir | delegar) y delegación con shell libre si se elige.
// Con GitDir, registra el evento pr-verify (§13) tras el cálculo.
func Verificar(opts OpcionesVerificar) (ResultadoVerificacion, error) {
	verif, err := verificarInterno(opts)
	if err != nil || opts.GitDir == "" {
		return verif, err
	}
	if err := registrarEventoPrVerify(opts.GitDir, opts.Worktree, verif); err != nil {
		return verif, fmt.Errorf("verificación en modo %s, pero no se pudo registrar el evento pr-verify: %w", verif.Modo, err)
	}
	return verif, nil
}

// verificarInterno calcula el resultado sin efectos secundarios (sin evento).
func verificarInterno(opts OpcionesVerificar) (ResultadoVerificacion, error) {
	comandos := append([]string{}, opts.Cfg.LintCommands...)
	comandos = append(comandos, opts.Cfg.TestCommands...)
	comandos = append(comandos, opts.Cfg.BuildCommands...)

	if len(comandos) > 0 {
		ejecutar := opts.Ejecutar
		if ejecutar == nil {
			ejecutar = func(comando string) (int, error) {
				return ejecutarShell(opts.Worktree, comando)
			}
		}
		verif := ResultadoVerificacion{Modo: ModoDeterminista}
		for _, comando := range comandos {
			exit, err := ejecutar(comando)
			if err != nil {
				return verif, fmt.Errorf("no se pudo ejecutar %q: %w", comando, err)
			}
			verif.Comandos = append(verif.Comandos, ResultadoComando{Comando: comando, Exit: exit})
		}
		return verif, nil
	}

	// Sin comandos configurados: aviso con elección (guía §12.3).
	if opts.Preguntar == nil {
		return ResultadoVerificacion{Modo: ModoOmitido, Motivo: "no_configurado"}, nil
	}
	ciDetectada := false
	if opts.Worktree != "" {
		ciDetectada = git.DetectarCI(opts.Worktree)
	}
	respuesta, err := opts.Preguntar(textoAvisoVerificacion(ciDetectada))
	if err != nil {
		// La verificación nunca bloquea: si el aviso no se pudo leer se
		// degrada a omitido con motivo propio y sin error.
		return ResultadoVerificacion{Modo: ModoOmitido, Motivo: "aviso_no_respondio"}, nil
	}
	switch strings.ToLower(strings.TrimSpace(respuesta)) {
	case "configurar", "c":
		return ResultadoVerificacion{Modo: ModoConfigurar}, nil
	case "omitir", "o":
		return ResultadoVerificacion{Modo: ModoOmitido, Motivo: "omitido"}, nil
	case "delegar", "d":
		if opts.Agente == nil {
			return ResultadoVerificacion{Modo: ModoOmitido, Motivo: "sin_agente"}, nil
		}
		salida, err := opts.Agente.EjecutarPrompt(promptVerificacion())
		if err != nil {
			return ResultadoVerificacion{Modo: ModoOmitido, Motivo: "agente_no_respondio"}, nil
		}
		tested, err := parsearContratoTested(salida)
		if err != nil {
			// Distinguir la incapacidad del agente (unavailable) de una
			// violación del protocolo: motivos distintos para el evento.
			if strings.Contains(strings.ToLower(salida), "unavailable") {
				return ResultadoVerificacion{Modo: ModoOmitido, Motivo: "agente_unavailable"}, nil
			}
			return ResultadoVerificacion{Modo: ModoOmitido, Motivo: "contrato_invalido"}, nil
		}
		return ResultadoVerificacion{Modo: ModoDelegado, Tested: tested}, nil
	}
	return ResultadoVerificacion{Modo: ModoOmitido, Motivo: "eleccion_invalida"}, nil
}

// textoAvisoVerificacion describe la situación y las tres opciones; distingue
// si hay CI detectada (la verificación externa cubrirá el PR).
func textoAvisoVerificacion(ciDetectada bool) string {
	var b strings.Builder
	b.WriteString("No hay comandos de verificación configurados (lint_commands/test_commands/build_commands en vassentinel.yml).\n")
	if ciDetectada {
		b.WriteString("Se detectó CI en el repositorio: el PR tendrá verificación automática externa.\n")
	} else {
		b.WriteString("No se detectó CI: este PR no tendrá ninguna verificación automática.\n")
	}
	b.WriteString("Responde: configurar (parar y editar el yml), omitir (continuar sin ejecutar tests) o delegar (ejecutar las pruebas con el agente).")
	return b.String()
}

// promptVerificacion es el prompt DISTINTO al de auditoría: la auditoría
// prohíbe herramientas (para que el agente no se cuelgue con builds/tests);
// esta delegación necesita shell libre y es un paso dedicado posterior a la
// revisión. Devuelve el contrato tested con los comandos ejecutados.
func promptVerificacion() string {
	return "Eres el paso de verificación de VAS Sentinel.\n" +
		"Tienes shell libre: descubre las pruebas del proyecto (Makefile, go.mod, scripts, convenciones del lenguaje) y ejecútalas.\n" +
		"Devuelve SOLO una línea final con el contrato tested, con los comandos ejecutados separados por ;:\n" +
		"tested: <comando>; <comando>\n" +
		"Si no puedes ejecutar las pruebas, devuelve SOLO: unavailable"
}

// parsearContratoTested extrae los comandos de la línea "tested: ..." de la
// salida del agente y rechaza unavailable / ausencia de contrato.
func parsearContratoTested(salida string) ([]string, error) {
	if strings.Contains(strings.ToLower(salida), "unavailable") {
		return nil, errors.New("el agente no pudo ejecutar las pruebas (unavailable)")
	}
	for _, linea := range strings.Split(salida, "\n") {
		recortada := strings.TrimSpace(linea)
		idx := strings.Index(recortada, "tested:")
		if idx < 0 {
			continue
		}
		resto := strings.TrimSpace(recortada[idx+len("tested:"):])
		var comandos []string
		for _, c := range strings.Split(resto, ";") {
			c = strings.TrimSpace(c)
			if c != "" {
				comandos = append(comandos, c)
			}
		}
		if len(comandos) == 0 {
			return nil, errors.New("contrato tested vacío")
		}
		return comandos, nil
	}
	return nil, errors.New("la salida no contiene un contrato tested")
}

// ejecutarShell lanza un comando a través de la shell del sistema en el
// worktree y devuelve su exit code (0 en éxito; -1 si no fue un fallo del
// comando sino de la ejecución). Los comandos vienen del vassentinel.yml del
// usuario: ejecutar con shell es el diseño (confianza equivalente al propio
// yml); no sanitizar aquí.
func ejecutarShell(worktree, comando string) (int, error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/c", comando)
	} else {
		cmd = exec.Command("sh", "-c", comando)
	}
	if worktree != "" {
		cmd.Dir = worktree
	}
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return -1, err
}

// registrarEventoPrVerify persiste el evento pr-verify (§5) con el detail del
// esquema: por comando {cmd, exit}; o el contrato tested; o el motivo.
func registrarEventoPrVerify(gitDir, worktree string, verif ResultadoVerificacion) error {
	switch verif.Modo {
	case ModoDeterminista:
		comandos := make([]map[string]any, 0, len(verif.Comandos))
		peor := 0
		for _, c := range verif.Comandos {
			comandos = append(comandos, map[string]any{"cmd": c.Comando, "exit": c.Exit})
			if c.Exit > peor {
				peor = c.Exit
			}
		}
		detalle, err := json.Marshal(map[string]any{"comandos": comandos})
		if err != nil {
			return err
		}
		return RegistrarEvento(gitDir, "pr-verify", peor, nil, string(detalle), worktree)
	case ModoDelegado:
		detalle, err := json.Marshal(map[string]any{"tested": verif.Tested})
		if err != nil {
			return err
		}
		return RegistrarEvento(gitDir, "pr-verify", 0, nil, string(detalle), worktree)
	default:
		detalle, err := json.Marshal(map[string]any{"motivo": verif.Motivo})
		if err != nil {
			return err
		}
		return RegistrarEvento(gitDir, "pr-verify", 0, nil, string(detalle), worktree)
	}
}