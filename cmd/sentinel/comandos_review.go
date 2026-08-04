package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/ops"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

// ejecutarReview audita uno o más commits contra el motor y guarda la ficha en
// el ledger. Códigos de salida (guía §9): 0 ok/warn, 1 block, 3 questions,
// 4 provider_unavailable. Con --gate, además, cualquier CRITICAL salta a 1.
func ejecutarReview(worktree string, args []string) {
	flags, err := parsearFlagsAuditoria(args)
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}

	cfg := config.CargarConfiguracionLocal(worktree)
	gitDir, err := git.ObtenerGitDir()
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}

	shas, err := resolverShasAuditoria(flags)
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}
	if len(shas) == 0 {
		fmt.Println("✅ No hay commits que auditar.")
		return
	}

	ledger := review.NuevoLedger(gitDir)
	detalle := fmt.Sprintf("flags: all=%v chain=%v gate=%v dims=%q", flags.all, flags.chain, flags.gate, flags.dims)
	exitFinal := 0

	if flags.prune {
		// --prune es un modo standalone: combinarlo con targets o flags de
		// auditoría sería ignorarlos en silencio (cf. flagsNoAplicablesAStatus).
		soloPrune := len(flags.targets) == 1 && flags.targets[0] == "HEAD" &&
			len(flags.dims) == 0 && !flags.all && !flags.chain && !flags.gate &&
			flags.profile == "" && flags.answer == ""
		if !soloPrune {
			fmt.Println("? review --prune no se combina con targets ni flags de auditoría (--dims/--all/--chain/--gate/--profile/--answer).")
			os.Exit(1)
		}
		eliminados, err := purgarHuerfanasConEventos(gitDir)
		if err != nil {
			fmt.Printf("? No se pudieron purgar fichas huérfanas: %v\n", err)
			os.Exit(1)
		}
		reportarPurga(gitDir, eliminados, flags.jsonOut, worktree)
		os.Exit(0)
	}
	total := len(shas)

	for idx, sha := range shas {
		fmt.Printf("⏳ [%d/%d] Auditar %s\n", idx+1, total, shaCorto(sha))
		mensaje, err := git.MensajeCommit(sha)
		if err != nil {
			fmt.Printf("⚠️ %s: no se pudo leer el mensaje: %v\n", sha[:8], err)
			continue
		}
		diff, err := git.DiffCommit(sha)
		if err != nil {
			fmt.Printf("⚠️ %s: no se pudo leer el diff: %v\n", sha[:8], err)
			continue
		}
		archivos, err := git.ArchivosDeCommit(sha)
		if err != nil {
			fmt.Printf("⚠️ %s: no se pudieron leer los archivos: %v\n", sha[:8], err)
			continue
		}

		dims := flags.dims
		if len(dims) == 0 {
			dims = review.DimensionesParaArchivos(archivos)
		}

		fabrica := func(dimension string) (review.AuditorAgente, string, error) {
			perfil := config.ResolverPerfil(cfg, dimension, flags.profile)
			adapter, err := agentadapter.NuevoAdaptadorConPerfil(cfg, perfil)
			if err != nil {
				return nil, perfil.Nombre, err
			}
			return adapter, perfil.Nombre, nil
		}

		resultado := review.AuditarCommit(fabrica, cfg.Review.Parallel, review.OpcionesAuditoria{
			SHA:            sha,
			Mensaje:        mensaje,
			Diff:           diff,
			Dims:           dims,
			Respuestas:     flags.answer,
			PerfilOverride: flags.profile,
			OnDimension: func(dim string) {
				fmt.Printf("  ⏳ %s …\n", dim)
			},
		})

		modelo := flags.profile
		if modelo == "" {
			modelo = "default"
		}
		fixed := revisionCorrigeBlockPrevio(ledger, sha, resultado.Veredicto)
		revision := review.Revision{At: time.Now(), Result: resultado.Veredicto, Fixed: fixed, Dims: dimsResultadosParaFicha(resultado.Dims)}
		if err := ledger.GuardarRevision(sha, mensaje, calcularBucket(archivos), modelo, revision); err != nil {
			fmt.Printf("⚠️ %s: no se pudo guardar la ficha: %v\n", sha[:8], err)
		}

		fmt.Print(resultado.String())
		if fixed {
			fmt.Printf("  ✅ revisión anterior en block corregida por esta revisión\n")
		}

		exit := codigoSalidaVeredicto(resultado.Veredicto)
		if flags.gate && tieneHallazgosCriticos(resultado) {
			exit = 1
		}
		if exit > exitFinal {
			exitFinal = exit
		}
		if err := ops.RegistrarEvento(gitDir, "review", exit, []string{sha}, detalle, worktree); err != nil {
			fmt.Printf("⚠️ No se pudo registrar el evento: %v\n", err)
		}
		registrarCorrecciones(ledger, gitDir, sha, archivos, mensaje, exit, worktree)
	}
	os.Exit(exitFinal)
}

// revisionCorrigeBlockPrevio indica si esta auditoría (sin block) corrige una
// revisión anterior del mismo SHA que estaba en block.
func revisionCorrigeBlockPrevio(ledger *review.Ledger, sha, veredicto string) bool {
	if veredicto == review.VerdictBlock {
		return false
	}
	ficha, err := ledger.LeerFicha(sha)
	if err != nil || ficha == nil || len(ficha.Revisions) == 0 {
		return false
	}
	return ficha.Revisions[len(ficha.Revisions)-1].Result == review.VerdictBlock
}

