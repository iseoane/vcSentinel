package reviewexec

// Engine-surfacing guarantees for ticket 07 slice 2a, proven over the real
// store + controller chain: an admission failure from the durable transport
// seam becomes first-class evidence whose engine unavailable reason starts
// with the literal "admission: " prefix, while a plain provider failure keeps
// its concrete text without wearing that prefix.

import (
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

func surfaceBundles() []review.ReviewBundle {
	return []review.ReviewBundle{{Name: "quality", Dimensions: []string{"logic"}, Priority: review.PriorityRequired}}
}

func runSurfaceAudit(t *testing.T, agent review.AuditorAgente, sha string) review.ResultadoAuditoria {
	t.Helper()
	transport := durableTestTransport(t, sha)
	return review.AuditarCommit(func(_ review.ReviewBundle, _ string) (review.AuditorAgente, string, error) {
		return agent, "normal", nil
	}, 1, review.OpcionesAuditoria{
		SHA:             sha,
		Mensaje:         "message",
		Diff:            "diff",
		Bundles:         surfaceBundles(),
		ReviewTransport: transport,
	})
}

func TestEngineSurfacesAdmissionFailureWithLiteralPrefix(t *testing.T) {
	// A transport bound to a malformed audited SHA (containing colons) can
	// never embed it as exactly one readable candidate segment, so every Run
	// deterministically fails snapshot binding before waiting on the provider.
	result := runSurfaceAudit(t, &cannedAgent{responses: []string{`{"dim":"logic","verdict":"ok"}`}}, "engine:forced-admission")

	if len(result.Dims) != 1 || result.Dims[0].Resultado == nil {
		t.Fatalf("dims = %+v, want one routed dimension result", result.Dims)
	}
	dim := result.Dims[0].Resultado
	if dim.Verdict != review.VerdictUnavailable {
		t.Fatalf("verdict = %q, want unavailable for an admission failure", dim.Verdict)
	}
	if !strings.HasPrefix(dim.Reason, "admission: ") {
		t.Fatalf("reason = %q, want the literal \"admission: \" evidence prefix naming the diverging identity", dim.Reason)
	}
	if !strings.Contains(dim.Reason, "exactly one segment") {
		t.Fatalf("reason = %q, want the concrete binding divergence preserved after the prefix", dim.Reason)
	}
}

func TestEnginePreservesPlainProviderFailureWithoutAdmissionPrefix(t *testing.T) {
	result := runSurfaceAudit(t, failingAgent{}, "engine-preserved-failure")

	if len(result.Dims) != 1 || result.Dims[0].Resultado == nil {
		t.Fatalf("dims = %+v, want one routed dimension result", result.Dims)
	}
	dim := result.Dims[0].Resultado
	if dim.Verdict != review.VerdictUnavailable {
		t.Fatalf("verdict = %q, want unavailable for a provider failure", dim.Verdict)
	}
	if !strings.Contains(dim.Reason, "provider exploded during audit") {
		t.Fatalf("reason = %q, want the concrete provider text preserved verbatim", dim.Reason)
	}
	if strings.HasPrefix(dim.Reason, "admission: ") {
		t.Fatalf("reason = %q, want non-admission failures kept free of the admission label", dim.Reason)
	}
}
