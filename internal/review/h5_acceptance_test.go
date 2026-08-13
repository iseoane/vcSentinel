package review

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	h5RendererCommit = "d5aa86b"
	h5RendererCase   = "1a61087"
	h5ExitCodeCase   = "914a975"
)

type h5Case struct {
	claim   string
	file    string
	needles []string
	line    int
}

type h5Reviewer struct{ findings []ReviewFinding }

func (r h5Reviewer) EjecutarPrompt(string) (string, error) { return r.resultado() }

func (r h5Reviewer) EjecutarRevision(string, string, []string) (string, error) {
	return r.resultado()
}

func (r h5Reviewer) resultado() (string, error) {
	return string(mustJSON(struct {
		Dim      string          `json:"dim"`
		Verdict  string          `json:"verdict"`
		Findings []ReviewFinding `json:"findings"`
	}{Dim: DimLogic, Verdict: VerdictBlock, Findings: r.findings})), nil
}

type h5Refuter struct {
	sha    string
	cases  []h5Case
	source map[string]string
}

func (r h5Refuter) EjecutarPrompt(string) (string, error) { return "", nil }

func (r h5Refuter) EjecutarRevision(prompt, _ string, _ []string) (string, error) {
	for _, caso := range r.cases {
		if !h5PromptHasClaim(prompt, caso.claim) {
			continue
		}
		source := r.source[caso.file]
		if source == "" || !h5ContainsAll(source, caso.needles) {
			break
		}
		lineStart := caso.line
		lineEnd := lineStart
		lines := strings.Split(source, "\n")
		evidence := strings.TrimSpace(lines[lineStart-1])
		response := respuestaRefutador{
			Refuted:   true,
			Reason:    "immutable final snapshot contains evidence that the claim is false",
			SHA:       r.sha,
			File:      caso.file,
			LineStart: lineStart,
			LineEnd:   lineEnd,
			Evidence:  evidence,
		}
		return string(mustJSON(response)), nil
	}
	return `{"refuted":false,"reason":"the immutable final snapshot does not prove the claim false","sha":"","file":"","line_start":0,"line_end":0,"evidence":""}`, nil
}

func TestH5HistoricalFalsePositivesUseFinalSnapshotEvidence(t *testing.T) {
	rendererDiff := h5Git(t, "diff", "--no-ext-diff", h5RendererCase+"^", h5RendererCase, "--", "internal/review/renderer.go", "internal/review/renderer_test.go")
	exitCodeDiff := h5Git(t, "diff", "--no-ext-diff", h5ExitCodeCase+"^", h5ExitCodeCase, "--", "cmd/sentinel/comandos_pr.go")
	for _, snippet := range []string{
		"func TruncarCuerpo(texto string, maxBytes int) string",
		"if len(texto) <= maxBytes || maxBytes <= 0",
		"exitCodeDeError(err)",
	} {
		if !strings.Contains(rendererDiff+exitCodeDiff, snippet) {
			t.Fatalf("historical diff fixture is missing %q", snippet)
		}
	}

	source := map[string]string{
		"internal/review/renderer.go":      h5Git(t, "show", h5RendererCommit+":internal/review/renderer.go"),
		"internal/review/renderer_test.go": h5Git(t, "show", h5RendererCommit+":internal/review/renderer_test.go"),
		"cmd/sentinel/comandos_estado.go":  h5Git(t, "show", h5RendererCommit+":cmd/sentinel/comandos_estado.go"),
	}
	cases := []h5Case{
		{claim: "UTF-8 rune truncation is broken", file: "internal/review/renderer.go", needles: []string{"func recortarRunas", "utf8.RuneStart", "conservado := recortarRunas"}},
		{claim: "maxBytes <= 0 truncates unexpectedly", file: "internal/review/renderer.go", needles: []string{"if maxBytes <= 0 || len(texto) <= maxBytes"}},
		{claim: "maxBytes <= 0 has no test coverage", file: "internal/review/renderer_test.go", needles: []string{"func TestTruncarCuerpoLimiteNulo", "TruncarCuerpo(texto, 0)", "TruncarCuerpo(texto, -5)"}},
		{claim: "exitCodeDeError does not exist", file: "cmd/sentinel/comandos_estado.go", needles: []string{"func exitCodeDeError(err error) int", "errors.As(err, &exitErr)"}},
	}
	for i := range cases {
		if !h5ContainsAll(source[cases[i].file], cases[i].needles) {
			t.Fatalf("fixture %q lacks required final-state evidence %q", cases[i].claim, cases[i].needles)
		}
		cases[i].line = h5Line(t, source[cases[i].file], cases[i].needles[0])
	}

	findings := make([]ReviewFinding, 0, len(cases))
	for _, caso := range cases {
		findings = append(findings, ReviewFinding{Dimension: DimLogic, File: caso.file, Line: Linea(caso.line), Severity: SevCritical, Description: caso.claim})
	}
	refuter := h5Refuter{sha: h5RendererCommit, cases: cases, source: source}
	for _, finding := range findings {
		response, err := refuter.EjecutarRevision(construirPromptRefutacion(h5RendererCommit, DimLogic, finding), h5RendererCommit, nil)
		if err != nil || !strings.Contains(response, `"refuted":true`) {
			t.Fatalf("fixture refuter did not refute %q: %v %s", finding.Description, err, response)
		}
	}
	resultado := AuditarCommit(
		func(ReviewBundle, string) (AuditorAgente, string, error) {
			return h5Reviewer{findings: findings}, "fixture", nil
		},
		1,
		OpcionesAuditoria{
			SHA:                   h5RendererCommit,
			Diff:                  rendererDiff + "\n" + exitCodeDiff,
			Bundles:               []ReviewBundle{{Name: "h5", Dimensions: []string{DimLogic}, Priority: PriorityRequired, Cost: 1}},
			RutasContexto:         []string{"internal/review/renderer.go", "internal/review/renderer_test.go", "cmd/sentinel/comandos_estado.go"},
			LeerContenidoSnapshot: func(_ string, file string) (string, error) { return source[file], nil },
			FabricaRefutador:      func() (AuditorAgente, string, error) { return refuter, "fixture", nil },
		},
	)

	for _, finding := range resultado.Dims[0].Resultado.Findings {
		if finding.Status != StatusRefuted {
			t.Errorf("%q remained %q; final snapshot evidence must refute it: %s", finding.Description, finding.Status, finding.RefutationReason)
		}
	}
	if resultado.Veredicto == VerdictBlock {
		t.Fatal("H5 claims must not produce confirmed CRITICAL blockers with final snapshot evidence")
	}
}

func h5Git(t *testing.T, args ...string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate H5 acceptance fixture")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "../.."))
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("immutable H5 fixture command failed: git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func h5Line(t *testing.T, source, needle string) int {
	t.Helper()
	for line, text := range strings.Split(source, "\n") {
		if strings.Contains(text, needle) {
			return line + 1
		}
	}
	t.Fatalf("immutable final snapshot is missing %q", needle)
	return 0
}

func h5ContainsAll(source string, needles []string) bool {
	for _, needle := range needles {
		if !strings.Contains(source, needle) {
			return false
		}
	}
	return true
}

func h5PromptHasClaim(prompt, claim string) bool {
	return strings.Contains(prompt, claim) || strings.Contains(prompt, strings.ReplaceAll(claim, "<", `\u003c`))
}

func mustJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}
