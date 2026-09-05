package main

// FU-2/C6 moved the pr orchestration (review and create) to internal/app/pr as
// a pure one-subcommand split: this file keeps the flag parsing, the option
// and seam structs the package-main tests construct, the production wiring
// (depsPrCreateReales, wiringPr) and thin dispatch wrappers into the flows.
// Movement hazard cleared: the 21 live block fichas cite snapshot.go,
// comandos_runs.go and siblings — none cites cmd/sentinel/comandos_pr.go —
// and there are no standing dispositions (no dispositions.jsonl), so moving
// the orchestration out of this file orphans nothing.

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/app/pr"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/modelprobe"
	"github.com/ISeoane-Quental/vas.sentinel/internal/ops"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

// honestNetIntention aliases the intent string that now lives with the flows.
const honestNetIntention = pr.HonestNetIntention

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

// The adapters below translate the package-main option structs into their
// exported counterparts inside internal/app/pr. They are dumb field mappings:
// the orchestration itself lives across the boundary.

func flagsPrReviewAPr(f flagsPrReview) pr.FlagsPrReview {
	return pr.FlagsPrReview{
		Base:           f.base,
		SoloPendientes: f.soloPendientes,
		Overview:       f.overview,
		JsonOut:        f.jsonOut,
		Parent:         f.parent,
	}
}

func flagsPrCreateAPr(f flagsPrCreate) pr.FlagsPrCreate {
	return pr.FlagsPrCreate{
		Base:    f.base,
		ChainPR: f.chainPR,
		Force:   f.force,
		Reason:  f.reason,
		Parent:  f.parent,
	}
}

func depsPrCreateAPr(d depsPrCreate) pr.DepsPrCreate {
	return pr.DepsPrCreate{
		CargarConfig:        d.cargarConfig,
		ObtenerGitDir:       d.obtenerGitDir,
		ObtenerSHAHead:      d.obtenerSHAHead,
		EjecutarValidacion:  d.ejecutarValidacion,
		AnalizarRama:        d.analizarRama,
		Verificar:           d.verificar,
		Publicar:            d.publicar,
		RegistrarEvento:     d.registrarEvento,
		ObtenerGitCommonDir: d.obtenerGitCommonDir,
		RegistrarDecision:   d.registrarDecision,
		ResolverActor:       d.resolverActor,
		EscribirPlantilla:   d.escribirPlantilla,
		BlobStore:           d.blobStore,
		LeerDisposiciones:   d.leerDisposiciones,
	}
}

func detalleEventoPrReview(base string, res *review.ResultadoRama, ci bool) (ops.EventDetail, error) {
	return pr.DetalleEventoPrReview(base, res, ci)
}

func aplicarDisposicionesPrReview(opciones review.OpcionesRama, worktree string, cargar func(string) ([]review.FindingDisposition, error)) (review.OpcionesRama, error) {
	return pr.AplicarDisposicionesPrReview(opciones, worktree, cargar)
}

func textoDecision(decision string, volumen int) string {
	return pr.TextoDecision(decision, volumen)
}

func salidaJSONPrReview(base string, res *review.ResultadoRama) map[string]any {
	return pr.SalidaJSONPrReview(base, res)
}

// opcionesRamaPrReview assembles the pr review branch options; the assembler
// lives in internal/app/pr and receives the package-main collaborators through
// wiringPr.
func opcionesRamaPrReview(cfg config.Config, verificador *modelprobe.Verificador, worktree string, flags flagsPrReview, fabrica review.FabricaAuditor) (review.OpcionesRama, error) {
	return pr.OpcionesRamaPrReview(cfg, verificador, worktree, flagsPrReviewAPr(flags), fabrica, wiringPr())
}

// ejecutarPrReview analiza la rama contra la base y muestra la matriz de
// auditoría, el resumen y la decisión single/chain. Es dry-run: no publica
// nada. Registra el evento pr-review al terminar; the flow lives in
// internal/app/pr.
func ejecutarPrReview(worktree string, args []string) {
	flags, err := parsearFlagsPrReview(args)
	if err != nil {
		fmt.Printf("? %v\n", err)
		os.Exit(1)
	}
	pr.EjecutarPrReview(worktree, flagsPrReviewAPr(flags), wiringPr())
}

func avisoSemantico(fichas []review.Ficha) (avisar bool, bloqueantes []review.ReviewFinding) {
	return pr.AvisoSemantico(fichas)
}

func avisoSemanticoWithDispositions(fichas []review.Ficha, dispositions []review.FindingDisposition) (avisar bool, bloqueantes []review.ReviewFinding) {
	return pr.AvisoSemanticoWithDispositions(fichas, dispositions)
}

