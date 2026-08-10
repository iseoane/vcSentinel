package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/ops"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

// verboPr decide la rama del subcomando pr: "review" y "create" son verbos
// propios; cualquier otra cosa (flags de gh o nada) es el passthrough legacy
// a gh pr create.
func verboPr(args []string) string {
	if len(args) > 0 {
		switch args[0] {
		case "review":
			return "review"
		case "create":
			return "create"
		}
	}
	return "legacy"
}

// ejecutarPr despacha el subcomando pr (fase 2): pr review analiza la rama
// sin publicar nada; pr create analiza, aplica el gate de block y publica con
// la plantilla honesta; el resto mantiene el passthrough legacy a gh pr create.
func ejecutarPr(worktree string, args []string) {
	switch verboPr(args) {
	case "review":
		ejecutarPrReview(worktree, args[1:])
	case "create":
		ejecutarPrCreate(worktree, args[1:])
	default:
		ejecutarPrLegacy(args)
	}
}

// ejecutarPrLegacy es el passthrough a gh pr create con la limpieza previa de
// fichas huérfanas (comportamiento histórico de sentinel pr).
func ejecutarPrLegacy(args []string) {
	if _, err := exec.LookPath("gh"); err != nil {
		fmt.Println("? gh (GitHub CLI) no está en el PATH. Instálalo o crea el PR manualmente.")
		os.Exit(1)
	}

	gitDir, err := git.ObtenerGitDir()
	if err != nil {
		fmt.Printf("? %v\n", err)
		os.Exit(1)
	}
	eliminados, err := purgarHuerfanasConEventos(gitDir)
	if err != nil {
		// La limpieza es auxiliar al PR: se avisa y se continúa, no se aborta.
		fmt.Printf("? Aviso: no se pudieron purgar fichas huérfanas (%v). El PR se crea igualmente.\n", err)
	}
	if len(eliminados) == 0 {
		fmt.Println("? Limpieza previa: no hay fichas huérfanas.")
	} else {
		fmt.Printf("? Limpieza previa: eliminadas %d fichas de commits que ya no existen (y sus eventos).\n", len(eliminados))
		for _, sha := range eliminados {
			fmt.Printf("  - %s\n", sha)
		}
	}

	cmdArgs := append([]string{"pr", "create"}, args...)
	cmd := exec.Command("gh", cmdArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("? gh pr create terminó con error (código %d).\n", exitCodeDeError(err))
		os.Exit(1)
	}
}

// flagsPrReview son las opciones de pr review.
type flagsPrReview struct {
	base           string
	soloPendientes bool // --only-unaudited
	overview       bool // --overview
	jsonOut        bool // --json
}

// parsearFlagsPrReview parsea las opciones de pr review con la misma sintaxis
// simple de pares "flag valor" que el resto de subcomandos.
func parsearFlagsPrReview(args []string) (flagsPrReview, error) {
	var flags flagsPrReview
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--base":
			if i+1 >= len(args) {
				return flags, fmt.Errorf("--base requiere un valor (rama de comparación)")
			}
			i++
			flags.base = args[i]
		case "--only-unaudited":
			flags.soloPendientes = true
		case "--overview":
			flags.overview = true
		case "--json":
			flags.jsonOut = true
		default:
			return flags, fmt.Errorf("opción desconocida para pr review: %s", arg)
		}
	}
	return flags, nil
}

// detalleEventoPrReview construye el detail del evento pr-review: string con
// JSON serializado (esquema de la guía §13).
func detalleEventoPrReview(base string, res *review.ResultadoRama, ci bool) (string, error) {
	detalle := map[string]any{
		"base":      base,
		"rama":      res.Rama,
		"auditadas": len(res.Fichas),
		"nuevas":    len(res.Pendientes),
		"volumen":   res.Volumen,
		"ci":        ci,
		"overview":  res.Overview != nil,
		"chain_pr":  res.Decision == "chain",
	}
	// Un fallo del overview no debe quedar en silencio en el evento: si se
	// pidió y falló, la decisión chain lleva su causa.
	if res.OverviewError != "" {
		detalle["overview_error"] = res.OverviewError
	}
	datos, err := json.Marshal(detalle)
	if err != nil {
		return "", err
	}
	return string(datos), nil
}

