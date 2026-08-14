package review

import "testing"

func TestSupersedeDeterministicFindingsRemovesOverlappingSemanticFinding(t *testing.T) {
	semantic := []Hallazgo{{
		Source:   SourceReview,
		Location: Ubicacion{Archivo: "config.go", LineaInicio: 12, LineaFin: 14},
	}}
	deterministic := []Hallazgo{{
		Source:   SourceValidation,
		Location: Ubicacion{Archivo: "config.go", LineaInicio: 13},
	}}

	kept := SupersedeDeterministicFindings(semantic, deterministic)
	if len(kept) != 0 {
		t.Fatalf("kept = %#v, expected the semantic finding to be superseded", kept)
	}
}

func TestSupersedeDeterministicFindingsKeepsSemanticFindingInAnotherFile(t *testing.T) {
	semantic := []Hallazgo{{
		Source:   SourceReview,
		Location: Ubicacion{Archivo: "other.go", LineaInicio: 12},
	}}
	deterministic := []Hallazgo{{
		Source:   SourceValidation,
		Location: Ubicacion{Archivo: "config.go", LineaInicio: 12},
	}}

	kept := SupersedeDeterministicFindings(semantic, deterministic)
	if len(kept) != 1 {
		t.Fatalf("kept = %#v, expected the semantic finding to survive", kept)
	}
}

func TestSupersedeDeterministicFindingsKeepsSemanticFindingOutsideDeterministicRange(t *testing.T) {
	semantic := []Hallazgo{{
		Source:   SourceReview,
		Location: Ubicacion{Archivo: "config.go", LineaInicio: 20},
	}}
	deterministic := []Hallazgo{{
		Source:   SourceValidation,
		Location: Ubicacion{Archivo: "config.go", LineaInicio: 12, LineaFin: 14},
	}}

	kept := SupersedeDeterministicFindings(semantic, deterministic)
	if len(kept) != 1 {
		t.Fatalf("kept = %#v, expected the semantic finding to survive", kept)
	}
}

func TestSupersedeDeterministicFindingsWholeFileDeterministicCoversEveryLine(t *testing.T) {
	semantic := []Hallazgo{{
		Source:   SourceReview,
		Location: Ubicacion{Archivo: "config.go", LineaInicio: 400},
	}}
	deterministic := []Hallazgo{{
		Source:   SourceValidation,
		Location: Ubicacion{Archivo: "config.go"}, // e.g. gofmt -l: no line, whole file
	}}

	kept := SupersedeDeterministicFindings(semantic, deterministic)
	if len(kept) != 0 {
		t.Fatalf("kept = %#v, expected a whole-file deterministic finding to supersede any line", kept)
	}
}

func TestSupersedeDeterministicFindingsNoOpWithoutDeterministicFindings(t *testing.T) {
	semantic := []Hallazgo{{Source: SourceReview, Location: Ubicacion{Archivo: "config.go", LineaInicio: 12}}}

	kept := SupersedeDeterministicFindings(semantic, nil)
	if len(kept) != 1 || kept[0] != semantic[0] {
		t.Fatalf("kept = %#v, expected the semantic finding untouched", kept)
	}
}
