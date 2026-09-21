// Package remediation routes findings to a remediation destination based on
// their deterministic Fixable classification from the finding contract
// (internal/review).
package remediation

import (
	"fmt"

	"github.com/ISeoane-Quental/vcSentinel/internal/review"
)

// Destination identifies where a finding's remediation flow continues after
// the planner routes it.
type Destination string

const (
	// DestinationAgent sends the finding to the remediation agent (T7.2): the
	// fix can be applied without human confirmation.
	DestinationAgent Destination = "remediation_agent"
	// DestinationUserReview proposes the fix to the user without applying it.
	DestinationUserReview Destination = "user_review"
	// DestinationManualReport reports the finding without attempting a fix.
	DestinationManualReport Destination = "manual_report"
)

// Route maps a finding's Fixable classification to its remediation
// destination. The mapping is deterministic and derived only from the
// finding's contract (internal/review), never from an agent's judgment call
// made at routing time.
func Route(finding review.Finding) (Destination, error) {
	switch finding.Fixable {
	case review.FixableSafe:
		return DestinationAgent, nil
	case review.FixableNeedsReview:
		return DestinationUserReview, nil
	case review.FixableManual:
		return DestinationManualReport, nil
	default:
		return "", fmt.Errorf("remediation: finding %s has unknown fixable classification %q", finding.ID, finding.Fixable)
	}
}
