package pr

import (
	"fmt"

	"github.com/ISeoane-Quental/vcSentinel/internal/review"
	"github.com/ISeoane-Quental/vcSentinel/internal/secret"
)

// SecretFindingsFactory scans every audited branch commit for exposed
// credentials (FU-11 residual) and prints the same advisory lines vcsentinel
// review prints. Findings ride the per-commit deterministic channel of
// review.BranchOptions: WARNING, dimensionless, voteless, empty evidence.
// The wording is identical because the strings come from the shared
// projection in internal/secret, not from a copy.
func SecretFindingsFactory() func(string, []string, string) []review.Finding {
	return func(_ string, files []string, diff string) []review.Finding {
		findings, advisories := secret.SecretFindingsAndAdvisories(files, diff)
		for _, advisory := range advisories {
			fmt.Println(advisory)
		}
		return findings
	}
}
