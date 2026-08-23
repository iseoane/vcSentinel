package review

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const (
	refutationSHA        = "abc12345"
	hallazgoCriticoJSON  = `{"dim":"logic","verdict":"block","findings":[{"dimension":"logic","file":"a.go","line":1,"severity":"CRITICAL","description":"bug"}]}`
	refutacionValidaJSON = `{"refuted":true,"reason":"the committed implementation is safe","sha":"abc12345","file":"a.go","evidence":"criticalCall()","line_start":1,"line_end":1}`
)

// llamadaTransporte records one ReviewTransport invocation for assertions.
type llamadaTransporte struct {
	bundleName string
	dimension  string
	prompt     string
}

// snapshotReaderRefutacion serves the immutable snapshot content the valid
// refutation evidence must match byte for byte.
func snapshotReaderRefutacion(t *testing.T) SnapshotReader {
	t.Helper()
	return func(sha, file string) (string, error) {
		if sha != refutationSHA || file != "a.go" {
			t.Fatalf("snapshot read sha=%q file=%q", sha, file)
		}
		return "criticalCall()\n", nil
	}
}

// ejecutarEscenarioConTransporte audits one logic CRITICAL blocker through a
// recording fake transport. The transport answers the dimension review with
// the blocking finding and answers the refutation envelope (bundleName
// "refutation") with responderRefutacion, so tests control exactly what the
// admitted refuter call produces. It returns the audit result, every transport
// invocation in order, and the refuter double so tests can prove no direct
// legacy call bypassed the transport.
func ejecutarEscenarioConTransporte(t *testing.T, responderRefutacion func() (string, string, error)) (ResultadoAuditoria, []llamadaTransporte, *agenteFake) {
	t.Helper()
	fabrica, _ := fabricaFija([]string{hallazgoCriticoJSON})
	fabricaRefutador, refutador := fabricaRefutadorFija([]string{refutacionValidaJSON})
	var llamadas []llamadaTransporte
	resultado := AuditarCommit(fabrica, 1, OpcionesAuditoria{
		SHA:                   refutationSHA,
		Bundles:               bundlesPrueba(DimLogic),
		FabricaRefutador:      fabricaRefutador,
		LeerContenidoSnapshot: snapshotReaderRefutacion(t),
		ReviewTransport: func(bundleName, dimension, prompt string, _ AuditorAgente) (string, string, error) {
			llamadas = append(llamadas, llamadaTransporte{bundleName: bundleName, dimension: dimension, prompt: prompt})
			if bundleName == "refutation" {
				return responderRefutacion()
			}
			return hallazgoCriticoJSON, "", nil
		},
	})
	return resultado, llamadas, refutador
}

// TestRefutationRoutesThroughReviewTransport proves the CRITICAL refuter call
// is admitted through the configured transport on the durable path: the
// transport sees the "refutation" envelope exactly once, the refuter double
// never answers directly, and an accepted refutation downgrades the blocker.
func TestRefutationRoutesThroughReviewTransport(t *testing.T) {
	resultado, llamadas, refutador := ejecutarEscenarioConTransporte(t, func() (string, string, error) {
		return refutacionValidaJSON, "inv-refutation-1", nil
	})

	var refutaciones []llamadaTransporte
	for _, llamada := range llamadas {
		if llamada.bundleName == "refutation" {
			refutaciones = append(refutaciones, llamada)
		}
	}
	if len(refutaciones) != 1 {
		t.Fatalf("refutation transport invocations = %d (%+v), want exactly one", len(refutaciones), llamadas)
	}
	if refutaciones[0].dimension != DimLogic {
		t.Fatalf("refutation dimension = %q, want %q", refutaciones[0].dimension, DimLogic)
	}
	if !strings.Contains(refutaciones[0].prompt, "Audited commit SHA (trusted): "+refutationSHA) {
		t.Fatalf("refutation prompt missing trusted SHA: %q", refutaciones[0].prompt)
	}
	refutador.mu.Lock()
	llamadasDirectas := refutador.llamadas
	refutador.mu.Unlock()
	if llamadasDirectas != 0 {
		t.Fatalf("direct refuter calls = %d, want zero when a transport is present", llamadasDirectas)
	}
	if len(resultado.Dims) != 1 || resultado.Dims[0].Resultado == nil {
		t.Fatalf("resultado = %+v, want one dimension result", resultado)
	}
	dim := resultado.Dims[0].Resultado
	finding := dim.Findings[0]
	if dim.Verdict != VerdictWarn || finding.Status != StatusRefuted || finding.RefutationReason == "" {
		t.Fatalf("result=%+v, expected accepted refutation through the transport to downgrade the blocker", resultado)
	}
}

