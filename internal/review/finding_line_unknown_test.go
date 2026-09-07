package review

import (
	"strings"
	"testing"
)

// TestParseDimensionResultKeepsFindingWithRangeLine pins FU-19: an agent
// emits "line" as range text ("17-19, 23-48"). One malformed line value must
// not void the whole payload into unavailable/schema_invalid: the finding
// survives with Line=0 (the unknown-line convention), the raw text is
// preserved for the operator in the persisted record (Dims), and the CRITICAL
// severity still blocks line-independently.
//
// FU-19 decision: NO range semantics. The persisted shape holds a single
// Line int, so deriving first-line precision from range text would
// misdirect the evidence window while pretending the line is known. Unknown
// is already answerable through the file-scoped refutation gate (FU-6
// defect 2), and the single-number forms keep parsing exactly as before.
func TestParseDimensionResultKeepsFindingWithRangeLine(t *testing.T) {
	output := `{"dim":"spec","verdict":"block","findings":[{"dimension":"spec","file":"a.go","line":"17-19, 23-48","severity":"CRITICAL","description":"multi-hunk problem","suggestion":"fix"}]}`
	result, err := ParseDimensionResult(output)
	if err != nil {
		t.Fatalf("ParseDimensionResult rejected the whole payload for one non-numeric line: %v", err)
	}
	if result == nil {
		t.Fatal("result = nil")
	}
	if result.Verdict != VerdictBlock {
		t.Errorf("verdict = %q, want %q (CRITICAL blocks, line-independent)", result.Verdict, VerdictBlock)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(result.Findings))
	}
	finding := result.Findings[0]
	if finding.Line != 0 {
		t.Errorf("line = %d, want 0 (unknown line)", finding.Line)
	}
	if finding.LineRaw != "17-19, 23-48" {
		t.Errorf("line_raw = %q, want the raw text preserved for the operator", finding.LineRaw)
	}
	if !IsBlocking(finding.Severity, finding.Status) {
		t.Errorf("IsBlocking(%q, %q) = false, want true (CRITICAL with no disposition blocks)", finding.Severity, finding.Status)
	}
	if len(result.Warnings) == 0 {
		t.Fatalf("warnings = %v, want the unknown-line normalization naming file and raw value", result.Warnings)
	}
	warning := result.Warnings[len(result.Warnings)-1]
	if !strings.Contains(warning, "a.go") || !strings.Contains(warning, "17-19, 23-48") {
		t.Errorf("warning = %q, want it to name the file and the raw line value", warning)
	}
}
