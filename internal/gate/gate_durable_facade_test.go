// Facade harness and compatibility pins for the real durable gate
// orchestration (R9 slice 2, sole execution path since ticket 13 R11). This
// file owns the byte-level facade contract: the facade table pins the exact
// Estado/Mensajes shapes per terminal class (including the refuted-CRITICAL
// NEEDS_USER_REVIEW case), and the infrastructure text pins fix the exact
// strings that have no pre-cutover equivalent to compare against. The CLI
// contract (stdout text, exit codes) is produced from Resultado
// Estado/Mensajes by cmd/sentinel, so these tests pin the facade at its
// source.
package gate

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewexec"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

// countedTransportFactory mirrors the production construction path of
// `sentinel review` (durableReviewTransport in cmd/sentinel): a REAL
// reviewexec.DurableTransport over a fresh store per audit, with the gate's
// root run ID threaded into its run policy as the persisted parent linkage.
// The counter seam records how often the factory itself was invoked — it must
// be zero whenever validation failed.
func countedTransportFactory(t *testing.T, sha string, invocaciones *int) func(agentrun.Identity) review.ReviewTransport {
	t.Helper()
	return func(rootRunID agentrun.Identity) review.ReviewTransport {
		*invocaciones++
		backing := store.NuevoStore(filepath.Join(t.TempDir(), "review-common"))
		transport := reviewexec.NewDurableTransport(backing, store.RunPolicy{ID: "policy:test-gate-review", ParentRunID: string(rootRunID)}, sha, nil)
		return func(bundleName, dimension, prompt string, agente review.AuditorAgente) (string, string, error) {
			restricted, ok := agente.(reviewexec.RestrictedReviewer)
			if !ok {
				return "", "", review.ErrRestrictedRequired
			}
			output, evidence, err := transport.Run(restricted, bundleName+"/"+dimension, prompt)
			if err != nil {
				return "", "", err
			}
			return output, evidence.InvocationID, nil
		}
	}
}

// opcionesDurable overrides an already-wired base Opciones set with a fresh
// temp-dir store and a counted transport factory, so tests can assert on
// factory invocations against fixtures built by opcionesBase.
func opcionesDurable(t *testing.T, base Opciones, transportes *int) Opciones {
	t.Helper()
	durable := base
	durable.Stage = "pre-push"
	durable.CandidateSHA = base.OpcionesRevision.SHA
	durable.DurableStore = store.NuevoStore(filepath.Join(t.TempDir(), "gate-common"))
	durable.DurableReviewTransportFactory = countedTransportFactory(t, base.OpcionesRevision.SHA, transportes)
	return durable
}

