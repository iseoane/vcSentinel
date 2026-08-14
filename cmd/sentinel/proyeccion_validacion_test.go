package main

import (
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

func TestProyectarHallazgosValidacionParsesCompilerStyleLocations(t *testing.T) {
	hallazgos := []validation.Hallazgo{{
		Source:     "validation",
		Severity:   "CRITICAL",
		Capability: "lint",
		Comando:    "go vet ./...",
		Evidencia:  "config.go:12:5: unreachable code\nother.go:7: unused import",
	}}

	proyectados := proyectarHallazgosValidacion(hallazgos)
	if len(proyectados) != 2 {
		t.Fatalf("proyectados = %d, expected 2: %#v", len(proyectados), proyectados)
	}
	if proyectados[0].Location != (review.Ubicacion{Archivo: "config.go", LineaInicio: 12}) {
		t.Errorf("proyectados[0].Location = %#v, expected config.go:12", proyectados[0].Location)
	}
	if proyectados[1].Location != (review.Ubicacion{Archivo: "other.go", LineaInicio: 7}) {
		t.Errorf("proyectados[1].Location = %#v, expected other.go:7", proyectados[1].Location)
	}
	for _, p := range proyectados {
		if p.Source != review.SourceValidation || p.Confidence != 1.0 {
			t.Errorf("hallazgo = %#v, expected Source=validation Confidence=1.0", p)
		}
		if p.Dimension != review.DimStyle {
			t.Errorf("hallazgo.Dimension = %q, expected %q for capability 'lint'", p.Dimension, review.DimStyle)
		}
	}
}

func TestProyectarHallazgosValidacionLeavesDimensionEmptyForUnknownCapability(t *testing.T) {
	// A capability outside the conservative map (e.g. a test run, which
	// executes branch-controlled code and could print an arbitrary file
	// path to try to fake supersession) must never get a Dimension, so
	// review.SupersedeDeterministicFindings can never use it to discard an
	// unrelated semantic finding.
	hallazgos := []validation.Hallazgo{{
		Capability: "unit_test", Comando: "go test ./...", Severity: "CRITICAL",
		Evidencia: "auth.go:10: assertion failed",
	}}

	proyectados := proyectarHallazgosValidacion(hallazgos)
	if len(proyectados) != 1 {
		t.Fatalf("proyectados = %d, expected 1: %#v", len(proyectados), proyectados)
	}
	if proyectados[0].Dimension != "" {
		t.Errorf("hallazgo.Dimension = %q, expected empty for an unmapped capability", proyectados[0].Dimension)
	}
}

func TestProyectarHallazgosValidacionBareFilePathCoversWholeFile(t *testing.T) {
	hallazgos := []validation.Hallazgo{{
		Capability: "format", Comando: "gofmt -l .", Severity: "CRITICAL",
		Evidencia: "config.go\nother.go",
	}}

	proyectados := proyectarHallazgosValidacion(hallazgos)
	if len(proyectados) != 2 {
		t.Fatalf("proyectados = %d, expected 2: %#v", len(proyectados), proyectados)
	}
	for i, archivo := range []string{"config.go", "other.go"} {
		if proyectados[i].Location != (review.Ubicacion{Archivo: archivo}) {
			t.Errorf("proyectados[%d].Location = %#v, expected %s with no line", i, proyectados[i].Location, archivo)
		}
	}
}

func TestProyectarHallazgosValidacionKeepsUnrecognizableEvidenceWithoutLocation(t *testing.T) {
	hallazgos := []validation.Hallazgo{{
		Capability: "unit_test", Comando: "go test ./...", Severity: "CRITICAL",
		Evidencia: "--- FAIL: TestSomething (0.00s)\nassertion failed",
	}}

	proyectados := proyectarHallazgosValidacion(hallazgos)
	if len(proyectados) != 1 {
		t.Fatalf("proyectados = %d, expected 1 (evidence kept without a location): %#v", len(proyectados), proyectados)
	}
	if proyectados[0].Location.Archivo != "" {
		t.Errorf("proyectados[0].Location = %#v, expected no location", proyectados[0].Location)
	}
	if proyectados[0].Evidence == "" {
		t.Error("proyectados[0].Evidence vacía, se esperaba preservar la evidencia real")
	}
}
