package review

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestParseDimensionResultClean(t *testing.T) {
	output := `{"dim":"logic","verdict":"warn","findings":[{"dimension":"logic","file":"internal/a.go","line":10,"severity":"WARNING","description":"condición redundante","suggestion":"simplifica"}]}`
	result, err := ParseDimensionResult(output)
	if err != nil {
		t.Fatalf("ParseDimensionResult returned error: %v", err)
	}
	if result.Dim != DimLogic {
		t.Errorf("dim = %q, want %q", result.Dim, DimLogic)
	}
	if result.Verdict != VerdictWarn {
		t.Errorf("verdict = %q, want %q", result.Verdict, VerdictWarn)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(result.Findings))
	}
	if result.Findings[0].Severity != SevWarning {
		t.Errorf("severity = %q, want %q", result.Findings[0].Severity, SevWarning)
	}
}

func TestParseDimensionResultWithFencesAndText(t *testing.T) {
	output := "Analizando el diff...\n```json\nBEGIN_REVIEW\n{\"dim\":\"security\",\"verdict\":\"ok\"}\nEND_REVIEW\n```\nFin"
	result, err := ParseDimensionResult(output)
	if err != nil {
		t.Fatalf("ParseDimensionResult returned error: %v", err)
	}
	if result.Dim != DimSecurity || result.Verdict != VerdictOK {
		t.Errorf("want security/ok, got %s/%s", result.Dim, result.Verdict)
	}
}

func TestParseDimensionResultWithoutDelimiters(t *testing.T) {
	output := "{\"dim\":\"design\",\"verdict\":\"warn\",\"findings\":[]}\n"
	result, err := ParseDimensionResult(output)
	if err != nil {
		t.Fatalf("ParseDimensionResult returned error: %v", err)
	}
	if result.Dim != DimDesign || result.Verdict != VerdictWarn {
		t.Errorf("want design/warn, got %s/%s", result.Dim, result.Verdict)
	}
}

func TestParseDimensionResultEmpty(t *testing.T) {
	_, err := ParseDimensionResult("")
	if !errors.Is(err, ErrEmptyOutput) {
		t.Errorf("want %v, got %v", ErrEmptyOutput, err)
	}
}

func TestParseDimensionResultInvalid(t *testing.T) {
	_, err := ParseDimensionResult("esto no es json\nBEGIN_REVIEW\ntampoco\nEND_REVIEW\n")
	if !errors.Is(err, ErrInvalidJSONL) {
		t.Errorf("want %v, got %v", ErrInvalidJSONL, err)
	}
}

func TestParseDimensionResultClassifiesDeterministicOutputErrors(t *testing.T) {
	longSecret := strings.Repeat("x", 300)
	tests := []struct {
		name     string
		output   string
		class    SemanticOutputClass
		legacy   error
		redacted string
	}{
		{name: "empty payload", output: "", class: SemanticOutputMissingPayload, legacy: ErrEmptyOutput},
		{name: "prose payload", output: "I cannot provide the requested review.", class: SemanticOutputMissingPayload, legacy: ErrInvalidJSONL},
		{name: "tool denial prose", output: "Permission denied: Read(/host/private.go)", class: SemanticOutputToolDenied, legacy: ErrInvalidJSONL},
		{name: "malformed JSON", output: `{"dim":"logic",`, class: SemanticOutputMalformedJSON, legacy: ErrInvalidJSONL},
		{name: "schema invalid result", output: `{"dim":"logic","verdict":false}`, class: SemanticOutputSchemaInvalid, legacy: ErrInvalidJSONL},
		{name: "redacts and bounds excerpt", output: "tool denied token=" + longSecret, class: SemanticOutputToolDenied, legacy: ErrInvalidJSONL, redacted: longSecret},
		{name: "redacts quoted json secret", output: `tool denied {"token":"s3cr3t-value"}`, class: SemanticOutputToolDenied, legacy: ErrInvalidJSONL, redacted: "s3cr3t-value"},
		{name: "redacts quoted spaced json secret", output: `access denied {"api_key": "AKIA-EXAMPLE", "detail": "read"}`, class: SemanticOutputToolDenied, legacy: ErrInvalidJSONL, redacted: "AKIA-EXAMPLE"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseDimensionResult(tt.output)
			if !errors.Is(err, tt.legacy) {
				t.Fatalf("errors.Is(%v, %v) = false", err, tt.legacy)
			}
			var outputErr *SemanticOutputError
			if !errors.As(err, &outputErr) {
				t.Fatalf("error = %T %v, expected SemanticOutputError", err, err)
			}
			if outputErr.Class != tt.class {
				t.Errorf("class = %q, expected %q", outputErr.Class, tt.class)
			}
			if len([]rune(outputErr.Evidence)) > maxSemanticOutputEvidenceRunes {
				t.Errorf("evidence length = %d, expected at most %d", len([]rune(outputErr.Evidence)), maxSemanticOutputEvidenceRunes)
			}
			if tt.redacted != "" && strings.Contains(outputErr.Evidence, tt.redacted) {
				t.Errorf("evidence leaks the supplied secret: %q", outputErr.Evidence)
			}
		})
	}
}

