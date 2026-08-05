package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

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
// sin publicar nada; pr create se implementa en la fase 6 y por ahora avisa;
// el resto mantiene el passthrough legacy a gh pr create.
func ejecutarPr(worktree string, args []string) {
	switch verboPr(args) {
	case "review":
		ejecutarPrReview(worktree, args[1:])
	case "create":
		fmt.Println("? pr create se implementa en la fase 6 del plan (plantilla honesta + gate de block). Usa 'gh pr create' o 'sentinel pr' directamente.")
		os.Exit(1)
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
