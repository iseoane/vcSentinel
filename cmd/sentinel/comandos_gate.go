package main

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/change"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/gate"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/graph"
	"github.com/ISeoane-Quental/vas.sentinel/internal/modelprobe"
	"github.com/ISeoane-Quental/vas.sentinel/internal/ops"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

// etapasValidasGate son los únicos valores aceptados por --stage. --stage
// identifica el punto del ciclo de vida que invoca el gate (para
// mensajes/registro del evento), no qué perfil de validación se ejecuta:
// --stage y --profile son ejes independientes (decisión de diseño de T1.7).
var etapasValidasGate = map[string]bool{
	"pre-commit": true,
	"pre-push":   true,
	"pr":         true,
}

// perfilGatePorDefecto es el perfil de validation.profiles que --profile usa
// cuando no se indica explícitamente. Decisión de diseño de T1.7 (la ficha no
// lo especifica): "standard" por convención; si no existe en la
// configuración, ejecutarGate corta con un error explícito en vez de asumir
// cualquier otro perfil arbitrario.
const perfilGatePorDefecto = "standard"

// ejecutarGate parsea --stage/--profile, carga la config ESTRICTA (punto de
// entrada donde una clave desconocida en el yml deja de descartarse en
// silencio, requisito añadido por el orquestador para el criterio de salida
// #4 de F1) y delega en internal/gate el orden fijo validación → revisión
// semántica sobre HEAD. Devuelve el exit code sin llamar a os.Exit (mismo
// patrón que ejecutarSlicePlan/ejecutarSliceApply) para poder testear sin
// terminar el proceso.
func ejecutarGate(w io.Writer, worktree string, args []string) int {
	stage, perfil, timeout, err := parsearFlagsGate(args)
	if err != nil {
		fmt.Fprintf(w, "❌ %v\n", err)
		return 1
	}

	cfg, err := config.CargarConfiguracionLocalEstricta(worktree)
	if err != nil {
		// Un yml roto es infraestructura de configuración, no un hallazgo de
		// validación ni de revisión semántica (requisito añadido por el
		// orquestador, fuera del texto original de T1.7).
		fmt.Fprintf(w, "❌ Error de configuración: %v\n", err)
		return finalizarGate(w, worktree, stage, gate.EstadoReviewInfrastructureError, nil)
	}

	// El override llega DESPUÉS de la carga estricta: --timeout no puede
	// rescatar un yml inválido, solo ampliar el presupuesto de esta revisión.
	cfg = aplicarTimeoutSegundos(cfg, timeout)

	if _, ok := cfg.Validation.Profiles[perfil]; !ok {
		fmt.Fprintf(w, "❌ El perfil de validación %q no está configurado. Define validation.profiles.%s en vassentinel.yml o indica --profile con un perfil existente.\n", perfil, perfil)
		return finalizarGate(w, worktree, stage, gate.EstadoReviewInfrastructureError, nil)
	}

	sha, mensaje, diff, archivos, err := datosCommitHEAD()
	if err != nil {
		fmt.Fprintf(w, "❌ No se pudo leer HEAD: %v\n", err)
		return finalizarGate(w, worktree, stage, gate.EstadoReviewInfrastructureError, nil)
	}
	profile, err := change.PerfilDeCommit(sha)
	if err != nil {
		fmt.Fprintf(w, "❌ No se pudo derivar el perfil de cambio de HEAD: %v\n", err)
		return finalizarGate(w, worktree, stage, gate.EstadoReviewInfrastructureError, nil)
	}

	// Fail closed. git.Attributes already returns "" with no error when the
	// tree has no .gitattributes, so an error here is a real read failure, and
	// the attributes now decide route classification and therefore whether the
	// security and concurrency bundles are scheduled at all. Continuing with
	// empty evidence would let the gate succeed on an under-classified plan.
	atributos, err := leerAtributosGate(sha)
	if err != nil {
		fmt.Fprintf(w, "❌ No se pudieron leer los atributos de %s: %v\n", sha, err)
		return finalizarGate(w, worktree, stage, gate.EstadoReviewInfrastructureError, nil)
	}

	verificador := nuevoVerificadorModelo(worktree)
	opciones := buildGateOptions(cfg, verificador, worktree, perfil, EvidenciaGate{
		SHA: sha, Mensaje: mensaje, Diff: diff, Gitattributes: atributos,
		Perfil: profile, Archivos: archivos,
	})
	applyDurableCutover(&opciones, cfg, worktree, stage, sha, archivos)

	resultado := gate.EjecutarGate(opciones)

	fmt.Fprintf(w, "🚦 gate [%s] perfil=%s → %s\n", stage, perfil, resultado.Estado)
	return finalizeGateWithDetails(w, worktree, stage, resultado.Estado, resultado.Mensajes, resultado.ContextSkipReason, resultado.ReviewerFailures)
}

