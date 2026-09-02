package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"runtime"
	"slices"
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
	// Sin default a "HEAD" aquí (B17): esta función la comparten status y
	// review, y solo review tiene un concepto de "target" (default en
	// resolverShasAuditoria, cmd/sentinel/comandos_review.go). Rellenar
	// targets aquí incondicionalmente hacía que flagsNoAplicablesAStatus
	// nunca pudiera distinguir "el usuario no pasó nada" de "el usuario pasó
	// un target": sentinel status sin argumentos SIEMPRE se rechazaba.
	return flags, nil
}

// ejecutarLint ejecuta los comandos de lint_commands de la configuración.
// Cada comando corre en el shell del sistema; si cualquiera falla, salida 1.
func ejecutarLint(worktree string) {
	// Config ESTRICTA (hallazgo del orquestador, fuera del texto original de
	// la ficha): una clave desconocida en el yml debe cortar aquí con error
	// explícito, no seguir en silencio con la config por defecto.
	cfg, err := config.CargarConfiguracionLocalEstricta(worktree)
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}
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

// aplicarTimeoutSegundos es el mismo override expresado en segundos, que es
// como lo recibe el gate. Vive junto a aplicarTimeoutFlag a propósito: un
// único sitio decide qué campo se sustituye, así que review y gate no pueden
// divergir en unidad ni en destino.
func aplicarTimeoutSegundos(cfg config.Config, segundos int) config.Config {
	if segundos <= 0 {
		return cfg
	}
	cfg.Review.Timeout = time.Duration(segundos) * time.Second
	return cfg
}

// purgarHuerfanas borra las fichas de commits que ya no existen en el repo y
// devuelve los SHAs eliminados. Útil tras rebase/amend/squash.
//
// Purga TODOS los ledgers del repositorio, no solo el del checkout actual. El
// ledger v1 se ancla en el gitDir, así que una revisión hecha desde un worktree
// enlazado escribe en <gitCommonDir>/worktrees/<nombre>/vas-sentinel, y
// delegar en un writer con worktree dedicado es el flujo habitual aquí. Purgar
// solo el propio dejaba huérfanas todas esas fichas.
//
// Importa más allá de la higiene: T9.5 construye su cascada de retención sobre
// esta primitiva y sobre collectProvenanceReferences, que ya enumera todos los
// ledgers. Decidir qué conservar mirando trece y borrar en uno deja la
// contabilidad de la cascada mal (FU-12).
func purgarHuerfanas(worktree, gitDir string) ([]string, error) {
	porDirectorio, err := purgarHuerfanasPorLedger(worktree, gitDir)
	if err != nil {
		return nil, err
	}
	eliminados := []string{}
	for _, dir := range slices.Sorted(maps.Keys(porDirectorio)) {
		eliminados = append(eliminados, porDirectorio[dir]...)
	}
	return eliminados, nil
}

// purgarHuerfanasPorLedger purga cada ledger del repositorio y devuelve los
// SHAs eliminados AGRUPADOS POR DIRECTORIO. La agrupación no es un detalle: los
// eventos también viven por gitDir, así que borrar la ficha de un checkout y
// sus eventos de otro deja registros huérfanos exactamente donde el comando
// afirma haberlos limpiado.
//
// La existencia se resuelve contra worktree, no contra el directorio de trabajo
// del proceso. Con un único ledger la diferencia era invisible porque ambos
// coincidían; al recorrer todos los ledgers del repositorio, clasificar con el
// CWD borraría fichas vivas en cuanto el proceso corriera desde otro sitio.
func purgarHuerfanasPorLedger(worktree, gitDir string) (map[string][]string, error) {
	// Se valida el repositorio UNA vez, antes de clasificar nada. A partir de
	// ahí un fallo por SHA solo puede significar objeto desconocido, que es el
	// caso huérfano; sin esta comprobación, una invocación rota se leería como
	// "ninguno de estos commits existe" y vaciaría los ledgers.
	if err := git.RepositorioUsable(worktree); err != nil {
		return nil, err
	}
	existe := func(sha string) (bool, error) { return git.ContenidoEnAlgunRefDe(worktree, sha) }

	gitCommonDir, err := git.ObtenerGitCommonDir(worktree)
	if err != nil {
		purgados, perr := review.NuevoLedger(gitDir).PurgarHuerfanas(existe)
		if perr != nil {
			return nil, perr
		}
		return map[string][]string{gitDir: purgados}, fmt.Errorf("linked worktree ledgers were not purged: %w", err)
	}
	directorios, err := directoriosLedgerV1(gitCommonDir)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(directorios, gitDir) {
		directorios = append(directorios, gitDir)
	}

	porDirectorio := map[string][]string{}
	for _, dir := range directorios {
		purgados, err := review.NuevoLedger(dir).PurgarHuerfanas(existe)
		// Recorded BEFORE the error is examined, and the accumulated map is
		// returned WITH it. PurgarHuerfanas hands back what it had already
		// deleted alongside its failure, and by the time one ledger fails the
		// earlier ones are already purged. Discarding that left those fichas
		// gone with their events intact, because events are cleaned from this
		// very result.
		porDirectorio[dir] = purgados
		if err != nil {
			return porDirectorio, fmt.Errorf("purging the ledger at %s: %w", dir, err)
		}
	}
	return porDirectorio, nil
}

