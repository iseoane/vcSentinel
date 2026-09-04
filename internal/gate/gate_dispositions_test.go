package gate

import (
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

// FU-6: the gate observes the same effective disposition as the engine and
// the branch blockers through the shared rule. A refuted or fixed CRITICAL
// finding is no longer effective; accepted, reopened, pending, confirmed,
// and legacy status-less findings still are.
func TestEffectiveCriticalFindingsHonoursTheSharedRule(t *testing.T) {
	mk := func(status string) review.Hallazgo {
		return review.Hallazgo{
			Dimension: review.DimLogic, Severity: review.SevCritical,
			Status:      status,
			Description: "finding " + status,
			Location:    review.Ubicacion{Archivo: "a.go", LineaInicio: 1},
		}
	}
	audit := review.ResultadoAuditoria{
		Findings: []review.Hallazgo{
			mk(review.StatusRefuted),
			mk(review.StatusFixed),
			mk(review.StatusAcceptedByUser),
			mk(review.StatusReopened),
			mk(review.StatusPending),
			mk(review.StatusConfirmed),
			mk(""),
		},
	}

	got := effectiveCriticalFindings(audit)
	statuses := map[string]bool{}
	for _, h := range got {
		statuses[h.Status] = true
	}
	for _, cleared := range []string{review.StatusRefuted, review.StatusFixed} {
		if statuses[cleared] {
			t.Fatalf("status %q is effective, want refuted and fixed findings cleared", cleared)
		}
	}
	for _, blocking := range []string{review.StatusAcceptedByUser, review.StatusReopened, review.StatusPending, review.StatusConfirmed, ""} {
		if !statuses[blocking] {
			t.Fatalf("status %q is not effective, want it blocking", blocking)
		}
	}
	if len(got) != 5 {
		t.Fatalf("effective = %d findings, want 5", len(got))
	}
}

// FU-6: a human-cleared audit passes the gate instead of asking for another
// human review: the downgrade carries no automated refutation flag.
func TestTraducirVeredictoPassesHumanClearedAudits(t *testing.T) {
	audit := review.ResultadoAuditoria{
		Veredicto: review.VerdictWarn,
		Dims: []review.ResultadoDimension{{
			Dim: "logic",
			Resultado: &review.DimensionResult{
				Dim:     review.DimLogic,
				Verdict: review.VerdictWarn,
				Findings: []review.ReviewFinding{{
					File: "a.go", Line: 2, Severity: review.SevCritical,
					Description:      "bug",
					Status:           review.StatusRefuted,
					RefutationActor:  review.RefutationActorHuman,
					RefutationReason: "verified safe",
				}},
			},
		}},
	}

	resultado := traducirVeredicto(audit)
	if resultado.Estado != EstadoPass {
		t.Fatalf("estado = %q, want a human-cleared audit to pass, got %q", resultado.Estado, resultado.Mensajes)
	}
}
