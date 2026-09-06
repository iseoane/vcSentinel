package pr

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/modelprobe"
	"github.com/ISeoane-Quental/vas.sentinel/internal/ops"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewexec"
)

const HonestNetIntention = "No PR title/description exists before publication: claims cover branch commits and the net diff only."

// FlagsPrReview carries the parsed `pr review` options across the dispatch
// boundary. cmd/sentinel owns the flag parsing (the package-main tests drive
// it); this struct is its counterpart here, so the fields are exported.
type FlagsPrReview struct {
	Base           string
	SoloPendientes bool // --only-unaudited
	Overview       bool // --overview
	JsonOut        bool // --json
	Parent         string
}

// DetalleEventoPrReview constructs the structured detail for the pr-review event
// (guide schema §13).
func DetalleEventoPrReview(base string, res *review.ResultadoRama, ci bool) (ops.EventDetail, error) {
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
	// An overview failure must not be silent in the event: if it was requested
	// and failed, the chain decision carries its cause.
	if res.OverviewError != "" {
		detalle["overview_error"] = res.OverviewError
	}
	if failures := reviewerFailureDetails(res.Fichas); len(failures) > 0 {
		detalle["reviewer_failures"] = failures
	}
	return detalle, nil
}

func reviewerFailureDetails(cards []review.Ficha) []ops.EventDetail {
	var failures []ops.EventDetail
	for _, card := range cards {
		if len(card.Revisions) == 0 {
			continue
		}
		latest := card.Revisions[len(card.Revisions)-1]
		for _, dimension := range latest.Dims {
			if dimension.Verdict != review.VerdictUnavailable || strings.TrimSpace(dimension.Reason) == "" {
				continue
			}
			failures = append(failures, ops.EventDetail{
				"sha":       card.SHA,
				"dimension": dimension.Dim,
				"reason":    review.CompactProviderCause(dimension.Reason),
			})
		}
	}
	return failures
}