func TestParseDimensionResultUnknownDimension(t *testing.T) {
	_, err := ParseDimensionResult(`{"dim":"perf","verdict":"ok"}`)
	if !errors.Is(err, ErrInvalidDimension) {
		t.Errorf("want %v, got %v", ErrInvalidDimension, err)
	}
	if err == nil || !strings.Contains(err.Error(), "perf") {
		t.Errorf("the error should name the unknown dimension, got %v", err)
	}
}

func TestParseDimensionResultDeFactoVerdictWithFindings(t *testing.T) {
	// The real agent sometimes returns "issues" as the verdict with findings:
	// with findings it is derived from the severities, without findings it is
	// an error.
	output := `{"dim":"logic","verdict":"issues","findings":[{"dimension":"logic","file":"a.go","line":1,"severity":"WARNING","description":"d"}]}`
	result, err := ParseDimensionResult(output)
	if err != nil {
		t.Fatalf("ParseDimensionResult returned error: %v", err)
	}
	if result.Verdict != VerdictWarn {
		t.Errorf("verdict = %q, want %q derived from WARNING", result.Verdict, VerdictWarn)
	}
	if len(result.Warnings) == 0 {
		t.Error("want a warning for the verdict normalization")
	}

	if _, err := ParseDimensionResult(`{"dim":"logic","verdict":"issues"}`); !errors.Is(err, ErrInvalidVerdict) {
		t.Errorf("a de facto verdict without findings should be %v, got %v", ErrInvalidVerdict, err)
	}
}

func TestParseDimensionResultOkWithCriticalRaisesToBlock(t *testing.T) {
	output := `{"dim":"security","verdict":"ok","findings":[{"dimension":"security","file":"a.go","line":2,"severity":"CRITICAL","description":"secreto expuesto"}]}`
	result, err := ParseDimensionResult(output)
	if err != nil {
		t.Fatalf("ParseDimensionResult returned error: %v", err)
	}
	if result.Verdict != VerdictBlock {
		t.Errorf("verdict = %q, want %q (findings win)", result.Verdict, VerdictBlock)
	}
}

func TestParseDimensionResultOkWithWarningRaisesToWarn(t *testing.T) {
	output := `{"dim":"style","verdict":"ok","findings":[{"dimension":"style","file":"a.go","line":3,"severity":"ADVISORY","description":"nombre confuso"}]}`
	result, err := ParseDimensionResult(output)
	if err != nil {
		t.Fatalf("ParseDimensionResult returned error: %v", err)
	}
	if result.Verdict != VerdictWarn {
		t.Errorf("verdict = %q, want %q (ADVISORY present)", result.Verdict, VerdictWarn)
	}
}

func TestParseDimensionResultQuestionIsRespected(t *testing.T) {
	output := `{"dim":"logic","verdict":"question","questions":[{"id":"Q1","text":"¿X?"}]}`
	result, err := ParseDimensionResult(output)
	if err != nil {
		t.Fatalf("ParseDimensionResult returned error: %v", err)
	}
	if result.Verdict != VerdictQuestion {
		t.Errorf("verdict = %q, want %q", result.Verdict, VerdictQuestion)
	}
}

