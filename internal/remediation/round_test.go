package remediation

import (
	"errors"
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
	// NEEDS_USER_REVIEW: only a CRITICAL unresolved finding or a blocked
	// revalidation does, matching how the rest of this codebase (e.g.
	// internal/gate) gates on severity — WARNING degrades, it does not
	// block.
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

	_, err := RunSingleRound("standard", nil, []string{"a.go"}, revalidate, reReview)
	if err == nil {
		t.Fatalf("RunSingleRound: expected an error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("RunSingleRound: error = %v, want it to wrap %v", err, wantErr)
	}
	if reReviewCalls != 0 {
		t.Fatalf("RunSingleRound: reReview called %d times after a revalidate error, want 0", reReviewCalls)
	}
}

func TestRunSingleRoundPropagatesReReviewError(t *testing.T) {
	wantErr := errors.New("infra failure: review agent crashed")
	revalidate := func(string, []string) (bool, error) { return false, nil }
	reReview := func([]string) ([]review.Hallazgo, error) { return nil, wantErr }

	_, err := RunSingleRound("standard", nil, []string{"a.go"}, revalidate, reReview)
	if err == nil {
		t.Fatalf("RunSingleRound: expected an error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("RunSingleRound: error = %v, want it to wrap %v", err, wantErr)
	}
}
