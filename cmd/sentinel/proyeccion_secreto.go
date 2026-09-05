package main

import (
	"fmt"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/secret"
)

// proyectarIncidentesSecreto converts exposed-credential incidents into
// deterministic review findings (FU-11).
//
// The projection is deliberately NOT a review dimension: Dimension stays
// empty, so the finding is reported even when the review plan schedules
// nothing and review.SupersedeDeterministicFindings can never use it to
// discard an unrelated semantic finding. Severity stays WARNING, so the
// shared review.IsBlocking rule never blocks on it: the incident reports,
// it does not gate. Evidence stays empty on purpose: persisting the matched
// value in the ledger would make the exposure permanent.
func proyectarIncidentesSecreto(incidentes []secret.Incident) []review.Hallazgo {
	hallazgos := make([]review.Hallazgo, 0, len(incidentes))
	for _, incidente := range incidentes {
		hallazgo := review.Hallazgo{
			Source:      review.SourceValidation,
			Severity:    review.SevWarning,
			Confidence:  0.9,
			Status:      review.StatusPending,
			Title:       fmt.Sprintf("exposed credential (%s)", incidente.Shape),
			Description: fmt.Sprintf("%s:%d matches %s (value withheld)", incidente.Path, incidente.Line, incidente.Shape),
			Fixable:     review.FixableManual,
			Location:    review.Ubicacion{Archivo: incidente.Path, LineaInicio: incidente.Line},
		}
		hallazgo.Fingerprint = review.Fingerprint(hallazgo)
		hallazgos = append(hallazgos, hallazgo)
	}
	return hallazgos
}

// hallazgosYavisosSecreto runs the credential scan over one commit's files
// and diff and returns both the deterministic findings for the audit result
// and the console advisory lines. Both stay empty when there is nothing to
// report and nothing the scanner could not read.
func hallazgosYavisosSecreto(archivos []string, diff string) ([]review.Hallazgo, []string) {
	incidentes, desconocidas := secret.Scan(archivos, diff)
	return proyectarIncidentesSecreto(incidentes), avisosSecreto(incidentes, desconocidas)
}

// avisosSecreto renders the console advisory for a credential scan: one line
// per incident plus one line naming paths the scanner could not read.
// Empty input renders nothing: absence is data, never a "clean" verdict and
// never a zero count. No line ever carries a matched value.
func avisosSecreto(incidentes []secret.Incident, desconocidas []string) []string {
	var avisos []string
	for _, incidente := range incidentes {
		avisos = append(avisos, fmt.Sprintf("  ⚠️ exposed credential: %s", incidente.String()))
	}
	if len(desconocidas) > 0 {
		avisos = append(avisos, fmt.Sprintf("  ⚠️ credential scan unavailable for: %s", joinComa(desconocidas)))
	}
	return avisos
}

func joinComa(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}
