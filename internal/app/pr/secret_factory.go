package pr

import (
	"fmt"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/secret"
)

// SecretFindingsFactory scans every audited branch commit for exposed
// credentials (FU-11 residual) and prints the same advisory lines sentinel
// review prints. Findings ride the per-commit deterministic channel of
// review.OpcionesRama: WARNING, dimensionless, voteless, empty evidence.
// The wording is identical because the strings come from the shared
// projection in internal/secret, not from a copy.
func SecretFindingsFactory() func(string, []string, string) []review.Hallazgo {
	return func(_ string, archivos []string, diff string) []review.Hallazgo {
		findings, advisories := secret.SecretFindingsAndAdvisories(archivos, diff)
		for _, advisory := range advisories {
			fmt.Println(advisory)
		}
		return findings
	}
}
