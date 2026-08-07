package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/ops"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

// flagsAuditoria son las opciones comunes de review y status.
type flagsAuditoria struct {
	targets []string // SHAs o expresiones a auditar (default [HEAD])
	dims    []string
	all     bool
	chain   bool
	gate    bool
	prune   bool // borra fichas de commits que ya no existen en el repo
	profile string
	answer  string
	jsonOut bool
	// timeout es el override por invocación del límite por llamada al agente
	// (review.timeout de la config). Cero significa "sin override".
	timeout time.Duration
}

// parsearFlagsAuditoria recorre los argumentos del subcomando y extrae las
// opciones con su valor. Los flags con valor consumen el siguiente argumento.
// Cada argumento posicional es un target: se admiten varios para auditar
// varios commits en una sola invocación (el contador i/N los numera).
func parsearFlagsAuditoria(args []string) (flagsAuditoria, error) {
	flags := flagsAuditoria{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--all":
			flags.all = true
		case "--chain":
			flags.chain = true
		case "--gate":
			flags.gate = true
		case "--prune":
			flags.prune = true
		case "--json":
			flags.jsonOut = true
		case "--dims", "--profile", "--answer", "--timeout":
			if i+1 >= len(args) {
				return flags, fmt.Errorf("el flag %s necesita un valor", arg)
			}
			i++
			valor := args[i]
			switch arg {
			case "--dims":
				for _, dim := range strings.Split(valor, ",") {
					if d := strings.TrimSpace(dim); d != "" {
						flags.dims = append(flags.dims, d)
					}
				}
			case "--profile":
				flags.profile = valor
			case "--answer":
				flags.answer = valor
			case "--timeout":
				segundos, err := strconv.Atoi(strings.TrimSpace(valor))
				if err != nil || segundos <= 0 {
					return flags, fmt.Errorf("--timeout necesita un número de segundos positivo, recibido %q", valor)
				}
				flags.timeout = time.Duration(segundos) * time.Second
			}
		default:
			if strings.HasPrefix(arg, "-") {
				return flags, fmt.Errorf("opción desconocida: %s", arg)
			}
			flags.targets = append(flags.targets, arg)
		}
	}
	if len(flags.targets) == 0 {
		flags.targets = []string{"HEAD"}
	}
	return flags, nil
}

// ejecutarLint ejecuta los comandos de lint_commands de la configuración.
// Cada comando corre en el shell del sistema; si cualquiera falla, salida 1.
func ejecutarLint(worktree string) {
	cfg := config.CargarConfiguracionLocal(worktree)
	if len(cfg.LintCommands) == 0 {
		fmt.Println("✅ No hay comandos de lint configurados (lint_commands en vassentinel.yml).")
		return
	}

	fallo := false
	for _, comando := range cfg.LintCommands {
		fmt.Printf("🔧 %s\n", comando)
		salida, err := ejecutarEnShell(comando)
		if salida != "" {
			fmt.Print(salida)
			if !strings.HasSuffix(salida, "\n") {
				fmt.Println()
			}
		}
		if err != nil {
			fmt.Printf("❌ Falló con código %d.\n", exitCodeDeError(err))
			fallo = true
		} else {
			fmt.Println("✅ OK")
		}
	}
	if fallo {
		os.Exit(1)
	}
}

// ejecutarEnShell ejecuta un comando a través del shell del sistema.
func ejecutarEnShell(comando string) (string, error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", comando)
	} else {
		cmd = exec.Command("sh", "-c", comando)
	}
	salida, err := cmd.CombinedOutput()
	return string(salida), err
}

