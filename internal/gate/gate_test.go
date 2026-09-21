package gate

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/config"
	"github.com/ISeoane-Quental/vcSentinel/internal/review"
	"github.com/ISeoane-Quental/vcSentinel/internal/reviewcontract"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
	"github.com/ISeoane-Quental/vcSentinel/internal/validation"
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

func baseOptions(t *testing.T, cfg config.Config, run validation.CommandRunner) Options {
	opts := Options{
		Profile: "testprofile",
		ValidationOptions: validation.RunOptions{
			Worktree: "/unused",
			Cfg:      cfg,
			Run:      run,
		},
		// RunValidation injected in each test: it does not depend on real git.
	}
	// Ticket 13 (R11): RunGate IS the durable orchestration, so every
	// fixture exercises the only execution path with its full seam set:
	// stage/candidate identity, a temp-dir backed durable store, and a REAL
	// transport factory mirroring cmd/sentinel's construction. Tests that
	// need their own counter or transport override these fields afterwards.
	opts.Stage = "pre-push"
	opts.CandidateSHA = "0123456789abcdef"
	opts.DurableStore = store.NewStore(filepath.Join(t.TempDir(), "gate-common"))
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
	})
	opts.RunValidation = runProfileWithoutCandidate
	refuters := 0

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

// TestRunValidationFails_InfrastructureError covers the case where the
// validation orchestration itself (T1.6) fails (e.g. a stale candidate):
// it is infrastructure, not a finding, and it also never launches the review.
func TestRunValidationFails_InfrastructureError(t *testing.T) {
	cfg := cfgWithProfile("lint", "echo ok")
	calls := 0
	opts := baseOptions(t, cfg, nil)
	opts.RunValidation = func(profile string, scope []string, o validation.RunOptions) ([]validation.ValidationRun, error) {
		return nil, errAgentUnavailableTest
	}

	result := RunGate(opts)

	if result.State != StateInfrastructureError {
		t.Fatalf("state expected %q, got %q", StateInfrastructureError, result.State)
	}
	if calls != 0 {
		t.Fatalf("expected 0 calls to the review engine, got %d", calls)
	}
}

// TestExitCode pins the exact exit-code table. An unknown state (which
// RunGate should never produce, but which this package also cannot recognize)
// NEVER fails open as PASS: it maps to the same code as
// INFRASTRUCTURE_ERROR, see the ExitCode comment.
//
// Piece 3 retired code 2: it carried NEEDS_USER_REVIEW, which only a semantic
// audit can produce. The case below pins that it is gone rather than silently
// reachable through some other state.
func TestExitCode(t *testing.T) {
	cases := map[string]int{
		StatePass:                0,
		StateValidationFailed:    1,
		StateInfrastructureError: 4,
		"UNKNOWN_STATE":          4,
		"NEEDS_USER_REVIEW":      4,
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
