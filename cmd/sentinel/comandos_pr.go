package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/modelprobe"
	"github.com/ISeoane-Quental/vas.sentinel/internal/ops"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewexec"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

const honestNetIntention = "No PR title/description exists before publication: claims cover branch commits and the net diff only."

// stackOwnDiff maps CLI stack signals; explicit --parent wins.
func stackOwnDiff(parent string, chainPR bool) *review.OwnDiffOptions {
	switch {
	case parent != "":
		return &review.OwnDiffOptions{Parent: parent}
	case chainPR:
		return &review.OwnDiffOptions{ResolveParent: true}
	}
	return nil
}

// resolveBlobStore resolves the blob store AnalizarRama needs to reuse reviews
// by CONTENT instead of by SHA (F2 exit criterion), which is what makes
// rebasing the base of a stacked PR cheap (F8 exit criterion 2): without it
// every rewritten SHA looks unreviewed and the whole stack is audited again.
//
// It MUST receive the git COMMON dir, never the per-worktree git dir: linked
// worktrees share one store, and store.NuevoStore documents that contract.
//
// Reuse never fabricates a verdict: AnalizarRama adopts an EXISTING ficha
// under the new SHA, exactly as the ledger cache already did per SHA. The
// store is therefore optional, and the error travels to the caller instead of
// to os.Stderr so every command routes the warning through the writer it
// already uses for its own diagnostics; a nil store restores the SHA-only
// behaviour rather than aborting a real review.
func resolveBlobStore(worktree string) (review.StoreBlobs, error) {
	commonDir, err := git.ObtenerGitCommonDir(worktree)
	if err != nil {
		return nil, err
	}
	return store.NuevoStore(commonDir), nil
}

func retiredPassthroughDisposition() (string, int) {
	return "The legacy 'sentinel pr [gh arguments]' passthrough was removed because it bypassed the guardian's review flow. Use 'sentinel pr create' to publish a reviewed pull request or 'sentinel pr review' for a dry-run analysis.", 1
}

func verboPr(args []string) string {
	if len(args) > 0 {
		switch args[0] {
		case "review":
			return "review"
		case "create":
			return "create"
		}
	}
	return ""
}

func ejecutarPr(worktree string, args []string) {
	switch verboPr(args) {
	case "review":
		ejecutarPrReview(worktree, args[1:])
	case "create":
		ejecutarPrCreate(worktree, args[1:])
	default:
		msg, code := retiredPassthroughDisposition()
		fmt.Println(msg)
		os.Exit(code)
	}
}

// flagsPrReview son las opciones de pr review.
type flagsPrReview struct {
	base           string
	soloPendientes bool // --only-unaudited
	overview       bool // --overview
	jsonOut        bool // --json
	parent         string
}

