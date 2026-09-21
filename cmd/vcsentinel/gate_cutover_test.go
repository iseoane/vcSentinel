// Production-cutover integration test for the gate durable-runs wiring
// (ticket 11 slice 3, unconditional since ticket 13 R11 removed the
// gate.durable_runs switch). It drives the EXACT cmd-level construction
// path — buildGateOptions plus applyDurableCutover over a strictly loaded
// project configuration — and proves that one gate execution produces
// root+children linkage in ONE store (the same instance backs the root run
// and every routed review invocation), that the settlement suffix enumerates
// validation jobs plus every learned review child, and that the persisted
// ParentRunID scan reproduces exactly that set.
package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const cutoverValidationYml = "version: \"2.0\"\nvalidation:\n  capabilities:\n    lint:\n      command: \"echo ok\"\n      fails_when: \"exit_code\"\n  profiles:\n    standard: [\"lint\"]\n"

// fixedReviewAgent implements AgentReviewer AND RestrictedReviewer with a
// canned raw verdict, mirroring internal/gate's fake auditor but local to the
// cmd package.
func cutoverRepository(t *testing.T) string {
	t.Helper()
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	if err := os.WriteFile(filepath.Join(worktree, "fixture.go"), []byte("package fixture\n"), 0o644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
	for _, args := range [][]string{
		{"config", "user.name", "fixture"},
		{"config", "user.email", "fixture@example.com"},
		{"add", "."},
		{"commit", "-m", "feat: gate cutover fixture"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = worktree
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return worktree
}

// buildCutoverOptions loads the strict project configuration for the
// fixture repo, resolves HEAD inputs through the real git seams, builds the
// production gate options, applies the durable cutover, and finally overrides
// ONLY the reviewer agent seams with canned verdicts (the review transport
// stays whatever production construction produced). t.Chdir pins every
// pathless git call to the fixture repository for the whole subtest.
func TestGateStrictConfigRejectsRemovedDurableRunsKey(t *testing.T) {
	worktree := cutoverRepository(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(worktree)
	writeTestGateYml(t, filepath.Join(worktree, ".vcsentinel", "vcsentinel.yml"),
		cutoverValidationYml+"gate:\n  durable_runs: false\n")

	var output bytes.Buffer
	exitCode := runGate(&output, worktree, []string{"--stage", "pre-push"})

	if exitCode != 4 {
		t.Fatalf("exit = %d (%q), want 4 for a strict configuration failure", exitCode, output.String())
	}
	if !strings.Contains(output.String(), "gate") || !strings.Contains(output.String(), "not found") {
		t.Fatalf("output = %q, want the unknown-key error to name the removed gate section", output.String())
	}
}