// TestGateFacadePinsTerminalClasses pins the compatibility facade table:
// success, multi-command validation failure, review block (confirmed and
// refuted CRITICAL), and infrastructure shapes. These literals are what the
// removed legacy orchestration produced for equivalent inputs; the durable
// orchestration must keep rendering them byte-for-byte through the shared
// helpers (mensajeValidacionNoEjecutada, mensajesValidacionFallida,
// traducirVeredicto).
func TestGateFacadePinsTerminalClasses(t *testing.T) {
	bloqueoJSON := `{"dim":"logic","verdict":"block","findings":[{"dimension":"logic","file":"a.go","line":1,"severity":"CRITICAL","description":"riesgo confirmable"}]}`
	refutacionNegativa := `{"refuted":false,"reason":"the risk remains"}`

	casos := []struct {
		name string
		base func(t *testing.T) Opciones
		// estado is the expected terminal state. When exacto is true the
		// joined mensajes must equal the pin byte-for-byte; otherwise each
		// entry must be contained (used when the facade embeds generated
		// finding identities). Every fixture audits exactly one dimension,
		// so the summary line is deterministic.
		estado   string
		exacto   bool
		mensajes []string
	}{
		{
			name: "success renders the PASS facade",
			base: func(t *testing.T) Opciones {
				llamadas := 0
				opts := opcionesBase(t, cfgConPerfil("lint", "echo ok"), ejecutorSeleccionado(nil), fabricaContadora(&llamadas, `{"dim":"logic","verdict":"ok","findings":[]}`, nil))
				opts.EjecutarValidacion = ejecutarPerfilSinCandidato
				return opts
			},
			estado: EstadoPass,
			exacto: true,
			mensajes: []string{
				"✅ Validación y revisión semántica en verde.",
				"\n🔎 Revisión de 01234567: ok\n  logic     ok          perfil-test",
			},
		},
		{
			name: "multi-command validation failure renders the VALIDATION_FAILED facade",
			base: func(t *testing.T) Opciones {
				llamadas := 0
				opts := opcionesBase(t, cfgDosCapabilities(), ejecutorSeleccionado(map[string]validation.ValidationRun{
					"echo test": {Exit: 1, Salida: "salida real del comando fallido"},
				}), fabricaContadora(&llamadas, "", nil))
				opts.EjecutarValidacion = ejecutarPerfilSinCandidato
				return opts
			},
			estado: EstadoValidationFailed,
			exacto: true,
			mensajes: []string{
				"❌ Validación FALLIDA: no se ejecuta la revisión semántica.",
				"  ✖ test (echo test):\nsalida real del comando fallido",
			},
		},
		{
			name: "confirmed CRITICAL review renders the CODE_REVIEW_FAILED facade",
			base: func(t *testing.T) Opciones {
				llamadas := 0
				opts := opcionesBase(t, cfgConPerfil("lint", "echo ok"), ejecutorSeleccionado(nil), fabricaContadora(&llamadas, bloqueoJSON, nil))
				opts.EjecutarValidacion = ejecutarPerfilSinCandidato
				opts.FabricaRefutador = func() (review.AuditorAgente, string, error) {
					return &auditorFalso{salida: refutacionNegativa}, "cheap", nil
				}
				return opts
			},
			estado: EstadoCodeReviewFailed,
			mensajes: []string{
				"❌ La revisión semántica confirmó hallazgos CRITICAL.",
				"🔎 Revisión de 01234567: block",
				"Confirmed CRITICAL finding evidence:",
				`    description: "riesgo confirmable"`,
			},
		},
		{
			name: "refuted CRITICAL renders the NEEDS_USER_REVIEW facade",
			base: func(t *testing.T) Opciones {
				llamadas := 0
				agente := &auditorSecuencial{respuestas: []string{bloqueoJSON}, llamadas: &llamadas}
				fabrica := func(_ review.ReviewBundle, _ string) (review.AuditorAgente, string, error) {
					return agente, "perfil-test", nil
				}
				opts := opcionesBase(t, cfgConPerfil("lint", "echo ok"), ejecutorSeleccionado(nil), fabrica)
				opts.EjecutarValidacion = ejecutarPerfilSinCandidato
				opts.FabricaRefutador = func() (review.AuditorAgente, string, error) {
					return &auditorFalso{salida: `{"refuted":true,"reason":"the final code already handles this case","sha":"0123456789abcdef","file":"a.go","line_start":1,"line_end":1,"evidence":"final code handles this case"}`}, "cheap", nil
				}
				opts.OpcionesRevision.LeerContenidoSnapshot = func(sha, file string) (string, error) {
					if sha != "0123456789abcdef" || file != "a.go" {
						t.Fatalf("snapshot read sha=%q file=%q", sha, file)
					}
					return "final code handles this case", nil
				}
				return opts
			},
			estado: EstadoNeedsUserReview,
			exacto: true,
			mensajes: []string{
				"❓ La revisión semántica refutó un hallazgo CRITICAL y requiere atención humana.",
				"\n🔎 Revisión de 01234567: warn\n  logic     warn        perfil-test",
			},
		},
		{
			name: "validation orchestration failure renders the infrastructure facade",
			base: func(t *testing.T) Opciones {
				llamadas := 0
				opts := opcionesBase(t, cfgConPerfil("lint", "echo ok"), nil, fabricaContadora(&llamadas, "", nil))
				opts.EjecutarValidacion = func(perfil string, alcance []string, o validation.OpcionesEjecucion) ([]validation.ValidationRun, error) {
					return nil, errAgenteNoDisponibleTest
				}
				return opts
			},
			estado: EstadoReviewInfrastructureError,
			exacto: true,
			mensajes: []string{
				"No se pudo ejecutar la validación: agente no disponible",
			},
		},
	}

	for _, caso := range casos {
		t.Run(caso.name, func(t *testing.T) {
			resultado := EjecutarGate(caso.base(t))
			if resultado.Estado != caso.estado {
				t.Fatalf("estado = %q, expected %q (%v)", resultado.Estado, caso.estado, resultado.Mensajes)
			}
			unido := strings.Join(resultado.Mensajes, "\n")
			if caso.exacto {
				if want := strings.Join(caso.mensajes, "\n"); unido != want {
					t.Fatalf("facade drifted:\n got  %q\n want %q", unido, want)
				}
				return
			}
			for _, fragmento := range caso.mensajes {
				if !strings.Contains(unido, fragmento) {
					t.Fatalf("facade lost %q:\n got %q", fragmento, unido)
				}
			}
		})
	}
}

// TestGateDurableInfrastructurePinsTexts fixes the exact infrastructure-only
// facade strings that have no green-path equivalent to pin elsewhere.
func TestGateDurableInfrastructurePinsTexts(t *testing.T) {
	t.Run("plan failure keeps the slice-1 prefix verbatim", func(t *testing.T) {
		opts := opcionesBase(t, cfgConPerfil("lint", "echo ok"), nil, nil)
		opts.Stage = ""
		opts.CandidateSHA = "abc123def456"
		opts.DurableStore = store.NuevoStore(filepath.Join(t.TempDir(), "gate-common"))

		resultado := EjecutarGate(opts)

		want := "gate durable run plan failed before wiring: gate: invalid durable run plan (stage): must be a non-empty lifecycle stage"
		if resultado.Estado != EstadoReviewInfrastructureError || resultado.Mensajes[0] != want {
			t.Fatalf("plan-failure facade drifted:\n got  %q / %q\n want %q", resultado.Estado, resultado.Mensajes[0], want)
		}
	})

	t.Run("missing store names the seam explicitly", func(t *testing.T) {
		opts := opcionesBase(t, cfgConPerfil("lint", "echo ok"), nil, nil)
		opts.Stage = "pr"
		opts.CandidateSHA = "abc123def456"
		opts.DurableStore = nil

		resultado := EjecutarGate(opts)

		want := "gate: durable runs require an injected store"
		if resultado.Estado != EstadoReviewInfrastructureError || resultado.Mensajes[0] != want {
			t.Fatalf("missing-store facade drifted:\n got  %q / %q\n want %q", resultado.Estado, resultado.Mensajes[0], want)
		}
	})
}
