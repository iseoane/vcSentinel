package main

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/gate"
	"github.com/ISeoane-Quental/vas.sentinel/internal/ops"
)

// writeTestGateYml writes a per-project vassentinel.yml for the tests of this
// file (its own minimum: it does not depend on internal/config helpers).
func writeTestGateYml(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("could not create %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("could not write %s: %v", path, err)
	}
}

// TestApplyTimeoutSeconds covers the other end of the flag: that the parsed
// value really reaches review.Timeout, in seconds and in that field.
// Checking only the parser would let through an erased override, in the wrong
// unit or written to another field.
func TestApplyTimeoutSeconds(t *testing.T) {
	base := config.Config{Review: config.ReviewConfig{Timeout: 600 * time.Second}}

	t.Run("overrides review.timeout in seconds", func(t *testing.T) {
		got := applyTimeoutSeconds(base, 1200)
		if got.Review.Timeout != 1200*time.Second {
			t.Errorf("Review.Timeout = %v, expected 1200s", got.Review.Timeout)
		}
	})

	t.Run("without an override the yml rules", func(t *testing.T) {
		if got := applyTimeoutSeconds(base, 0); got.Review.Timeout != base.Review.Timeout {
			t.Errorf("Review.Timeout = %v, expected the yml value %v", got.Review.Timeout, base.Review.Timeout)
		}
	})
}

// TestParseGateFlags covers the required --stage with fixed values and the
// optional --profile with its default.
func TestParseGateFlags(t *testing.T) {
	t.Run("valid stage without profile uses the default", func(t *testing.T) {
		stage, profile, _, err := parseGateFlags([]string{"--stage", "pre-commit"})
		if err != nil {
			t.Fatalf("did not expect an error: %v", err)
		}
		if stage != "pre-commit" || profile != defaultGateProfile {
			t.Errorf("got stage=%q profile=%q", stage, profile)
		}
	})

	t.Run("explicit stage and profile", func(t *testing.T) {
		stage, profile, _, err := parseGateFlags([]string{"--stage", "pr", "--profile", "custom"})
		if err != nil {
			t.Fatalf("did not expect an error: %v", err)
		}
		if stage != "pr" || profile != "custom" {
			t.Errorf("got stage=%q profile=%q", stage, profile)
		}
	})

	t.Run("missing stage is an error", func(t *testing.T) {
		if _, _, _, err := parseGateFlags([]string{}); err == nil {
			t.Fatal("expected an error for the missing --stage")
		}
	})

	t.Run("valid timeout overrides review.timeout only in this invocation", func(t *testing.T) {
		_, _, timeout, err := parseGateFlags([]string{"--stage", "pre-push", "--timeout", "1200"})
		if err != nil {
			t.Fatalf("did not expect an error: %v", err)
		}
		if timeout != 1200 {
			t.Errorf("timeout = %d, expected 1200", timeout)
		}
	})

	t.Run("without timeout there is no override", func(t *testing.T) {
		_, _, timeout, err := parseGateFlags([]string{"--stage", "pre-push"})
		if err != nil {
			t.Fatalf("did not expect an error: %v", err)
		}
		if timeout != 0 {
			t.Errorf("timeout = %d, expected 0: without the flag the yml rules", timeout)
		}
	})

	t.Run("unrepresentable timeout is an error", func(t *testing.T) {
		// Above the maximum the value would overflow time.Duration and turn
		// negative. On 32 bits it does not even get there, because strconv.Atoi
		// rejects it first by range; both paths are an error and that is what
		// is asserted here, without tying to the platform's int width.
		_, _, _, err := parseGateFlags([]string{"--stage", "pr", "--timeout", strconv.FormatInt(maxTimeoutSeconds+1, 10)})
		if err == nil {
			t.Fatal("expected an error for an unrepresentable timeout")
		}

		// The largest ACCEPTABLE value depends on the platform: where int is
		// 32 bits, the duration bound falls out of reach and the real ceiling
		// is the int's own. Pinning the 64-bit value would make the suite fail
		// exactly on the targets the production fix protects.
		maximum := maxTimeoutSeconds
		if int64(math.MaxInt) < maximum {
			maximum = int64(math.MaxInt)
		}
		if _, _, timeout, err := parseGateFlags([]string{"--stage", "pr", "--timeout", strconv.FormatInt(maximum, 10)}); err != nil || int64(timeout) != maximum {
			t.Errorf("the largest acceptable value must be accepted, got timeout=%d err=%v", timeout, err)
		}
	})

	t.Run("non-numeric or non-positive timeout is an error", func(t *testing.T) {
		for _, value := range []string{"abc", "0", "-5"} {
			if _, _, _, err := parseGateFlags([]string{"--stage", "pr", "--timeout", value}); err == nil {
				t.Errorf("--timeout %q should be an error", value)
			}
		}
		if _, _, _, err := parseGateFlags([]string{"--stage", "pr", "--timeout"}); err == nil {
			t.Error("--timeout without a value should be an error")
		}
	})

	t.Run("unrecognized stage is a clear error", func(t *testing.T) {
		_, _, _, err := parseGateFlags([]string{"--stage", "no-such-stage"})
		if err == nil {
			t.Fatal("expected an error for an unrecognized --stage")
		}
		if !strings.Contains(err.Error(), "no-such-stage") {
			t.Errorf("the error must quote the received value, got: %v", err)
		}
	})

	t.Run("unrecognized flag is an error", func(t *testing.T) {
		if _, _, _, err := parseGateFlags([]string{"--stage", "pr", "--other"}); err == nil {
			t.Fatal("expected an error for an unrecognized flag")
		}
	})
}

