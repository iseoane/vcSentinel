package review

import (
	"testing"
)

// TestParseDimensionResultLineAsString covers the real case of an agent that
// serializes "line" as a string ("126" instead of 126): json.Unmarshal
// against an int fails, the line is discarded and the audit degrades to
// unavailable (ErrInvalidJSONL). Parsing must accept both types.
func TestParseDimensionResultLineAsString(t *testing.T) {
	output := `{"dim":"spec","verdict":"FAIL","findings":[{"dimension":"spec","file":"README.md","line":"126","severity":"CRITICAL","description":"d"}]}`
	result, err := ParseDimensionResult(output)
	if err != nil {
		t.Fatalf("ParseDimensionResult returned error with line as string: %v", err)
	}
	if result.Dim != DimSpec {
		t.Errorf("dim = %q, want %q", result.Dim, DimSpec)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(result.Findings))
	}
	if result.Findings[0].Line != 126 {
		t.Errorf("line = %d, want 126", result.Findings[0].Line)
	}
	if result.Verdict != VerdictBlock {
		t.Errorf("verdict = %q, want %q (CRITICAL raises to block)", result.Verdict, VerdictBlock)
	}
}

// TestParseDimensionResultLineNumber makes sure the numeric form keeps
// working after the string form was accepted.
func TestParseDimensionResultLineNumber(t *testing.T) {
	output := `{"dim":"spec","verdict":"FAIL","findings":[{"dimension":"spec","file":"a.go","line":42,"severity":"CRITICAL","description":"d"}]}`
	result, err := ParseDimensionResult(output)
	if err != nil {
		t.Fatalf("ParseDimensionResult returned error: %v", err)
	}
	if len(result.Findings) != 1 || result.Findings[0].Line != 42 {
		t.Fatalf("findings = %+v, want line 42", result.Findings)
	}
}

// TestParseDimensionResultLineNonNumeric: a non-numeric string, or any
// other JSON type, no longer discards the finding (FU-19): the line stays 0
// (unknown, the line-less convention) and the raw text is preserved in
// LineRaw — never a panic nor an invented number.
func TestParseDimensionResultLineNonNumeric(t *testing.T) {
	result, err := ParseDimensionResult(`{"dim":"spec","verdict":"FAIL","findings":[{"dimension":"spec","file":"a.go","line":"L126-130","severity":"CRITICAL","description":"d"}]}`)
	if err != nil {
		t.Fatalf("a non-numeric line must not discard the whole payload: %v", err)
	}
	if len(result.Findings) != 1 || result.Findings[0].Line != 0 {
		t.Fatalf("findings = %+v, want 1 finding with unknown line", result.Findings)
	}
	if result.Findings[0].LineRaw != "L126-130" {
		t.Errorf("line_raw = %q, want the preserved raw text", result.Findings[0].LineRaw)
	}
}
