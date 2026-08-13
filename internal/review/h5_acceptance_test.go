package review

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type h5Case struct {
	claim        string
	file         string
	requirements []string
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

type h5Refuter struct{ cases []h5Case }

func (r h5Refuter) EjecutarPrompt(string) (string, error) { return "", nil }

func (r h5Refuter) EjecutarRevision(prompt, sha string, paths []string) (string, error) {
	trustedSHA, finding, ok := h5RefutationRequest(prompt)
	if !ok || trustedSHA != sha {
		return h5RefutationFailure(), nil
	}
	for _, caso := range r.cases {
		if finding.Description != caso.claim || finding.File != caso.file || !h5PermitsPath(paths, caso.file) {
			continue
		}
		source, err := leerContenidoSnapshot(trustedSHA, finding.File)
		if err != nil || !h5ContainsAll(source, caso.requirements) {
			return h5RefutationFailure(), nil
		}
		line, ok := h5Line(source, caso.requirements[0])
		if !ok || line != int(finding.Line) {
			return h5RefutationFailure(), nil
		}
		response := respuestaRefutador{
			Refuted:   true,
			Reason:    "immutable final snapshot contains evidence that the claim is false",
			SHA:       trustedSHA,
			File:      finding.File,
			LineStart: line,
			LineEnd:   line,
			Evidence:  strings.TrimSpace(strings.Split(source, "\n")[line-1]),
		}
		return string(mustJSON(response)), nil
	}
	return h5RefutationFailure(), nil
}

func TestH5HistoricalFalsePositivesUseFinalSnapshotEvidence(t *testing.T) {
	repo, auditedSHA, rendererDiff, exitCodeDiff := h5FixtureRepository(t)
	for _, snippet := range []string{
		"func TruncarCuerpo(texto string, maxBytes int) string",
		"if maxBytes <= 0 || len(texto) <= maxBytes",
		"exitCodeDeError(err)",
	} {
		if !strings.Contains(rendererDiff+exitCodeDiff, snippet) {
			t.Fatalf("historical diff fixture is missing %q", snippet)
		}
	}

	cases := []h5Case{
		{claim: "UTF-8 rune truncation is broken", file: "internal/review/renderer.go", requirements: []string{"func recortarRunas", "utf8.RuneStart", "conservado := recortarRunas"}},
		{claim: "maxBytes <= 0 truncates unexpectedly", file: "internal/review/renderer.go", requirements: []string{"if maxBytes <= 0 || len(texto) <= maxBytes"}},
		{claim: "maxBytes <= 0 has no test coverage", file: "internal/review/renderer_test.go", requirements: []string{"func TestTruncarCuerpoLimiteNulo", "TruncarCuerpo(texto, 0)", "TruncarCuerpo(texto, -5)"}},
		{claim: "exitCodeDeError does not exist", file: "cmd/sentinel/comandos_estado.go", requirements: []string{"func exitCodeDeError(err error) int", "errors.As(err, &exitErr)"}},
	}

	previousWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previousWD) })

	findings := make([]ReviewFinding, 0, len(cases))
	for _, caso := range cases {
		source, err := leerContenidoSnapshot(auditedSHA, caso.file)
		if err != nil || !h5ContainsAll(source, caso.requirements) {
			t.Fatalf("immutable fixture snapshot lacks final-state evidence for %q: %v", caso.claim, err)
		}
		line, ok := h5Line(source, caso.requirements[0])
		if !ok {
			t.Fatalf("immutable fixture snapshot lacks location for %q", caso.claim)
		}
		findings = append(findings, ReviewFinding{Dimension: DimLogic, File: caso.file, Line: Linea(line), Severity: SevCritical, Description: caso.claim})
	}

	// The worktree now contradicts the audited commit; refutation must still use Git objects.
	h5WriteFile(t, filepath.Join(repo, "internal/review/renderer.go"), "package review\n// diverged worktree\n")
	liveRenderer, err := os.ReadFile(filepath.Join(repo, "internal/review/renderer.go"))
	if err != nil || strings.Contains(string(liveRenderer), "recortarRunas") {
		t.Fatalf("fixture worktree did not diverge from audited snapshot: %v", err)
	}

	opts := OpcionesAuditoria{
		SHA:                   auditedSHA,
		Diff:                  rendererDiff + "\n" + exitCodeDiff,
		Bundles:               []ReviewBundle{{Name: "h5", Dimensions: []string{DimLogic}, Priority: PriorityRequired, Cost: 1}},
		RutasContexto:         []string{"internal/review/renderer.go", "internal/review/renderer_test.go", "cmd/sentinel/comandos_estado.go"},
		LeerContenidoSnapshot: leerContenidoSnapshot,
	}
	factory := func(ReviewBundle, string) (AuditorAgente, string, error) {
		return h5Reviewer{findings: findings}, "fixture", nil
	}

	red := AuditarCommit(factory, 1, opts)
	h5AssertHistoricalFindings(t, red, cases, "")
	if red.Veredicto != VerdictBlock {
		t.Fatalf("RED: expected confirmed historical CRITICAL findings to block, got %q", red.Veredicto)
	}

	opts.FabricaRefutador = func() (AuditorAgente, string, error) { return h5Refuter{cases: cases}, "fixture", nil }
	green := AuditarCommit(factory, 1, opts)
	h5AssertHistoricalFindings(t, green, cases, StatusRefuted)
	if green.Veredicto == VerdictBlock {
		t.Fatal("GREEN: final snapshot evidence must refute all historical CRITICAL claims")
	}
}

