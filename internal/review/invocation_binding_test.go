package review

// Focused tests for ticket 07 slice 2b: the durable invocation identity
// reported by a ReviewTransport travels additively into dimension results and
// findings, while the content-stable fingerprint stays byte-identical for
// identical content regardless of which invocation produced it.

import (
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
)

// respuestaV2Invocacion carries one v2 finding (the "id" marker makes esV2
// true) so the engine-level tests can observe per-finding binding, not just
// the dimension result.
const respuestaV2Invocacion = "BEGIN_REVIEW\n" +
	`{"dim":"logic","verdict":"block","findings":[{"dimension":"logic","severity":"CRITICAL","description":"unchecked nil dereference in handler","id":"inv-bind-1","title":"nil dereference","evidence":"handler dereferences cfg before the nil guard","confidence":"high","location":{"file":"a.go","line_start":3,"line_end":3},"status":"open"}]}` +
	"\nEND_REVIEW"

func bundlesInvocacion() []ReviewBundle {
	return []ReviewBundle{{Name: BundleQuality, Dimensions: []string{DimLogic}, Priority: PriorityRequired}}
}

func TestFingerprintIgnoresInvocationID(t *testing.T) {
	base := Hallazgo{
		Dimension:   DimLogic,
		Title:       "nil dereference",
		Description: "unchecked nil dereference in handler",
		Evidence:    "handler dereferences cfg before the nil guard",
		Location:    Ubicacion{Archivo: "a.go", LineaInicio: 3},
	}
	con := base
	sin := base
	con.InvocationID = "inv-producer-42"

	if Fingerprint(con) != Fingerprint(sin) {
		t.Fatalf("fingerprints differ for findings identical except InvocationID: %q vs %q",
			Fingerprint(con), Fingerprint(sin))
	}
	if Fingerprint(con) == "" {
		t.Fatalf("fingerprint = %q, want a non-empty stable hash", Fingerprint(con))
	}
}

type agenteTransporteSilencioso struct{}

func (agenteTransporteSilencioso) EjecutarPrompt(string) (string, error) { return "", nil }

func (agenteTransporteSilencioso) ReviewWithPolicy(string, string, []string, reviewcontract.ToolPolicy) (string, error) {
	return "", nil
}

type agenteRestringidoContado struct{ llamadas int }

func (a *agenteRestringidoContado) EjecutarPrompt(string) (string, error) { return "", nil }

func (a *agenteRestringidoContado) EjecutarRevision(string, string, []string) (string, error) {
	a.llamadas++
	return respuestaV2Invocacion, nil
}

func (a *agenteRestringidoContado) ReviewWithPolicy(prompt, sha string, paths []string, _ reviewcontract.ToolPolicy) (string, error) {
	return a.EjecutarRevision(prompt, sha, paths)
}

func TestAuditarCommitBindsTransportInvocationToFindings(t *testing.T) {
	resultado := AuditarCommit(func(_ ReviewBundle, _ string) (AuditorAgente, string, error) {
		return agenteTransporteSilencioso{}, "stub", nil
	}, 1, OpcionesAuditoria{
		SHA:     "sha-inv-bind",
		Bundles: bundlesInvocacion(),
		ReviewTransport: func(_, _, _ string, _ AuditorAgente) (string, string, error) {
			return respuestaV2Invocacion, "inv-producer-42", nil
		},
	})

	if len(resultado.Dims) != 1 || resultado.Dims[0].Resultado == nil {
		t.Fatalf("dims = %+v, want one routed dimension result", resultado.Dims)
	}
	dim := resultado.Dims[0].Resultado
	if dim.Verdict != VerdictBlock {
		t.Fatalf("verdict = %q, want block from the transported finding", dim.Verdict)
	}
	if dim.InvocationID != "inv-producer-42" {
		t.Fatalf("dimension invocation id = %q, want the identity the transport reported", dim.InvocationID)
	}
	if len(dim.Hallazgos) != 1 {
		t.Fatalf("hallazgos = %+v, want the single v2 finding bound to the invocation", dim.Hallazgos)
	}
	if dim.Hallazgos[0].InvocationID != "inv-producer-42" {
		t.Fatalf("finding invocation id = %q, want \"inv-producer-42\" stamped at finalization", dim.Hallazgos[0].InvocationID)
	}
}

func TestLegacyAuditLeavesInvocationIdentityEmpty(t *testing.T) {
	agente := &agenteRestringidoContado{}
	resultado := AuditarCommit(func(_ ReviewBundle, _ string) (AuditorAgente, string, error) {
		return agente, "stub", nil
	}, 1, OpcionesAuditoria{
		SHA:     "sha-inv-legacy",
		Bundles: bundlesInvocacion(),
	})

	if len(resultado.Dims) != 1 || resultado.Dims[0].Resultado == nil {
		t.Fatalf("dims = %+v, want one legacy dimension result", resultado.Dims)
	}
	dim := resultado.Dims[0].Resultado
	if dim.InvocationID != "" {
		t.Fatalf("legacy dimension invocation id = %q, want empty on the direct path", dim.InvocationID)
	}
	if len(dim.Hallazgos) != 1 || dim.Hallazgos[0].InvocationID != "" {
		t.Fatalf("legacy hallazgos = %+v, want findings untouched by invocation binding", dim.Hallazgos)
	}
}
