package gate

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

// fakeReviewer implements review.AgentReviewer returning a fixed output; a
// minimal seam so the gate tests do not depend on any real agent.
type fakeReviewer struct {
	output string
	err    error
}

func (a *fakeReviewer) RunPrompt(prompt string) (string, error) {
	return completeTestContract(a.output), a.err
}

func (a *fakeReviewer) RunReview(prompt, _ string, _ []string) (string, error) {
	return a.RunPrompt(prompt)
}

func (a *fakeReviewer) ReviewWithPolicy(prompt, sha string, paths []string, _ reviewcontract.ToolPolicy) (string, error) {
	return a.RunReview(prompt, sha, paths)
}

func completeTestContract(output string) string {
	var result map[string]any
	if json.Unmarshal([]byte(output), &result) != nil {
		return output
	}
	findings, ok := result["findings"].([]any)
	if !ok {
		return output
	}
	for _, item := range findings {
		finding, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if _, ok := finding["evidence"]; !ok {
			finding["evidence"] = "test evidence"
		}
		if _, ok := finding["confidence"]; !ok {
			finding["confidence"] = "high"
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return output
	}
	return string(encoded)
}

// countingFactory builds a review.ReviewerFactory that counts how many times
// it is invoked (one per dimension) and always returns the same fake
// reviewer: it lets a test check "zero calls to the semantic review engine"
// when validation fails (central rule of T1.7).
func countingFactory(calls *int, output string, err error) review.ReviewerFactory {
	return func(_ review.ReviewBundle, dimension string) (review.AgentReviewer, string, error) {
		*calls++
		return &fakeReviewer{output: output, err: err}, "profile-test", nil
	}
}

func cfgWithProfile(nameCapability string, command string) config.Config {
	cfg := config.Config{
		Validation: config.ValidationConfig{
			Capabilities: map[string]config.CapabilityConfig{
				nameCapability: {Command: command, FailsWhen: config.FailsWhenExitCode},
			},
			Profiles: map[string][]string{
				"testprofile": {nameCapability},
			},
			Mode: config.ModeInplace,
		},
	}
	return cfg
}

func baseOptions(t *testing.T, cfg config.Config, run validation.CommandRunner, factory review.ReviewerFactory) Options {
	opts := Options{
		Profile: "testprofile",
		ValidationOptions: validation.RunOptions{
			Worktree: "/unused",
			Cfg:      cfg,
			Run:      run,
		},
		ReviewerFactory: factory,
		Parallel:        1,
		ReviewOptions: review.AuditOptions{
			SHA:     "0123456789abcdef",
			Bundles: []review.ReviewBundle{{Name: "test", Dimensions: []string{review.DimLogic}, Priority: 1, Cost: 1}},
		},
		// RunValidation injected in each test: it does not depend on real git.
	}
	// Ticket 13 (R11): RunGate IS the durable orchestration, so every
	// fixture exercises the only execution path with its full seam set:
	// stage/candidate identity, a temp-dir backed durable store, and a REAL
	// transport factory mirroring cmd/sentinel's construction. Tests that
	// need their own counter or transport override these fields afterwards.
	opts.Stage = "pre-push"
	opts.CandidateSHA = opts.ReviewOptions.SHA
	opts.DurableStore = store.NewStore(filepath.Join(t.TempDir(), "gate-common"))
	opts.DurableReviewTransportFactory = countedTransportFactory(t, opts.ReviewOptions.SHA, new(int))
	return opts
}

func runProfileWithoutCandidate(profile string, _ []string, opts validation.RunOptions) ([]validation.ValidationRun, error) {
	return validation.RunProfile(profile, opts)
}

// TestValidationRed_DoesNotLaunchReview covers acceptance #1: if validation
// fails, the semantic review engine is NEVER invoked (fixed order:
// validation first) and the state is VALIDATION_FAILED.
func TestValidationRed_DoesNotLaunchReview(t *testing.T) {
	cfg := cfgWithProfile("lint", "echo boom")
	calls := 0
	opts := baseOptions(t, cfg, func(string) (int, string, error) {
		return 1, "real output of the failed command", nil
	}, countingFactory(&calls, "", nil))
	opts.RunValidation = runProfileWithoutCandidate
	refuters := 0
	opts.RefuterFactory = func() (review.AgentReviewer, string, error) {
		refuters++
		return &fakeReviewer{}, "cheap", nil
	}

	result := RunGate(opts)

	if result.State != StateValidationFailed {
		t.Fatalf("state expected %q, got %q", StateValidationFailed, result.State)
	}
	if calls != 0 {
		t.Fatalf("expected 0 calls to the review engine, got %d", calls)
	}
	if refuters != 0 {
		t.Fatalf("expected 0 refuters for a validation CRITICAL, got %d", refuters)
	}
	joined := strings.Join(result.Messages, "\n")
	if !strings.Contains(joined, "real output of the failed command") {
		t.Fatalf("the message must show the real output of the failed command, got: %q", joined)
	}
}

func TestValidationGreenWithConfirmedCritical_Blocks(t *testing.T) {
	cfg := cfgWithProfile("lint", "echo ok")
	calls := 0
	outputAgent := `{"dim":"logic","verdict":"block","findings":[{"dimension":"logic","file":"a.go","line":1,"severity":"CRITICAL","description":"riesgo"}]}`
	opts := baseOptions(t, cfg, func(string) (int, string, error) {
		return 0, "", nil
	}, countingFactory(&calls, outputAgent, nil))
	opts.RunValidation = runProfileWithoutCandidate
	refuters := 0
	opts.RefuterFactory = func() (review.AgentReviewer, string, error) {
		refuters++
		return &fakeReviewer{output: `{"refuted":false,"reason":"the risk remains"}`}, "cheap", nil
	}

	result := RunGate(opts)

	if result.State != StateCodeReviewFailed {
		t.Fatalf("state expected %q, got %q", StateCodeReviewFailed, result.State)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call to the review engine, got %d", calls)
	}
	if refuters != 1 {
		t.Fatalf("expected 1 call to the cheap refuter, got %d", refuters)
	}
	joined := strings.Join(result.Messages, "\n")
	if !strings.Contains(joined, "confirmed CRITICAL findings") {
		t.Fatalf("expected the CRITICAL confirmation, got: %q", joined)
	}
}

func TestCriticalRefuted_NeedsUserReview(t *testing.T) {
	cfg := cfgWithProfile("lint", "echo ok")
	calls := 0
	agent := &sequentialReviewer{answers: []string{`{"dim":"logic","verdict":"block","findings":[{"dimension":"logic","file":"a.go","line":1,"severity":"CRITICAL","description":"risk"}]}`}, calls: &calls}
	factory := func(_ review.ReviewBundle, _ string) (review.AgentReviewer, string, error) {
		return agent, "profile-test", nil
	}
	opts := baseOptions(t, cfg, func(string) (int, string, error) { return 0, "", nil }, factory)
	opts.RunValidation = runProfileWithoutCandidate
	opts.RefuterFactory = func() (review.AgentReviewer, string, error) {
		return &fakeReviewer{output: `{"refuted":true,"reason":"the final code already handles this case","sha":"0123456789abcdef","file":"a.go","line_start":1,"line_end":1,"evidence":"final code handles this case"}`}, "cheap", nil
	}
	opts.ReviewOptions.ReadSnapshotContent = func(sha, file string) (string, error) {
		if sha != "0123456789abcdef" || file != "a.go" {
			t.Fatalf("snapshot read sha=%q file=%q", sha, file)
		}
		return "final code handles this case", nil
	}

	result := RunGate(opts)

	if result.State != StateNeedsUserReview || ExitCode(result.State) != 2 {
		t.Fatalf("state=%q exit=%d, expected NEEDS_USER_REVIEW and exit 2", result.State, ExitCode(result.State))
	}
}

// TestReviewWithQuestions_NeedsHumanReview covers the NEEDS_USER_REVIEW
// state: question verdict (the agent asks for clarifications without
// resolving them) demands explicit human attention.
func TestReviewWithQuestions_NeedsHumanReview(t *testing.T) {
	cfg := cfgWithProfile("lint", "echo ok")
	calls := 0
	outputAgent := `{"dim":"logic","verdict":"question","questions":[{"id":"q1","text":"why this change?"}]}`
	opts := baseOptions(t, cfg, func(string) (int, string, error) {
		return 0, "", nil
	}, countingFactory(&calls, outputAgent, nil))
	opts.RunValidation = runProfileWithoutCandidate

	result := RunGate(opts)

	if result.State != StateNeedsUserReview {
		t.Fatalf("state expected %q, got %q", StateNeedsUserReview, result.State)
	}
}

// TestReviewWithoutAgentAvailable_InfrastructureError covers
// REVIEW_INFRASTRUCTURE_ERROR when the review agent does not respond: it is
// not a code finding, it is infrastructure.
func TestReviewWithoutAgentAvailable_InfrastructureError(t *testing.T) {
	cfg := cfgWithProfile("lint", "echo ok")
	opts := baseOptions(t, cfg, func(string) (int, string, error) {
		return 0, "", nil
	}, func(_ review.ReviewBundle, dimension string) (review.AgentReviewer, string, error) {
		return nil, "profile-test", errAgentUnavailableTest
	})
	opts.RunValidation = runProfileWithoutCandidate

	result := RunGate(opts)

	if result.State != StateReviewInfrastructureError {
		t.Fatalf("state expected %q, got %q", StateReviewInfrastructureError, result.State)
	}
}

// TestRunValidationFails_InfrastructureError covers the case where the
// validation orchestration itself (T1.6) fails (e.g. a stale candidate):
// it is infrastructure, not a finding, and it also never launches the review.
func TestRunValidationFails_InfrastructureError(t *testing.T) {
	cfg := cfgWithProfile("lint", "echo ok")
	calls := 0
	opts := baseOptions(t, cfg, nil, countingFactory(&calls, "", nil))
	opts.RunValidation = func(profile string, scope []string, o validation.RunOptions) ([]validation.ValidationRun, error) {
		return nil, errAgentUnavailableTest
	}

	result := RunGate(opts)

	if result.State != StateReviewInfrastructureError {
		t.Fatalf("state expected %q, got %q", StateReviewInfrastructureError, result.State)
	}
	if calls != 0 {
		t.Fatalf("expected 0 calls to the review engine, got %d", calls)
	}
}

// TestExitCode pins the exact exit-code table of the ticket. An unknown
// state (which RunGate should never produce, but which this package also
// cannot recognize) NEVER fails open as PASS: it maps to the same code as
// REVIEW_INFRASTRUCTURE_ERROR (correction over the original design defect,
// see the ExitCode comment).
func TestExitCode(t *testing.T) {
	cases := map[string]int{
		StatePass:                      0,
		StateValidationFailed:          1,
		StateCodeReviewFailed:          1,
		StateNeedsUserReview:           2,
		StateReviewInfrastructureError: 4,
		"UNKNOWN_STATE":                4,
	}
	for state, expected := range cases {
		if got := ExitCode(state); got != expected {
			t.Errorf("ExitCode(%q) = %d, expected %d", state, got, expected)
		}
	}
}

var errAgentUnavailableTest = &fixedError{"agent unavailable"}

type fixedError struct{ msg string }

func (e *fixedError) Error() string { return e.msg }

type sequentialReviewer struct {
	answers []string
	calls   *int
}

func (a *sequentialReviewer) RunPrompt(string) (string, error) {
	output := a.answers[*a.calls]
	*a.calls++
	return completeTestContract(output), nil
}

func (a *sequentialReviewer) RunReview(prompt, sha string, paths []string) (string, error) {
	return a.RunPrompt(prompt)
}

func (a *sequentialReviewer) ReviewWithPolicy(prompt, sha string, paths []string, _ reviewcontract.ToolPolicy) (string, error) {
	return a.RunReview(prompt, sha, paths)
}

func TestTranslateVerdictRendersCurrentEvidence(t *testing.T) {
	confirmed := review.Finding{
		ID:          "finding-1",
		Fingerprint: "fingerprint-1",
		Dimension:   review.DimLogic,
		Severity:    review.SevCritical,
		Status:      review.StatusConfirmed,
		Description: "unsafe fallback is reachable",
		Evidence:    "return fallbackValue",
		Confidence:  0.92,
		Location: review.Location{
			File:      "internal/service.go",
			LineStart: 17,
			LineEnd:   19,
			Simbolo:   "loadValue",
		},
		Producer: review.Producer{
			Agent:         "reviewer-cli",
			Binary:        "reviewer-cli",
			Model:         "model-a",
			Effort:        "high",
			ModelVerified: true,
		},
	}
	refuted := review.Finding{
		ID:          "refuted-1",
		Fingerprint: "refuted-fingerprint",
		Dimension:   review.DimLogic,
		Severity:    review.SevCritical,
		Status:      review.StatusRefuted,
		Description: "already disproved",
		Evidence:    "safe path",
		Location: review.Location{
			File:      "internal/service.go",
			LineStart: 23,
		},
	}

	cases := []struct {
		name          string
		auditResult   review.AuditResult
		expectedState string
		expectedExit  int
		findingCount  int
		contains      []string
		excludes      []string
	}{
		{
			name: "blocked findings include all identifying evidence",
			auditResult: review.AuditResult{
				SHA:      "0123456789abcdef",
				Verdict:  review.VerdictBlock,
				Findings: []review.Finding{confirmed},
			},
			expectedState: StateCodeReviewFailed,
			expectedExit:  1,
			findingCount:  1,
			contains: []string{
				"finding-1",
				"fingerprint-1",
				"logic",
				"internal/service.go",
				"unsafe fallback is reachable",
				"return fallbackValue",
				"0.92",
				"reviewer-cli",
				"model-a",
			},
		},
		{
			name: "unavailable dimensions retain their reasons",
			auditResult: review.AuditResult{
				SHA:     "0123456789abcdef",
				Verdict: review.VerdictUnavailable,
				Dims: []review.DimensionOutcome{
					{Dim: review.DimLogic, Result: &review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictUnavailable, Reason: "provider rate limit"}},
					{Dim: review.DimSecurity, Error: errAgentUnavailableTest},
				},
			},
			expectedState: StateReviewInfrastructureError,
			expectedExit:  4,
			contains: []string{
				"Unavailable dimensions:",
				`dimension="logic" reason="provider rate limit"`,
				`dimension="security" reason="agent unavailable"`,
			},
		},
		{
			name: "block precedence retains unavailable dimension reasons",
			auditResult: review.AuditResult{
				SHA:      "0123456789abcdef",
				Verdict:  review.VerdictBlock,
				Findings: []review.Finding{confirmed},
				Dims: []review.DimensionOutcome{
					{Dim: review.DimLogic, Result: &review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictBlock}},
					{Dim: review.DimSecurity, Result: &review.DimensionResult{Dim: review.DimSecurity, Verdict: review.VerdictUnavailable, Reason: "security reviewer timed out"}},
				},
			},
			expectedState: StateCodeReviewFailed,
			expectedExit:  1,
			findingCount:  1,
			contains: []string{
				"finding-1",
				`dimension="security" reason="security reviewer timed out"`,
			},
		},
		{
			name: "question precedence retains unavailable dimension reasons",
			auditResult: review.AuditResult{
				SHA:       "0123456789abcdef",
				Verdict:   review.VerdictQuestion,
				Questions: []review.AgentQuestion{{ID: "q1", Text: "which behavior is expected?"}},
				Dims: []review.DimensionOutcome{
					{Dim: review.DimLogic, Result: &review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictQuestion}},
					{Dim: review.DimSecurity, Result: &review.DimensionResult{Dim: review.DimSecurity, Verdict: review.VerdictUnavailable, Reason: "security reviewer timed out"}},
				},
			},
			expectedState: StateNeedsUserReview,
			expectedExit:  2,
			contains: []string{
				"which behavior is expected?",
				`dimension="security" reason="security reviewer timed out"`,
			},
		},
		{
			name: "refuted critical remains human review",
			auditResult: review.AuditResult{
				SHA:     "0123456789abcdef",
				Verdict: review.VerdictWarn,
				Dims:    []review.DimensionOutcome{{Dim: review.DimLogic, Result: &review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictWarn, RefutedCritical: true}}},
			},
			expectedState: StateNeedsUserReview,
			expectedExit:  2,
			contains:      []string{"refuted a CRITICAL finding"},
		},
		{
			name: "mixed v1 and v2 dimensions render distinct effective blockers",
			auditResult: review.AuditResult{
				SHA:     "0123456789abcdef",
				Verdict: review.VerdictBlock,
				Findings: []review.Finding{
					confirmed,
					refuted,
				},
				Dims: []review.DimensionOutcome{
					{
						Dim: review.DimLogic,
						Result: &review.DimensionResult{
							Dim:     review.DimLogic,
							Verdict: review.VerdictBlock,
							Findings: []review.ReviewFinding{
								{Dimension: review.DimLogic, File: confirmed.Location.File, Line: 17, Severity: confirmed.Severity, Description: confirmed.Description, Status: review.StatusConfirmed},
								{Dimension: review.DimLogic, File: refuted.Location.File, Line: 23, Severity: refuted.Severity, Description: refuted.Description, Status: review.StatusRefuted},
							},
						},
					},
					{
						Dim: review.DimSecurity,
						Result: &review.DimensionResult{
							Dim:     review.DimSecurity,
							Verdict: review.VerdictBlock,
							Findings: []review.ReviewFinding{
								{Dimension: review.DimSecurity, File: "internal/legacy.go", Line: 42, Severity: review.SevCritical, Description: "legacy blocker remains effective", Status: review.StatusConfirmed},
								{Dimension: review.DimSecurity, File: "internal/refuted.go", Line: 8, Severity: review.SevCritical, Description: "legacy blocker was refuted", Status: review.StatusRefuted},
							},
						},
					},
				},
			},
			expectedState: StateCodeReviewFailed,
			expectedExit:  1,
			findingCount:  2,
			contains:      []string{"finding-1", "fingerprint-1", "legacy blocker remains effective", "internal/legacy.go"},
			excludes:      []string{"refuted-1", "refuted-fingerprint", "safe path", "legacy blocker was refuted", "internal/refuted.go"},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			translated := translateVerdict(testCase.auditResult)
			if translated.State != testCase.expectedState {
				t.Fatalf("state = %q, expected %q", translated.State, testCase.expectedState)
			}
			if got := ExitCode(translated.State); got != testCase.expectedExit {
				t.Fatalf("exit = %d, expected %d", got, testCase.expectedExit)
			}
			output := strings.Join(translated.Messages, "\n")
			if got := strings.Count(output, "finding identity="); got != testCase.findingCount {
				t.Errorf("finding count = %d, expected %d: %s", got, testCase.findingCount, output)
			}
			for _, value := range testCase.contains {
				if !strings.Contains(output, value) {
					t.Errorf("output does not contain %q: %s", value, output)
				}
			}
			for _, value := range testCase.excludes {
				if strings.Contains(output, value) {
					t.Errorf("output unexpectedly contains %q: %s", value, output)
				}
			}
		})
	}
}