func TestParseDimensionResultUnknownSeverity(t *testing.T) {
	output := `{"dim":"logic","verdict":"warn","findings":[{"dimension":"logic","file":"a.go","line":1,"severity":"FATAL","description":"d"}]}`
	result, err := ParseDimensionResult(output)
	if err != nil {
		t.Fatalf("ParseDimensionResult returned error: %v", err)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(result.Findings))
	}
	if result.Findings[0].Severity != SevAdvisory {
		t.Errorf("severity = %q, want normalized to %q", result.Findings[0].Severity, SevAdvisory)
	}
	if len(result.Warnings) == 0 {
		t.Error("want a warning for the normalization")
	}
}

func TestParseDimensionResultDiscardsJunkLines(t *testing.T) {
	// Lines that do not even decode as JSON are discarded; the valid line
	// wins. (A valid JSON line with an unknown dimension, on the other hand,
	// aborts with an explicit error: TestParseDimensionResultUnknownDimension.)
	output := "texto del agente sin sentido\n```\n{\"dim\":\"tests\",\"verdict\":\"ok\"}\n```\n"
	result, err := ParseDimensionResult(output)
	if err != nil {
		t.Fatalf("ParseDimensionResult returned error: %v", err)
	}
	if result.Dim != DimTests || result.Verdict != VerdictOK {
		t.Errorf("want tests/ok, got %s/%s", result.Dim, result.Verdict)
	}
	// The text line and the fence do not decode: they must be recorded.
	foundWarning := false
	for _, warning := range result.Warnings {
		if len(warning) > 0 {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Errorf("want a warning for the discarded lines, warnings = %v", result.Warnings)
	}
}

func TestParseDimensionResultQuestions(t *testing.T) {
	output := `{"dim":"logic","verdict":"question","questions":[{"id":"Q1","text":"¿El rebase debe abortar si hay cambios sin commitear?"}]}`
	result, err := ParseDimensionResult(output)
	if err != nil {
		t.Fatalf("ParseDimensionResult returned error: %v", err)
	}
	if result.Verdict != VerdictQuestion {
		t.Errorf("verdict = %q, want %q", result.Verdict, VerdictQuestion)
	}
	if len(result.Questions) != 1 || result.Questions[0].ID != "Q1" {
		t.Errorf("questions = %+v, want one with ID Q1", result.Questions)
	}
}

func TestFindingSerializationRoundTrip(t *testing.T) {
	original := Finding{
		ID:     "h-1",
		Source: SourceReview,
		Producer: Producer{
			Agent:         "claude",
			Binary:        "",
			Model:         "claude-sonnet-5",
			Effort:        "high",
			ModelVerified: true,
		},
		Dimension:   DimLogic,
		Severity:    SevCritical,
		Confidence:  0.87,
		Status:      StatusPending,
		Title:       "always-true condition",
		Description: "the conditional never evaluates to false because of the operator used",
		Location: Location{
			File:      "internal/a.go",
			Blob:      "deadbeef",
			LineStart: 10,
			LineEnd:   12,
			Simbolo:   "FuncionX",
		},
		Evidence:       "if x >= 0 || x < 0 {",
		Impact:         "the error branch never executes",
		Recommendation: "use a single comparison operator",
		Fixable:        FixableNeedsReview,
		IntroducedBy:   "abc123",
		Fingerprint:    "sha256:xyz",
	}

	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}

	var rebuilt Finding
	if err := json.Unmarshal(raw, &rebuilt); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}

	if rebuilt != original {
		t.Errorf("the finding did not survive the full round trip:\noriginal:      %+v\nrebuilt:  %+v", original, rebuilt)
	}
}

