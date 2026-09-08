package execution

import (
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// TestStoreObservationCarriesTurnCount pins the mapping this task adds:
// AdapterObservation.Turns must survive storeObservation into
// store.AttemptObservation.Turns as an independent clone, not the caller's
// own pointer (mutating the source afterward must not affect the mapped
// value).
func TestStoreObservationCarriesTurnCount(t *testing.T) {
	turns := 3
	mapped := storeObservation(&AdapterObservation{StopReason: "end_turn", Turns: &turns})
	if mapped == nil || mapped.Turns == nil {
		t.Fatalf("mapped = %+v, want a non-nil observed turn count", mapped)
	}
	if *mapped.Turns != 3 {
		t.Errorf("Turns = %d, want 3", *mapped.Turns)
	}
	turns = 99
	if *mapped.Turns != 3 {
		t.Error("Turns aliases the caller's pointer, want an independent clone")
	}
}

// TestStoreObservationLeavesTurnCountNilWhenUnobserved pins the negative
// case: an observation with no observed turn count must map to nil, never a
// fabricated zero that a later calibration could misread as a measurement.
func TestStoreObservationLeavesTurnCountNilWhenUnobserved(t *testing.T) {
	mapped := storeObservation(&AdapterObservation{StopReason: "end_turn"})
	if mapped == nil {
		t.Fatal("mapped = nil, want a non-nil observation")
	}
	if mapped.Turns != nil {
		t.Errorf("Turns = %d, want nil", *mapped.Turns)
	}
}

// TestStoreObservationPreservesObservedZeroTurns pins the nil-vs-zero
// invariant that is the entire point of this task, at the storeObservation
// mapping layer specifically: a pointer to zero (a legitimately observed
// zero-turn review) must survive as a non-nil pointer to zero, never
// collapse to nil. Every other propagation test in this file uses a
// non-zero count, so this is the only test that would catch a regression
// that treated an observed zero as "unset" during mapping.
func TestStoreObservationPreservesObservedZeroTurns(t *testing.T) {
	zero := 0
	mapped := storeObservation(&AdapterObservation{StopReason: "end_turn", Turns: &zero})
	if mapped == nil {
		t.Fatal("mapped = nil, want a non-nil observation")
	}
	if mapped.Turns == nil {
		t.Fatal("Turns = nil, want a non-nil pointer to the observed zero, not a collapse to unknown")
	}
	if *mapped.Turns != 0 {
		t.Errorf("Turns = %d, want 0", *mapped.Turns)
	}
}

// TestApplyObservationCarriesTurnCountOntoOutcome pins the outcome-side half
// of the same mapping: applyObservation must project Turns onto
// outcome.Observation.
func TestApplyObservationCarriesTurnCountOntoOutcome(t *testing.T) {
	turns := 5
	outcome := &store.AttemptOutcome{}
	applyObservation(outcome, &AdapterObservation{StopReason: "end_turn", Turns: &turns})
	if outcome.Observation == nil || outcome.Observation.Turns == nil {
		t.Fatalf("outcome.Observation = %+v, want a non-nil observed turn count", outcome.Observation)
	}
	if *outcome.Observation.Turns != 5 {
		t.Errorf("Turns = %d, want 5", *outcome.Observation.Turns)
	}
}