// textoDecision explica la decisión single/chain en la salida terminal.
func textoDecision(decision string, volumen int) string {
	switch decision {
	case "chain":
		return "Decisión PR: cadena (--chain-pr, fase 6). La rama supera " +
			"el umbral de volumen y no se demostró coherencia: conviene dividirla en PRs encadenadas."
	case "single":
		if volumen > review.LimiteDecisionChain {
			return "Decisión PR: una sola PR. La rama supera el umbral de volumen pero " +
				"el overview confirmó que es un cambio coherente."
		}
		return "Decisión PR: una sola PR (volumen dentro del umbral)."
	}
	return "Decisión PR: " + decision
}

// ejecutarPrReview analiza la rama contra la base y muestra la matriz de
// auditoría, el resumen y la decisión single/chain. Es dry-run: no publica
// nada. Registra el evento pr-review al terminar.
func ejecutarPrReview(worktree string, args []string) {
	flags, err := parsearFlagsPrReview(args)
	if err != nil {
		fmt.Printf("? %v\n", err)
		os.Exit(1)
	}

	// Config ESTRICTA (hallazgo del orquestador, fuera del texto original de
	// la ficha): una clave desconocida en el yml debe cortar aquí con error
	// explícito, no seguir en silencio con la config por defecto.
	cfg, err := config.CargarConfiguracionLocalEstricta(worktree)
	if err != nil {
		fmt.Printf("? %v\n", err)
		os.Exit(1)
	}
	gitDir, err := git.ObtenerGitDir()
	if err != nil {
		fmt.Printf("? %v\n", err)
		os.Exit(1)
	}

	fabrica := func(dimension string) (review.AuditorAgente, string, error) {
		perfil := config.ResolverPerfil(cfg, dimension, "")
		adapter, err := agentadapter.NuevoAdaptadorConPerfil(cfg, perfil)
		if err != nil {
			return nil, perfil.Nombre, err
		}
		return adapter, perfil.Nombre, nil
	}

	base := flags.base
	if base == "" {
		base = "main"
	}
	ledger := review.NuevoLedger(gitDir)
	res, err := review.AnalizarRama(ledger, review.OpcionesRama{
		Base:           base,
		SoloPendientes: flags.soloPendientes,
		Overview:       flags.overview,
		Fabrica:        fabrica,
		Parallel:       cfg.Review.Parallel,
		OnCommit: func(idx, total int, sha string) {
			fmt.Printf("⏳ [%d/%d] Auditar %s\n", idx+1, total, shaCorto(sha))
		},
		OnDimension: func(dim string) {
			fmt.Printf("  ⏳ %s …\n", dim)
		},
	})
	if err != nil {
		fmt.Printf("? %v\n", err)
		os.Exit(1)
	}

	detalle, err := detalleEventoPrReview(base, res, git.DetectarCI(worktree))
	if err != nil {
		fmt.Printf("? Aviso: no se pudo construir el detalle del evento: %v\n", err)
	}
	if err := ops.RegistrarEvento(gitDir, "pr-review", 0, res.SHAs, detalle, worktree); err != nil {
		fmt.Printf("? Aviso: no se pudo registrar el evento: %v\n", err)
	}

	if flags.jsonOut {
		salida := map[string]any{
			"rama":       res.Rama,
			"base":       base,
			"shas":       res.SHAs,
			"pendientes": res.Pendientes,
			"fichas":     res.Fichas,
			"volumen":    res.Volumen,
			"decision":   res.Decision,
			"overview":   res.Overview,
		}
		if res.OverviewError != "" {
			salida["overview_error"] = res.OverviewError
		}
		datos, err := json.MarshalIndent(salida, "", "  ")
		if err != nil {
			fmt.Printf("? %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(datos))
		return
	}

	if len(res.Fichas) == 0 {
		fmt.Println("_No hay commits auditados en la rama._")
		return
	}
	fmt.Println(review.RenderMatriz(res.Fichas))
	fmt.Println()
	fmt.Println(review.RenderResumen(res.Fichas))
	fmt.Println()
	fmt.Println(textoDecision(res.Decision, res.Volumen))
	if res.OverviewError != "" {
		fmt.Printf("? Aviso: el overview no se pudo obtener (%s); se decidió por volumen.\n", res.OverviewError)
	}
}

// flagsPrCreate son las opciones de pr create.
type flagsPrCreate struct {
	base    string
	chainPR bool   // --chain-pr: publicar la rama completa aunque sea descomunal
	force   bool   // --force: superar la validación en rojo (T1.8: el único gate que bloquea)
	reason  string // --reason: motivo explícito y obligatorio junto a --force
}

// parsearFlagsPrCreate parsea las opciones de pr create con la misma sintaxis
// simple de pares "flag valor" que el resto de subcomandos. --force exige
// --reason (T1.8): con el veredicto semántico ya en advisory, la validación es
// el único gate real que se puede forzar, y forzarla sin motivo no queda
// registrado en el evento de forma útil.
func parsearFlagsPrCreate(args []string) (flagsPrCreate, error) {
	var flags flagsPrCreate
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--base":
			if i+1 >= len(args) {
				return flags, fmt.Errorf("--base requiere un valor (rama de comparación)")
			}
			i++
			flags.base = args[i]
		case "--chain-pr":
			flags.chainPR = true
		case "--force":
			flags.force = true
		case "--reason":
			if i+1 >= len(args) {
				return flags, fmt.Errorf("--reason requiere un valor (motivo del --force)")
			}
			i++
			flags.reason = args[i]
		default:
			return flags, fmt.Errorf("opción desconocida para pr create: %s", arg)
		}
	}
	if flags.force && flags.reason == "" {
		return flags, fmt.Errorf("--force requiere --reason con el motivo explícito de por qué se supera la validación")
	}
	return flags, nil
}