// TextoDecision explica la decisión single/chain en la salida terminal.
func TextoDecision(decision string, volumen int) string {
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

// OpcionesRamaPrReview assembles the branch-analysis options pr review hands to
// AnalizarRama. It is a separate function, not an inline literal, because
// EjecutarPrReview calls os.Exit and cannot be driven from a test: the wiring it
// carries — notably the blob store that keeps a base rebase cheap (F8 criterion
// 2) — would otherwise be deletable without failing anything.
//
// The named results say what the signature alone would get wrong: avisoStore is
// ONLY the blob-store resolution failure, never a reason to abort. Reuse is
// optional, so opciones comes back fully usable with a nil Store and the caller
// warns through its own stream instead of returning.
func OpcionesRamaPrReview(cfg config.Config, verificador *modelprobe.Verificador, worktree string, flags FlagsPrReview, fabrica review.FabricaAuditor, wiring Wiring) (opciones review.OpcionesRama, avisoStore error) {
	base := flags.Base
	if base == "" {
		base = "main"
	}
	blobStore, avisoStore := ResolveBlobStore(worktree)
	return wiring.OpcionesRamaConRefutador(cfg, verificador, review.OpcionesRama{
		Base:                   base,
		SoloPendientes:         flags.SoloPendientes,
		Overview:               flags.Overview,
		Fabrica:                fabrica,
		Parallel:               cfg.Review.Parallel,
		Store:                  blobStore,
		ReviewTransportFactory: wiring.TransportFactory(cfg, worktree),
		// FU-11 residual: exposed-credential incidents ride every audited
		// commit through the per-commit deterministic channel.
		DeterministicFindingsFactory: SecretFindingsFactory(),
		ModelVerifier:                verificador,
		OnCommit: func(idx, total int, sha string) {
			fmt.Printf("⏳ [%d/%d] Auditar %s\n", idx+1, total, wiring.ShaCorto(sha))
		},
		OnDimension: func(dim string) {
			fmt.Printf("  ⏳ %s …\n", dim)
		},
		OwnDiff:   stackOwnDiff(flags.Parent, false),
		NetReview: &review.NetReviewOptions{Intention: HonestNetIntention, Validation: "pr review performs no deterministic validation"},
	}), avisoStore
}

// AplicarDisposicionesPrReview loads the standing human answers and sets
// them on the net review input for the cross-SHA carry-over. A corrupt log
// is an error: pr review must fail closed before spending review tokens
// rather than audit as if no human answered. The loader is a seam so tests
// drive this without git. A nil net review (no net audit requested) leaves
// the options untouched and still reports a loader failure.
func AplicarDisposicionesPrReview(opciones review.OpcionesRama, worktree string, cargar func(string) ([]review.FindingDisposition, error)) (review.OpcionesRama, error) {
	dispositions, err := cargar(worktree)
	if err != nil {
		return opciones, err
	}
	if opciones.NetReview != nil {
		opciones.NetReview.Dispositions = dispositions
	}
	return opciones, nil
}

// EjecutarPrReview analiza la rama contra la base y muestra la matriz de
// auditoría, el resumen y la decisión single/chain. Es dry-run: no publica
// nada. Registra el evento pr-review al terminar. cmd/sentinel parses the
// flags and exits on a parse error before dispatching here.
func EjecutarPrReview(worktree string, flags FlagsPrReview, wiring Wiring) {
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

	verificadorModelo := wiring.NuevoVerificadorModelo(worktree)
	fabrica := func(_ review.ReviewBundle, dimension string) (review.AuditorAgente, string, error) {
		profile := config.ResolverPerfil(cfg, reviewcontract.DefaultProfile(dimension), "")
		adapter, err := agentadapter.NuevoAdaptadorConPerfil(cfg, profile)
		if err != nil {
			return nil, profile.Nombre, err
		}
		verificadorModelo.Verify(profile.Nombre, profile.Modelo, adapter)
		return adapter, profile.Nombre, nil
	}

	// Anchored on the common directory, not on gitDir: see sharedReviewLedger.
	// AnalizarRama also WRITES here, through GuardarRevision and AdoptarFicha,
	// so this is where a rebase-adopted copy lands too.
	//
	// Resolved BEFORE the branch options, which warn-and-continue when the same
	// common directory cannot be resolved: reporting that content reuse is
	// degraded and then dying on the identical lookup told the operator the run
	// would continue when it could not.
	ledger, err := wiring.SharedReviewLedger(worktree)
	if err != nil {
		fmt.Printf("? %v\n", err)
		os.Exit(1)
	}
	opciones, err := OpcionesRamaPrReview(cfg, verificadorModelo, worktree, flags, fabrica, wiring)
	if err != nil {
		fmt.Printf("⚠️  Aviso: no se pudo resolver el git-common-dir; las revisiones no se reutilizaran por contenido tras un rebase (%v).\n", err)
	}
	// Standing human answers carry into the net audit (FU-6 unit A). A
	// corrupt log fails closed rather than auditing as if no human answered.
	var derr error
	opciones, derr = AplicarDisposicionesPrReview(opciones, worktree, wiring.LoadDispositions)
	if derr != nil {
		fmt.Printf("? %v\n", derr)
		os.Exit(1)
	}
	base := opciones.Base
	res, err := review.AnalizarRama(ledger, opciones)
	if err != nil {
		fmt.Printf("? %v\n", err)
		os.Exit(1)
	}

	detalle, err := DetalleEventoPrReview(base, res, git.DetectarCI(worktree))
	if err != nil {
		fmt.Printf("? Aviso: no se pudo construir el detalle del evento: %v\n", err)
	}
	if err := ops.RegistrarEvento(gitDir, "pr-review", 0, res.SHAs, detalle, worktree); err != nil {
		fmt.Printf("? Aviso: no se pudo registrar el evento: %v\n", err)
	}

	if flags.JsonOut {
		salida := SalidaJSONPrReview(base, res)
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
		fmt.Println(TextoDecision(res.Decision, res.Volumen))
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

// SalidaJSONPrReview builds the public --json result shape of pr review,
// including the ticket-07 classification of unavailable dimension records:
// review_admission_failures and review_infrastructure_failures are counted
// separately so consumers can distinguish rejected evidence from outages.
func SalidaJSONPrReview(base string, res *review.ResultadoRama) map[string]any {
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
