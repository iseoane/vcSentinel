package remediation

import (
	"fmt"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

// Verdicts RunSingleRound can return.
const (
	// ResultOK means the round resolved everything it needed to: no
	// blocking revalidation result and no unresolved CRITICAL finding.
	ResultOK = "ok"
	// ResultNeedsUserReview means a blocking condition remains after the
	// single allowed round: the caller must stop and hand this off to a
	// human, never launch another round on its own.
	ResultNeedsUserReview = "needs_user_review"
)

// UnresolvedFinding pairs a still-open finding with whether DiffGuard's
// located/unlocated window logic (T7.3) may have silently rejected any fix
// attempt scoped to it, rather than the fix genuinely failing to resolve it.
type UnresolvedFinding struct {
	Finding            review.Hallazgo
	SwallowedByLocated bool
}

// RoundResult is what one remediation round produced against the findings it
// was remediating (`before`): Unresolved are `before` findings still present
// afterward; New are findings the fix introduced that were not in `before`.
type RoundResult struct {
	Verdict    string
	Unresolved []UnresolvedFinding
	New        []review.Hallazgo
}

// Revalidate re-runs the validation profile that originally failed, scoped
// to the files a remediation round touched. Returns whether any blocking
// (non-zero-exit, per validation.Fallo semantics — caller's problem, not
// this package's) result remains.
type Revalidate func(profile string, touchedFiles []string) (blocked bool, err error)

// ReReview re-runs semantic review restricted to the touched files and
// returns the resulting findings.
type ReReview func(touchedFiles []string) ([]review.Hallazgo, error)

// RunSingleRound runs exactly one revalidation + re-review pass over the
// files a remediation fix touched, comparing the resulting findings against
// the findings that were being remediated (before) to report what is still
// unresolved and what the fix newly introduced. It NEVER loops: this
// function calls revalidate and reReview exactly once each, structurally (no
// loop construct around them) — enforcing the ficha's hard single-round
// limit. A caller that wants "try again" must call RunSingleRound again
// itself; RunSingleRound never does so on its own. A real error from either
// dependency (an infra failure, not a validation/review verdict) is
// propagated as this function's own error rather than swallowed; on a
// revalidate error, reReview is never called at all, since there is nothing
// meaningful left to compare against.
func RunSingleRound(profile string, before []review.Hallazgo, touchedFiles []string, revalidate Revalidate, reReview ReReview) (RoundResult, error) {
	blocked, err := revalidate(profile, touchedFiles)
	if err != nil {
		return RoundResult{}, fmt.Errorf("remediation: revalidation failed: %w", err)
	}

	after, err := reReview(touchedFiles)
	if err != nil {
		return RoundResult{}, fmt.Errorf("remediation: re-review failed: %w", err)
	}

	beforeFingerprints := make(map[string]bool, len(before))
	for _, f := range before {
		beforeFingerprints[f.Fingerprint] = true
	}

	var unresolved []UnresolvedFinding
	var newFindings []review.Hallazgo
	hasCriticalUnresolved := false
	for _, f := range after {
		if !beforeFingerprints[f.Fingerprint] {
			newFindings = append(newFindings, f)
			continue
		}
		unresolved = append(unresolved, UnresolvedFinding{
			Finding:            f,
			SwallowedByLocated: swallowedByLocated(f, before),
		})
		if f.Severity == review.SevCritical {
			hasCriticalUnresolved = true
		}
	}

	verdict := ResultOK
	if blocked || hasCriticalUnresolved {
		verdict = ResultNeedsUserReview
	}

	return RoundResult{Verdict: verdict, Unresolved: unresolved, New: newFindings}, nil
}

// swallowedByLocated reports whether finding might have been silently
// rejected by DiffGuard's located/unlocated window logic (T7.3) rather than
// genuinely failing to resolve: finding itself has no location, and another
// finding in before, for the SAME file, IS located — so allowedWindows would
// have authorized only that other finding's narrow window in the file,
// silently swallowing any real fix scoped to finding. See diffguard.go's
// allowedWindows doc comment for the swallowing behavior this flags.
func swallowedByLocated(finding review.Hallazgo, before []review.Hallazgo) bool {
	if finding.Location.LineaInicio > 0 {
		return false
	}
	for _, other := range before {
		if other.Fingerprint == finding.Fingerprint {
			continue
		}
		if other.Location.Archivo == finding.Location.Archivo && other.Location.LineaInicio > 0 {
			return true
		}
	}
	return false
}
