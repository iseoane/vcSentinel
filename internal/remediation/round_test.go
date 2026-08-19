package remediation

import (
	"errors"
	"slices"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

func TestRunSingleRoundNeverLaunchesASecondRound(t *testing.T) {
	// revalidate reports a real blocker (still blocked after the fix, not an
	// infra error) and reReview returns the same CRITICAL finding still
	// present: the round must come back as ResultNeedsUserReview WITHOUT
	// ever calling either dependency a second time. RunSingleRound has no
	// loop construct to retry against a fresh attempt; this test is the
	// ficha's literal acceptance criterion for that hard single-round limit.
	before := []review.Hallazgo{
		{Fingerprint: "fp-critical", Severity: review.SevCritical, Location: review.Ubicacion{Archivo: "a.go", LineaInicio: 10}},
	}

	var revalidateCalls, reReviewCalls int
	revalidate := func(profile string, touched []string) (bool, error) {
		revalidateCalls++
		return true, nil // still blocked after the fix
	}
	reReview := func(touched []string) ([]review.Hallazgo, error) {
		reReviewCalls++
		return before, nil // the CRITICAL finding is still there, unresolved
	}

	result, err := RunSingleRound("standard", before, []string{"a.go"}, revalidate, reReview)
	if err != nil {
		t.Fatalf("RunSingleRound: unexpected error: %v", err)
	}
	if result.Verdict != ResultNeedsUserReview {
		t.Fatalf("RunSingleRound: Verdict = %q, want %q", result.Verdict, ResultNeedsUserReview)
	}
	if revalidateCalls != 1 {
		t.Fatalf("RunSingleRound: revalidate called %d times, want exactly 1 (no second round)", revalidateCalls)
	}
	if reReviewCalls != 1 {
		t.Fatalf("RunSingleRound: reReview called %d times, want exactly 1 (no second round)", reReviewCalls)
	}
}

func TestRunSingleRoundFingerprintArithmetic(t *testing.T) {
	still := review.Hallazgo{Fingerprint: "fp-still", Severity: review.SevWarning, Location: review.Ubicacion{Archivo: "a.go", LineaInicio: 5}}
	resolved := review.Hallazgo{Fingerprint: "fp-resolved", Severity: review.SevWarning, Location: review.Ubicacion{Archivo: "a.go", LineaInicio: 20}}
	introduced := review.Hallazgo{Fingerprint: "fp-new", Severity: review.SevWarning, Location: review.Ubicacion{Archivo: "a.go", LineaInicio: 30}}

	before := []review.Hallazgo{still, resolved}

	revalidate := func(string, []string) (bool, error) { return false, nil }
	reReview := func([]string) ([]review.Hallazgo, error) {
		// resolved's fingerprint is gone (the fix resolved it); introduced's
		// fingerprint was never in before (the fix introduced it new).
		return []review.Hallazgo{still, introduced}, nil
	}

	result, err := RunSingleRound("standard", before, []string{"a.go"}, revalidate, reReview)
	if err != nil {
		t.Fatalf("RunSingleRound: unexpected error: %v", err)
	}
	if len(result.Unresolved) != 1 || result.Unresolved[0].Finding.Fingerprint != "fp-still" {
		t.Fatalf("RunSingleRound: Unresolved = %+v, want exactly one finding with fingerprint fp-still", result.Unresolved)
	}
	if len(result.New) != 1 || result.New[0].Fingerprint != "fp-new" {
		t.Fatalf("RunSingleRound: New = %+v, want exactly one finding with fingerprint fp-new", result.New)
	}
	// introduced is SevWarning, not SevCritical: a mutation that scoped
	// hasCriticalNew to "any new finding" instead of "any new CRITICAL
	// finding" would still pass every other assertion in this test, so pin
	// the verdict too.
	if result.Verdict != ResultOK {
		t.Fatalf("RunSingleRound: Verdict = %q, want %q (the new finding is only SevWarning)", result.Verdict, ResultOK)
	}
}

func TestRunSingleRoundSwallowedByLocatedDistinction(t *testing.T) {
	// f1 has no location; f2, in the SAME file, IS located. Per T7.3's
	// DiffGuard.allowedWindows, a located finding in a file authorizes only
	// its own margin window, silently swallowing any unlocated finding
	// sharing that file: a real fix attempt for f1 could have been rejected
	// as "out of scope" indistinguishably from a fix that just didn't work.
	f1 := review.Hallazgo{Fingerprint: "fp-unlocated-shared", Severity: review.SevWarning, Location: review.Ubicacion{Archivo: "shared.go"}}
	f2 := review.Hallazgo{Fingerprint: "fp-located", Severity: review.SevWarning, Location: review.Ubicacion{Archivo: "shared.go", LineaInicio: 5}}
	// f3 has no location either, but nothing else in before shares its file:
	// no located sibling exists that could have swallowed a fix for it.
	f3 := review.Hallazgo{Fingerprint: "fp-unlocated-alone", Severity: review.SevWarning, Location: review.Ubicacion{Archivo: "alone.go"}}

	before := []review.Hallazgo{f1, f2, f3}

	revalidate := func(string, []string) (bool, error) { return false, nil }
	reReview := func([]string) ([]review.Hallazgo, error) {
		return []review.Hallazgo{f1, f2, f3}, nil // none resolved
	}

	result, err := RunSingleRound("standard", before, []string{"shared.go", "alone.go"}, revalidate, reReview)
	if err != nil {
		t.Fatalf("RunSingleRound: unexpected error: %v", err)
	}
	if len(result.Unresolved) != 3 {
		t.Fatalf("RunSingleRound: got %d unresolved, want 3: %+v", len(result.Unresolved), result.Unresolved)
	}

	swallowed := map[string]bool{}
	for _, u := range result.Unresolved {
		swallowed[u.Finding.Fingerprint] = u.SwallowedByLocated
	}
	if !swallowed["fp-unlocated-shared"] {
		t.Fatalf("RunSingleRound: fp-unlocated-shared should be SwallowedByLocated=true, got %+v", result.Unresolved)
	}
	if swallowed["fp-unlocated-alone"] {
		t.Fatalf("RunSingleRound: fp-unlocated-alone should be SwallowedByLocated=false (no located sibling), got %+v", result.Unresolved)
	}
	if swallowed["fp-located"] {
		t.Fatalf("RunSingleRound: fp-located is itself located, should never be SwallowedByLocated, got %+v", result.Unresolved)
	}
}

func TestRunSingleRoundOK(t *testing.T) {
	// An unresolved WARNING-severity finding alone must NOT force
	// NEEDS_USER_REVIEW: only a blocked revalidation, an unresolved CRITICAL
	// finding, or a newly introduced CRITICAL finding does, matching how the
	// rest of this codebase (e.g. internal/gate) gates on severity — WARNING
	// degrades, it does not block.
	warning := review.Hallazgo{Fingerprint: "fp-warning", Severity: review.SevWarning, Location: review.Ubicacion{Archivo: "a.go", LineaInicio: 5}}
	before := []review.Hallazgo{warning}

	revalidate := func(string, []string) (bool, error) { return false, nil }
	reReview := func([]string) ([]review.Hallazgo, error) { return []review.Hallazgo{warning}, nil }

	result, err := RunSingleRound("standard", before, []string{"a.go"}, revalidate, reReview)
	if err != nil {
		t.Fatalf("RunSingleRound: unexpected error: %v", err)
	}
	if result.Verdict != ResultOK {
		t.Fatalf("RunSingleRound: Verdict = %q, want %q (an unresolved WARNING alone must not force review)", result.Verdict, ResultOK)
	}
	if len(result.Unresolved) != 1 {
		t.Fatalf("RunSingleRound: got %d unresolved, want 1", len(result.Unresolved))
	}
	if len(result.New) != 0 {
		t.Fatalf("RunSingleRound: got %d new findings, want 0", len(result.New))
	}
}

func TestRunSingleRoundPropagatesRevalidateError(t *testing.T) {
	wantErr := errors.New("infra failure: validation profile crashed")
	var reReviewCalls int
	revalidate := func(string, []string) (bool, error) { return false, wantErr }
	reReview := func([]string) ([]review.Hallazgo, error) {
		reReviewCalls++
		return nil, nil
	}

	result, err := RunSingleRound("standard", nil, []string{"a.go"}, revalidate, reReview)
	if err == nil {
		t.Fatalf("RunSingleRound: expected an error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("RunSingleRound: error = %v, want it to wrap %v", err, wantErr)
	}
	if reReviewCalls != 0 {
		t.Fatalf("RunSingleRound: reReview called %d times after a revalidate error, want 0", reReviewCalls)
	}
	// The zero-value-on-error case must be the safe/blocking one: a caller
	// that only checks result.Verdict != ResultNeedsUserReview (forgetting to
	// check err) must not be silently allowed to proceed on an infra failure.
	if result.Verdict != ResultNeedsUserReview {
		t.Fatalf("RunSingleRound: on revalidate error, Verdict = %q, want %q (safe default)", result.Verdict, ResultNeedsUserReview)
	}
}

func TestRunSingleRoundPropagatesReReviewError(t *testing.T) {
	wantErr := errors.New("infra failure: review agent crashed")
	// revalidate succeeds and reports a real blocked=true before reReview
	// fails: the error path must still carry that real value through, not
	// silently reset it to false.
	revalidate := func(string, []string) (bool, error) { return true, nil }
	reReview := func([]string) ([]review.Hallazgo, error) { return nil, wantErr }

	result, err := RunSingleRound("standard", nil, []string{"a.go"}, revalidate, reReview)
	if err == nil {
		t.Fatalf("RunSingleRound: expected an error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("RunSingleRound: error = %v, want it to wrap %v", err, wantErr)
	}
	// Same safe-default guarantee as the revalidate error path above.
	if result.Verdict != ResultNeedsUserReview {
		t.Fatalf("RunSingleRound: on reReview error, Verdict = %q, want %q (safe default)", result.Verdict, ResultNeedsUserReview)
	}
	if !result.Blocked {
		t.Fatalf("RunSingleRound: on reReview error, Blocked = false, want true (revalidate already reported it before reReview failed)")
	}
}

func TestRunSingleRoundNewCriticalForcesReview(t *testing.T) {
	// A CRITICAL finding that appears only in `after` (never in `before`, so
	// it lands in result.New, not result.Unresolved) must still force
	// NeedsUserReview: the fix itself introduced a new critical defect, even
	// though revalidate is not blocked and nothing critical was left
	// unresolved from before. This locks in Bug 1's fix.
	warning := review.Hallazgo{Fingerprint: "fp-warning", Severity: review.SevWarning, Location: review.Ubicacion{Archivo: "a.go", LineaInicio: 5}}
	newCritical := review.Hallazgo{Fingerprint: "fp-new-critical", Severity: review.SevCritical, Location: review.Ubicacion{Archivo: "a.go", LineaInicio: 40}}

	before := []review.Hallazgo{warning}

	revalidate := func(string, []string) (bool, error) { return false, nil }
	reReview := func([]string) ([]review.Hallazgo, error) {
		return []review.Hallazgo{warning, newCritical}, nil
	}

	result, err := RunSingleRound("standard", before, []string{"a.go"}, revalidate, reReview)
	if err != nil {
		t.Fatalf("RunSingleRound: unexpected error: %v", err)
	}
	if result.Verdict != ResultNeedsUserReview {
		t.Fatalf("RunSingleRound: Verdict = %q, want %q (a new CRITICAL finding must force review)", result.Verdict, ResultNeedsUserReview)
	}
	if len(result.New) != 1 || result.New[0].Fingerprint != "fp-new-critical" {
		t.Fatalf("RunSingleRound: New = %+v, want exactly the new critical finding", result.New)
	}
	if result.Blocked {
		t.Fatalf("RunSingleRound: Blocked = true, want false (revalidate was not blocked)")
	}
}

func TestRunSingleRoundBlockedAloneForcesReview(t *testing.T) {
	// revalidate reports blocked=true with no unresolved or new CRITICAL
	// finding at all (only a WARNING survives): NeedsUserReview must still
	// follow from the blocked branch alone, independent of severity.
	warning := review.Hallazgo{Fingerprint: "fp-warning", Severity: review.SevWarning, Location: review.Ubicacion{Archivo: "a.go", LineaInicio: 5}}
	before := []review.Hallazgo{warning}

	revalidate := func(string, []string) (bool, error) { return true, nil } // blocked, no critical involved
	reReview := func([]string) ([]review.Hallazgo, error) { return []review.Hallazgo{warning}, nil }

	result, err := RunSingleRound("standard", before, []string{"a.go"}, revalidate, reReview)
	if err != nil {
		t.Fatalf("RunSingleRound: unexpected error: %v", err)
	}
	if result.Verdict != ResultNeedsUserReview {
		t.Fatalf("RunSingleRound: Verdict = %q, want %q (blocked alone must force review)", result.Verdict, ResultNeedsUserReview)
	}
	if !result.Blocked {
		t.Fatalf("RunSingleRound: Blocked = false, want true")
	}
}

func TestRunSingleRoundCriticalUnresolvedAloneForcesReview(t *testing.T) {
	// revalidate reports blocked=false, but a CRITICAL finding from `before`
	// is still present in `after` (unresolved): NeedsUserReview must follow
	// from the CRITICAL-unresolved branch alone, isolated from `blocked`.
	critical := review.Hallazgo{Fingerprint: "fp-critical", Severity: review.SevCritical, Location: review.Ubicacion{Archivo: "a.go", LineaInicio: 10}}
	before := []review.Hallazgo{critical}

	revalidate := func(string, []string) (bool, error) { return false, nil } // not blocked
	reReview := func([]string) ([]review.Hallazgo, error) { return []review.Hallazgo{critical}, nil }

	result, err := RunSingleRound("standard", before, []string{"a.go"}, revalidate, reReview)
	if err != nil {
		t.Fatalf("RunSingleRound: unexpected error: %v", err)
	}
	if result.Verdict != ResultNeedsUserReview {
		t.Fatalf("RunSingleRound: Verdict = %q, want %q (unresolved CRITICAL alone must force review)", result.Verdict, ResultNeedsUserReview)
	}
	if result.Blocked {
		t.Fatalf("RunSingleRound: Blocked = true, want false")
	}
}

func TestRunSingleRoundPassesThroughProfileAndTouchedFiles(t *testing.T) {
	// profile and touchedFiles must reach revalidate and reReview exactly as
	// passed to RunSingleRound, so a future accidental argument swap or
	// dropped parameter is caught.
	// A distinctive value, not the "standard" default every other test uses:
	// if RunSingleRound ever dropped the profile parameter and hardcoded
	// "standard" instead, this test must catch it, not pass by coincidence.
	wantProfile := "sentinel-passthrough-profile"
	wantTouched := []string{"a.go", "b.go"}

	var gotRevalidateProfile string
	var gotRevalidateTouched []string
	var gotReReviewTouched []string

	revalidate := func(profile string, touched []string) (bool, error) {
		gotRevalidateProfile = profile
		gotRevalidateTouched = touched
		return false, nil
	}
	reReview := func(touched []string) ([]review.Hallazgo, error) {
		gotReReviewTouched = touched
		return nil, nil
	}

	_, err := RunSingleRound(wantProfile, nil, wantTouched, revalidate, reReview)
	if err != nil {
		t.Fatalf("RunSingleRound: unexpected error: %v", err)
	}
	if gotRevalidateProfile != wantProfile {
		t.Fatalf("revalidate received profile = %q, want %q", gotRevalidateProfile, wantProfile)
	}
	if !slices.Equal(gotRevalidateTouched, wantTouched) {
		t.Fatalf("revalidate received touchedFiles = %v, want %v", gotRevalidateTouched, wantTouched)
	}
	if !slices.Equal(gotReReviewTouched, wantTouched) {
		t.Fatalf("reReview received touchedFiles = %v, want %v", gotReReviewTouched, wantTouched)
	}
}

func TestSwallowedByLocatedNoFalsePositiveOnEmptyArchivo(t *testing.T) {
	// finding has no file at all (fully unlocated); other also has an empty
	// Archivo but a positive LineaInicio (a malformed/inconsistent finding:
	// a line number without a file). Before the fix, the string comparison
	// other.Location.Archivo == finding.Location.Archivo was true (both
	// empty), producing a false "swallowed" verdict even though no real file
	// exists for any DiffGuard window to have swallowed anything against.
	finding := review.Hallazgo{Fingerprint: "fp-no-file", Severity: review.SevWarning, Location: review.Ubicacion{Archivo: ""}}
	other := review.Hallazgo{Fingerprint: "fp-malformed", Severity: review.SevWarning, Location: review.Ubicacion{Archivo: "", LineaInicio: 5}}

	if swallowedByLocated(finding, []review.Hallazgo{other}) {
		t.Fatalf("swallowedByLocated: got true, want false (no real file to be swallowed against)")
	}
}
