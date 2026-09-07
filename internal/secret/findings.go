package secret

import (
	"fmt"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

// ProjectSecretIncidents converts exposed-credential incidents into
// deterministic review findings (FU-11).
//
// The projection is deliberately NOT a review dimension: Dimension stays
// empty, so the finding is reported even when the review plan schedules
// nothing and review.SupersedeDeterministicFindings can never use it to
// discard an unrelated semantic finding. Severity stays WARNING, so the
// shared review.IsBlocking rule never blocks on it: the incident reports,
// it does not gate. Evidence stays empty on purpose: persisting the matched
// value in the ledger would make the exposure permanent.
func ProjectSecretIncidents(incidents []Incident) []review.Finding {
	findings := make([]review.Finding, 0, len(incidents))
	for _, incident := range incidents {
		finding := review.Finding{
			Source:      review.SourceValidation,
			Severity:    review.SevWarning,
			Confidence:  0.9,
			Status:      review.StatusPending,
			Title:       fmt.Sprintf("exposed credential (%s)", incident.Shape),
			Description: fmt.Sprintf("%s:%d matches %s (value withheld)", incident.Path, incident.Line, incident.Shape),
			Fixable:     review.FixableManual,
			Location:    review.Location{File: incident.Path, LineStart: incident.Line},
		}
		finding.Fingerprint = review.Fingerprint(finding)
		findings = append(findings, finding)
	}
	return findings
}

// SecretFindingsAndAdvisories runs the credential scan over one commit's files
// and diff and returns both the deterministic findings for the audit result
// and the console advisory lines. Both stay empty when there is nothing to
// report and nothing the scanner could not read.
func SecretFindingsAndAdvisories(paths []string, diff string) ([]review.Finding, []string) {
	incidents, unreadable := Scan(paths, diff)
	return ProjectSecretIncidents(incidents), SecretAdvisories(incidents, unreadable)
}

// SecretAdvisories renders the console advisory for a credential scan: one line
// per incident plus one line naming paths the scanner could not read.
// Empty input renders nothing: absence is data, never a "clean" verdict and
// never a zero count. No line ever carries a matched value.
func SecretAdvisories(incidents []Incident, unreadable []string) []string {
	var advisories []string
	for _, incident := range incidents {
		advisories = append(advisories, fmt.Sprintf("  ⚠️ exposed credential: %s", incident.String()))
	}
	if len(unreadable) > 0 {
		advisories = append(advisories, fmt.Sprintf("  ⚠️ credential scan unavailable for: %s", strings.Join(unreadable, ", ")))
	}
	return advisories
}
