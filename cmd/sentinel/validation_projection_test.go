package main

import (
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

func TestProjectValidationFindingsParsesCompilerStyleLocations(t *testing.T) {
	findings := []validation.Finding{{
		Source:     "validation",
		Severity:   "CRITICAL",
		Capability: "lint",
		Command:    "go vet ./...",
		Evidence:   "config.go:12:5: unreachable code\nother.go:7: unused import",
	}}

	projected := projectValidationFindings(findings)
	if len(projected) != 2 {
		t.Fatalf("projected = %d, expected 2: %#v", len(projected), projected)
	}
	if projected[0].Location != (review.Location{File: "config.go", LineStart: 12}) {
		t.Errorf("projected[0].Location = %#v, expected config.go:12", projected[0].Location)
	}
	if projected[1].Location != (review.Location{File: "other.go", LineStart: 7}) {
		t.Errorf("projected[1].Location = %#v, expected other.go:7", projected[1].Location)
	}
	for _, p := range projected {
		if p.Source != review.SourceValidation || p.Confidence != 1.0 {
			t.Errorf("finding = %#v, expected Source=validation Confidence=1.0", p)
		}
		if p.Dimension != review.DimStyle {
			t.Errorf("finding.Dimension = %q, expected %q for capability 'lint'", p.Dimension, review.DimStyle)
		}
	}
}

func TestProjectValidationFindingsLeavesDimensionEmptyForUnknownCapability(t *testing.T) {
	// A capability outside the conservative map (e.g. a test run, which
	// executes branch-controlled code and could print an arbitrary file
	// path to try to fake supersession) must never get a Dimension, so
	// review.SupersedeDeterministicFindings can never use it to discard an
	// unrelated semantic finding.
	findings := []validation.Finding{{
		Capability: "unit_test", Command: "go test ./...", Severity: "CRITICAL",
		Evidence: "auth.go:10: assertion failed",
	}}

	projected := projectValidationFindings(findings)
	if len(projected) != 1 {
		t.Fatalf("projected = %d, expected 1: %#v", len(projected), projected)
	}
	if projected[0].Dimension != "" {
		t.Errorf("finding.Dimension = %q, expected empty for an unmapped capability", projected[0].Dimension)
	}
}

func TestProjectValidationFindingsBareFilePathCoversWholeFile(t *testing.T) {
	findings := []validation.Finding{{
		Capability: "format", Command: "gofmt -l .", Severity: "CRITICAL",
		Evidence: "config.go\nother.go",
	}}

	projected := projectValidationFindings(findings)
	if len(projected) != 2 {
		t.Fatalf("projected = %d, expected 2: %#v", len(projected), projected)
	}
	for i, file := range []string{"config.go", "other.go"} {
		if projected[i].Location != (review.Location{File: file}) {
			t.Errorf("projected[%d].Location = %#v, expected %s with no line", i, projected[i].Location, file)
		}
	}
}

func TestProjectValidationFindingsKeepsUnrecognizableEvidenceWithoutLocation(t *testing.T) {
	findings := []validation.Finding{{
		Capability: "unit_test", Command: "go test ./...", Severity: "CRITICAL",
		Evidence: "--- FAIL: TestSomething (0.00s)\nassertion failed",
	}}

	projected := projectValidationFindings(findings)
	if len(projected) != 1 {
		t.Fatalf("projected = %d, expected 1 (evidence kept without a location): %#v", len(projected), projected)
	}
	if projected[0].Location.File != "" {
		t.Errorf("projected[0].Location = %#v, expected no location", projected[0].Location)
	}
	if projected[0].Evidence == "" {
		t.Error("projected[0].Evidence is empty; the real evidence was expected to be preserved")
	}
}
