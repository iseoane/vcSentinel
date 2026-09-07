package review

import (
	"strings"
	"testing"
)

// TestParseDimensionResultJSONMultiline: the cheap profile may return the
// JSON object pretty-printed across several lines inside the delimiters.
// The parser must accept it: no single line is valid JSONL, but the whole
// block is one JSON object.
func TestParseDimensionResultJSONMultiline(t *testing.T) {
	output := `BEGIN_REVIEW
{
  "dim": "spec",
  "verdict": "ok",
  "reason": "todo en orden"
}
END_REVIEW`

	result, err := ParseDimensionResult(output)
	if err != nil {
		t.Fatalf("ParseDimensionResult returned error with pretty-printed JSON: %v", err)
	}
	if result.Dim != "spec" {
		t.Errorf("Dim = %q, want spec", result.Dim)
	}
	if result.Verdict != VerdictOK {
		t.Errorf("Verdict = %q, want ok", result.Verdict)
	}
}

// TestParseDimensionResultJSONMultilineWithFindings: same case with the
// complete findings structure in pretty-printed form (severity, line as a
// string, etc. — what the cheap model emits de facto).
func TestParseDimensionResultJSONMultilineWithFindings(t *testing.T) {
	output := `BEGIN_REVIEW
{
  "dim": "spec",
  "verdict": "FAIL",
  "findings": [
    {
      "dimension": "spec",
      "file": ".claudecode.md",
      "line": 1,
      "severity": "CRITICAL",
      "description": "Out-of-scope",
      "suggestion": "Separate commit"
    }
  ],
  "reason": "Motive"
}
END_REVIEW`

	result, err := ParseDimensionResult(output)
	if err != nil {
		t.Fatalf("ParseDimensionResult returned error with multiline JSON + findings: %v", err)
	}
	if result.Dim != "spec" {
		t.Errorf("Dim = %q, want spec", result.Dim)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("len(Findings) = %d, want 1", len(result.Findings))
	}
	h := result.Findings[0]
	if h.File != ".claudecode.md" {
		t.Errorf("File = %q, want .claudecode.md", h.File)
	}
	if int(h.Line) != 1 {
		t.Errorf("Line = %d, want 1", h.Line)
	}
	if h.Severity != SevCritical {
		t.Errorf("Severity = %q, want CRITICAL", h.Severity)
	}
	if result.Verdict != VerdictBlock {
		t.Errorf("Verdict = %q, want block (derived from CRITICAL)", result.Verdict)
	}
}

// TestParseDimensionResultJSONMultilineWithoutDelimiters: the whole block
// must also work when the model omits BEGIN_REVIEW/END_REVIEW.
func TestParseDimensionResultJSONMultilineWithoutDelimiters(t *testing.T) {
	output := `{
  "dim": "design",
  "verdict": "warn",
  "findings": []
}`

	result, err := ParseDimensionResult(output)
	if err != nil {
		t.Fatalf("ParseDimensionResult returned error without delimiters: %v", err)
	}
	if result.Dim != "design" {
		t.Errorf("Dim = %q, want design", result.Dim)
	}
}

// TestParseJSONMultilineIsFallback: a valid one-line JSONL must keep
// priority over the multiline object (do not break the existing contracts).
func TestParseJSONMultilineIsFallback(t *testing.T) {
	output := `BEGIN_REVIEW
{"dim":"logic","verdict":"ok"}
{
  "dim": "spec",
  "verdict": "block"
}
END_REVIEW`

	result, err := ParseDimensionResult(output)
	if err != nil {
		t.Fatalf("ParseDimensionResult returned error: %v", err)
	}
	if result.Dim != "logic" {
		t.Errorf("Dim = %q, want logic (first JSONL line wins)", result.Dim)
	}
	if result.Verdict != VerdictOK {
		t.Errorf("Verdict = %q, want ok", result.Verdict)
	}
}

// TestParseDimensionResultJSONMultilineWithV2Fields verifies that the
// multiline fallback (parseMultilineObject) recognizes v2-only Finding
// fields inside a finding, matching the one-line JSONL path. The parse
// output is the single v1 projection, so the v1 fields (description) must
// survive; the durable v2 projection belongs to the engine.
func TestParseDimensionResultJSONMultilineWithV2Fields(t *testing.T) {
	output := `BEGIN_REVIEW
{
  "dim": "design",
  "verdict": "warn",
  "findings": [
    {
      "file": "b.go",
      "line": 3,
      "severity": "WARNING",
      "description": "unnecessary coupling",
      "evidence": "import legacy",
      "title": "coupling to a legacy module"
    }
  ]
}
END_REVIEW`

	result, err := ParseDimensionResult(output)
	if err != nil {
		t.Fatalf("ParseDimensionResult returned error with v2 fields in multiline: %v", err)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("len(Findings) = %d, want 1 (v2 fields detected in pretty-printed)", len(result.Findings))
	}
	if result.Findings[0].Description != "unnecessary coupling" {
		t.Errorf("Findings[0].Description = %q, want the finding's description", result.Findings[0].Description)
	}
	if result.Findings[0].Dimension != "design" {
		t.Errorf("Findings[0].Dimension = %q, want design (the line's dimension)", result.Findings[0].Dimension)
	}
}

// TestParseJSONMultilineWithSurroundingNoise: output text around the object
// (agent reasoning outside the block) must not break the fallback.
func TestParseJSONMultilineWithSurroundingNoise(t *testing.T) {
	output := `Analizando el diff:
BEGIN_REVIEW
{
  "dim": "tests",
  "verdict": "ok"
}
END_REVIEW
Fin de la auditoría.`

	result, err := ParseDimensionResult(output)
	if err != nil {
		t.Fatalf("ParseDimensionResult returned error with surrounding text: %v", err)
	}
	if result.Dim != "tests" {
		t.Errorf("Dim = %q, want tests", result.Dim)
	}
	if !strings.Contains(output, "BEGIN_REVIEW") {
		t.Fatal("misconfigured test: the example must include the delimiters")
	}
}