// detalleEventoPrCreate construye el detail del evento pr-create (guía §13):
// acta de publicación con pr_url, fallback y chain_pr. Amplía T1.8: force
// registra si se superó la validación en rojo, y motivo (solo si force) deja
// constancia explícita de por qué — la excepción nunca queda en silencio.
func detalleEventoPrCreate(prURL string, fallback, chain, force bool, motivo string) (string, error) {
	detalle := map[string]any{
		"pr_url":   prURL,
		"fallback": fallback,
		"chain_pr": chain,
		"force":    force,
	}
	if force {
		detalle["motivo"] = motivo
	}
	datos, err := json.Marshal(detalle)
	if err != nil {
		return "", err
	}
	return string(datos), nil
}

// verificarParaPlantilla ejecuta la verificación honesta (guía §12.3) y la
// traduce a la sección de la plantilla: exit codes reales por comando
// configurado, contrato tested del agente o motivo de omisión. Un fallo de la
// verificación NUNCA queda en silencio: se refleja como motivo en la
// plantilla para que el PR sea transparente sobre lo que se comprobó.
func verificarParaPlantilla(worktree, gitDir string, cfg config.Config) review.VerificacionPlantilla {
	return verificarParaPlantillaCon(worktree, gitDir, cfg, ops.Verificar)
}