func TestFindingCoexistsWithReviewFindingV1InSamePackage(t *testing.T) {
	// A v1 record must keep deserializing without error in the same package
	// that already defines the new Finding type (v2): both coexist without
	// the existence of v2 breaking the reading of v1.
	rawV1 := `{"dimension":"logic","file":"a.go","line":5,"severity":"WARNING","description":"d","suggestion":"s"}`
	var findingV1 ReviewFinding
	if err := json.Unmarshal([]byte(rawV1), &findingV1); err != nil {
		t.Fatalf("Unmarshal of ReviewFinding v1 returned error: %v", err)
	}
	if findingV1.Dimension != DimLogic || findingV1.Line != 5 {
		t.Errorf("v1 finding badly deserialized: %+v", findingV1)
	}

	rawV2 := `{"id":"h-2","source":"validation","producer":{"agent":"validation","model_verified":false},"dimension":"tests","severity":"CRITICAL","confidence":1.0,"status":"pending","title":"t","description":"d","location":{"file":"b.go","line_start":1},"evidence":"e","fixable":"manual","fingerprint":"f"}`
	var findingV2 Finding
	if err := json.Unmarshal([]byte(rawV2), &findingV2); err != nil {
		t.Fatalf("Unmarshal of Finding v2 returned error: %v", err)
	}
	if findingV2.Source != SourceValidation || findingV2.Confidence != 1.0 {
		t.Errorf("v2 finding badly deserialized: %+v", findingV2)
	}
}

// findingWithEvidence builds a minimal valid Finding except for
// Evidence/Location.File, which each test adjusts to what it wants to probe.
func findingWithEvidence(id, file, evidence string) Finding {
	return Finding{
		ID:        id,
		Source:    SourceReview,
		Dimension: DimLogic,
		Severity:  SevWarning,
		Status:    StatusPending,
		Location:  Location{File: file},
		Evidence:  evidence,
		Fixable:   FixableManual,
	}
}

func TestFindingsWithValidEvidenceInventedEvidenceIsDiscarded(t *testing.T) {
	read := func(file string) (string, error) {
		return "func Real() {\n\treturn nil\n}\n", nil
	}
	h := findingWithEvidence("h-1", "a.go", "esto no aparece en ningún lado")

	valid, dismissals := FindingsWithValidEvidence([]Finding{h}, read)

	if len(valid) != 0 {
		t.Errorf("valid = %d, want 0 (invented evidence)", len(valid))
	}
	if len(dismissals) != 1 || dismissals[0].Reason != ReasonEvidenceNotFound {
		t.Errorf("dismissals = %+v, want 1 with reason %q", dismissals, ReasonEvidenceNotFound)
	}
}

func TestFindingsWithValidEvidenceToleratesReindentation(t *testing.T) {
	// The real content has the line with an indentation different from the
	// one the LLM cited: the normalization (edge trim) must tolerate it.
	read := func(file string) (string, error) {
		return "func F() {\n        if x >= 0 || x < 0 {\n            return true\n        }\n}\n", nil
	}
	h := findingWithEvidence("h-2", "a.go", "if x >= 0 || x < 0 {")

	valid, dismissals := FindingsWithValidEvidence([]Finding{h}, read)

	if len(dismissals) != 0 {
		t.Errorf("dismissals = %+v, want none (only the indentation changed)", dismissals)
	}
	if len(valid) != 1 || valid[0].ID != "h-2" {
		t.Errorf("valid = %+v, want [h-2]", valid)
	}
}

func TestFindingsWithValidEvidenceDiscardsOnlyTheInvalidOne(t *testing.T) {
	contentA := "package a\n\nfunc A() {\n\treturn\n}\n"
	read := func(file string) (string, error) {
		switch file {
		case "a.go":
			return contentA, nil
		default:
			return "", errors.New("file not found in the commit")
		}
	}

	valid1 := findingWithEvidence("h-ok-1", "a.go", "func A() {")
	invalid := findingWithEvidence("h-bad", "a.go", "esto jamás apareció")
	valid2 := findingWithEvidence("h-ok-2", "a.go", "package a")

	valid, dismissals := FindingsWithValidEvidence([]Finding{valid1, invalid, valid2}, read)

	if len(valid) != 2 {
		t.Fatalf("valid = %d, want 2 (both correct ones survive)", len(valid))
	}
	if valid[0].ID != "h-ok-1" || valid[1].ID != "h-ok-2" {
		t.Errorf("valid = %+v, want [h-ok-1, h-ok-2] in order", valid)
	}
	if len(dismissals) != 1 || dismissals[0].Finding.ID != "h-bad" {
		t.Errorf("dismissals = %+v, want only h-bad", dismissals)
	}
}

