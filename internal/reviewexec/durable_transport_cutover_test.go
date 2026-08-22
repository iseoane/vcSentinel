package reviewexec

// Focused tests for ticket 07 slice 3: the evidence-admission cutover flag.
// Strict admission is the constructor default so accidental non-wiring stays
// safe; WithEvidenceAdmission(false) restores the pre-R6 observe-but-admit
// lenient behavior, and the typed/string classifiers let surfacing layers
// distinguish admission failures from infrastructure failures.

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

func TestIsAdmissionErrorClassifiesTypedAndWrappedErrors(t *testing.T) {
	if !IsAdmissionError(&AdmissionError{Identity: "quality/logic", Reason: "hash mismatch"}) {
		t.Fatal("IsAdmissionError(*AdmissionError) = false, want true")
	}
	wrapped := fmt.Errorf("review run quality/logic failed: %w", &AdmissionError{Reason: "stale snapshot"})
	if !IsAdmissionError(wrapped) {
		t.Fatal("IsAdmissionError(wrapped AdmissionError) = false, want true through errors.As")
	}
	if IsAdmissionError(nil) {
		t.Fatal("IsAdmissionError(nil) = true, want false")
	}
	if IsAdmissionError(errors.New("provider exploded")) {
		t.Fatal("IsAdmissionError(plain error) = true, want false")
	}
	if IsAdmissionError(&TerminalError{Identity: "quality/logic", Text: "provider exploded"}) {
		t.Fatal("IsAdmissionError(TerminalError) = true, want infrastructure failures kept out of the admission class")
	}
}

func TestIsAdmissionReasonMatchesTheLiteralPrefixOnly(t *testing.T) {
	if !IsAdmissionReason(AdmissionReasonPrefix + "output hash mismatch") {
		t.Fatal("IsAdmissionReason(admission reason) = false, want true")
	}
	for _, reason := range []string{
		"provider exploded during audit",
		"admitted without prefix",
		"",
	} {
		if IsAdmissionReason(reason) {
			t.Fatalf("IsAdmissionReason(%q) = true, want false for non-admission reasons", reason)
		}
	}
	var admission *AdmissionError
	admission = &AdmissionError{Reason: "x"}
	if admission.Error() != AdmissionReasonPrefix+"x" {
		t.Fatalf("Error() = %q, want the single-source literal prefix", admission.Error())
	}
}

func TestRunStaysStrictByDefaultOnDivergentSnapshot(t *testing.T) {
	// A transport-bound SHA containing colons can never appear as exactly one
	// readable candidate segment, so strict snapshot binding always rejects.
	backing := store.NuevoStore(t.TempDir())
	transport := NewDurableTransport(backing, store.RunPolicy{ID: "policy:cutover"}, "cutover:default-strict", nil)
	reviewer := &scriptedReviewer{name: "dimension-logic", output: "raw verdict"}

	output, evidence, err := transport.Run(reviewer, "quality/logic", "the prompt")
	if output != "" || evidence != (Evidence{}) {
		t.Fatalf("Run() = %q, %+v, want empty output and evidence on strict rejection", output, evidence)
	}
	if !IsAdmissionError(err) {
		t.Fatalf("err = %T(%v), want an AdmissionError from the untouched constructor default", err, err)
	}
}

func TestRunWithEvidenceAdmissionDisabledRestoresLenientMode(t *testing.T) {
	backing := store.NuevoStore(t.TempDir())
	transport := NewDurableTransport(backing, store.RunPolicy{ID: "policy:cutover"}, "cutover:lenient",
		nil, WithEvidenceAdmission(false))
	reviewer := &scriptedReviewer{name: "dimension-logic", output: "raw verdict"}

	output, evidence, err := transport.Run(reviewer, "quality/logic", "the prompt")
	if err != nil {
		t.Fatalf("Run() error = %v, want lenient mode to admit despite the divergent-snapshot sha that strict mode rejects", err)
	}
	want := "the prompt|cutover:lenient||raw verdict"
	if output != want {
		t.Fatalf("output = %q, want %q byte-compatible with the pre-R6 adapter echo", output, want)
	}
	if evidence != (Evidence{}) {
		t.Fatalf("evidence = %+v, want zero Evidence exactly like pre-R6 behavior", evidence)
	}
}

func TestRunWithEvidenceAdmissionDisabledStillRecordsInspectableRuns(t *testing.T) {
	backing := store.NuevoStore(t.TempDir())
	transport := NewDurableTransport(backing, store.RunPolicy{ID: "policy:cutover"},
		"cutover-observable", nil, WithEvidenceAdmission(false))
	reviewer := silentReviewer{name: "dimension-style"}

	output, _, err := transport.Run(reviewer, "style/design", "the prompt")
	if err != nil || output != "" {
		t.Fatalf("Run() = %q, %v, want a successful empty-output admission in lenient mode", output, err)
	}
	runIDs, err := backing.ListExecutionIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(runIDs) != 1 {
		t.Fatalf("execution ids = %v, want one inspectable durable run", runIDs)
	}
	outcomes, err := backing.ReadAttemptOutcomes(runIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) == 0 || outcomes[len(outcomes)-1].Class != agentrun.OutcomeSuccess {
		t.Fatalf("outcomes = %+v, want the terminal success outcome still recorded while admission is off", outcomes)
	}
}
