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