// TestRefutationTransportRejectionPreservesBlocker proves a transport rejection
// (admission refused or provider unavailable) behaves exactly like any other
// unavailable refuter answer: the CRITICAL finding keeps its confirmed
// blocking status and the dimension verdict stays block.
func TestRefutationTransportRejectionPreservesBlocker(t *testing.T) {
	resultado, llamadas, refutador := ejecutarEscenarioConTransporte(t, func() (string, string, error) {
		return "", "", errors.New("durable: admission rejected the refutation envelope")
	})

	refutaciones := 0
	for _, llamada := range llamadas {
		if llamada.bundleName == "refutation" {
			refutaciones++
		}
	}
	if refutaciones != 1 {
		t.Fatalf("refutation transport attempts = %d (%+v), want exactly one", refutaciones, llamadas)
	}
	refutador.mu.Lock()
	llamadasDirectas := refutador.llamadas
	refutador.mu.Unlock()
	if llamadasDirectas != 0 {
		t.Fatalf("direct refuter calls = %d, want zero when a transport is present", llamadasDirectas)
	}
	if len(resultado.Dims) != 1 || resultado.Dims[0].Resultado == nil {
		t.Fatalf("resultado = %+v, want one dimension result", resultado)
	}
	dim := resultado.Dims[0].Resultado
	finding := dim.Findings[0]
	if dim.Verdict != VerdictBlock || finding.Status != StatusConfirmed {
		t.Fatalf("result=%+v, expected transport rejection to preserve the confirmed blocker", resultado)
	}
}

// TestRefutationLegacyNilTransportParity guards the rollback branch: with a
// nil transport the refuter answers through its restricted EjecutarRevision
// capability and the outcome must be identical to the admitted-transport run.
func TestRefutationLegacyNilTransportParity(t *testing.T) {
	porTransporte, _, _ := ejecutarEscenarioConTransporte(t, func() (string, string, error) {
		return refutacionValidaJSON, "inv-refutation-1", nil
	})

	fabrica, _ := fabricaFija([]string{hallazgoCriticoJSON})
	fabricaRefutador, refutador := fabricaRefutadorFija([]string{refutacionValidaJSON})
	legado := AuditarCommit(fabrica, 1, OpcionesAuditoria{
		SHA:                   refutationSHA,
		Bundles:               bundlesPrueba(DimLogic),
		FabricaRefutador:      fabricaRefutador,
		LeerContenidoSnapshot: snapshotReaderRefutacion(t),
	})

	refutador.mu.Lock()
	llamadasDirectas := refutador.llamadas
	refutador.mu.Unlock()
	if llamadasDirectas != 1 {
		t.Fatalf("direct refuter calls = %d, want the single legacy EjecutarRevision call", llamadasDirectas)
	}
	if legado.Veredicto != porTransporte.Veredicto {
		t.Fatalf("verdict legacy=%q transported=%q, want parity", legado.Veredicto, porTransporte.Veredicto)
	}
	if len(legado.Dims) != 1 || len(porTransporte.Dims) != 1 ||
		legado.Dims[0].Resultado == nil || porTransporte.Dims[0].Resultado == nil {
		t.Fatalf("dims legacy=%d transported=%d, want one each", len(legado.Dims), len(porTransporte.Dims))
	}
	if !reflect.DeepEqual(legado.Dims[0].Resultado.Findings[0], porTransporte.Dims[0].Resultado.Findings[0]) {
		t.Fatalf("finding legacy=%+v transported=%+v, want identical refuted findings",
			legado.Dims[0].Resultado.Findings[0], porTransporte.Dims[0].Resultado.Findings[0])
	}
}
