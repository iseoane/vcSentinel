// Facade equivalence harness and compatibility pins for the real durable
// gate orchestration (R9 slice 2). This file owns the byte-level facade
// contract: an equivalence harness runs BOTH orchestration paths on
// identical fixtures, the facade table pins Estado/Mensajes equality per
// terminal class (including the refuted-CRITICAL NEEDS_USER_REVIEW case),
// and the infrastructure text pins fix the exact strings that have no
// legacy equivalent to harness against. The CLI contract (stdout text,
// exit codes) is produced from Resultado.Estado/Mensajes by cmd/sentinel,
// so these tests pin the facade at its source.
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

// opcionesDurable clones a base Opciones set into a fully wired durable one:
// same fixtures, plus stage/candidate/store/transport-factory seams.
func opcionesDurable(t *testing.T, base Opciones, transportes *int) Opciones {
	t.Helper()
	durable := base
	durable.DurableRuns = true
	durable.Stage = "pre-push"
	durable.CandidateSHA = base.OpcionesRevision.SHA
	durable.DurableStore = store.NuevoStore(filepath.Join(t.TempDir(), "gate-common"))
	durable.DurableReviewTransportFactory = countedTransportFactory(t, base.OpcionesRevision.SHA, transportes)
	return durable
}

// ejecutarEquivalencia runs BOTH orchestration paths on identical fixtures
// and returns their results for byte-level facade comparison.
func ejecutarEquivalencia(t *testing.T, base Opciones) (legado, durable Resultado, transportes int) {
	t.Helper()
	durableOpts := opcionesDurable(t, base, &transportes)
	return EjecutarGate(base), EjecutarGate(durableOpts), transportes
}

func afirmarFachadaIgual(t *testing.T, legado, durable Resultado) {
	t.Helper()
	if legado.Estado != durable.Estado {
		t.Fatalf("estado divergió: legacy=%q durable=%q", legado.Estado, durable.Estado)
	}
	if strings.Join(legado.Mensajes, "\n") != strings.Join(durable.Mensajes, "\n") {
		t.Fatalf("mensajes divergieron:\nlegacy: %q\ndurable: %q", legado.Mensajes, durable.Mensajes)
	}
}

// TestGateDurableFacadeEquivalence pins the compatibility facade table:
// success, multi-command validation failure, review block, and infrastructure
// shapes. Estado must match what the LEGACY path produces for equivalent
// inputs, and Mensajes must match byte-for-byte through the harness.
func TestGateDurableFacadeEquivalence(t *testing.T) {
	bloqueoJSON := `{"dim":"logic","verdict":"block","findings":[{"dimension":"logic","file":"a.go","line":1,"severity":"CRITICAL","description":"riesgo confirmable"}]}`
	refutacionNegativa := `{"refuted":false,"reason":"the risk remains"}`

	casos := []struct {
		name string
		base func(t *testing.T) Opciones
	}{
		{
			name: "success renders the legacy PASS facade",
			base: func(t *testing.T) Opciones {
				llamadas := 0
				opts := opcionesBase(cfgConPerfil("lint", "echo ok"), ejecutorSeleccionado(nil), fabricaContadora(&llamadas, `{"dim":"logic","verdict":"ok","findings":[]}`, nil))
				opts.EjecutarValidacion = ejecutarPerfilSinCandidato
				return opts
			},
		},
		{
			name: "multi-command validation failure renders the legacy VALIDATION_FAILED facade",
			base: func(t *testing.T) Opciones {
				llamadas := 0
				opts := opcionesBase(cfgDosCapabilities(), ejecutorSeleccionado(map[string]validation.ValidationRun{
					"echo test": {Exit: 1, Salida: "salida real del comando fallido"},
				}), fabricaContadora(&llamadas, "", nil))
				opts.EjecutarValidacion = ejecutarPerfilSinCandidato
				return opts
			},
		},
		{
			name: "confirmed CRITICAL review renders the legacy CODE_REVIEW_FAILED facade",
			base: func(t *testing.T) Opciones {
				llamadas := 0
				opts := opcionesBase(cfgConPerfil("lint", "echo ok"), ejecutorSeleccionado(nil), fabricaContadora(&llamadas, bloqueoJSON, nil))
				opts.EjecutarValidacion = ejecutarPerfilSinCandidato
				opts.FabricaRefutador = func() (review.AuditorAgente, string, error) {
					return &auditorFalso{salida: refutacionNegativa}, "cheap", nil
				}
				return opts
			},
		},
		{
			name: "refuted CRITICAL renders the legacy NEEDS_USER_REVIEW facade",
			base: func(t *testing.T) Opciones {
				llamadas := 0
				// One block response per orchestration path: the equivalence
				// harness runs BOTH the legacy and the durable gate against
				// this shared sequential auditor.
				agente := &auditorSecuencial{respuestas: []string{bloqueoJSON, bloqueoJSON}, llamadas: &llamadas}
				fabrica := func(_ review.ReviewBundle, _ string) (review.AuditorAgente, string, error) {
					return agente, "perfil-test", nil
				}
				opts := opcionesBase(cfgConPerfil("lint", "echo ok"), ejecutorSeleccionado(nil), fabrica)
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
		},
		{
			name: "validation orchestration failure renders the legacy infrastructure facade",
			base: func(t *testing.T) Opciones {
				llamadas := 0
				opts := opcionesBase(cfgConPerfil("lint", "echo ok"), nil, fabricaContadora(&llamadas, "", nil))
				opts.EjecutarValidacion = func(perfil string, alcance []string, o validation.OpcionesEjecucion) ([]validation.ValidationRun, error) {
					return nil, errAgenteNoDisponibleTest
				}
				return opts
			},
		},
	}

	for _, caso := range casos {
		t.Run(caso.name, func(t *testing.T) {
			legado, durable, _ := ejecutarEquivalencia(t, caso.base(t))
			afirmarFachadaIgual(t, legado, durable)
		})
	}
}

// TestGateDurableInfrastructurePinsTexts fixes the exact infrastructure-only
// facade strings that have no legacy equivalent to harness against.
func TestGateDurableInfrastructurePinsTexts(t *testing.T) {
	t.Run("plan failure keeps the slice-1 prefix verbatim", func(t *testing.T) {
		opts := opcionesBase(cfgConPerfil("lint", "echo ok"), nil, nil)
		opts.DurableRuns = true
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
		opts := opcionesBase(cfgConPerfil("lint", "echo ok"), nil, nil)
		opts.DurableRuns = true
		opts.Stage = "pr"
		opts.CandidateSHA = "abc123def456"

		resultado := EjecutarGate(opts)

		want := "gate: durable runs require an injected store"
		if resultado.Estado != EstadoReviewInfrastructureError || resultado.Mensajes[0] != want {
			t.Fatalf("missing-store facade drifted:\n got  %q / %q\n want %q", resultado.Estado, resultado.Mensajes[0], want)
		}
	})
}