// verificarParaPlantillaCon es la versión inyectable de verificarParaPlantilla:
// verificar nil se sustituye por ops.Verificar en producción.
func verificarParaPlantillaCon(worktree, gitDir string, cfg config.Config,
	verificar func(ops.OpcionesVerificar) (ops.ResultadoVerificacion, error)) review.VerificacionPlantilla {

	if verificar == nil {
		verificar = ops.Verificar
	}

	perfil := config.ResolverPerfil(cfg, "", "")
	adapter, err := agentadapter.NuevoAdaptadorConPerfil(cfg, perfil)
	if err != nil {
		// Sin agente no hay vía de delegación; la vía determinista sigue viva.
		// ops.Verificar acepta Agente nil (lo comprueba antes de usarlo):
		// la delegación se degrada a "sin_agente", nunca panic.
		adapter = nil
	}
	verif, err := verificar(ops.OpcionesVerificar{
		Worktree: worktree,
		GitDir:   gitDir,
		Cfg:      cfg,
		Agente:   adapter,
		Preguntar: func(aviso string) (string, error) {
			fmt.Println(aviso)
			fmt.Print("> ")
			var respuesta string
			if _, err := fmt.Scanln(&respuesta); err != nil {
				return "", err
			}
			return respuesta, nil
		},
	})
	if err != nil {
		return review.VerificacionPlantilla{
			Modo:   ops.ModoOmitido,
			Motivo: fmt.Sprintf("error_de_verificacion: %v", err),
		}
	}
	plantilla := review.VerificacionPlantilla{
		Modo:   verif.Modo,
		Tested: verif.Tested,
		Motivo: verif.Motivo,
	}
	for _, c := range verif.Comandos {
		plantilla.Comandos = append(plantilla.Comandos, review.ComandoVerificado{Comando: c.Comando, Exit: c.Exit})
	}
	return plantilla
}

// salirSiError centraliza el patrón de salida del CLI: imprime el error y
// abandona con código 1. Sin error no hace nada.
func salirSiError(err error) {
	if err != nil {
		fmt.Printf("? %v\n", err)
		os.Exit(1)
	}
}

// escribirPlantillaPR guarda el cuerpo del PR en un archivo temporal y
// devuelve su ruta. El archivo temporal evita ensuciar el worktree: la
// plantilla es un artefacto efímero de publicación.
func escribirPlantillaPR(cuerpo string) (string, error) {
	archivo, err := os.CreateTemp("", "sentinel_pr_*.md")
	if err != nil {
		return "", fmt.Errorf("no se pudo crear el archivo temporal de la plantilla: %w", err)
	}
	defer archivo.Close()
	if _, err := archivo.WriteString(cuerpo); err != nil {
		return "", fmt.Errorf("no se pudo escribir la plantilla: %w", err)
	}
	return archivo.Name(), nil
}

// copiarPortapapeles copia texto al portapapeles del sistema según la
// plataforma: clip (Windows), wl-copy (Wayland), xclip (X11).
func copiarPortapapeles(texto string) error {
	return copiarPortapapelesCon(texto,
		func(nombre string) bool { _, err := exec.LookPath(nombre); return err == nil },
		func(nombre, contenido string) error {
			c := exec.Command(nombre)
			c.Stdin = strings.NewReader(contenido)
			return c.Run()
		})
}

// copiarPortapapelesCon es la versión inyectable de copiarPortapapeles:
// existe decide qué herramienta está disponible; ejecutar lanza la copia.
// Usa la PRIMERA herramienta del orden canónico (clip > wl-copy > xclip) que
// exista en el PATH: si esa ejecución falla, no se intenta la siguiente. Es
// una decisión deliberada: la primera disponible es la canónica de la
// plataforma y un fallo suyo casi siempre indica un entorno roto, no un
// fallo de la herramienta.
func copiarPortapapelesCon(texto string, existe func(string) bool, ejecutar func(string, string) error) error {
	candidatos := []string{"clip", "wl-copy", "xclip"}
	for _, nombre := range candidatos {
		if !existe(nombre) {
			continue
		}
		if err := ejecutar(nombre, texto); err != nil {
			return fmt.Errorf("no se pudo copiar al portapapeles con %s: %w", nombre, err)
		}
		return nil
	}
	return errors.New("no se encontró ninguna herramienta de portapapeles (clip/wl-copy/xclip)")
}

// opcionesPublicarPR agrupa las dependencias inyectables de publicarPRCon:
// ghDisponible decide si gh está en el PATH, ejecutarGh lanza gh y devuelve
// su salida (args completos, incluyendo el worktree como cwd), copiar se usa
// solo en el fallback (portapapeles).
type opcionesPublicarPR struct {
	ghDisponible func(string) bool
	ejecutarGh   func(worktree string, args ...string) ([]byte, error)
	copiar       func(string) error
}

