package gate

// Focused tests for ticket 07 slice 3: admission failures surface through the
// gate's public result shape distinctly from infrastructure failures. Exit
// codes and terminal states are untouched; only the evidence classification
// in the messages differs.

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewexec"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

func TestGateSurfacesAdmissionDistinctFromInfrastructure(t *testing.T) {
	cfg := cfgConPerfil("lint", "echo ok")
	opts := opcionesBase(cfg, func(string) (int, string, error) { return 0, "ok", nil },
		fabricaContadora(new(int), "", nil))
	opts.EjecutarValidacion = func(_ string, _ []string, _ validation.OpcionesEjecucion) ([]validation.ValidationRun, error) {
		return nil, nil // validation green: the review stage is what this test exercises
	}
	opts.OpcionesRevision.Bundles = []review.ReviewBundle{
		{Name: "test", Dimensions: []string{review.DimLogic, review.DimSecurity}, Priority: 1, Cost: 1},
	}
	opts.OpcionesRevision.ReviewTransport = func(_ string, dimension, _ string, _ review.AuditorAgente) (string, string, error) {
		if dimension == review.DimLogic {
			return "", "", &reviewexec.AdmissionError{
				Identity: "quality/logic",
				Reason:   "output hash mismatch for the admitted invocation",
			}
		}
		return "", "", errors.New("durable store is unreachable")
	}

	resultado := EjecutarGate(opts)

	if resultado.Estado != EstadoReviewInfrastructureError {
		t.Fatalf("estado = %q, want %q (exit-code contracts unchanged)", resultado.Estado, EstadoReviewInfrastructureError)
	}
	unido := strings.Join(resultado.Mensajes, "\n")
	if !strings.Contains(unido, `dimension="logic" reason="admission: output hash mismatch for the admitted invocation" class=admission`) {
		t.Fatalf("messages = %q, want the logic dimension labeled class=admission with its evidence prefix intact", unido)
	}
	if !strings.Contains(unido, `dimension="security" reason="durable store is unreachable" class=infrastructure`) {
		t.Fatalf("messages = %q, want the security dimension labeled class=infrastructure", unido)
	}
	if strings.Count(unido, "class=admission") != 1 || strings.Count(unido, "class=infrastructure") != 1 {
		t.Fatalf("messages = %q, want exactly one label per failure class", unido)
	}
}

// TestFailureClassPrefersTypedErrorThenReason pins the classification order:
// the typed transport error wins when the engine retained it; once only the
// persisted reason survives, the literal admission prefix decides.
func TestFailureClassPrefersTypedErrorThenReason(t *testing.T) {
	admitted := review.ResultadoDimension{
		Error:     fmt.Errorf("review run failed: %w", &reviewexec.AdmissionError{Reason: "stale snapshot"}),
		Resultado: &review.DimensionResult{Verdict: review.VerdictUnavailable},
	}
	if got := failureClass(admitted); got != "admission" {
		t.Fatalf("failureClass(typed admission error) = %q, want admission", got)
	}

	persisted := review.ResultadoDimension{
		Resultado: &review.DimensionResult{Verdict: review.VerdictUnavailable, Reason: "admission: prompt identity diverged"},
	}
	if got := failureClass(persisted); got != "admission" {
		t.Fatalf("failureClass(persisted admission reason) = %q, want admission", got)
	}

	outage := review.ResultadoDimension{
		Error:     errors.New("durable store is unreachable"),
		Resultado: &review.DimensionResult{Verdict: review.VerdictUnavailable, Reason: "durable store is unreachable"},
	}
	if got := failureClass(outage); got != "infrastructure" {
		t.Fatalf("failureClass(infrastructure failure) = %q, want infrastructure", got)
	}
}