// registrarCorrecciones asocia un commit fix (mensaje fix(...) que sale sin
// críticos) con las fichas previas en block que tocan los mismos archivos:
// marca su FixedIn y registra un evento fix.
func registrarCorrecciones(ledger *review.Ledger, gitDir, sha string, archivos []string, mensaje string, exit int, worktree string) {
	if exit != 0 || !strings.HasPrefix(mensaje, "fix(") {
		return
	}
	shasPrevios, err := ledger.ListarFichas()
	if err != nil {
		return
	}
	archivosFix := map[string]bool{}
	for _, a := range archivos {
		archivosFix[a] = true
	}

	corregidos := []string{}
	for _, shaPrev := range shasPrevios {
		if shaPrev == sha {
			continue
		}
		ficha, err := ledger.LeerFicha(shaPrev)
		if err != nil || ficha == nil || len(ficha.Revisions) == 0 || ficha.FixedIn != "" {
			continue
		}
		ultima := ficha.Revisions[len(ficha.Revisions)-1]
		if ultima.Result != review.VerdictBlock {
			continue
		}
		if !fixTocaHallazgos(archivosFix, ultima.Dims) {
			continue
		}
		if err := ledger.MarcarCorregida(shaPrev, sha); err == nil {
			corregidos = append(corregidos, shaPrev)
		}
	}

	if len(corregidos) > 0 {
		_ = ops.RegistrarEvento(gitDir, "fix", 0, []string{sha},
			"corrige: "+strings.Join(corregidos, ","), worktree)
		fmt.Printf("  🔧 fix %s marcado como corrección de: %s\n", shaCorto(sha), strings.Join(corregidos, ","))
	}
}

// shaCorto recorta un SHA a 8 caracteres sin reventar si es más corto (los
// tests usan SHAs cortos).
func shaCorto(sha string) string {
	if len(sha) <= 8 {
		return sha
	}
	return sha[:8]
}

// fixTocaHallazgos indica si el commit fix toca algún archivo señalado en los
// hallazgos de las dimensiones previas.
func fixTocaHallazgos(archivosFix map[string]bool, dims []review.DimensionResult) bool {
	for _, dim := range dims {
		for _, hallazgo := range dim.Findings {
			if archivosFix[hallazgo.File] {
				return true
			}
		}
	}
	return false
}

// resolverShasAuditoria calcula los SHAs a auditar según los flags: la lista
// de commits pedida (default HEAD), la cadena desde el base (--chain) o todos
// los commits sin ficha (--all). --chain/--all no se combinan con targets
// explícitos: no tiene sentido mezclar dos criterios de selección.
func resolverShasAuditoria(flags flagsAuditoria) ([]string, error) {
	switch {
	case flags.chain:
		if len(flags.targets) > 1 {
			return nil, errors.New("--chain no se combina con una lista de commits: usa un solo target como extremo")
		}
		base, err := git.UpstreamOMain()
		if err != nil {
			return nil, err
		}
		return git.SHAsRango(base, flags.targets[0])
	case flags.all:
		if len(flags.targets) > 1 {
			return nil, errors.New("--all no se combina con una lista de commits: audita todo el historial sin ficha")
		}
		todos, err := git.SHAsHasta(flags.targets[0])
		if err != nil {
			return nil, err
		}
		gitDir, err := git.ObtenerGitDir()
		if err != nil {
			return nil, err
		}
		ledger := review.NuevoLedger(gitDir)
		auditados, err := ledger.ListarFichas()
		if err != nil {
			return nil, err
		}
		yaAuditados := map[string]bool{}
		for _, sha := range auditados {
			yaAuditados[sha] = true
		}
		var pendientes []string
		for _, sha := range todos {
			if !yaAuditados[sha] {
				pendientes = append(pendientes, sha)
			}
		}
		return pendientes, nil
	default:
		resueltos := make([]string, 0, len(flags.targets))
		for _, expresion := range flags.targets {
			sha, err := git.ResolverSHA(expresion)
			if err != nil {
				return nil, fmt.Errorf("no se pudo resolver %q: %v", expresion, err)
			}
			resueltos = append(resueltos, sha)
		}
		return resueltos, nil
	}
}

// codigoSalidaVeredicto traduce el veredicto global al código de salida según
// la guía §9: ok/warn 0, block 1, question 3, unavailable 4.
func codigoSalidaVeredicto(veredicto string) int {
	switch veredicto {
	case review.VerdictBlock:
		return 1
	case review.VerdictQuestion:
		return 3
	case review.VerdictUnavailable:
		return 4
	default:
		return 0
	}
}

// tieneHallazgosCriticos indica si alguna dimensión reportó CRITICAL.
func tieneHallazgosCriticos(resultado review.ResultadoAuditoria) bool {
	for _, rd := range resultado.Dims {
		if rd.Resultado == nil {
			continue
		}
		for _, hallazgo := range rd.Resultado.Findings {
			if hallazgo.Severity == review.SevCritical {
				return true
			}
		}
	}
	return false
}

// dimsResultadosParaFicha copia los resultados de las dimensiones a la forma
// que persiste la ficha (sin el error ni el perfil, que ya van en otros campos).
func dimsResultadosParaFicha(dims []review.ResultadoDimension) []review.DimensionResult {
	resultados := make([]review.DimensionResult, 0, len(dims))
	for _, rd := range dims {
		if rd.Resultado != nil {
			resultados = append(resultados, *rd.Resultado)
		}
	}
	return resultados
}

// calcularBucket deduce el saco del commit: "mixto" si toca varias capas; si
// no, la capa de su único archivo (o "backend" como último recurso).
func calcularBucket(archivos []string) string {
	capas := map[string]bool{}
	for _, archivo := range archivos {
		capas[git.ClasificarCapa(archivo)] = true
	}
	if len(capas) == 1 {
		for capa := range capas {
			return capa
		}
	}
	if len(capas) == 0 {
		return "backend"
	}
	return "mixto"
}
