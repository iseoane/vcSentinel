package review

import "testing"

func TestSupersedeDeterministicFindingsRemovesOverlappingSemanticFindingInSameDimension(t *testing.T) {
	semantic := []Finding{{
		Source:    SourceReview,
		Dimension: DimStyle,
		Location:  Location{File: "config.go", LineStart: 12, LineEnd: 14},
	}}
	deterministic := []Finding{{
		Source:    SourceValidation,
		Dimension: DimStyle,
		Location:  Location{File: "config.go", LineStart: 13},
	}}

	kept := SupersedeDeterministicFindings(semantic, deterministic)
	if len(kept) != 0 {
		t.Fatalf("kept = %#v, expected the semantic finding to be superseded", kept)
	}
}

func TestSupersedeDeterministicFindingsKeepsUnrelatedDimensionAtSameLocation(t *testing.T) {
	// Same file and overlapping line as a formatting failure, but a security
	// finding: a linter catching a formatting issue must never discard an
	// unrelated semantic finding just because they share a spot.
	semantic := []Finding{{
		Source:    SourceReview,
		Dimension: DimSecurity,
		Location:  Location{File: "config.go", LineStart: 12, LineEnd: 14},
	}}
	deterministic := []Finding{{
		Source:    SourceValidation,
		Dimension: DimStyle,
		Location:  Location{File: "config.go", LineStart: 13},
	}}

	kept := SupersedeDeterministicFindings(semantic, deterministic)
	if len(kept) != 1 {
		t.Fatalf("kept = %#v, expected the unrelated-dimension finding to survive", kept)
	}
}

func TestSupersedeDeterministicFindingsIgnoresDeterministicWithoutDimension(t *testing.T) {
	// A caller that cannot map its validation capability to a semantic
	// dimension must leave Dimension empty; an empty Dimension must never
	// act as a wildcard that supersedes everything.
	semantic := []Finding{{
		Source:    SourceReview,
		Dimension: DimStyle,
		Location:  Location{File: "config.go", LineStart: 12},
	}}
	deterministic := []Finding{{
		Source:   SourceValidation,
		Location: Location{File: "config.go", LineStart: 12},
	}}

	kept := SupersedeDeterministicFindings(semantic, deterministic)
	if len(kept) != 1 {
		t.Fatalf("kept = %#v, expected a dimensionless deterministic finding to supersede nothing", kept)
	}
}

func TestSupersedeDeterministicFindingsIgnoresNonReviewSourceFindings(t *testing.T) {
	// A finding whose Source is not SourceReview (e.g. already
	// SourceValidation itself) must survive regardless of location overlap:
	// the guarantee is scoped to semantic findings, not everything passed in.
	semantic := []Finding{{
		Source:    SourceValidation,
		Dimension: DimStyle,
		Location:  Location{File: "config.go", LineStart: 12},
	}}
	deterministic := []Finding{{
		Source:    SourceValidation,
		Dimension: DimStyle,
		Location:  Location{File: "config.go", LineStart: 12},
	}}

	kept := SupersedeDeterministicFindings(semantic, deterministic)
	if len(kept) != 1 {
		t.Fatalf("kept = %#v, expected the non-review finding untouched", kept)
	}
}

func TestSupersedeDeterministicFindingsKeepsSemanticFindingInAnotherFile(t *testing.T) {
	semantic := []Finding{{
		Source:    SourceReview,
		Dimension: DimStyle,
		Location:  Location{File: "other.go", LineStart: 12},
	}}
	deterministic := []Finding{{
		Source:    SourceValidation,
		Dimension: DimStyle,
		Location:  Location{File: "config.go", LineStart: 12},
	}}

	kept := SupersedeDeterministicFindings(semantic, deterministic)
	if len(kept) != 1 {
		t.Fatalf("kept = %#v, expected the semantic finding to survive", kept)
	}
}

func TestSupersedeDeterministicFindingsNormalizesEquivalentPathSpellings(t *testing.T) {
	semantic := []Finding{{
		Source:    SourceReview,
		Dimension: DimStyle,
		Location:  Location{File: "./config.go", LineStart: 12},
	}}
	deterministic := []Finding{{
		Source:    SourceValidation,
		Dimension: DimStyle,
		Location:  Location{File: "config.go", LineStart: 12},
	}}

	kept := SupersedeDeterministicFindings(semantic, deterministic)
	if len(kept) != 0 {
		t.Fatalf("kept = %#v, expected equivalent path spellings to be treated as the same file", kept)
	}
}

func TestSupersedeDeterministicFindingsKeepsSemanticFindingOutsideDeterministicRange(t *testing.T) {
	semantic := []Finding{{
		Source:    SourceReview,
		Dimension: DimStyle,
		Location:  Location{File: "config.go", LineStart: 20},
	}}
	deterministic := []Finding{{
		Source:    SourceValidation,
		Dimension: DimStyle,
		Location:  Location{File: "config.go", LineStart: 12, LineEnd: 14},
	}}

	kept := SupersedeDeterministicFindings(semantic, deterministic)
	if len(kept) != 1 {
		t.Fatalf("kept = %#v, expected the semantic finding to survive", kept)
	}
}

func TestSupersedeDeterministicFindingsWholeFileDeterministicCoversEveryLineInSameDimension(t *testing.T) {
	semantic := []Finding{{
		Source:    SourceReview,
		Dimension: DimStyle,
		Location:  Location{File: "config.go", LineStart: 400},
	}}
	deterministic := []Finding{{
		Source:    SourceValidation,
		Dimension: DimStyle,
		Location:  Location{File: "config.go"}, // e.g. gofmt -l: no line, whole file
	}}

	kept := SupersedeDeterministicFindings(semantic, deterministic)
	if len(kept) != 0 {
		t.Fatalf("kept = %#v, expected a whole-file deterministic finding to supersede any line in the same dimension", kept)
	}
}

func TestSupersedeDeterministicFindingsNoOpWithoutDeterministicFindings(t *testing.T) {
	semantic := []Finding{{Source: SourceReview, Dimension: DimStyle, Location: Location{File: "config.go", LineStart: 12}}}

	kept := SupersedeDeterministicFindings(semantic, nil)
	if len(kept) != 1 || kept[0] != semantic[0] {
		t.Fatalf("kept = %#v, expected the semantic finding untouched", kept)
	}
}