// publicarPR publica el PR con gh pr create --draft -F plantilla. Si gh no
// está en el PATH, fallback a archivo + portapapeles (guía §12.4): el cuerpo
// se re-lee del archivo recién escrito. Devuelve la URL del PR (vacía en
// fallback) y si se usó el fallback.
func publicarPR(worktree, rutaPlantilla, base string) (string, bool, error) {
	return publicarPRCon(worktree, rutaPlantilla, base, opcionesPublicarPR{
		ghDisponible: func(nombre string) bool { _, err := exec.LookPath(nombre); return err == nil },
		ejecutarGh: func(worktree string, args ...string) ([]byte, error) {
			cmd := exec.Command("gh", args...)
			cmd.Dir = worktree
			var stderr strings.Builder
			cmd.Stderr = &stderr
			salida, err := cmd.Output()
			if err != nil {
				return nil, fmt.Errorf("gh pr create terminó con error (código %d): %s",
					exitCodeDeError(err), strings.TrimSpace(stderr.String()))
			}
			return salida, nil
		},
		copiar: copiarPortapapeles,
	})
}

// publicarPRCon es la versión inyectable de publicarPR (seam de prueba).
func publicarPRCon(worktree, rutaPlantilla, base string, opciones opcionesPublicarPR) (string, bool, error) {
	if opciones.ghDisponible("gh") {
		args := []string{"pr", "create", "--draft"}
		if base != "" {
			// La PR debe targetear la MISMA base que se auditó: sin --base
			// explícito, la revisión y la PR podrían divergir en silencio.
			args = append(args, "--base", base)
		}
		args = append(args, "-F", rutaPlantilla)
		salida, err := opciones.ejecutarGh(worktree, args...)
		if err != nil {
			return "", false, err
		}
		return strings.TrimSpace(string(salida)), false, nil
	}

	cuerpo, err := os.ReadFile(rutaPlantilla)
	if err != nil {
		return "", true, fmt.Errorf("no se pudo releer la plantilla para el portapapeles: %w", err)
	}
	fmt.Printf("? gh no está en el PATH: la plantilla quedó en %s y se copia al portapapeles.\n", rutaPlantilla)
	if err := opciones.copiar(string(cuerpo)); err != nil {
		return "", true, err
	}
	return "", true, nil
}

// avisoSemantico decide si el veredicto semántico de la rama merece un aviso
// destacado en la publicación (T1.8): el gate de bloqueo por veredicto pasa a
// advisory, igual que internal/gate desde T1.7 — la validación (más abajo) es
// ahora el único gate que puede impedir publicar. avisoSemantico NUNCA decide
// si se publica, solo si hay que avisar. Devuelve los hallazgos CRITICAL
// ESTRUCTURADOS: el formateo sigue siendo responsabilidad del CLI.
//
// Antes se llamaba gateBlock y devolvía "permitido"; se renombra porque una
// función que ya no bloquea no puede seguir llamándose "gate...Block" sin
// mentir sobre lo que hace.
func avisoSemantico(fichas []review.Ficha) (avisar bool, bloqueantes []review.ReviewFinding) {
	if review.VeredictoDeRama(fichas) != review.VerdictBlock {
		return false, nil
	}
	return true, review.BloqueantesDeRama(fichas)
}

// comandosDeValidacion traduce las ValidationRun de internal/validation a
// ComandoVerificado para la plantilla: misma forma de evidencia (comando +
// exit code real), por eso se reusa el tipo en vez de duplicarlo — lo que
// cambia es el origen (validación previa, no la verificación post-hoc de
// ops.Verificar), de ahí que viva en su propio campo/sección.
func comandosDeValidacion(runs []validation.ValidationRun) []review.ComandoVerificado {
	cmds := make([]review.ComandoVerificado, 0, len(runs))
	for _, r := range runs {
		cmds = append(cmds, review.ComandoVerificado{Comando: r.Comando, Exit: r.Exit})
	}
	return cmds
}