func h5AssertHistoricalFindings(t *testing.T, resultado ResultadoAuditoria, cases []h5Case, status string) {
	t.Helper()
	if len(resultado.Dims) != 1 || resultado.Dims[0].Resultado == nil {
		t.Fatalf("expected one logic result, got %#v", resultado.Dims)
	}
	findings := resultado.Dims[0].Resultado.Findings
	if len(findings) != len(cases) {
		t.Fatalf("expected exactly %d injected historical findings, got %d", len(cases), len(findings))
	}
	byClaim := make(map[string]ReviewFinding, len(findings))
	for _, finding := range findings {
		if _, duplicate := byClaim[finding.Description]; duplicate {
			t.Fatalf("historical finding %q appears more than once", finding.Description)
		}
		byClaim[finding.Description] = finding
	}
	for _, caso := range cases {
		finding, ok := byClaim[caso.claim]
		if !ok {
			t.Errorf("injected historical finding %q disappeared", caso.claim)
			continue
		}
		if status != "" && finding.Status != status {
			t.Errorf("historical finding %q has status %q, want %q", caso.claim, finding.Status, status)
		}
	}
}

func h5FixtureRepository(t *testing.T) (repo, auditedSHA, rendererDiff, exitCodeDiff string) {
	t.Helper()
	repo = t.TempDir()
	h5Git(t, repo, "init")
	h5Git(t, repo, "config", "user.name", "H5 Fixture")
	h5Git(t, repo, "config", "user.email", "h5@example.test")
	h5WriteFile(t, filepath.Join(repo, "internal/review/renderer.go"), "package review\n\nfunc TruncarCuerpo(texto string, maxBytes int) string { return texto }\n")
	h5WriteFile(t, filepath.Join(repo, "internal/review/renderer_test.go"), "package review\n")
	h5WriteFile(t, filepath.Join(repo, "cmd/sentinel/comandos_estado.go"), "package main\n")
	h5Git(t, repo, "add", ".")
	h5Git(t, repo, "commit", "-m", "test fixture baseline")

	h5WriteFile(t, filepath.Join(repo, "internal/review/renderer.go"), `package review

import "unicode/utf8"

func recortarRunas(texto string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(texto) <= maxBytes {
		return texto
	}
	cortado := texto[:maxBytes]
	for len(cortado) > 0 && !utf8.RuneStart(texto[len(cortado)]) {
		cortado = cortado[:len(cortado)-1]
	}
	return cortado
}

func TruncarCuerpo(texto string, maxBytes int) string {
	if maxBytes <= 0 || len(texto) <= maxBytes {
		return texto
	}
	conservado := recortarRunas(texto, maxBytes)
	return conservado
}
`)
	h5WriteFile(t, filepath.Join(repo, "internal/review/renderer_test.go"), `package review

func TestTruncarCuerpoLimiteNulo() {
	texto := "áé"
	_ = TruncarCuerpo(texto, 0)
	_ = TruncarCuerpo(texto, -5)
}
`)
	h5Git(t, repo, "add", "internal/review")
	h5Git(t, repo, "commit", "-m", "historical renderer change")
	rendererSHA := strings.TrimSpace(h5Git(t, repo, "rev-parse", "HEAD"))
	rendererDiff = h5Git(t, repo, "diff", "--no-ext-diff", rendererSHA+"^", rendererSHA, "--", "internal/review/renderer.go", "internal/review/renderer_test.go")

	h5WriteFile(t, filepath.Join(repo, "cmd/sentinel/comandos_estado.go"), `package main

import (
	"errors"
	"os/exec"
)

func ejecutarPR(err error) int { return exitCodeDeError(err) }

func exitCodeDeError(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}
`)
	h5Git(t, repo, "add", "cmd/sentinel/comandos_estado.go")
	h5Git(t, repo, "commit", "-m", "historical exit code change")
	auditedSHA = strings.TrimSpace(h5Git(t, repo, "rev-parse", "HEAD"))
	exitCodeDiff = h5Git(t, repo, "diff", "--no-ext-diff", auditedSHA+"^", auditedSHA, "--", "cmd/sentinel/comandos_estado.go")
	return repo, auditedSHA, rendererDiff, exitCodeDiff
}

func h5RefutationRequest(prompt string) (string, ReviewFinding, bool) {
	const shaPrefix = "Audited commit SHA (trusted): "
	before, after, ok := strings.Cut(prompt, shaPrefix)
	if !ok || before == "" {
		return "", ReviewFinding{}, false
	}
	trustedSHA, after, ok := strings.Cut(after, "\n\n")
	if !ok || trustedSHA == "" {
		return "", ReviewFinding{}, false
	}
	_, after, ok = strings.Cut(after, "Untrusted finding data:\n")
	if !ok {
		return "", ReviewFinding{}, false
	}
	data, _, ok := strings.Cut(after, "\n\n\tReturn ONLY JSON")
	if !ok {
		return "", ReviewFinding{}, false
	}
	var finding ReviewFinding
	if json.Unmarshal([]byte(data), &finding) != nil {
		return "", ReviewFinding{}, false
	}
	return trustedSHA, finding, true
}

func h5RefutationFailure() string {
	return `{"refuted":false,"reason":"the immutable final snapshot does not prove the claim false","sha":"","file":"","line_start":0,"line_end":0,"evidence":""}`
}

func h5PermitsPath(paths []string, want string) bool {
	for _, path := range paths {
		if path == want {
			return true
		}
	}
	return false
}

func h5Git(t *testing.T, repo string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repo}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("H5 fixture command failed: git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func h5WriteFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func h5Line(source, needle string) (int, bool) {
	for line, text := range strings.Split(source, "\n") {
		if strings.Contains(text, needle) {
			return line + 1, true
		}
	}
	return 0, false
}

func h5ContainsAll(source string, needles []string) bool {
	for _, needle := range needles {
		if !strings.Contains(source, needle) {
			return false
		}
	}
	return true
}

func mustJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}
