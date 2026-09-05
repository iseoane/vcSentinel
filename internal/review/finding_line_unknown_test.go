package review

import (
	"strings"
	"testing"
)

// TestParseDimensionResultKeepsFindingWithRangeLine pins FU-19: an agent
// emits "line" as range text ("17-19, 23-48"). One malformed line value must
// not void the whole payload into unavailable/schema_invalid: the finding
// survives with Line=0 (the unknown-line convention), the raw text is
// preserved for the operator in the persisted ficha (Dims), and the CRITICAL
// severity still blocks line-independently.
//
// FU-19 decision: NO range semantics. The persisted shape holds a single
// Linea int, so deriving first-line precision from range text would
// misdirect the evidence window while pretending the line is known. Unknown
// is already answerable through the file-scoped refutation gate (FU-6
// defect 2), and the single-number forms keep parsing exactly as before.
func TestParseDimensionResultKeepsFindingWithRangeLine(t *testing.T) {
	salida := `{"dim":"spec","verdict":"block","findings":[{"dimension":"spec","file":"a.go","line":"17-19, 23-48","severity":"CRITICAL","description":"multi-hunk problem","suggestion":"fix"}]}`
	resultado, err := ParseDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParseDimensionResult rejected the whole payload for one non-numeric line: %v", err)
	}
	if resultado == nil {
		t.Fatal("resultado = nil")
	}
	if resultado.Verdict != VerdictBlock {
		t.Errorf("verdict = %q, want %q (CRITICAL blocks, line-independent)", resultado.Verdict, VerdictBlock)
	}
	if len(resultado.Findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(resultado.Findings))
	}
	finding := resultado.Findings[0]
	if finding.Line != 0 {
		t.Errorf("line = %d, want 0 (unknown line)", finding.Line)
	}
	if finding.LineRaw != "17-19, 23-48" {
		t.Errorf("line_raw = %q, want the raw text preserved for the operator", finding.LineRaw)
	}
	if !IsBlocking(finding.Severity, finding.Status) {
		t.Errorf("IsBlocking(%q, %q) = false, want true (CRITICAL with no disposition blocks)", finding.Severity, finding.Status)
	}
	if len(resultado.Advertencias) == 0 {
		t.Fatalf("advertencias = %v, want the unknown-line normalization naming file and raw value", resultado.Advertencias)
	}
	advertencia := resultado.Advertencias[len(resultado.Advertencias)-1]
	if !strings.Contains(advertencia, "a.go") || !strings.Contains(advertencia, "17-19, 23-48") {
		t.Errorf("advertencia = %q, want it to name the file and the raw line value", advertencia)
	}
}