func TestFindingsWithValidEvidenceEmptyEvidence(t *testing.T) {
	read := func(file string) (string, error) { return "content", nil }
	h := findingWithEvidence("h-3", "a.go", "")

	valid, dismissals := FindingsWithValidEvidence([]Finding{h}, read)

	if len(valid) != 0 {
		t.Errorf("valid = %d, want 0 (empty evidence)", len(valid))
	}
	if len(dismissals) != 1 || dismissals[0].Reason != ReasonNoEvidence {
		t.Errorf("dismissals = %+v, want 1 with reason %q", dismissals, ReasonNoEvidence)
	}
}

func TestFindingsWithValidEvidenceUnresolvedFileDoesNotPropagateError(t *testing.T) {
	// The content read fails (file renamed/deleted/mistyped path): the
	// finding is dismissed, but the function must not propagate the error
	// nor panic.
	read := func(file string) (string, error) {
		return "", errors.New("the file does not exist in that commit")
	}
	h := findingWithEvidence("h-4", "no-existe.go", "algo")

	valid, dismissals := FindingsWithValidEvidence([]Finding{h}, read)

	if len(valid) != 0 {
		t.Errorf("valid = %d, want 0 (unresolved file)", len(valid))
	}
	if len(dismissals) != 1 || dismissals[0].Reason != ReasonFileUnresolved {
		t.Errorf("dismissals = %+v, want 1 with reason %q", dismissals, ReasonFileUnresolved)
	}
}

func TestFindingsWithValidEvidenceFileEmptyInLocation(t *testing.T) {
	read := func(file string) (string, error) { return "content", nil }
	h := findingWithEvidence("h-5", "", "algo")

	_, dismissals := FindingsWithValidEvidence([]Finding{h}, read)

	if len(dismissals) != 1 || dismissals[0].Reason != ReasonFileUnresolved {
		t.Errorf("dismissals = %+v, want 1 with reason %q", dismissals, ReasonFileUnresolved)
	}
}

// fingerprintFixture builds a minimal Finding with exactly the fields that
// intervene in Fingerprint, leaving the rest at their zero value: the tests
// in this section only vary Simbolo/File, Evidence, Title and the lines.
func fingerprintFixture(dimension, symbol, file, evidence, title string, lineStart, lineEnd int) Finding {
	return Finding{
		Dimension: dimension,
		Title:     title,
		Evidence:  evidence,
		Location: Location{
			File:      file,
			Simbolo:   symbol,
			LineStart: lineStart,
			LineEnd:   lineEnd,
		},
	}
}

func TestFingerprintStableAcrossReindentationAndLineChange(t *testing.T) {
	// Same logical finding, but the LLM cited it with a different
	// indentation and at other lines (e.g. the file was reformatted between
	// two runs): the fingerprint must not depend on edge whitespace nor on
	// the line.
	a := fingerprintFixture(DimLogic, "FuncionX", "a.go",
		"if x >= 0 || x < 0 {", "always-true condition", 10, 12)
	b := fingerprintFixture(DimLogic, "FuncionX", "a.go",
		"    if x >= 0 || x < 0 {   ", "always-true condition", 45, 47)

	if Fingerprint(a) != Fingerprint(b) {
		t.Errorf("different fingerprints under reindentation/renumbering: %q vs %q", Fingerprint(a), Fingerprint(b))
	}
}

func TestFingerprintStableAcrossRenumberingByEditElsewhereInFile(t *testing.T) {
	// The file grew or lost lines BEFORE the finding (e.g. an import was
	// added): the evidence and the symbol do not change, only the numbering.
	a := fingerprintFixture(DimSecurity, "ValidarToken", "auth.go",
		"if token == \"\" { return nil }", "missing input validation", 20, 20)
	b := fingerprintFixture(DimSecurity, "ValidarToken", "auth.go",
		"if token == \"\" { return nil }", "missing input validation", 63, 63)

	if Fingerprint(a) != Fingerprint(b) {
		t.Errorf("different fingerprints after pure renumbering: %q vs %q", Fingerprint(a), Fingerprint(b))
	}
}