func exitCodeDeError(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// ejecutarRebase actualiza la rama con fetch + rebase contra su upstream.
// Nunca actúa en silencio: si el worktree está sucio o no hay upstream, aborta.
func ejecutarRebase() {
	limpio, err := git.WorktreeLimpio()
	if err != nil {
		fmt.Printf("❌ No se pudo verificar el estado del worktree: %v\n", err)
		os.Exit(1)
	}
	if !limpio {
		fmt.Println("❌ El worktree tiene cambios pendientes. Confirma o fragmenta antes de rebasear.")
		os.Exit(1)
	}

	upstream, err := git.UpstreamOMain()
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}
	rama, err := git.RamaActual()
	if err != nil {
		fmt.Printf("❌ No se pudo leer la rama actual: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("🔎 Rebase de %s contra %s (fetch + rebase).\n", rama, upstream)
	fmt.Print("¿Procedo? (S/N): ")
	linea, err := leerLinea()
	if err != nil {
		return
	}
	respuesta := strings.ToLower(strings.TrimSpace(linea))
	if respuesta != "s" && respuesta != "si" && respuesta != "y" && respuesta != "yes" {
		fmt.Println("🚫 Cancelado, sin cambios.")
		return
	}

	remoto := ""
	if upstream == "@{u}" {
		// El remoto real viene de la config de la rama, no del ref simbólico.
		remoto = git.RemotoDeRama(rama)
	} else if partes := strings.Split(upstream, "/"); len(partes) > 1 {
		remoto = partes[0]
	}
	if remoto == "" {
		fmt.Println("❌ No se pudo determinar el remoto para el fetch. Revisa branch." + rama + ".remote.")
		os.Exit(1)
	}
	if salida, err := ejecutarEnShell("git fetch " + remoto); err != nil {
		fmt.Printf("❌ Falló el fetch (%s):\n%s", remoto, salida)
		os.Exit(1)
	}

	salida, err := ejecutarEnShell("git rebase " + upstream)
	if err != nil {
		fmt.Printf("❌ Falló el rebase:\n%s", salida)
		fmt.Println("Resuelve los conflictos o ejecuta 'git rebase --abort'.")
		os.Exit(1)
	}
	fmt.Println(salida)
	fmt.Printf("✅ %s rebaseada sobre %s.\n", rama, upstream)
}

// flagsNoAplicablesAStatus indica si los flags parseados no aplican a status
// (que solo entiende --json y --prune). Se rechazan en lugar de aceptarse en
// silencio.
func flagsNoAplicablesAStatus(flags flagsAuditoria) bool {
	return len(flags.targets) > 0 || len(flags.dims) > 0 || flags.all || flags.chain || flags.gate ||
		flags.profile != "" || flags.answer != "" || flags.timeout > 0
}

// aplicarTimeoutFlag devuelve la configuración con el timeout de auditoría
// sobrescrito por --timeout cuando se pasó. Sin el flag la config manda: el
// flag es un override por invocación, no un cambio persistente. Devuelve una
// copia; nunca muta la config recibida.
func aplicarTimeoutFlag(cfg config.Config, flags flagsAuditoria) config.Config {
	if flags.timeout > 0 {
		cfg.Review.Timeout = flags.timeout
	}
	return cfg
}

// purgarHuerfanas borra las fichas de commits que ya no existen en el repo y
// devuelve los SHAs eliminados. Útil tras rebase/amend/squash.
func purgarHuerfanas(gitDir string) ([]string, error) {
	return review.NuevoLedger(gitDir).PurgarHuerfanas()
}

// purgarHuerfanasConEventos purga las fichas huérfanas y, por cada SHA
// eliminado, borra también sus líneas del events.jsonl: los eventos de
// commits que siguen vivos se conservan siempre. Un fallo en la limpieza de
// eventos devuelve error pero las fichas ya purgadas no se restauran.
func purgarHuerfanasConEventos(gitDir string) ([]string, error) {
	eliminados, err := purgarHuerfanas(gitDir)
	if err != nil || len(eliminados) == 0 {
		return eliminados, err
	}
	if _, err := ops.PurgeEventosDe(gitDir, eliminados); err != nil {
		return eliminados, fmt.Errorf("fichas purgadas pero falló limpiar sus eventos: %w", err)
	}
	return eliminados, nil
}

// reportarPurga muestra el resultado de purgarHuerfanasConEventos en texto o
// JSON según jsonOut. Comparte la presentación entre status, review y pr para
// no duplicar el formato.
func reportarPurga(gitDir string, eliminados []string, jsonOut bool, worktree string) {
	if jsonOut {
		salida := map[string]any{
			"worktree": worktree,
			"purgadas": eliminados,
		}
		datos, err := json.MarshalIndent(salida, "", "  ")
		if err != nil {
			fmt.Printf("? No se pudo serializar el estado: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(datos))
		return
	}
	if len(eliminados) == 0 {
		fmt.Println("? --prune: no hay fichas huérfanas (todos los SHAs existen).")
		return
	}
	fmt.Printf("? --prune: eliminadas %d fichas de commits que ya no existen (y sus eventos).\n", len(eliminados))
	for _, sha := range eliminados {
		fmt.Printf("  - %s\n", sha)
	}
}

// ejecutarStatus resume el estado del guardián: volumen pendiente, fichas de
// auditoría y últimos eventos. Con --json emite la misma información en JSON.
func ejecutarStatus(worktree string, args []string) {
	flags, err := parsearFlagsAuditoria(args)
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}
	// status solo entiende --json y --prune; el resto de flags de auditoría no
	// aplican aquí y se rechazan en lugar de aceptarse en silencio.
	if flagsNoAplicablesAStatus(flags) {
		fmt.Println("? status solo acepta los flags --json y --prune.")
		os.Exit(1)
	}

	gitDir, err := git.ObtenerGitDir()
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}

	lineas, estado, err := git.CheckDiffLimits()
	if err != nil {
		fmt.Printf("❌ No se pudo calcular el volumen: %v\n", err)
		os.Exit(1)
	}

	ledger := review.NuevoLedger(gitDir)

	if flags.prune {
		eliminados, err := purgarHuerfanasConEventos(gitDir)
		if err != nil {
			fmt.Printf("? No se pudieron purgar fichas huérfanas: %v\n", err)
			os.Exit(1)
		}
		reportarPurga(gitDir, eliminados, flags.jsonOut, worktree)
		os.Exit(0)
	}

	shas, err := ledger.ListarFichas()
	if err != nil {
		fmt.Printf("⚠️ No se pudieron listar las fichas: %v\n", err)
	}

	fichas := []map[string]any{}
	huerfanos := 0
	for _, sha := range shas {
		veredicto := "sin revisión"
		fixedIn := ""
		ficha, err := ledger.LeerFicha(sha)
		if err == nil && ficha != nil && len(ficha.Revisions) > 0 {
			veredicto = ficha.Revisions[len(ficha.Revisions)-1].Result
			fixedIn = ficha.FixedIn
		}
		esHuerfano := !git.ContenidoEnAlgunRef(sha)
		if esHuerfano {
			huerfanos++
		}
		fichas = append(fichas, map[string]any{
			"sha":       sha,
			"veredicto": veredicto,
			"fixedIn":   fixedIn,
			"huerfana":  esHuerfano,
		})
	}

	eventos, err := ops.UltimosEventos(gitDir, 5)
	if err != nil {
		fmt.Printf("⚠️ No se pudieron leer los eventos: %v\n", err)
	}

	if flags.jsonOut {
		salida := map[string]any{
			"worktree":       worktree,
			"lineas":         lineas,
			"estado":         estado,
			"fichas":         fichas,
			"huerfanas":      huerfanos,
			"ultimosEventos": eventos,
		}
		datos, err := json.MarshalIndent(salida, "", "  ")
		if err != nil {
			fmt.Printf("❌ No se pudo serializar el estado: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(datos))
		return
	}

	fmt.Printf("📊 Líneas modificadas en este Worktree: %d [%s]\n", lineas, estado)
	fmt.Printf("🔎 Fichas de auditoría: %d (huérfanas: %d)\n", len(fichas), huerfanos)
	for _, ficha := range fichas {
		corregida := ""
		if ficha["fixedIn"] != "" {
			corregida = fmt.Sprintf("  🔧 corregida en %s", ficha["fixedIn"])
		}
		fmt.Printf("  %s  %s%s\n", ficha["sha"], ficha["veredicto"], corregida)
	}
	if len(eventos) > 0 {
		fmt.Printf("🕒 Últimos eventos:\n")
		for _, evento := range eventos {
			fmt.Printf("  %s %s (%d)\n", evento.At.Format("15:04:05"), evento.Cmd, evento.Exit)
		}
	}
}