// depsPrCreate agrupa las costuras inyectables del pipeline de pr create
// (T1.8): permite testear el ORDEN (validación antes de auditar, cero tokens
// si falla) sin git, agentes ni gh reales. En producción, ejecutarPrCreate las
// resuelve a las funciones reales.
type depsPrCreate struct {
	cargarConfig       func(worktree string) (config.Config, error)
	obtenerGitDir      func() (string, error)
	ejecutarValidacion func(perfil string, alcance []string, opts validation.OpcionesEjecucion) ([]validation.ValidationRun, error)
	analizarRama       func(gitDir string, opts review.OpcionesRama) (*review.ResultadoRama, error)
	verificar          func(worktree, gitDir string, cfg config.Config) review.VerificacionPlantilla
	publicar           func(worktree, rutaPlantilla, base string) (string, bool, error)
	registrarEvento    func(gitDir, tipo string, exit int, shas []string, detalle, worktree string) error
}

// ejecutarPrCreate implementa pr create (T1.8): valida ANTES de auditar (si
// falla sin --force, ni se llama a AnalizarRama: cero tokens), aviso advisory
// del veredicto semántico, plantilla honesta con las dos naturalezas de
// evidencia y publicación con gh o fallback a portapapeles.
func ejecutarPrCreate(worktree string, args []string) {
	os.Exit(ejecutarPrCreateCon(os.Stdout, worktree, args, depsPrCreate{
		// Config ESTRICTA (hallazgo del orquestador, F1): pr create es
		// justo el comando cuyo punto entero es "la validación manda", así
		// que un yml roto debe fallar alto igual que gate/pr review/status,
		// nunca seguir en silencio con la config por defecto.
		cargarConfig:       config.CargarConfiguracionLocalEstricta,
		obtenerGitDir:      git.ObtenerGitDir,
		ejecutarValidacion: validation.EjecutarPerfilSobreCandidato,
		analizarRama: func(gitDir string, opts review.OpcionesRama) (*review.ResultadoRama, error) {
			return review.AnalizarRama(review.NuevoLedger(gitDir), opts)
		},
		verificar:       verificarParaPlantilla,
		publicar:        publicarPR,
		registrarEvento: ops.RegistrarEvento,
	}))
}