func TestFingerprintDiffersForSameDefectInTwoSymbols(t *testing.T) {
	// Same defect (same evidence and rule) but copied/pasted into two
	// different functions: they are two instances of the defect, not the
	// same one.
	a := fingerprintFixture(DimLogic, "FuncionA", "a.go",
		"if x >= 0 || x < 0 {", "always-true condition", 1, 1)
	b := fingerprintFixture(DimLogic, "FuncionB", "a.go",
		"if x >= 0 || x < 0 {", "always-true condition", 1, 1)

	if Fingerprint(a) == Fingerprint(b) {
		t.Errorf("expected different fingerprints for different symbols, both %q", Fingerprint(a))
	}
}

func TestFingerprintDiffersForTwoDefectsInSameSymbol(t *testing.T) {
	// Same function, but two independent defects inside it: they must be
	// trackable separately across successive revisions.
	a := fingerprintFixture(DimLogic, "FuncionX", "a.go",
		"if x >= 0 || x < 0 {", "always-true condition", 1, 1)
	b := fingerprintFixture(DimLogic, "FuncionX", "a.go",
		"return nil // TODO", "error return not handled", 5, 5)

	if Fingerprint(a) == Fingerprint(b) {
		t.Errorf("expected different fingerprints for different defects, both %q", Fingerprint(a))
	}
}

func TestFingerprintUsesFileWhenNoSymbol(t *testing.T) {
	// Without a symbol (the agent does not always resolve it), the
	// fingerprint must fall back to Location.File instead of staying empty
	// or colliding with everything.
	a := fingerprintFixture(DimStyle, "", "b.go",
		"var x int", "unused variable", 1, 1)
	b := fingerprintFixture(DimStyle, "", "c.go",
		"var x int", "unused variable", 1, 1)

	if Fingerprint(a) == Fingerprint(b) {
		t.Errorf("expected different fingerprints for different files without a symbol, both %q", Fingerprint(a))
	}
}

func TestParseDimensionResultV1OnlyFindingStaysV1(t *testing.T) {
	// Today's contract: the agent sends only v1 fields inside "findings".
	// The parse output is the single v1 projection (ReviewFinding); the v2
	// parallel projection was removed from the parse pipeline — the engine
	// projects v1 into the durable v2 Finding shape exactly once.
	output := `{"dim":"logic","verdict":"warn","findings":[{"dimension":"logic","file":"a.go","line":10,"severity":"WARNING","description":"d","suggestion":"s"}]}`
	result, err := ParseDimensionResult(output)
	if err != nil {
		t.Fatalf("ParseDimensionResult returned error: %v", err)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("Findings = %d, want 1", len(result.Findings))
	}
	if result.Findings[0].Severity != SevWarning {
		t.Errorf("severity = %q, want %q", result.Findings[0].Severity, SevWarning)
	}
	if result.Findings[0].Description != "d" {
		t.Errorf("description = %q, want %q", result.Findings[0].Description, "d")
	}
}

// TestParseDimensionResultEmptyLocationUsesV1Fallback reproduces B16: an
// explicit "location": {} (present, but with no File and no Simbolo) must
// not disturb the v1 parse path, because that would reintroduce the
// Fingerprint collision across different files that the fallback exists to
// avoid. ReviewFinding carries no Location: the v2 Location fallback is
// applied later, by the engine's single projection.
func TestParseDimensionResultEmptyLocationUsesV1Fallback(t *testing.T) {
	output := `{"dim":"security","verdict":"warn","findings":[{"file":"a.go","line":7,"severity":"WARNING","description":"x","evidence":"y","location":{}}]}`
	result, err := ParseDimensionResult(output)
	if err != nil {
		t.Fatalf("ParseDimensionResult returned error: %v", err)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("Findings = %d, want 1", len(result.Findings))
	}
	h := result.Findings[0]
	if h.File != "a.go" || h.Line != 7 {
		t.Errorf("Findings[0] = %+v, want the v1 fallback (File=a.go, Line=7)", h)
	}
}

