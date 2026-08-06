package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/ops"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
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

	cfg := config.CargarConfiguracionLocal(worktree)
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
	chainPR bool // --chain-pr: publicar la rama completa aunque sea descomunal
	force   bool // --force: superar el gate de block
}

// parsearFlagsPrCreate parsea las opciones de pr create con la misma sintaxis
// simple de pares "flag valor" que el resto de subcomandos.
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
		default:
			return flags, fmt.Errorf("opción desconocida para pr create: %s", arg)
		}
	}
	return flags, nil
}

// detalleEventoPrCreate construye el detail del evento pr-create (guía §13):
// acta de publicación con pr_url, fallback y chain_pr.
func detalleEventoPrCreate(prURL string, fallback, chain bool) (string, error) {
	detalle := map[string]any{
		"pr_url":   prURL,
		"fallback": fallback,
		"chain_pr": chain,
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
	perfil := config.ResolverPerfil(cfg, "", "")
	adapter, err := agentadapter.NuevoAdaptadorConPerfil(cfg, perfil)
	if err != nil {
		// Sin agente no hay vía de delegación; la vía determinista sigue viva.
		// ops.Verificar acepta Agente nil (lo comprueba antes de usarlo):
		// la delegación se degrada a "sin_agente", nunca panic.
		adapter = nil
	}
	verif, err := ops.Verificar(ops.OpcionesVerificar{
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

// publicarPR publica el PR con gh pr create --draft -F plantilla. Si gh no
// está en el PATH, fallback a archivo + portapapeles (guía §12.4): el cuerpo
// se re-lee del archivo recién escrito. Devuelve la URL del PR (vacía en
// fallback) y si se usó el fallback.
func publicarPR(worktree, rutaPlantilla string) (string, bool, error) {
	if _, err := exec.LookPath("gh"); err == nil {
		cmd := exec.Command("gh", "pr", "create", "--draft", "-F", rutaPlantilla)
		cmd.Dir = worktree
		var stderr strings.Builder
		cmd.Stderr = &stderr
		salida, err := cmd.Output()
		if err != nil {
			return "", false, fmt.Errorf("gh pr create terminó con error (código %d): %s",
				exitCodeDeError(err), strings.TrimSpace(stderr.String()))
		}
		return strings.TrimSpace(string(salida)), false, nil
	}

	cuerpo, err := os.ReadFile(rutaPlantilla)
	if err != nil {
		return "", true, fmt.Errorf("no se pudo releer la plantilla para el portapapeles: %w", err)
	}
	fmt.Printf("? gh no está en el PATH: la plantilla quedó en %s y se copia al portapapeles.\n", rutaPlantilla)
	if err := copiarPortapapeles(string(cuerpo)); err != nil {
		return "", true, err
	}
	return "", true, nil
}

// gateBlock decide si el gate de block permite publicar (guía §12.4): block
// sin superar -> no publica y devuelve los hallazgos CRITICAL que lo causan;
// --force lo supera explícitamente. Devuelve los hallazgos ESTRUCTURADOS: el
// formateo es responsabilidad del CLI, no del gate.
func gateBlock(fichas []review.Ficha, force bool) (permitido bool, bloqueantes []review.ReviewFinding) {
	if review.VeredictoDeRama(fichas) != review.VerdictBlock || force {
		return true, nil
	}
	return false, review.BloqueantesDeRama(fichas)
}

// ejecutarPrCreate implementa pr create (guía §12.4): pipeline compartido de
// analizarRama, gate de block, plantilla honesta y publicación con gh o
// fallback a portapapeles. Registra el evento pr-create al terminar.
func ejecutarPrCreate(worktree string, args []string) {
	flags, err := parsearFlagsPrCreate(args)
	salirSiError(err)

	cfg := config.CargarConfiguracionLocal(worktree)
	gitDir, err := git.ObtenerGitDir()
	salirSiError(err)

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
		SoloPendientes: false,
		Overview:       true,
		Fabrica:        fabrica,
		Parallel:       cfg.Review.Parallel,
		OnDimension: func(dim string) {
			fmt.Printf("  ⏳ %s …\n", dim)
		},
	})
	salirSiError(err)

	if len(res.Fichas) == 0 {
		fmt.Println("_No hay commits auditados en la rama._")
		os.Exit(1)
	}

	// Gate de block: sin superar no se publica.
	permitido, bloqueantes := gateBlock(res.Fichas, flags.force)
	if !permitido {
		fmt.Println("🚨 Veredicto de auditoría: block. No se publica la PR. Bloqueantes:")
		for _, h := range bloqueantes {
			fmt.Printf("  - [%s] %s (%s:%d)\n", h.Severity, h.Description, h.File, h.Line)
		}
		fmt.Println("Resuelve los bloqueantes o repite con --force para publicar igualmente.")
		os.Exit(1)
	}

	// Rama descomunal sin --chain-pr: se propone la cadena, no se publica una
	// PR gigante (guía §12.4).
	if res.Decision == "chain" && !flags.chainPR {
		fmt.Println("🚨 Rama descomunal: supera el umbral de volumen sin coherencia demostrada.")
		fmt.Println("Se propone dividirla en PRs encadenadas (--chain-pr) en lugar de una PR gigante.")
		os.Exit(1)
	}

	verificacion := verificarParaPlantilla(worktree, gitDir, cfg)

	cuerpo := review.RenderPlantillaPr(res.Fichas, res.Overview, verificacion, version)
	rutaPlantilla, err := escribirPlantillaPR(cuerpo)
	salirSiError(err)

	prURL, fallback, err := publicarPR(worktree, rutaPlantilla)
	salirSiError(err)
	if fallback {
		// El archivo es el artefacto entregable del fallback: se conserva.
		fmt.Println("? Plantilla en portapapeles: crea la PR manualmente con ese contenido.")
	} else {
		fmt.Printf("? PR creada: %s\n", prURL)
		// El cuerpo ya vive en la PR: el temporal efímero se limpia.
		if err := os.Remove(rutaPlantilla); err != nil {
			fmt.Printf("? Aviso: no se pudo limpiar el archivo temporal (%v).\n", err)
		}
	}

	detalle, err := detalleEventoPrCreate(prURL, fallback, flags.chainPR)
	if err != nil {
		fmt.Printf("? Aviso: no se pudo construir el detalle del evento: %v\n", err)
	}
	if err := ops.RegistrarEvento(gitDir, "pr-create", 0, res.SHAs, detalle, worktree); err != nil {
		fmt.Printf("? Aviso: no se pudo registrar el evento: %v\n", err)
	}
}
