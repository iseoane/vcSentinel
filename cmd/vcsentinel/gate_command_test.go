package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/config"
	"github.com/ISeoane-Quental/vcSentinel/internal/gate"
	"github.com/ISeoane-Quental/vcSentinel/internal/ops"
)

// writeTestGateYml writes a per-project vcsentinel.yml for the tests of this
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
//
// Piece 3: --timeout is refused rather than accepted and ignored. It only ever
// widened the semantic review budget, and gate is deterministic since it
// stopped auditing, so honouring it would be a lie to whatever script passed
// it.
func TestParseGateFlags(t *testing.T) {
	t.Run("timeout is refused and says where it went", func(t *testing.T) {
		_, _, err := parseGateFlags([]string{"--stage", "pre-push", "--timeout", "1200"})
		if err == nil {
			t.Fatal("gate must refuse --timeout instead of silently ignoring it")
		}
		if !strings.Contains(err.Error(), "vcsentinel review") {
			t.Fatalf("the refusal must point at the command that still honours it, got %v", err)
		}
	})

	t.Run("valid stage without profile uses the default", func(t *testing.T) {
		stage, profile, err := parseGateFlags([]string{"--stage", "pre-commit"})
		if err != nil {
			t.Fatalf("did not expect an error: %v", err)
		}
		if stage != "pre-commit" || profile != defaultGateProfile {
			t.Errorf("got stage=%q profile=%q", stage, profile)
		}
	})

	t.Run("explicit stage and profile", func(t *testing.T) {
		stage, profile, err := parseGateFlags([]string{"--stage", "pr", "--profile", "custom"})
		if err != nil {
			t.Fatalf("did not expect an error: %v", err)
		}
		if stage != "pr" || profile != "custom" {
			t.Errorf("got stage=%q profile=%q", stage, profile)
		}
	})

	t.Run("missing stage is an error", func(t *testing.T) {
		if _, _, err := parseGateFlags([]string{}); err == nil {
			t.Fatal("expected an error for the missing --stage")
		}
	})

	t.Run("non-numeric or non-positive timeout is an error", func(t *testing.T) {
		for _, value := range []string{"abc", "0", "-5"} {
			if _, _, err := parseGateFlags([]string{"--stage", "pr", "--timeout", value}); err == nil {
				t.Errorf("--timeout %q should be an error", value)
			}
		}
		if _, _, err := parseGateFlags([]string{"--stage", "pr", "--timeout"}); err == nil {
			t.Error("--timeout without a value should be an error")
		}
	})

	t.Run("unrecognized stage is a clear error", func(t *testing.T) {
		_, _, err := parseGateFlags([]string{"--stage", "no-such-stage"})
		if err == nil {
			t.Fatal("expected an error for an unrecognized --stage")
		}
		if !strings.Contains(err.Error(), "no-such-stage") {
			t.Errorf("the error must quote the received value, got: %v", err)
		}
	})

	t.Run("unrecognized flag is an error", func(t *testing.T) {
		if _, _, err := parseGateFlags([]string{"--stage", "pr", "--other"}); err == nil {
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

	configPath := filepath.Join(worktree, ".vcsentinel", "vcsentinel.yml")
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
		gate.StateInfrastructureError,
		[]string{rawMessage},
		"codegraph context skipped: dirty_worktree",
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
	// Piece 3: the gate has no reviewers, so it persists no reviewer
	// failures. What it still must persist is the operational metadata
	// above, and what it still must NOT persist is the console message.
	if _, exists := detail["reviewer_failures"]; exists {
		t.Fatalf("a deterministic gate persisted reviewer failures: %#v", detail["reviewer_failures"])
	}
}

// TestRunGateFailsClosedWithoutAttributes is gone with piece 3. It pinned
// that an unreadable .gitattributes stopped the gate, because the attributes
// decided which review dimensions were scheduled. The gate schedules no
// dimensions now, so computing them only to fail closed on them would refuse
// to run for a reason that cannot affect the outcome. `vcsentinel review` still
// reads them, for the audit that still uses them.