func TestParseDimensionResultMixedV1V2FieldsKeepsV1Finding(t *testing.T) {
	// A finding with v1 fields (file/line/severity/description) AND some v2
	// fields (evidence/confidence) decodes into the single v1 projection:
	// the parse pipeline keeps one output shape (ReviewFinding); the engine
	// projects it into the durable v2 Finding shape exactly once.
	output := `{"dim":"security","verdict":"warn","findings":[{"file":"a.go","line":7,"severity":"WARNING","description":"posible fuga","evidence":"token := req.Header.Get(\"X\")","confidence":0.6}]}`
	result, err := ParseDimensionResult(output)
	if err != nil {
		t.Fatalf("ParseDimensionResult returned error: %v", err)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("Findings = %d, want 1", len(result.Findings))
	}
	h := result.Findings[0]
	if h.File != "a.go" || h.Severity != SevWarning {
		t.Errorf("Findings[0] = %+v, want the v1 fields preserved", h)
	}
	if h.Dimension != DimSecurity {
		t.Errorf("Findings[0].Dimension = %q, want %q (the line's dimension)", h.Dimension, DimSecurity)
	}
}

// TestParseDimensionResultAcceptsCategoricalConfidence is a regression test
// for a real gate failure: the review prompt (T5.6) instructs the model to
// report confidence as "high"/"medium"/"low", but the parser only accepted a
// raw float. Any finding with a categorical confidence made the whole JSONL
// line fail json.Unmarshal, which made every line look invalid and produced
// ErrInvalidJSONL even though the model's output was well-formed. The
// categorical-to-float mapping is no longer observable on the v1 parse
// output; what this test pins is that a categorical confidence must not
// abort the parse and must not discard the finding.
func TestParseDimensionResultAcceptsCategoricalConfidence(t *testing.T) {
	cases := []struct {
		level string
	}{
		{"high"},
		{"medium"},
		{"low"},
	}
	for _, tc := range cases {
		output := `{"dim":"tests","verdict":"fail","findings":[{"dimension":"tests","file":"a_test.go","line":1,"severity":"WARNING","description":"d","evidence":"e","confidence":"` + tc.level + `"}]}`
		result, err := ParseDimensionResult(output)
		if err != nil {
			t.Fatalf("level %q: ParseDimensionResult returned error: %v", tc.level, err)
		}
		if len(result.Findings) != 1 {
			t.Fatalf("level %q: Findings = %d, want 1", tc.level, len(result.Findings))
		}
		if result.Findings[0].Severity != SevWarning {
			t.Errorf("level %q: severity = %q, want %q", tc.level, result.Findings[0].Severity, SevWarning)
		}
	}
}

func TestParseDimensionResultRejectsUnknownConfidenceLevel(t *testing.T) {
	output := `{"dim":"tests","verdict":"fail","findings":[{"dimension":"tests","file":"a_test.go","line":1,"severity":"WARNING","description":"d","evidence":"e","confidence":"certain"}]}`
	_, err := ParseDimensionResult(output)
	if err == nil {
		t.Fatal("ParseDimensionResult() error = nil, expected an explicit error for an unknown confidence level")
	}
}

func TestParseDimensionResultUnknownSeverityWithV2FieldsNormalized(t *testing.T) {
	// Same severity-normalization rule as the pure v1 path (T2.4: a single
	// criterion), applied to a finding that also carries v2-only fields.
	output := `{"dim":"logic","verdict":"warn","findings":[{"file":"a.go","line":1,"severity":"FATAL","description":"d","evidence":"e"}]}`
	result, err := ParseDimensionResult(output)
	if err != nil {
		t.Fatalf("ParseDimensionResult returned error: %v", err)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("Findings = %d, want 1", len(result.Findings))
	}
	if result.Findings[0].Severity != SevAdvisory {
		t.Errorf("Findings[0].Severity = %q, want normalized to %q", result.Findings[0].Severity, SevAdvisory)
	}
	if len(result.Warnings) == 0 {
		t.Error("want a warning for the v2-fields severity normalization")
	}
}

func TestParseDimensionResultUnavailable(t *testing.T) {
	output := `{"dim":"security","verdict":"unavailable","reason":"rate_limit"}`
	result, err := ParseDimensionResult(output)
	if err != nil {
		t.Fatalf("ParseDimensionResult returned error: %v", err)
	}
	if result.Verdict != VerdictUnavailable || result.Reason != "rate_limit" {
		t.Errorf("want unavailable/rate_limit, got %s/%s", result.Verdict, result.Reason)
	}
}