// buildGateOptions assembles the gate.Opciones the command hands to
// internal/gate: validation profile, HEAD-derived revision inputs, the real
// reviewer seams, and the per-commit review transport (nil unless
// review.durable_routes is on). Shared by ejecutarGate and the cutover tests,
// so tests exercise the exact production construction.
// EvidenciaGate agrupa lo que el gate deriva de HEAD. Es una struct y no una
// lista de parámetros porque eran seis cadenas adyacentes: un argumento
// olvidado, desplazado o intercambiado compilaba igual, y una de ellas decide
// si se programa la revisión de seguridad. El compilador ya no lo permite.
type EvidenciaGate struct {
	SHA           string
	Mensaje       string
	Diff          string
	Gitattributes string
	Perfil        change.ChangeProfile
	Archivos      []string
}

func buildGateOptions(cfg config.Config, verificador *modelprobe.Verificador, worktree, perfil string, evidencia EvidenciaGate) gate.Opciones {
	sha, mensaje, diff, gitattributes := evidencia.SHA, evidencia.Mensaje, evidencia.Diff, evidencia.Gitattributes
	profile, archivos := evidencia.Perfil, evidencia.Archivos
	reviewTransport, metricsFinalizer := durableReviewTransportWithMetrics(cfg, worktree, sha, archivos)
	return gate.Opciones{
		Perfil:         perfil,
		RutasCambiadas: archivos,
		OpcionesValidacion: validation.OpcionesEjecucion{
			Worktree: worktree,
			Cfg:      cfg,
			ProveedorGraph: func(snapshot, treeOID string) graph.GraphProvider {
				return graph.NuevoProveedorNativo(snapshot, treeOID)
			},
		},
		FabricaAuditor:   fabricaAuditorGate(cfg, verificador),
		FabricaRefutador: fabricaRefutadorGate(cfg, verificador),
		Parallel:         cfg.Review.Parallel,
		OpcionesRevision: review.OpcionesAuditoria{
			SHA: sha, Mensaje: mensaje, Diff: diff, Bundles: review.PlanForProfile(profile, archivos, diff, gitattributes).Bundles,
			ProveedorContexto: proveedorContextoReview(cfg, worktree), RutasContexto: archivos,
			ReviewTransportWithEvidence: reviewTransport,
			FinalizeMetrics:             metricsFinalizer,
		},
	}
}

// fabricaRefutadorGate resolves the explicit cheap profile separately from the
// per-dimension auditor so each semantic CRITICAL gets an independent refuter.
func fabricaRefutadorGate(cfg config.Config, verificador *modelprobe.Verificador) review.FabricaRefutador {
	return fabricaRefutador(cfg, verificador)
}

// fabricaAuditorGate construye el agente de cada dimensión de la revisión
// semantic review with the contract-selected provider profile, without a
// review override: gate --profile selects a validation profile only.
func fabricaAuditorGate(cfg config.Config, verificador *modelprobe.Verificador) review.FabricaAuditor {
	return func(_ review.ReviewBundle, dimension string) (review.AuditorAgente, string, error) {
		profile := config.ResolverPerfil(cfg, reviewcontract.DefaultProfile(dimension), "")
		adapter, err := agentadapter.NuevoAdaptadorConPerfil(cfg, profile)
		if err != nil {
			return nil, profile.Nombre, err
		}
		verificador.Verificar(profile.Nombre, profile.Modelo, adapter)
		return adapter, profile.Nombre, nil
	}
}

// leerAtributosGate es la costura de lectura de .gitattributes del gate: una
// variable para que el test pueda ejercitar la rama de fallo cerrado, que es la
// que decide si el gate sigue con un plan infravalorado.
var leerAtributosGate = git.Attributes

// datosCommitHEAD resuelve el SHA de HEAD y lee su mensaje/diff/archivos:
// gate audita siempre HEAD (a diferencia de 'review', que admite un target
// explícito), porque es el punto que pre-commit/pre-push/pr ya congelaron.
func datosCommitHEAD() (sha, mensaje, diff string, archivos []string, err error) {
	sha, err = git.ResolverSHA("HEAD")
	if err != nil {
		return "", "", "", nil, err
	}
	mensaje, err = git.MensajeCommit(sha)
	if err != nil {
		return "", "", "", nil, err
	}
	diff, err = git.DiffCommit(sha)
	if err != nil {
		return "", "", "", nil, err
	}
	archivos, err = git.ArchivosDeCommit(sha)
	if err != nil {
		return "", "", "", nil, err
	}
	return sha, mensaje, diff, archivos, nil
}

