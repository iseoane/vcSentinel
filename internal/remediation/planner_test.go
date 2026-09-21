package remediation

import (
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/review"
)

func TestRoute(t *testing.T) {
	cases := []struct {
		name    string
		fixable string
		want    Destination
		wantErr bool
	}{
		{"safe routes to remediation agent", review.FixableSafe, DestinationAgent, false},
		{"needs_review routes to user review", review.FixableNeedsReview, DestinationUserReview, false},
		{"manual routes to manual report", review.FixableManual, DestinationManualReport, false},
		{"unknown fixable value fails closed", "unknown", "", true},
		{"empty fixable value fails closed", "", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			finding := review.Finding{ID: "f1", Fixable: tc.fixable}
			got, err := Route(finding)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Route(%q) expected error, got nil", tc.fixable)
				}
				return
			}
			if err != nil {
				t.Fatalf("Route(%q) unexpected error: %v", tc.fixable, err)
			}
			if got != tc.want {
				t.Fatalf("Route(%q) = %q, want %q", tc.fixable, got, tc.want)
			}
		})
	}
}
