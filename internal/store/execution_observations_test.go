package store

import "testing"

// TestCloneAttemptObservationDeepCopiesTurns pins the deep-copy contract for
// the observed turn count, mirroring the existing DurationNanos and Usage
// handling: mutating the clone's Turns must never affect the original.
func TestCloneAttemptObservationDeepCopiesTurns(t *testing.T) {
	turns := 4
	original := &AttemptObservation{Turns: &turns}
	clone := cloneAttemptObservation(original)
	if clone == nil || clone.Turns == nil {
		t.Fatalf("clone = %+v, want a non-nil cloned turn count", clone)
	}
	if clone.Turns == original.Turns {
		t.Fatal("clone.Turns aliases the original pointer, want an independent copy")
	}
	*clone.Turns = 99
	if *original.Turns != 4 {
		t.Errorf("original.Turns = %d, want 4 (unaffected by mutating the clone)", *original.Turns)
	}
}

// TestCloneAttemptObservationLeavesNilTurnsNil covers the nil case: cloning
// an observation with no observed turn count must produce a nil Turns, not
// a fabricated zero.
func TestCloneAttemptObservationLeavesNilTurnsNil(t *testing.T) {
	clone := cloneAttemptObservation(&AttemptObservation{})
	if clone == nil {
		t.Fatal("clone = nil, want a non-nil observation")
	}
	if clone.Turns != nil {
		t.Errorf("Turns = %d, want nil", *clone.Turns)
	}
}

// TestCloneAttemptObservationPreservesObservedZeroTurns pins the nil-vs-zero
// invariant that is the entire point of this task, at the
// cloneAttemptObservation layer specifically: a pointer to zero (a
// legitimately observed zero-turn review) must survive cloning as a non-nil
// pointer to zero, never collapse to nil. Every other clone test above uses
// a non-zero count (4), so this is the only test that would catch a
// regression that treated an observed zero as "unset" during cloning.
func TestCloneAttemptObservationPreservesObservedZeroTurns(t *testing.T) {
	zero := 0
	clone := cloneAttemptObservation(&AttemptObservation{Turns: &zero})
	if clone == nil {
		t.Fatal("clone = nil, want a non-nil observation")
	}
	if clone.Turns == nil {
		t.Fatal("Turns = nil, want a non-nil pointer to the observed zero, not a collapse to unknown")
	}
	if *clone.Turns != 0 {
		t.Errorf("Turns = %d, want 0", *clone.Turns)
	}
}

// TestValidateAttemptObservationRejectsNegativeTurns mirrors the existing
// negative-duration check: a negative observed turn count is corrupt
// evidence and must be rejected.
func TestValidateAttemptObservationRejectsNegativeTurns(t *testing.T) {
	negative := -1
	if err := validateAttemptObservation(&AttemptObservation{Turns: &negative}); err == nil {
		t.Fatal("validateAttemptObservation() error = nil, want a rejection for a negative turn count")
	}
}

// TestValidateAttemptObservationAcceptsZeroTurns ensures a legitimately
// observed zero (a real measurement, distinct from nil/unknown) is not
// mistaken for corrupt evidence.
func TestValidateAttemptObservationAcceptsZeroTurns(t *testing.T) {
	zero := 0
	if err := validateAttemptObservation(&AttemptObservation{Turns: &zero}); err != nil {
		t.Fatalf("validateAttemptObservation() error = %v, want a zero turn count to be accepted", err)
	}
}