// finalizarGate imprime los mensajes del resultado, registra el evento con el
// estado final (mecanismo ya existente en internal/ops, mismo patrón que
// ejecutarReview) y devuelve el exit code exacto de la ficha.
func finalizarGate(w io.Writer, worktree, stage, estado string, mensajes []string) int {
	return finalizeGateWithDetails(w, worktree, stage, estado, mensajes, "", nil)
}

func finalizeGateWithDetails(w io.Writer, worktree, stage, state string, messages []string, contextSkipReason string, reviewerFailures []gate.ReviewerFailure) int {
	for _, message := range messages {
		fmt.Fprintln(w, message)
	}
	recordGateEventWithDetails(worktree, stage, state, contextSkipReason, reviewerFailures)
	return gate.CodigoSalida(state)
}

// recordGateEventWithDetails appends operational metadata to the repository
// that owns worktree. An unresolved Git directory is intentionally ignored:
// writing relative to the process directory could contaminate another
// repository's event log.
func recordGateEventWithDetails(worktree, stage, state, contextSkipReason string, reviewerFailures []gate.ReviewerFailure) {
	gitDir, err := git.ObtenerGitDirDe(worktree)
	if err != nil {
		return
	}
	detail := ops.EventDetail{"stage": stage, "state": state}
	if contextSkipReason != "" {
		detail["context_skip_reason"] = contextSkipReason
	}
	if len(reviewerFailures) > 0 {
		failures := make([]ops.EventDetail, 0, len(reviewerFailures))
		for _, failure := range reviewerFailures {
			failures = append(failures, ops.EventDetail{
				"bundle":    failure.Bundle,
				"dimension": failure.Dimension,
				"reason":    failure.Reason,
			})
		}
		detail["reviewer_failures"] = failures
	}
	_ = ops.RegistrarEvento(gitDir, "gate", gate.CodigoSalida(state), nil, detail, worktree)
}

// maxSegundosTimeout es el mayor valor de --timeout que time.Duration puede
// representar. Sin este techo un valor positivo y perfectamente parseable
// desborda al multiplicarlo por time.Second y se convierte en una duración
// negativa, así que el override se aceptaría sin representar lo pedido.
//
// Es int64 y no int a propósito: el valor no cabe en un int de 32 bits, y
// declararlo así impedía COMPILAR el paquete entero en esos objetivos aunque
// time.Duration siguiera siendo int64 y pudiera representarlo. En 32 bits la
// comprobación resulta inalcanzable —el máximo de un int es menor— y eso es
// correcto: allí ningún valor parseable puede desbordar.
const maxSegundosTimeout int64 = math.MaxInt64 / int64(time.Second)

// parsearFlagsGate extrae --stage (obligatorio, valores fijos), --profile
// (opcional, perfilGatePorDefecto si se omite) y --timeout (opcional).
//
// --timeout tiene exactamente la misma semántica que en review: sustituye
// review.timeout SOLO en esta invocación. El gate ejecuta la misma revisión
// semántica, así que negarle la palanca obligaba a editar el yml —un cambio
// global y persistente— para un candidato grande puntual. Ampliar el
// presupuesto no debilita ninguna puerta: una revisión que iba a bloquear
// sigue bloqueando, solo llega a terminar.
func parsearFlagsGate(args []string) (stage, perfil string, timeout int, err error) {
	perfil = perfilGatePorDefecto
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--stage":
			i++
			if i >= len(args) {
				return "", "", 0, fmt.Errorf("--stage requiere un valor (pre-commit, pre-push o pr)")
			}
			stage = args[i]
		case "--profile":
			i++
			if i >= len(args) {
				return "", "", 0, fmt.Errorf("--profile requiere un valor")
			}
			perfil = args[i]
		case "--timeout":
			i++
			if i >= len(args) {
				return "", "", 0, fmt.Errorf("--timeout requiere un valor en segundos")
			}
			segundos, convErr := strconv.Atoi(args[i])
			if convErr != nil || segundos <= 0 {
				return "", "", 0, fmt.Errorf("--timeout %q no es un número de segundos positivo", args[i])
			}
			if int64(segundos) > maxSegundosTimeout {
				return "", "", 0, fmt.Errorf("--timeout %q excede el máximo representable (%d segundos)", args[i], maxSegundosTimeout)
			}
			timeout = segundos
		default:
			return "", "", 0, fmt.Errorf("flag no reconocido: %q (usa --stage, --profile y --timeout)", args[i])
		}
	}
	if stage == "" {
		return "", "", 0, fmt.Errorf("--stage es obligatorio (valores: pre-commit, pre-push, pr)")
	}
	if !etapasValidasGate[stage] {
		return "", "", 0, fmt.Errorf("--stage %q no es válido (valores: pre-commit, pre-push, pr)", stage)
	}
	return stage, perfil, timeout, nil
}
