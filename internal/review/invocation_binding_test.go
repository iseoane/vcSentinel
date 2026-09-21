package review

// Focused tests for ticket 07 slice 2b: the durable invocation identity
// reported by a ReviewTransport travels additively into dimension results and
// findings, while the content-stable fingerprint stays byte-identical for
// identical content regardless of which invocation produced it.

import (
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/reviewcontract"
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
	base := Finding{
		Dimension:   DimLogic,
		Title:       "nil dereference",
		Description: "unchecked nil dereference in handler",
		Evidence:    "handler dereferences cfg before the nil guard",
		Location:    Location{File: "a.go", LineStart: 3},
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

func (agenteTransporteSilencioso) RunPrompt(string) (string, error) { return "", nil }

func (agenteTransporteSilencioso) ReviewWithPolicy(string, string, []string, reviewcontract.ToolPolicy) (string, error) {
	return "", nil
}

type agenteRestringidoContado struct{ llamadas int }

func (a *agenteRestringidoContado) RunPrompt(string) (string, error) { return "", nil }

func (a *agenteRestringidoContado) RunReview(string, string, []string) (string, error) {
	a.llamadas++
	return respuestaV2Invocacion, nil
}

func (a *agenteRestringidoContado) ReviewWithPolicy(prompt, sha string, paths []string, _ reviewcontract.ToolPolicy) (string, error) {
	return a.RunReview(prompt, sha, paths)
}

func TestAuditCommitBindsTransportInvocationToFindings(t *testing.T) {
	resultado := AuditCommit(func(_ ReviewBundle, _ string) (AgentReviewer, string, error) {
		return agenteTransporteSilencioso{}, "stub", nil
	}, 1, AuditOptions{
		SHA:     "sha-inv-bind",
		Bundles: bundlesInvocacion(),
		ReviewTransport: func(_, _, _ string, _ AgentReviewer) (string, string, error) {
			return respuestaV2Invocacion, "inv-producer-42", nil
		},
	})

	if len(resultado.Dims) != 1 || resultado.Dims[0].Result == nil {
		t.Fatalf("dims = %+v, want one routed dimension result", resultado.Dims)
	}
	dim := resultado.Dims[0].Result
	if dim.Verdict != VerdictBlock {
		t.Fatalf("verdict = %q, want block from the transported finding", dim.Verdict)
	}
	if dim.InvocationID != "inv-producer-42" {
		t.Fatalf("dimension invocation id = %q, want the identity the transport reported", dim.InvocationID)
	}
	if len(resultado.Findings) != 1 {
		t.Fatalf("aggregate findings = %+v, want the single finding bound to the invocation", resultado.Findings)
	}
	if resultado.Findings[0].InvocationID != "inv-producer-42" {
		t.Fatalf("finding invocation id = %q, want \"inv-producer-42\" stamped from the transport identity", resultado.Findings[0].InvocationID)
	}
}

func TestLegacyAuditLeavesInvocationIdentityEmpty(t *testing.T) {
	agente := &agenteRestringidoContado{}
	resultado := AuditCommit(func(_ ReviewBundle, _ string) (AgentReviewer, string, error) {
		return agente, "stub", nil
	}, 1, AuditOptions{
		SHA:     "sha-inv-legacy",
		Bundles: bundlesInvocacion(),
	})

	if len(resultado.Dims) != 1 || resultado.Dims[0].Result == nil {
		t.Fatalf("dims = %+v, want one legacy dimension result", resultado.Dims)
	}
	dim := resultado.Dims[0].Result
	if dim.InvocationID != "" {
		t.Fatalf("legacy dimension invocation id = %q, want empty on the direct path", dim.InvocationID)
	}
	if len(resultado.Findings) != 1 || resultado.Findings[0].InvocationID != "" {
		t.Fatalf("legacy aggregate findings = %+v, want findings untouched by invocation binding", resultado.Findings)
	}
}