// TestRunGate_InvalidStage_RunsNothing covers the acceptance: --stage with an
// unrecognized value cuts with a clear error before touching configuration,
// git or agents.
func TestRunGate_InvalidStage_RunsNothing(t *testing.T) {
	var output bytes.Buffer
	exit := runGate(&output, t.TempDir(), []string{"--stage", "no-such-stage"})
	if exit != 1 {
		t.Errorf("expected exit 1, got %d", exit)
	}
	if !strings.Contains(output.String(), "no-such-stage") {
		t.Errorf("the output must explain the invalid value, got: %q", output.String())
	}
}

// TestRunGate_InvalidConfigWithLine covers the acceptance: a yml with an
// unknown key ends the gate with exit 4 and the message includes the line of
// the error (config.LoadStrictLocalConfig, requirement added by the
// orchestrator).
func TestRunGate_InvalidConfigWithLine(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	configPath := filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml")
	writeTestGateYml(t, configPath, "active_agent: \"claude\"\nunknown_key: true\n")

	var output bytes.Buffer
	exit := runGate(&output, worktree, []string{"--stage", "pre-commit"})

	if exit != 4 {
		t.Errorf("expected exit 4, got %d", exit)
	}
	if !strings.Contains(output.String(), "line") {
		t.Errorf("the output must include the line of the yml error, got: %q", output.String())
	}
}

// TestRunGate_ProfileNotConfigured_Exit4 covers the T1.7 design decision: if
// --profile (or its default "standard") does not exist in validation.profiles,
// the gate cuts with an explicit error instead of assuming an arbitrary
// profile.
func TestRunGate_ProfileNotConfigured_Exit4(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	var output bytes.Buffer
	exit := runGate(&output, worktree, []string{"--stage", "pre-commit"})

	if exit != 4 {
		t.Errorf("expected exit 4 (profile %q not configured), got %d", defaultGateProfile, exit)
	}
	if !strings.Contains(output.String(), defaultGateProfile) {
		t.Errorf("the output must quote the missing profile, got: %q", output.String())
	}
}

func TestGateEventPersistsOnlyOperationalMetadata(t *testing.T) {

	worktree := t.TempDir()
	if output, err := exec.Command("git", "init", "-q", worktree).CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v: %s", err, output)
	}
	const rawMessage = "raw provider evidence must remain console-only"
	var output bytes.Buffer
	finalizeGateWithDetails(
		&output,
		worktree,
		"pre-push",
		gate.StateReviewInfrastructureError,
		[]string{rawMessage},
		"codegraph context skipped: dirty_worktree",
		[]gate.ReviewerFailure{{
			Bundle:    "correctness",
			Dimension: "logic",
			Reason:    "provider reported: ripgrep execution failed",
		}},
	)
	if !strings.Contains(output.String(), rawMessage) {
		t.Fatalf("console output = %q, want raw diagnostic", output.String())
	}

	events, err := ops.RecentEvents(filepath.Join(worktree, ".git"), 1)
	if err != nil {
		t.Fatalf("read gate event: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want one", len(events))
	}
	detail, ok := events[0].Detail.(map[string]any)
	if !ok {
		t.Fatalf("detail = %#v, want object", events[0].Detail)
	}
	if _, exists := detail["messages"]; exists {
		t.Fatalf("gate event persisted console messages: %#v", detail["messages"])
	}
	if strings.Contains(fmt.Sprint(detail), rawMessage) {
		t.Fatalf("gate event leaked raw console message: %#v", detail)
	}
	if detail["context_skip_reason"] != "codegraph context skipped: dirty_worktree" {
		t.Errorf("context skip reason = %#v", detail["context_skip_reason"])
	}
	failures, ok := detail["reviewer_failures"].([]any)
	if !ok || len(failures) != 1 {
		t.Fatalf("reviewer failures = %#v, want one", detail["reviewer_failures"])
	}
	failure, ok := failures[0].(map[string]any)
	if !ok {
		t.Fatalf("reviewer failure = %#v, want object", failures[0])
	}
	if failure["bundle"] != "correctness" || failure["dimension"] != "logic" || failure["reason"] != "provider reported: ripgrep execution failed" {
		t.Fatalf("reviewer failure = %#v", failure)
	}
}