func TestTranslateVerdictCarriesCompactReviewerFailureMetadata(t *testing.T) {
	translated := translateVerdict(review.AuditResult{
		ContextSkipReason: "dirty_worktree",
		Verdict:           review.VerdictUnavailable,
		Dims: []review.DimensionOutcome{{
			Bundle: review.BundleCorrectness,
			Dim:    review.DimLogic,
			Result: &review.DimensionResult{
				Dim:     review.DimLogic,
				Verdict: review.VerdictUnavailable,
				Reason:  "provider reported: ripgrep execution failed | run restricted reviewer timed out after 10m0s",
			},
		}},
	})
	if translated.ContextSkipReason != "dirty_worktree" {
		t.Fatalf("context skip reason = %q, want it exposed on the gate result", translated.ContextSkipReason)
	}
	if len(translated.ReviewerFailures) != 1 {
		t.Fatalf("reviewer failures = %#v, want one compact failure", translated.ReviewerFailures)
	}
	failure := translated.ReviewerFailures[0]
	if failure.Bundle != review.BundleCorrectness || failure.Dimension != review.DimLogic {
		t.Fatalf("failure identity = %#v, want correctness/logic", failure)
	}
	if want := "provider reported: ripgrep execution failed"; failure.Reason != want {
		t.Fatalf("failure reason = %q, want %q", failure.Reason, want)
	}
	if strings.Contains(failure.Reason, "timed out") {
		t.Fatalf("failure reason = %q, want no timeout trace", failure.Reason)
	}
}
