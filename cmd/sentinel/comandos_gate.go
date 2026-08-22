package main

import (
	"fmt"
	"io"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/change"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/gate"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/graph"
	"github.com/ISeoane-Quental/vas.sentinel/internal/modelprobe"
	"github.com/ISeoane-Quental/vas.sentinel/internal/ops"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
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
	stage, perfil, err := parsearFlagsGate(args)
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

	verificador := nuevoVerificadorModelo(worktree)
	resultado := gate.EjecutarGate(gate.Opciones{
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
			SHA: sha, Mensaje: mensaje, Diff: diff, Bundles: review.PlanForProfile(profile, archivos).Bundles,
			ProveedorContexto: proveedorContextoReview(cfg, worktree), RutasContexto: archivos,
			ReviewTransport: durableReviewTransport(cfg, worktree, sha, archivos),
		},
	})

	fmt.Fprintf(w, "🚦 gate [%s] perfil=%s → %s\n", stage, perfil, resultado.Estado)
	return finalizarGate(w, worktree, stage, resultado.Estado, resultado.Mensajes)
}

// fabricaRefutadorGate resolves the explicit cheap profile separately from the
// per-dimension auditor so each semantic CRITICAL gets an independent refuter.
func fabricaRefutadorGate(cfg config.Config, verificador *modelprobe.Verificador) review.FabricaRefutador {
	return fabricaRefutador(cfg, verificador)
}

// fabricaAuditorGate construye el agente de cada dimensión de la revisión
// semántica con el perfil por dimensión de cfg.Review.Dims, sin override de
// --profile: --profile de gate elige el perfil de VALIDACIÓN
// (validation.profiles), no el perfil de revisión por dimensión, que sigue
// siendo el de siempre (mismo criterio que ejecutarReview sin --profile).
func fabricaAuditorGate(cfg config.Config, verificador *modelprobe.Verificador) review.FabricaAuditor {
	return func(_ review.ReviewBundle, dimension string) (review.AuditorAgente, string, error) {
		perfil := config.ResolverPerfil(cfg, dimension, "")
		adapter, err := agentadapter.NuevoAdaptadorConPerfil(cfg, perfil)
		if err != nil {
			return nil, perfil.Nombre, err
		}
		verificador.Verificar(perfil.Nombre, perfil.Modelo, adapter)
		return adapter, perfil.Nombre, nil
	}
}

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
	for _, m := range mensajes {
		fmt.Fprintln(w, m)
	}
	registrarEventoGate(worktree, stage, estado)
	return gate.CodigoSalida(estado)
}

// registrarEventoGate anexa el evento "gate" al log del repositorio. Un
// gitDir no resoluble (worktree fuera de un repo Git) no puede pasar de
// requireInicializado, pero por si acaso el registro es best-effort: no
// aborta gate por un fallo al registrar su propio evento.
func registrarEventoGate(worktree, stage, estado string) {
	gitDir, err := git.ObtenerGitDir()
	if err != nil {
		return
	}
	detalle := fmt.Sprintf("stage=%s estado=%s", stage, estado)
	_ = ops.RegistrarEvento(gitDir, "gate", gate.CodigoSalida(estado), nil, detalle, worktree)
}

// parsearFlagsGate extrae --stage (obligatorio, valores fijos) y --profile
// (opcional, perfilGatePorDefecto si se omite).
func parsearFlagsGate(args []string) (stage, perfil string, err error) {
	perfil = perfilGatePorDefecto
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--stage":
			i++
			if i >= len(args) {
				return "", "", fmt.Errorf("--stage requiere un valor (pre-commit, pre-push o pr)")
			}
			stage = args[i]
		case "--profile":
			i++
			if i >= len(args) {
				return "", "", fmt.Errorf("--profile requiere un valor")
			}
			perfil = args[i]
		default:
			return "", "", fmt.Errorf("flag no reconocido: %q (usa --stage y --profile)", args[i])
		}
	}
	if stage == "" {
		return "", "", fmt.Errorf("--stage es obligatorio (valores: pre-commit, pre-push, pr)")
	}
	if !etapasValidasGate[stage] {
		return "", "", fmt.Errorf("--stage %q no es válido (valores: pre-commit, pre-push, pr)", stage)
	}
	return stage, perfil, nil
}