// purgarHuerfanasConEventos purga las fichas huérfanas y, por cada SHA
// eliminado, borra también sus líneas del events.jsonl: los eventos de
// commits que siguen vivos se conservan siempre. Un fallo en la limpieza de
// eventos devuelve error pero las fichas ya purgadas no se restauran.
func purgarHuerfanasConEventos(worktree, gitDir string) ([]string, error) {
	porDirectorio, errEnumeracion := purgarHuerfanasPorLedger(worktree, gitDir)
	// A partial result travels WITH its error. The common-directory fallback
	// deletes this checkout's fichas and only then reports that the linked
	// ledgers could not be enumerated, so returning nil here dropped the SHAs
	// it had already removed: their events survived pointing at fichas the
	// command had silently deleted, and the operator was told only "it failed".
	if porDirectorio == nil {
		return nil, errEnumeracion
	}
	eliminados := []string{}
	// Los eventos se limpian en el MISMO directorio en el que estaba la ficha.
	// events.jsonl vive por gitDir igual que el ledger, así que borrar las
	// fichas de todos los checkouts y los eventos de uno solo dejaría eventos
	// apuntando a SHAs eliminados justo donde el comando dice haberlos
	// limpiado.
	for _, dir := range slices.Sorted(maps.Keys(porDirectorio)) {
		purgados := porDirectorio[dir]
		eliminados = append(eliminados, purgados...)
		if len(purgados) == 0 {
			continue
		}
		if err := limpiarEventos(dir, purgados); err != nil {
			// Joined, not replaced. errEnumeracion says some ledgers were never
			// enumerated at all; overwriting it with the event failure left the
			// operator believing the purge had reached every ledger and only
			// stumbled on cleanup.
			return eliminados, errors.Join(errEnumeracion, err)
		}
	}
	return eliminados, errEnumeracion
}

// limpiarEventos borra del events.jsonl de un gitDir las líneas de los SHAs
// purgados. Extraído para que el punto de fallo tenga un nombre en el error
// combinado que devuelve purgarHuerfanasConEventos.
func limpiarEventos(dir string, purgados []string) error {
	if _, err := ops.PurgeEventosDe(dir, purgados); err != nil {
		return fmt.Errorf("fichas purgadas pero falló limpiar sus eventos en %s: %w", dir, err)
	}
	return nil
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

	gitDir, err := git.ObtenerGitDirDe(worktree)
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}

	lineas, estado, err := git.CheckDiffLimits()
	if err != nil {
		fmt.Printf("❌ No se pudo calcular el volumen: %v\n", err)
		os.Exit(1)
	}

	// --prune runs BEFORE the shared ledger is built, and that order is load
	// bearing. purgarHuerfanasConEventos enumerates every per-checkout ledger
	// and keeps a documented fallback for when the common directory cannot be
	// resolved; building the shared ledger first turned that same resolution
	// failure into an exit, so the fallback became unreachable.
	//
	// No test pins this order, and that gap is recorded rather than glossed:
	// reaching it needs ObtenerGitCommonDir to fail while the checkout's own
	// gitDir still resolves, and neither this command nor purgarHuerfanasPorLedger
	// takes that resolver as a seam. Adding one is the fix; until then, moving
	// the shared ledger above this block silently restores the regression.
	if flags.prune {
		eliminados, err := purgarHuerfanasConEventos(worktree, gitDir)
		if err != nil {
			// Only what the purge actually deleted is reported, and only if it
			// deleted anything. reportarPurga's empty case prints a VERIFIED
			// conclusion — "no orphans exist, every SHA is reachable" — and a
			// purge that failed never established that. Its JSON form says the
			// same with an empty list. The failure goes to stderr so --json
			// still emits at most one parseable object on stdout.
			if len(eliminados) > 0 {
				reportarPurga(gitDir, eliminados, flags.jsonOut, worktree)
			}
			fmt.Fprintf(os.Stderr, "? No se pudieron purgar todas las fichas huérfanas: %v\n", err)
			os.Exit(1)
		}
		reportarPurga(gitDir, eliminados, flags.jsonOut, worktree)
		os.Exit(0)
	}

	// Anchored on the common directory, not on gitDir: see sharedReviewLedger.
	// gitDir stays for the purge above and the event log, which enumerate every
	// per-checkout ledger on purpose.
	ledger, err := sharedReviewLedger(worktree)
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
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
