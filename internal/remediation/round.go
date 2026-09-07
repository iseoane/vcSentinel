package remediation

import (
	"fmt"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

// Verdicts RunSingleRound can return.
const (
	// ResultOK means the round resolved everything it needed to: no
	// blocking revalidation result, no unresolved CRITICAL finding, and no
	// newly introduced CRITICAL finding.
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
	Finding            review.Finding
	SwallowedByLocated bool
}

// RoundResult is what one remediation round produced against the findings it
// was remediating (`before`): Unresolved are `before` findings still present
// afterward; New are findings the fix introduced that were not in `before`.
// Blocked reports revalidate's own blocked/not-blocked result: real whenever
// revalidate itself ran successfully (including the reReview error path,
// where revalidate already returned before reReview failed), and false only
// when revalidate itself errored, since it never got to report a real state
// there — the returned error, not Blocked, is what signals that case.
// Combined with Unresolved/New (whose Severity a caller can inspect
// directly), Blocked exposes the one piece of "why NeedsUserReview" that
// isn't otherwise recoverable from the result alone.
type RoundResult struct {
	Verdict    string
	Unresolved []UnresolvedFinding
	New        []review.Finding
	Blocked    bool
}

// Revalidate re-runs the validation profile that originally failed, scoped
// to the files a remediation round touched. Returns whether any blocking
// (non-zero-exit, per validation.Failed semantics — caller's problem, not
// this package's) result remains.
type Revalidate func(profile string, touchedFiles []string) (blocked bool, err error)

// ReReview re-runs semantic review restricted to the touched files and
// returns the resulting findings.
type ReReview func(touchedFiles []string) ([]review.Finding, error)

// RunSingleRound runs exactly one revalidation + re-review pass over the
// files a remediation fix touched, comparing the resulting findings against
// the findings that were being remediated (before) to report what is still
// unresolved and what the fix newly introduced. It NEVER loops: this
// function calls revalidate and reReview exactly once each, structurally (no
// loop construct around them) — enforcing the spec's hard single-round
// limit. A caller that wants "try again" must call RunSingleRound again
// itself; RunSingleRound never does so on its own. A real error from either
// dependency (an infra failure, not a validation/review verdict) is
// propagated as this function's own error rather than swallowed; on a
// revalidate error, reReview is never called at all, since there is nothing
// meaningful left to compare against. On either error, Verdict is set
// explicitly to ResultNeedsUserReview — its zero value, "", would not be
// blocking — so a caller that inspects only Verdict, forgetting to check the
// error first, still fails closed instead of silently proceeding. The
// reReview error path also carries the real Blocked value revalidate already
// reported (it ran and returned successfully before reReview failed); the
// revalidate error path leaves Blocked at its zero value (false) because
// revalidate itself never got to report a real state there.
func RunSingleRound(profile string, before []review.Finding, touchedFiles []string, revalidate Revalidate, reReview ReReview) (RoundResult, error) {
	blocked, err := revalidate(profile, touchedFiles)
	if err != nil {
		return RoundResult{Verdict: ResultNeedsUserReview}, fmt.Errorf("remediation: revalidation failed: %w", err)
	}

	after, err := reReview(touchedFiles)
	if err != nil {
		return RoundResult{Verdict: ResultNeedsUserReview, Blocked: blocked}, fmt.Errorf("remediation: re-review failed: %w", err)
	}

	beforeFingerprints := make(map[string]bool, len(before))
	for _, f := range before {
		beforeFingerprints[f.Fingerprint] = true
	}

	var unresolved []UnresolvedFinding
	var newFindings []review.Finding
	hasCriticalUnresolved := false
	hasCriticalNew := false
	for _, f := range after {
		if !beforeFingerprints[f.Fingerprint] {
			newFindings = append(newFindings, f)
			if f.Severity == review.SevCritical {
				hasCriticalNew = true
			}
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
	if blocked || hasCriticalUnresolved || hasCriticalNew {
		verdict = ResultNeedsUserReview
	}

	return RoundResult{Verdict: verdict, Unresolved: unresolved, New: newFindings, Blocked: blocked}, nil
}

// swallowedByLocated reports whether finding might have been silently
// rejected by DiffGuard's located/unlocated window logic (T7.3) rather than
// genuinely failing to resolve: finding names a file but has no line within
// it, and another finding in before, for the SAME file, IS located — so
// allowedWindows would have authorized only that other finding's narrow
// window in the file, silently swallowing any real fix scoped to finding.
// A finding with no file at all (empty File) can never be "swallowed" by
// a same-file sibling, since there is no real file for any DiffGuard window
// to have narrowed in the first place — matching an unrelated finding that
// also happens to have an empty File would be a coincidence of the zero
// value, not a shared file. See diffguard.go's allowedWindows doc comment
// for the swallowing behavior this flags.
func swallowedByLocated(finding review.Finding, before []review.Finding) bool {
	if finding.Location.File == "" || finding.Location.LineStart > 0 {
		return false
	}
	for _, other := range before {
		if other.Fingerprint == finding.Fingerprint {
			continue
		}
		if other.Location.File == finding.Location.File && other.Location.LineStart > 0 {
			return true
		}
	}
	return false
}