func detalleEventoPrCreate(prURL string, fallback, chain, force bool, motivo string) (ops.EventDetail, error) {
	return pr.DetalleEventoPrCreate(prURL, fallback, chain, force, motivo)
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

// verificarParaPlantillaCon delegates to internal/app/pr; the model-probe
// constructor travels as a closure so the package-main var is read at call
// time (tests swap it).
func verificarParaPlantillaCon(worktree, gitDir string, cfg config.Config, verificadorModelo *modelprobe.Verificador,
	verificar func(ops.OpcionesVerificar) (ops.ResultadoVerificacion, error)) review.VerificacionPlantilla {
	return pr.VerificarParaPlantillaCon(worktree, gitDir, cfg, verificadorModelo, verificar,
		func(worktree string) *modelprobe.Verificador { return nuevoVerificadorModelo(worktree) })
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

func copiarPortapapelesCon(texto string, existe func(string) bool, ejecutar func(string, string) error) error {
	return pr.CopiarPortapapelesCon(texto, existe, ejecutar)
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
// fallback) y si se usó el fallback. It stays in package main because the
// production ejecutarGh closure reports gh's exit code through
// exitCodeDeError, which the status command shares.
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

func publicarPRCon(worktree, rutaPlantilla, base string, opciones opcionesPublicarPR) (string, bool, error) {
	return pr.PublicarPRCon(worktree, rutaPlantilla, base, opciones.ghDisponible, opciones.ejecutarGh, opciones.copiar)
}

func resolveBlobStore(worktree string) (review.StoreBlobs, error) {
	return pr.ResolveBlobStore(worktree)
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
	// analizarRama receives the WORKTREE, not a gitDir: where the review
	// ledger is anchored is a production decision that lives in
	// sharedReviewLedger, not something the caller picks per invocation.
	analizarRama    func(worktree string, opts review.OpcionesRama) (*review.ResultadoRama, error)
	verificar       func(worktree, gitDir string, cfg config.Config, verificadorModelo *modelprobe.Verificador) review.VerificacionPlantilla
	publicar        func(worktree, rutaPlantilla, base string) (string, bool, error)
	registrarEvento func(gitDir, tipo string, exit int, shas []string, detalle ops.EventDetail, worktree string) error
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
	// leerDisposiciones reads the standing human answers for the advisory
	// overlay. It is a seam because loadDispositionsForWorktree resolves
	// the real git common dir, which depsPrCreate exists to avoid; nil
	// means no standing answers, the fixture every older test builds.
	leerDisposiciones func(worktree string) ([]review.FindingDisposition, error)
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
		analizarRama: func(worktree string, opts review.OpcionesRama) (*review.ResultadoRama, error) {
			ledger, err := sharedReviewLedger(worktree)
			if err != nil {
				return nil, err
			}
			return review.AnalizarRama(ledger, opts)
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
		escribirPlantilla: pr.EscribirPlantillaPR,
		blobStore:         resolveBlobStore,
		leerDisposiciones: loadDispositionsForWorktree,
	}
}

// ejecutarPrCreate implementa pr create (T1.8): valida ANTES de auditar (si
// falla sin --force, ni se llama a AnalizarRama: cero tokens), aviso advisory
// del veredicto semántico, plantilla honesta con las dos naturalezas de
// evidencia y publicación con gh o fallback a portapapeles.
func ejecutarPrCreate(worktree string, args []string) {
	os.Exit(ejecutarPrCreateCon(os.Stdout, worktree, args, depsPrCreateReales()))
}

// ejecutarPrCreateCon es la versión inyectable de ejecutarPrCreate (seam de
// prueba): devuelve el exit code sin terminar el proceso, mismo patrón que
// ejecutarGate; the flow lives in internal/app/pr.
func ejecutarPrCreateCon(w io.Writer, worktree string, args []string, deps depsPrCreate) int {
	flags, err := parsearFlagsPrCreate(args)
	if err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}
	return pr.EjecutarPrCreateCon(w, worktree, flagsPrCreateAPr(flags), depsPrCreateAPr(deps), wiringPr())
}

// opcionesRamaConRefutador sets the per-finding refuter factory on the branch
// options; it stays here because package-main tests drive it directly and the
// moved flows reach it through wiringPr.
func opcionesRamaConRefutador(cfg config.Config, verificador *modelprobe.Verificador, opts review.OpcionesRama) review.OpcionesRama {
	opts.FabricaRefutador = fabricaRefutador(cfg, verificador)
	return opts
}

// wiringPr collects the production collaborators that remain in package main
// because the review and gate commands share them. nuevoVerificadorModelo is
// read through a closure so a test that swaps the var is honored at call time.
func wiringPr() pr.Wiring {
	return pr.Wiring{
		NuevoVerificadorModelo:   func(worktree string) *modelprobe.Verificador { return nuevoVerificadorModelo(worktree) },
		SharedReviewLedger:       sharedReviewLedger,
		LoadDispositions:         loadDispositionsForWorktree,
		TransportFactory:         reviewTransportFactory,
		OpcionesRamaConRefutador: opcionesRamaConRefutador,
		ShaCorto:                 shaCorto,
		PerfilGatePorDefecto:     perfilGatePorDefecto,
		ProyectarHallazgos:       proyectarHallazgosValidacion,
		Version:                  version,
	}
}