// TestRunGateFailsClosedWithoutAttributes exercises the fail-closed path that
// the review of ce8316d found untested, and it is the one that matters: the
// repository attributes decide route classification and therefore whether the
// security and concurrency bundles are scheduled at all, so a gate that
// continued with empty evidence would succeed on an under-classified plan.
//
// git.Attributes already returns "" with no error when the tree has no
// .gitattributes, so reaching this branch always means a real read failure.
func TestRunGateFailsClosedWithoutAttributes(t *testing.T) {
	worktree := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = worktree
		// Built from empty, not appended to os.Environ: appending keeps
		// GIT_CONFIG_COUNT and the GIT_CONFIG_KEY_*/VALUE_* pairs, which
		// override the neutralisation the other variables state, and keeps a
		// global commit.gpgSign or hook that would break setup on some hosts.
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"), "HOME=" + worktree,
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_CONFIG_NOSYSTEM=1",
		}
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, output)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "core.hooksPath", "")

	// A configured validation profile and a real HEAD are both required: the
	// gate stops before the attribute read without them, and a test that never
	// reaches the branch it names proves nothing.
	if err := os.MkdirAll(filepath.Join(worktree, ".vas_sentinel"), 0o755); err != nil {
		t.Fatal(err)
	}
	configYaml := "validation:\n  capabilities:\n    format:\n      command: \"true\"\n  profiles:\n    standard: [format]\n"
	if err := os.WriteFile(filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), []byte(configYaml), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-qm", "first")

	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(worktree); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(previous) })

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	original := readAttributesGate
	t.Cleanup(func() { readAttributesGate = original })
	called := false
	readAttributesGate = func(string) (string, error) {
		called = true
		return "", errors.New("simulated attribute read failure")
	}

	var output bytes.Buffer
	exit := runGate(&output, worktree, []string{"--stage", "pre-commit"})

	// The gate stops before this seam when configuration is missing, so a run
	// that never reached it would prove nothing about the fail-closed branch.
	if !called {
		t.Fatalf("the gate never reached the attribute read, so this test proves nothing about the fail-closed branch; output: %q", output.String())
	}
	// The exact code matters: accepting any non-zero would pass if the attribute
	// failure were misreported as a validation or review failure, which would
	// send the operator looking in the wrong place.
	want := gate.ExitCode(gate.StateReviewInfrastructureError)
	if exit != want {
		t.Errorf("gate exited %d after an attribute read failure, want %d (review infrastructure error). Output: %q", exit, want, output.String())
	}
	if !strings.Contains(output.String(), "attributes") {
		t.Errorf("the output must name the attribute failure, got: %q", output.String())
	}
}

// FU-6: the gate must surface corrupt human-answer history as review
// infrastructure failure rather than running without the disposition overlay.
func TestRunGateFailsClosedOnCorruptHumanDisposition(t *testing.T) {
	worktree := worktreeWithCorruptHumanDisposition(t)
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(worktree); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(previous) })
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	var output bytes.Buffer
	exit := runGate(&output, worktree, []string{"--stage", "pre-commit"})
	want := gate.ExitCode(gate.StateReviewInfrastructureError)
	if exit != want {
		t.Fatalf("gate exit = %d, want %d; output: %q", exit, want, output.String())
	}
	if !strings.Contains(output.String(), "reading human dispositions") {
		t.Fatalf("gate did not report the disposition read failure: %q", output.String())
	}
}