func parseParentFlagValue(args []string, at int) (string, error) {
	if at >= len(args) || strings.TrimSpace(args[at]) == "" || strings.HasPrefix(args[at], "-") {
		return "", fmt.Errorf("--parent requires a non-blank branch value (the stacked parent branch)")
	}
	return args[at], nil
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
		case "--parent":
			i++
			val, err := parseParentFlagValue(args, i)
			if err != nil {
				return flags, err
			}
			flags.parent = val
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

// detalleEventoPrReview construye el detail estructurado del evento pr-review
// (esquema de la guía §13).
func detalleEventoPrReview(base string, res *review.ResultadoRama, ci bool) (ops.EventDetail, error) {
	detalle := ops.EventDetail{
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
	if fallos := detallesFallasFichas(res.Fichas); len(fallos) > 0 {
		detalle["reviewer_failures"] = fallos
	}
	return detalle, nil
}

func detallesFallasFichas(fichas []review.Ficha) []ops.EventDetail {
	var fallos []ops.EventDetail
	for _, ficha := range fichas {
		if len(ficha.Revisions) == 0 {
			continue
		}
		ultima := ficha.Revisions[len(ficha.Revisions)-1]
		for _, dimension := range ultima.Dims {
			if dimension.Verdict != review.VerdictUnavailable || strings.TrimSpace(dimension.Reason) == "" {
				continue
			}
			fallos = append(fallos, ops.EventDetail{
				"sha":       ficha.SHA,
				"dimension": dimension.Dim,
				"reason":    review.CausaProveedorCompacta(dimension.Reason),
			})
		}
	}
	return fallos
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

// opcionesRamaPrReview assembles the branch-analysis options pr review hands to
// AnalizarRama. It is a separate function, not an inline literal, because
// ejecutarPrReview calls os.Exit and cannot be driven from a test: the wiring it
// carries — notably the blob store that keeps a base rebase cheap (F8 criterion
// 2) — would otherwise be deletable without failing anything.
//
// The named results say what the signature alone would get wrong: avisoStore is
// ONLY the blob-store resolution failure, never a reason to abort. Reuse is
// optional, so opciones comes back fully usable with a nil Store and the caller
// warns through its own stream instead of returning.
func opcionesRamaPrReview(cfg config.Config, verificador *modelprobe.Verificador, worktree string, flags flagsPrReview, fabrica review.FabricaAuditor) (opciones review.OpcionesRama, avisoStore error) {
	base := flags.base
	if base == "" {
		base = "main"
	}
	blobStore, avisoStore := resolveBlobStore(worktree)
	return opcionesRamaConRefutador(cfg, verificador, review.OpcionesRama{
		Base:                   base,
		SoloPendientes:         flags.soloPendientes,
		Overview:               flags.overview,
		Fabrica:                fabrica,
		Parallel:               cfg.Review.Parallel,
		Store:                  blobStore,
		ReviewTransportFactory: reviewTransportFactory(cfg, worktree),
		OnCommit: func(idx, total int, sha string) {
			fmt.Printf("⏳ [%d/%d] Auditar %s\n", idx+1, total, shaCorto(sha))
		},
		OnDimension: func(dim string) {
			fmt.Printf("  ⏳ %s …\n", dim)
		},
		OwnDiff:   stackOwnDiff(flags.parent, false),
		NetReview: &review.NetReviewOptions{Intention: honestNetIntention, Validation: "pr review performs no deterministic validation"},
	}), avisoStore
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
	gitDir, err := git.ObtenerGitDirDe(worktree)
	if err != nil {
		fmt.Printf("? %v\n", err)
		os.Exit(1)
	}

	verificadorModelo := nuevoVerificadorModelo(worktree)
	fabrica := func(_ review.ReviewBundle, dimension string) (review.AuditorAgente, string, error) {
		profile := config.ResolverPerfil(cfg, reviewcontract.DefaultProfile(dimension), "")
		adapter, err := agentadapter.NuevoAdaptadorConPerfil(cfg, profile)
		if err != nil {
			return nil, profile.Nombre, err
		}
		verificadorModelo.Verificar(profile.Nombre, profile.Modelo, adapter)
		return adapter, profile.Nombre, nil
	}

	opciones, err := opcionesRamaPrReview(cfg, verificadorModelo, worktree, flags, fabrica)
	if err != nil {
		fmt.Printf("⚠️  Aviso: no se pudo resolver el git-common-dir; las revisiones no se reutilizaran por contenido tras un rebase (%v).\n", err)
	}
	base := opciones.Base
	ledger := review.NuevoLedger(gitDir)
	res, err := review.AnalizarRama(ledger, opciones)
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
		salida := salidaJSONPrReview(base, res)
		datos, err := json.MarshalIndent(salida, "", "  ")
		if err != nil {
			fmt.Printf("? %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(datos))
		return
	}

	if res.Net != nil { // T8.4/A: the authoritative verdict leads the report
		fmt.Println(review.VerdictLine(res))
	}
	if len(res.Fichas) > 0 {
		fmt.Println("OWN (per-commit audit)")
		fmt.Println(review.RenderMatriz(res.Fichas))
		if res.Net == nil { // historical summary only without a net authority
			fmt.Println(review.RenderResumen(res.Fichas))
		}
		fmt.Println(textoDecision(res.Decision, res.Volumen))
	}
	fmt.Print(review.InheritedSection(res.Heredados))
	// Ticket 07: admission failures are first-class evidence, so the terminal
	// report never lets them pass as generic infrastructure unavailability.
	if admission, _ := conteoNoDisponibles(res.Fichas); admission > 0 {
		fmt.Printf("? %d unavailable dimension record(s) are ADMISSION failures (evidence rejected before verdicts), not infrastructure outages.\n", admission)
	}
	if res.OverviewError != "" {
		fmt.Printf("? Aviso: el overview no se pudo obtener (%s); se decidió por volumen.\n", res.OverviewError)
	}
}

// salidaJSONPrReview builds the public --json result shape of pr review,
// including the ticket-07 classification of unavailable dimension records:
// review_admission_failures and review_infrastructure_failures are counted
// separately so consumers can distinguish rejected evidence from outages.
func salidaJSONPrReview(base string, res *review.ResultadoRama) map[string]any {
	admission, infrastructure := conteoNoDisponibles(res.Fichas)
	salida := map[string]any{
		"rama":       res.Rama,
		"base":       base,
		"shas":       res.SHAs,
		"pendientes": res.Pendientes,
		"fichas":     res.Fichas,
		"volumen":    res.Volumen,
		"decision":   res.Decision,
		"overview":   res.Overview,
		// Ticket 07: admission vs infrastructure split over the append-only
		// revision history of every audited commit on the branch.
		"review_admission_failures":      admission,
		"review_infrastructure_failures": infrastructure,
	}
	if res.OverviewError != "" {
		salida["overview_error"] = res.OverviewError
	}
	if res.Propio != nil { // T8.4/E
		salida["own"] = res.Propio
	}
	if len(res.Heredados) > 0 {
		salida["inherited"] = res.Heredados
	}
	if res.Net != nil {
		salida["net"] = res.Net
	}
	return salida
}

// conteoNoDisponibles classifies every unavailable DimensionResult recorded
// in the branch's revisions: reasons carrying the literal admission prefix
// are admission failures; everything else stays infrastructure. It reads the
// persisted ledger shape, where the typed transport error no longer exists.
func conteoNoDisponibles(fichas []review.Ficha) (admission, infrastructure int) {
	for _, ficha := range fichas {
		for _, revision := range ficha.Revisions {
			for _, dim := range revision.Dims {
				if dim.Verdict != review.VerdictUnavailable {
					continue
				}
				if reviewexec.IsAdmissionReason(dim.Reason) {
					admission++
				} else {
					infrastructure++
				}
			}
		}
	}
	return admission, infrastructure
}

// flagsPrCreate son las opciones de pr create.
type flagsPrCreate struct {
	base    string
	chainPR bool   // --chain-pr: publicar la rama completa aunque sea descomunal
	force   bool   // --force: superar la validación en rojo (T1.8: el único gate que bloquea)
	reason  string // --reason: motivo explícito y obligatorio junto a --force
	parent  string
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
		case "--parent":
			i++
			val, err := parseParentFlagValue(args, i)
			if err != nil {
				return flags, err
			}
			flags.parent = val
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
func detalleEventoPrCreate(prURL string, fallback, chain, force bool, motivo string) (ops.EventDetail, error) {
	detalle := ops.EventDetail{
		"pr_url":   prURL,
		"fallback": fallback,
		"chain_pr": chain,
		"force":    force,
	}
	if force {
		detalle["motivo"] = motivo
	}
	return detalle, nil
}

// resolverActor identifica quién ejecuta el proceso, para la trazabilidad de
// decisiones de T7.5 (informe M3: --force sin traza de quién ni por qué).
// No existía ningún helper de identidad en el codebase (verificado): prueba
// `git config user.name` primero (con cmd.Dir=worktree, NUNCA el cwd del
// proceso: si worktree difiere del repositorio actual y ese otro repo define
// un user.name local propio, la decisión se atribuiría al actor equivocado),
// cae a $USER (POSIX) / $USERNAME (Windows) si está vacío o falla, y usa un
// placeholder explícito como último recurso en vez de dejar el campo vacío.
func resolverActor(worktree string) string {
	cmd := exec.Command("git", "config", "user.name")
	cmd.Dir = worktree
	if salida, err := cmd.Output(); err == nil {
		if nombre := strings.TrimSpace(string(salida)); nombre != "" {
			return nombre
		}
	}
	if u := strings.TrimSpace(os.Getenv("USER")); u != "" {
		return u
	}
	if u := strings.TrimSpace(os.Getenv("USERNAME")); u != "" {
		return u
	}
	return "desconocido"
}

// verificarParaPlantilla ejecuta la verificación honesta (guía §12.3) y la
// traduce a la sección de la plantilla: exit codes reales por comando
// configurado, contrato tested del agente o motivo de omisión. Un fallo de la
// verificación NUNCA queda en silencio: se refleja como motivo en la
// plantilla para que el PR sea transparente sobre lo que se comprobó.
func verificarParaPlantilla(worktree, gitDir string, cfg config.Config) review.VerificacionPlantilla {
	return verificarParaPlantillaCon(worktree, gitDir, cfg, nuevoVerificadorModelo(worktree), ops.Verificar)
}

// verificarParaPlantillaCon es la versión inyectable de verificarParaPlantilla:
// verificar nil se sustituye por ops.Verificar en producción. El verificador
// debe ser el compartido por toda la invocación para no repetir el sondeo.
func verificarParaPlantillaCon(worktree, gitDir string, cfg config.Config, verificadorModelo *modelprobe.Verificador,
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
	if verificadorModelo == nil {
		verificadorModelo = nuevoVerificadorModelo(worktree)
	}
	verificadorModelo.Verificar(perfil.Nombre, perfil.Modelo, adapter)
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
	obtenerSHAHead     func() (string, error)
	ejecutarValidacion func(perfil string, alcance []string, opts validation.OpcionesEjecucion) ([]validation.ValidationRun, error)
	analizarRama       func(gitDir string, opts review.OpcionesRama) (*review.ResultadoRama, error)
	verificar          func(worktree, gitDir string, cfg config.Config, verificadorModelo *modelprobe.Verificador) review.VerificacionPlantilla
	publicar           func(worktree, rutaPlantilla, base string) (string, bool, error)
	registrarEvento    func(gitDir, tipo string, exit int, shas []string, detalle any, worktree string) error
	// obtenerGitCommonDir y registrarDecision cubren T7.5 (informe M3): el
	// --force que supera una validación en rojo deja de ser una excepción
	// sin traza. store.NuevoStore exige el git-common-dir (compartido entre
	// worktrees enlazados), NUNCA el gitDir por-worktree que ya usa
	// registrarEvento arriba: son dos directorios con dos contratos
	// distintos (ver el doc comment de store.NuevoStore).
	obtenerGitCommonDir func(worktree string) (string, error)
	registrarDecision   func(commonDir string, d *store.Decision) error
	// resolverActor es una costura más de este mismo esfuerzo: sin ella,
	// ejecutarPrCreateCon llamaría a resolverActor(worktree) directamente,
	// que shellea a `git config user.name` de verdad, rompiendo la promesa
	// de depsPrCreate de testear "sin git, agentes ni gh reales" (comentario
	// de arriba).
	resolverActor func(worktree string) string
	// escribirPlantilla allows tests to observe whether the PR template was
	// created. When nil, ejecutarPrCreateCon uses escribirPlantillaPR.
	escribirPlantilla func(string) (string, error)
	// blobStore builds the content-addressed store that lets AnalizarRama
	// reuse reviews after a rebase (F8 criterion 2). It is a seam because
	// resolveBlobStore shells out to git, which depsPrCreate exists to avoid;
	// nil means no reuse, the behaviour before this wiring.
	blobStore func(worktree string) (review.StoreBlobs, error)
}

// ejecutarPrCreate implementa pr create (T1.8): valida ANTES de auditar (si
// falla sin --force, ni se llama a AnalizarRama: cero tokens), aviso advisory
// del veredicto semántico, plantilla honesta con las dos naturalezas de
// evidencia y publicación con gh o fallback a portapapeles.
func ejecutarPrCreate(worktree string, args []string) {
	os.Exit(ejecutarPrCreateCon(os.Stdout, worktree, args, depsPrCreateReales()))
}

// depsPrCreateReales resolves the production seams of pr create. Extracted from
// ejecutarPrCreate for the same reason as opcionesRamaPrReview: ejecutarPrCreate
// calls os.Exit, so a seam silently losing its production wiring — the blob
// store of F8 criterion 2 among them — would fail no test.
func depsPrCreateReales() depsPrCreate {
	return depsPrCreate{
		// Config ESTRICTA (hallazgo del orquestador, F1): pr create es
		// justo el comando cuyo punto entero es "la validación manda", así
		// que un yml roto debe fallar alto igual que gate/pr review/status,
		// nunca seguir en silencio con la config por defecto.
		cargarConfig:       config.CargarConfiguracionLocalEstricta,
		obtenerGitDir:      git.ObtenerGitDir,
		obtenerSHAHead:     git.SHAHead,
		ejecutarValidacion: validation.EjecutarPerfilSobreCandidato,
		analizarRama: func(gitDir string, opts review.OpcionesRama) (*review.ResultadoRama, error) {
			return review.AnalizarRama(review.NuevoLedger(gitDir), opts)
		},
		verificar: func(worktree, gitDir string, cfg config.Config, verificadorModelo *modelprobe.Verificador) review.VerificacionPlantilla {
			return verificarParaPlantillaCon(worktree, gitDir, cfg, verificadorModelo, ops.Verificar)
		},
		publicar:            publicarPR,
		registrarEvento:     ops.RegistrarEvento,
		obtenerGitCommonDir: git.ObtenerGitCommonDir,
		registrarDecision: func(commonDir string, d *store.Decision) error {
			return store.NuevoStore(commonDir).RegistrarDecision(d)
		},
		resolverActor:     resolverActor,
		escribirPlantilla: escribirPlantillaPR,
		blobStore:         resolveBlobStore,
	}
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
		// T7.5 (informe M3): --force deja de ser una excepción sin traza.
		// No aborta si falla (--force ya decidió seguir pese a la
		// validación en rojo, igual que el aviso de obtenerSHAHead más
		// abajo): se avisa y se continúa.
		if commonDir, err := deps.obtenerGitCommonDir(worktree); err != nil {
			fmt.Fprintf(w, "⚠️  Aviso: no se pudo resolver el git-common-dir, la decisión de --force no queda registrada (%v).\n", err)
		} else if err := deps.registrarDecision(commonDir, &store.Decision{
			Decision: store.DecisionForceBypass,
			Actor:    deps.resolverActor(worktree),
			At:       time.Now().UTC(),
			Motivo:   flags.reason,
			Alcance:  store.AlcancePrCreate,
		}); err != nil {
			fmt.Fprintf(w, "⚠️  Aviso: no se pudo escribir la decisión de --force en decisions.jsonl (%v).\n", err)
		}
	}
	// Con --force, la revisión semántica SÍ se ejecuta pese a la validación en
	// rojo (a diferencia de sentinel gate, que corta en corto para no gastar
	// tokens): ambas fuentes conviven en el mismo reporte, así que el
	// hallazgo determinista debe poder suplantar al semántico equivalente
	// (T6.2) en vez de duplicar la misma señal dos veces. El SHA validado se
	// resuelve explícitamente (nunca inferido por posición en la rama): sin
	// él, AnalizarRama no aplica los hallazgos a ningún commit (fail-safe).
	var hallazgosDeterministas []review.Hallazgo
	var shaValidado string
	if forzoValidacionEnRojo {
		hallazgosDeterministas = proyectarHallazgosValidacion(hallazgos)
		sha, err := deps.obtenerSHAHead()
		if err != nil {
			// No aborta la publicación (--force ya decidió seguir pese a la
			// validación en rojo): pero sin el SHA no hay a qué commit
			// asociar los hallazgos deterministas, así que el supersede de
			// T6.2 no se aplica en esta ejecución. Se avisa explícitamente
			// en vez de descartarlo en silencio.
			fmt.Fprintf(w, "⚠️  Aviso: no se pudo resolver el commit validado (%v); los hallazgos deterministas no suplantarán al hallazgo semántico equivalente en este reporte.\n", err)
		} else {
			shaValidado = sha
		}
	}

	verificadorModelo := nuevoVerificadorModelo(worktree)
	fabrica := func(_ review.ReviewBundle, dimension string) (review.AuditorAgente, string, error) {
		profile := config.ResolverPerfil(cfg, reviewcontract.DefaultProfile(dimension), "")
		adapter, err := agentadapter.NuevoAdaptadorConPerfil(cfg, profile)
		if err != nil {
			return nil, profile.Nombre, err
		}
		verificadorModelo.Verificar(profile.Nombre, profile.Modelo, adapter)
		return adapter, profile.Nombre, nil
	}

	base := flags.base
	if base == "" {
		base = "main"
	}
	var blobStore review.StoreBlobs
	if deps.blobStore != nil {
		var err error
		if blobStore, err = deps.blobStore(worktree); err != nil {
			fmt.Fprintf(w, "⚠️  Aviso: no se pudo resolver el git-common-dir; las revisiones no se reutilizaran por contenido tras un rebase (%v).\n", err)
		}
	}
	res, err := deps.analizarRama(gitDir, opcionesRamaConRefutador(cfg, verificadorModelo, review.OpcionesRama{
		Base:                      base,
		SoloPendientes:            false,
		Overview:                  true,
		HallazgosDeterministas:    hallazgosDeterministas,
		HallazgosDeterministasSHA: shaValidado,
		Fabrica:                   fabrica,
		Parallel:                  cfg.Review.Parallel,
		Store:                     blobStore,
		ReviewTransportFactory:    reviewTransportFactory(cfg, worktree),
		OnCommit: func(idx, total int, sha string) {
			fmt.Fprintf(w, "⏳ [%d/%d] Auditar %s\n", idx+1, total, shaCorto(sha))
		},
		OnDimension: func(dim string) {
			fmt.Fprintf(w, "  ⏳ %s …\n", dim)
		},
		OwnDiff:   stackOwnDiff(flags.parent, flags.chainPR),
		NetReview: &review.NetReviewOptions{Intention: honestNetIntention, Validation: fmt.Sprint(comandosDeValidacion(runs))},
	}))
	if err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}

	if len(res.Fichas) == 0 {
		fmt.Fprintln(w, "_No hay commits auditados en la rama._")
		return 1
	}

	// The net audit is the advisory authority when present.
	if res.Net != nil {
		fmt.Fprintln(w, review.VerdictLine(res))
	} else if avisar, bloqueantes := avisoSemantico(res.Fichas); avisar {
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

	verificacion := deps.verificar(worktree, gitDir, cfg, verificadorModelo)
	verificacion.Validacion = comandosDeValidacion(runs)

	publishBase := base
	if res.Propio != nil {
		if res.Propio.PublicationBranch == "" {
			fmt.Fprintln(w, "? Refusing to publish: the stacked parent has no verified publication branch.")
			return 1
		}
		publishBase = res.Propio.PublicationBranch
	}

	cuerpo := review.RenderBranchPRTemplate(res, verificacion, version)
	escribir := deps.escribirPlantilla
	if escribir == nil {
		escribir = escribirPlantillaPR
	}
	rutaPlantilla, err := escribir(cuerpo)
	if err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}
	prURL, fallback, err := deps.publicar(worktree, rutaPlantilla, publishBase)
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

func opcionesRamaConRefutador(cfg config.Config, verificador *modelprobe.Verificador, opts review.OpcionesRama) review.OpcionesRama {
	opts.FabricaRefutador = fabricaRefutador(cfg, verificador)
	return opts
}
