package pr

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/modelprobe"
	"github.com/ISeoane-Quental/vas.sentinel/internal/ops"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

// FlagsPrCreate carries the parsed `pr create` options across the dispatch
// boundary. cmd/sentinel owns the flag parsing (the package-main tests drive
// it); this struct is its counterpart here, so the fields are exported.
type FlagsPrCreate struct {
	Base    string
	ChainPR bool   // --chain-pr: publicar la rama completa aunque sea descomunal
	Force   bool   // --force: superar la validación en rojo (T1.8: el único gate que bloquea)
	Reason  string // --reason: motivo explícito y obligatorio junto a --force
	Parent  string
}

// DetalleEventoPrCreate construye el detail del evento pr-create (guía §13):
// acta de publicación con pr_url, fallback y chain_pr. Amplía T1.8: force
// registra si se superó la validación en rojo, y motivo (solo si force) deja
// constancia explícita de por qué — la excepción nunca queda en silencio.
func DetalleEventoPrCreate(prURL string, fallback, chain, force bool, motivo string) (ops.EventDetail, error) {
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

// DepsPrCreate agrupa las costuras inyectables del pipeline de pr create
// (T1.8): permite testear el ORDEN (validación antes de auditar, cero tokens
// si falla) sin git, agentes ni gh reales. En producción las resuelve
// depsPrCreateReales en cmd/sentinel, que construye el struct gemelo con
// campos no exportados que los tests del paquete main construyen; este es su
// equivalente exportado dentro del flujo.
type DepsPrCreate struct {
	CargarConfig       func(worktree string) (config.Config, error)
	ObtenerGitDir      func() (string, error)
	ObtenerSHAHead     func() (string, error)
	EjecutarValidacion func(perfil string, alcance []string, opts validation.OpcionesEjecucion) ([]validation.ValidationRun, error)
	// AnalizarRama receives the WORKTREE, not a gitDir: where the review
	// ledger is anchored is a production decision that lives in
	// sharedReviewLedger, not something the caller picks per invocation.
	AnalizarRama    func(worktree string, opts review.OpcionesRama) (*review.ResultadoRama, error)
	Verificar       func(worktree, gitDir string, cfg config.Config, verificadorModelo *modelprobe.Verificador) review.VerificacionPlantilla
	Publicar        func(worktree, rutaPlantilla, base string) (string, bool, error)
	RegistrarEvento func(gitDir, tipo string, exit int, shas []string, detalle ops.EventDetail, worktree string) error
	// ObtenerGitCommonDir y RegistrarDecision cubren T7.5 (informe M3): el
	// --force que supera una validación en rojo deja de ser una excepción
	// sin traza. store.NuevoStore exige el git-common-dir (compartido entre
	// worktrees enlazados), NUNCA el gitDir por-worktree que ya usa
	// RegistrarEvento arriba: son dos directorios con dos contratos
	// distintos (ver el doc comment de store.NuevoStore).
	ObtenerGitCommonDir func(worktree string) (string, error)
	RegistrarDecision   func(commonDir string, d *store.Decision) error
	// ResolverActor es una costura más de este mismo esfuerzo: sin ella,
	// EjecutarPrCreateCon llamaría a resolverActor(worktree) directamente,
	// que shellea a `git config user.name` de verdad, rompiendo la promesa
	// de DepsPrCreate de testear "sin git, agentes ni gh reales" (comentario
	// de arriba).
	ResolverActor func(worktree string) string
	// EscribirPlantilla allows tests to observe whether the PR template was
	// created. When nil, EjecutarPrCreateCon uses EscribirPlantillaPR.
	EscribirPlantilla func(string) (string, error)
	// BlobStore builds the content-addressed store that lets AnalizarRama
	// reuse reviews after a rebase (F8 criterion 2). It is a seam because
	// ResolveBlobStore shells out to git, which DepsPrCreate exists to avoid;
	// nil means no reuse, the behaviour before this wiring.
	BlobStore func(worktree string) (review.StoreBlobs, error)
	// LeerDisposiciones reads the standing human answers for the advisory
	// overlay. It is a seam because loadDispositionsForWorktree resolves
	// the real git common dir, which DepsPrCreate exists to avoid; nil
	// means no standing answers, the fixture every older test builds.
	LeerDisposiciones func(worktree string) ([]review.FindingDisposition, error)
}

// EjecutarPrCreateCon es la versión inyectable de ejecutarPrCreate (seam de
// prueba): devuelve el exit code sin terminar el proceso, mismo patrón que
// ejecutarGate. cmd/sentinel parses the flags (error → "? %v" and exit 1)
// before dispatching here; wiring carries the package-main collaborators this
// flow shares with the review and gate commands (see Wiring).
func EjecutarPrCreateCon(w io.Writer, worktree string, flags FlagsPrCreate, deps DepsPrCreate, wiring Wiring) int {
	cfg, err := deps.CargarConfig(worktree)
	if err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}
	gitDir, err := deps.ObtenerGitDir()
	if err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}

	// Validación PRIMERO (T1.8): reusa internal/validation (misma pieza que
	// usa internal/gate, ver comandos_gate.go), no el orquestador completo de
	// gate, porque AnalizarRama audita la RAMA entera, no un único commit como
	// hace AuditarCommit. Si falla sin --force, AnalizarRama NUNCA se invoca:
	// cero tokens gastados.
	runs, err := deps.EjecutarValidacion(wiring.PerfilGatePorDefecto, nil, validation.OpcionesEjecucion{
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
		if !flags.Force {
			fmt.Fprintln(w, "🚨 Validación en rojo: no se publica la PR. Comandos:")
			for _, h := range hallazgos {
				fmt.Fprintf(w, "  - ✖ %s (%s):\n%s\n", h.Capability, h.Comando, strings.TrimSpace(h.Evidencia))
			}
			fmt.Fprintln(w, "Corrige los comandos en rojo o repite con --force --reason \"motivo\" para publicar igualmente.")
			return 1
		}
		forzoValidacionEnRojo = true
		fmt.Fprintf(w, "⚠️  Validación en rojo superada con --force (motivo: %s).\n", flags.Reason)
		// T7.5 (informe M3): --force deja de ser una excepción sin traza.
		// No aborta si falla (--force ya decidió seguir pese a la
		// validación en rojo, igual que el aviso de obtenerSHAHead más
		// abajo): se avisa y se continúa.
		if commonDir, err := deps.ObtenerGitCommonDir(worktree); err != nil {
			fmt.Fprintf(w, "⚠️  Aviso: no se pudo resolver el git-common-dir, la decisión de --force no queda registrada (%v).\n", err)
		} else if err := deps.RegistrarDecision(commonDir, &store.Decision{
			Decision: store.DecisionForceBypass,
			Actor:    deps.ResolverActor(worktree),
			At:       time.Now().UTC(),
			Motivo:   flags.Reason,
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
		hallazgosDeterministas = wiring.ProyectarHallazgos(hallazgos)
		sha, err := deps.ObtenerSHAHead()
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

	verificadorModelo := wiring.NuevoVerificadorModelo(worktree)
	fabrica := func(_ review.ReviewBundle, dimension string) (review.AuditorAgente, string, error) {
		profile := config.ResolverPerfil(cfg, reviewcontract.DefaultProfile(dimension), "")
		adapter, err := agentadapter.NuevoAdaptadorConPerfil(cfg, profile)
		if err != nil {
			return nil, profile.Nombre, err
		}
		verificadorModelo.Verificar(profile.Nombre, profile.Modelo, adapter)
		return adapter, profile.Nombre, nil
	}

	base := flags.Base
	if base == "" {
		base = "main"
	}
	var blobStore review.StoreBlobs
	if deps.BlobStore != nil {
		var err error
		if blobStore, err = deps.BlobStore(worktree); err != nil {
			fmt.Fprintf(w, "⚠️  Aviso: no se pudo resolver el git-common-dir; las revisiones no se reutilizaran por contenido tras un rebase (%v).\n", err)
		}
	}
	// Standing human answers drive every rendered and advisory finding, and
	// carry into the net audit (FU-6 unit A). A corrupt log fails closed
	// before spending review tokens rather than auditing as if no human
	// answered. A nil seam means no standing answers, the fixture older
	// tests build; production always wires the real loader.
	var branchDispositions []review.FindingDisposition
	if deps.LeerDisposiciones != nil {
		var err error
		branchDispositions, err = deps.LeerDisposiciones(worktree)
		if err != nil {
			fmt.Fprintf(w, "? %v\n", err)
			return 1
		}
	}
	res, err := deps.AnalizarRama(worktree, wiring.OpcionesRamaConRefutador(cfg, verificadorModelo, review.OpcionesRama{
		Base:                      base,
		SoloPendientes:            false,
		Overview:                  true,
		HallazgosDeterministas:    hallazgosDeterministas,
		HallazgosDeterministasSHA: shaValidado,
		Fabrica:                   fabrica,
		Parallel:                  cfg.Review.Parallel,
		Store:                     blobStore,
		ReviewTransportFactory:    wiring.TransportFactory(cfg, worktree),
		// FU-11 residual: exposed-credential incidents ride every audited
		// commit through the per-commit deterministic channel.
		DeterministicFindingsFactory: SecretFindingsFactory(),
		NetReview:                    &review.NetReviewOptions{Intention: HonestNetIntention, Validation: fmt.Sprint(comandosDeValidacion(runs)), Dispositions: branchDispositions},
		OnCommit: func(idx, total int, sha string) {
			fmt.Fprintf(w, "⏳ [%d/%d] Auditar %s\n", idx+1, total, wiring.ShaCorto(sha))
		},
		OnDimension: func(dim string) {
			fmt.Fprintf(w, "  ⏳ %s …\n", dim)
		},
		OwnDiff: stackOwnDiff(flags.Parent, flags.ChainPR),
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
	} else if avisar, bloqueantes := AvisoSemanticoWithDispositions(res.Fichas, branchDispositions); avisar {
		fmt.Fprintln(w, "⚠️  AVISO: veredicto de auditoría semántica = block (no bloquea la publicación, advisory).")
		for _, h := range bloqueantes {
			fmt.Fprintf(w, "  - [%s] %s (%s:%d)\n", h.Severity, h.Description, h.File, h.Line)
		}
	}

	// Rama descomunal sin --chain-pr: se propone la cadena, no se publica una
	// PR gigante (guía §12.4).
	if res.Decision == "chain" && !flags.ChainPR {
		fmt.Fprintln(w, "🚨 Rama descomunal: supera el umbral de volumen sin coherencia demostrada.")
		fmt.Fprintln(w, "Se propone dividirla en PRs encadenadas (--chain-pr) en lugar de una PR gigante.")
		return 1
	}

	verificacion := deps.Verificar(worktree, gitDir, cfg, verificadorModelo)
	verificacion.Validacion = comandosDeValidacion(runs)

	publishBase := base
	if res.Propio != nil {
		if res.Propio.PublicationBranch == "" {
			fmt.Fprintln(w, "? Refusing to publish: the stacked parent has no verified publication branch.")
			return 1
		}
		publishBase = res.Propio.PublicationBranch
	}

	cuerpo := review.RenderBranchPRTemplateWithDispositions(res, verificacion, wiring.Version, branchDispositions)
	escribir := deps.EscribirPlantilla
	if escribir == nil {
		escribir = EscribirPlantillaPR
	}
	rutaPlantilla, err := escribir(cuerpo)
	if err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}
	prURL, fallback, err := deps.Publicar(worktree, rutaPlantilla, publishBase)
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

	detalle, err := DetalleEventoPrCreate(prURL, fallback, flags.ChainPR, forzoValidacionEnRojo, flags.Reason)
	if err != nil {
		fmt.Fprintf(w, "? Aviso: no se pudo construir el detalle del evento: %v\n", err)
	}
	if err := deps.RegistrarEvento(gitDir, "pr-create", 0, res.SHAs, detalle, worktree); err != nil {
		fmt.Fprintf(w, "? Aviso: no se pudo registrar el evento: %v\n", err)
	}
	return 0
}

// AvisoSemantico decide si el veredicto semántico de la rama merece un aviso
// destacado en la publicación (T1.8): el gate de bloqueo por veredicto pasa a
// advisory, igual que internal/gate desde T1.7 — la validación (más abajo) es
// ahora el único gate que puede impedir publicar. AvisoSemantico NUNCA decide
// si se publica, solo si hay que avisar. Devuelve los hallazgos CRITICAL
// ESTRUCTURADOS: el formateo sigue siendo responsabilidad del CLI.
//
// Antes se llamaba gateBlock y devolvía "permitido"; se renombra porque una
// función que ya no bloquea no puede seguir llamándose "gate...Block" sin
// mentir sobre lo que hace.
func AvisoSemantico(fichas []review.Ficha) (avisar bool, bloqueantes []review.ReviewFinding) {
	bloqueantes = review.BloqueantesDeRama(fichas)
	return len(bloqueantes) > 0, bloqueantes
}

// AvisoSemanticoWithDispositions is AvisoSemantico overlaid with the
// standing human answers (FU-6): a valid human refutation clears its
// finding from the branch blockers shown here exactly as in the engine and
// the gate.
func AvisoSemanticoWithDispositions(fichas []review.Ficha, dispositions []review.FindingDisposition) (avisar bool, bloqueantes []review.ReviewFinding) {
	bloqueantes = review.BloqueantesDeRamaWithDispositions(fichas, dispositions)
	return len(bloqueantes) > 0, bloqueantes
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
