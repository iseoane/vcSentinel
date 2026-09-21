package review

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/reviewcontract"
)

type h5Case struct {
	claim        string
	file         string
	requirements []string
}

type h5Reviewer struct{ findings []ReviewFinding }

func (r h5Reviewer) RunPrompt(string) (string, error) { return r.result() }

func (r h5Reviewer) RunReview(string, string, []string) (string, error) {
	return r.result()
}

func (r h5Reviewer) ReviewWithPolicy(prompt, sha string, paths []string, _ reviewcontract.ToolPolicy) (string, error) {
	return r.RunReview(prompt, sha, paths)
}

func (r h5Reviewer) result() (string, error) {
	return completeTestContract(string(mustJSON(struct {
		Dim      string          `json:"dim"`
		Verdict  string          `json:"verdict"`
		Findings []ReviewFinding `json:"findings"`
	}{Dim: DimLogic, Verdict: VerdictBlock, Findings: r.findings}))), nil
}

type h5Refuter struct {
	cases  []h5Case
	reader SnapshotReader
}

func (r h5Refuter) RunPrompt(string) (string, error) { return "", nil }

func (r h5Refuter) RunReview(prompt, sha string, paths []string) (string, error) {
	trustedSHA, finding, ok := h5RefutationRequest(prompt)
	if !ok || trustedSHA != sha {
		return h5RefutationFailure(), nil
	}
	for _, c := range r.cases {
		if finding.Description != c.claim || finding.File != c.file || !h5PermitsPath(paths, c.file) {
			continue
		}
		source, err := r.reader(trustedSHA, finding.File)
		if err != nil || !h5ContainsAll(source, c.requirements) {
			return h5RefutationFailure(), nil
		}
		line, ok := h5Line(source, c.requirements[0])
		if !ok || line != int(finding.Line) {
			return h5RefutationFailure(), nil
		}
		response := refuterResponse{
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

func (r h5Refuter) ReviewWithPolicy(prompt, sha string, paths []string, _ reviewcontract.ToolPolicy) (string, error) {
	return r.RunReview(prompt, sha, paths)
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
	reader := NewSnapshotReader(repo)

	findings := make([]ReviewFinding, 0, len(cases))
	for _, c := range cases {
		source, err := reader(auditedSHA, c.file)
		if err != nil || !h5ContainsAll(source, c.requirements) {
			t.Fatalf("immutable fixture snapshot lacks final-state evidence for %q: %v", c.claim, err)
		}
		line, ok := h5Line(source, c.requirements[0])
		if !ok {
			t.Fatalf("immutable fixture snapshot lacks location for %q", c.claim)
		}
		findings = append(findings, ReviewFinding{Dimension: DimLogic, File: c.file, Line: Line(line), Severity: SevCritical, Description: c.claim, Status: StatusConfirmed})
	}

	// The worktree now contradicts the audited commit; refutation must still use Git objects.
	h5WriteFile(t, filepath.Join(repo, "internal/review/renderer.go"), "package review\n// diverged worktree\n")
	liveRenderer, err := os.ReadFile(filepath.Join(repo, "internal/review/renderer.go"))
	if err != nil || strings.Contains(string(liveRenderer), "recortarRunas") {
		t.Fatalf("fixture worktree did not diverge from audited snapshot: %v", err)
	}

	opts := AuditOptions{
		SHA:                 auditedSHA,
		Diff:                rendererDiff + "\n" + exitCodeDiff,
		Bundles:             []ReviewBundle{{Name: "h5", Dimensions: []string{DimLogic}, Priority: PriorityRequired, Cost: 1}},
		ContextPaths:        []string{"internal/review/renderer.go", "internal/review/renderer_test.go", "cmd/sentinel/comandos_estado.go"},
		ReadSnapshotContent: reader,
		RefuterFactory: func() (AgentReviewer, string, error) {
			return h5Refuter{}, "fixture", nil
		},
		// Post-cutover engine contract: refutations route through a
		// transport; this fixture uses the direct-call double with the same
		// reviewer path allowlist the audit options declare.
		ReviewTransport: directTransport(auditedSHA,
			"internal/review/renderer.go", "internal/review/renderer_test.go", "cmd/sentinel/comandos_estado.go"),
	}
	factory := func(ReviewBundle, string) (AgentReviewer, string, error) {
		return h5Reviewer{findings: findings}, "fixture", nil
	}

	red := AuditCommit(factory, 1, opts)
	h5AssertHistoricalFindings(t, red, cases, StatusConfirmed)
	if red.Verdict != VerdictBlock {
		t.Fatalf("RED: expected confirmed historical CRITICAL findings to block, got %q", red.Verdict)
	}

	opts.RefuterFactory = func() (AgentReviewer, string, error) { return h5Refuter{cases: cases, reader: reader}, "fixture", nil }
	green := AuditCommit(factory, 1, opts)
	h5AssertHistoricalFindings(t, green, cases, StatusRefuted)
	if green.Verdict == VerdictBlock {
		t.Fatal("GREEN: final snapshot evidence must refute all historical CRITICAL claims")
	}
}

func h5AssertHistoricalFindings(t *testing.T, result AuditResult, cases []h5Case, status string) {
	t.Helper()
	if status == "" {
		t.Fatal("expected historical finding status is required")
	}
	if len(result.Dims) != 1 || result.Dims[0].Result == nil {
		t.Fatalf("expected one logic result, got %#v", result.Dims)
	}
	findings := result.Dims[0].Result.Findings
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
	for _, c := range cases {
		finding, ok := byClaim[c.claim]
		if !ok {
			t.Errorf("injected historical finding %q disappeared", c.claim)
			continue
		}
		if finding.File != c.file || finding.Severity != SevCritical {
			t.Errorf("historical finding %q = %+v, want CRITICAL in %q", c.claim, finding, c.file)
		}
		if finding.Status != status {
			t.Errorf("historical finding %q has status %q, want %q", c.claim, finding.Status, status)
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