// ejecutarPrCreateCon es la versión inyectable de ejecutarPrCreate (seam de
// prueba): devuelve el exit code sin terminar el proceso, mismo patrón que
// ejecutarGate.
func ejecutarPrCreateCon(w io.Writer, worktree string, args []string, deps depsPrCreate) int {
	flags, err := parsearFlagsPrCreate(args)
	if err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}

	cfg, err := deps.cargarConfig(worktree)
	if err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}
	gitDir, err := deps.obtenerGitDir()
	if err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}

	// Validación PRIMERO (T1.8): reusa internal/validation (misma pieza que
	// usa internal/gate, ver comandos_gate.go), no el orquestador completo de
	// gate, porque AnalizarRama audita la RAMA entera, no un único commit como
	// hace AuditarCommit. Si falla sin --force, AnalizarRama NUNCA se invoca:
	// cero tokens gastados.
	runs, err := deps.ejecutarValidacion(perfilGatePorDefecto, nil, validation.OpcionesEjecucion{
		Worktree: worktree,
		Cfg:      cfg,
	})
	if err != nil {
		fmt.Fprintf(w, "? No se pudo ejecutar la validación: %v\n", err)
		return 1
	}
	hallazgos := validation.Hallazgos(runs, cfg.Validation.Capabilities)
	// forzoValidacionEnRojo distingue la PRESENCIA del flag --force de su
	// EFECTO real (hallazgo del orquestador, Fix 2): solo es true cuando de
	// verdad había hallazgos en rojo que --force tuvo que superar. Si
	// --force se pasó pero la validación ya estaba en verde, el flag no
	// ejerció ningún efecto y el evento no debe registrar una excepción que
	// nunca ocurrió.
	var forzoValidacionEnRojo bool
	if len(hallazgos) > 0 {
		if !flags.force {
			fmt.Fprintln(w, "🚨 Validación en rojo: no se publica la PR. Comandos:")
			for _, h := range hallazgos {
				fmt.Fprintf(w, "  - ✖ %s (%s):\n%s\n", h.Capability, h.Comando, strings.TrimSpace(h.Evidencia))
			}
			fmt.Fprintln(w, "Corrige los comandos en rojo o repite con --force --reason \"motivo\" para publicar igualmente.")
			return 1
		}
		forzoValidacionEnRojo = true
		fmt.Fprintf(w, "⚠️  Validación en rojo superada con --force (motivo: %s).\n", flags.reason)
	}

	fabrica := func(dimension string) (review.AuditorAgente, string, error) {
		perfil := config.ResolverPerfil(cfg, dimension, "")
		adapter, err := agentadapter.NuevoAdaptadorConPerfil(cfg, perfil)
		if err != nil {
			return nil, perfil.Nombre, err
		}
		return adapter, perfil.Nombre, nil
	}

	base := flags.base
	if base == "" {
		base = "main"
	}
	res, err := deps.analizarRama(gitDir, review.OpcionesRama{
		Base:           base,
		SoloPendientes: false,
		Overview:       true,
		Fabrica:        fabrica,
		Parallel:       cfg.Review.Parallel,
		OnCommit: func(idx, total int, sha string) {
			fmt.Fprintf(w, "⏳ [%d/%d] Auditar %s\n", idx+1, total, shaCorto(sha))
		},
		OnDimension: func(dim string) {
			fmt.Fprintf(w, "  ⏳ %s …\n", dim)
		},
	})
	if err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}

	if len(res.Fichas) == 0 {
		fmt.Fprintln(w, "_No hay commits auditados en la rama._")
		return 1
	}

	// Gate semántico advisory (T1.8): nunca bloquea, solo avisa destacado.
	if avisar, bloqueantes := avisoSemantico(res.Fichas); avisar {
		fmt.Fprintln(w, "⚠️  AVISO: veredicto de auditoría semántica = block (no bloquea la publicación, advisory).")
		for _, h := range bloqueantes {
			fmt.Fprintf(w, "  - [%s] %s (%s:%d)\n", h.Severity, h.Description, h.File, h.Line)
		}
	}

	// Rama descomunal sin --chain-pr: se propone la cadena, no se publica una
	// PR gigante (guía §12.4).
	if res.Decision == "chain" && !flags.chainPR {
		fmt.Fprintln(w, "🚨 Rama descomunal: supera el umbral de volumen sin coherencia demostrada.")
		fmt.Fprintln(w, "Se propone dividirla en PRs encadenadas (--chain-pr) en lugar de una PR gigante.")
		return 1
	}

	verificacion := deps.verificar(worktree, gitDir, cfg)
	verificacion.Validacion = comandosDeValidacion(runs)

	cuerpo := review.RenderPlantillaPr(res.Fichas, res.Overview, verificacion, version)
	rutaPlantilla, err := escribirPlantillaPR(cuerpo)
	if err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}

	prURL, fallback, err := deps.publicar(worktree, rutaPlantilla, base)
	if err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}
	if fallback {
		// El archivo es el artefacto entregable del fallback: se conserva.
		fmt.Fprintln(w, "? Plantilla en portapapeles: crea la PR manualmente con ese contenido.")
	} else {
		fmt.Fprintf(w, "? PR creada: %s\n", prURL)
		// El cuerpo ya vive en la PR: el temporal efímero se limpia.
		if err := os.Remove(rutaPlantilla); err != nil {
			fmt.Fprintf(w, "? Aviso: no se pudo limpiar el archivo temporal (%v).\n", err)
		}
	}

	detalle, err := detalleEventoPrCreate(prURL, fallback, flags.chainPR, forzoValidacionEnRojo, flags.reason)
	if err != nil {
		fmt.Fprintf(w, "? Aviso: no se pudo construir el detalle del evento: %v\n", err)
	}
	if err := deps.registrarEvento(gitDir, "pr-create", 0, res.SHAs, detalle, worktree); err != nil {
		fmt.Fprintf(w, "? Aviso: no se pudo registrar el evento: %v\n", err)
	}
	return 0
}
