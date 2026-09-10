package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/consent"
	git "github.com/ISeoane-Quental/vas.sentinel/internal/git"
)

type adapterSlicePlanFake struct {
	baseCalls      int
	diffCalls      int
	diff           string
	promptCalls    int
	prompt         string
	promptResponse string
	promptErr      error
}

func (a *adapterSlicePlanFake) GetCommitMessage([]string, string, int) (string, error) {
	a.baseCalls++
	return "", errors.New("the base method must not run")
}

func (a *adapterSlicePlanFake) GetCommitMessageWithDiff(_ []string, _ string, _ int, diff string) (string, error) {
	a.diffCalls++
	a.diff = diff
	return "feat(slice): message from commit profile", nil
}

func (a *adapterSlicePlanFake) RunPrompt(prompt string) (string, error) {
	a.promptCalls++
	a.prompt = prompt
	return a.promptResponse, a.promptErr
}

func TestRunSlicePlanWithConsentUsesAdapterWithDiffNotBaseMethod(t *testing.T) {
	prepareRepoForPlan(t)
	writeDiffRequest(t, true)
	for _, args := range [][]string{{"add", ".vas_sentinel/vassentinel.yml"}, {"commit", "-m", "chore: request external diff"}} {
		if err := exec.Command("git", args...).Run(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := consent.GrantExternalDiff("."); err != nil {
		t.Fatal(err)
	}
	if !allowsExternalAgentDiff(".") {
		status, err := consent.ExternalDiffStatus(".")
		t.Fatalf("request+grant did not enable diff: status=%+v err=%v", status, err)
	}
	if err := os.WriteFile("app.go", []byte("package app\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	var factoryPath string
	previous := newAgentAdapterForMessage
	fake := &adapterSlicePlanFake{}
	newAgentAdapterForMessage = func(path string) (agentadapter.AgentAdapter, error) {
		factoryPath = path
		return fake, nil
	}
	t.Cleanup(func() { newAgentAdapterForMessage = previous })

	var out bytes.Buffer
	if code := runSlicePlan(&out, []string{"--json"}); code != 0 {
		t.Fatalf("exit = %d: %s", code, out.String())
	}
	var plan struct {
		Batches []struct {
			Message string `json:"message"`
		} `json:"batches"`
	}
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(factoryPath) != filepath.Clean(cwd) {
		t.Fatalf("factory received %q, expected root %q", factoryPath, cwd)
	}
	if plan.Batches[0].Message != "feat(slice): message from commit profile" {
		t.Fatalf("serialized message = %q", plan.Batches[0].Message)
	}
	if fake.diffCalls != 1 || fake.baseCalls != 0 {
		t.Fatalf("diff/base calls = %d/%d", fake.diffCalls, fake.baseCalls)
	}
}

func TestRunSlicePlanWithoutConsentDoesNotInvokeAgent(t *testing.T) {
	prepareRepoForPlan(t)
	writeDiffRequest(t, true)
	if err := os.WriteFile("app.go", []byte("package app\n"), 0644); err != nil {
		t.Fatal(err)
	}
	invoked := false
	newAgentAdapterForMessage = func(string) (agentadapter.AgentAdapter, error) {
		invoked = true
		return &adapterSlicePlanFake{}, nil
	}

	var out bytes.Buffer
	if code := runSlicePlan(&out, []string{"--json"}); code != 0 {
		t.Fatalf("exit = %d: %s", code, out.String())
	}
	if invoked {
		t.Fatal("the agent factory was invoked without consent to expose the micro-diff")
	}
	if !strings.Contains(out.String(), "chore(slice): auto-fragmented") {
		t.Fatalf("the deterministic fallback was not used: %s", out.String())
	}
}

func TestChooseAdapterWithoutConsentUsesDeterministicFallback(t *testing.T) {
	path := t.TempDir()
	t.Chdir(path)
	writeDiffRequest(t, false)
	plan := &git.FragmentationPlan{Batches: []git.PlannedBatch{{
		Number: 1, AutoMessage: "chore(slice): local fallback",
	}}}

	adapter, canceled := chooseAdapterAndGenerateMessages(path, plan)
	if adapter != nil || canceled {
		t.Fatalf("adapter = %v, canceled = %t", adapter, canceled)
	}
	if plan.Batches[0].Message != "chore(slice): local fallback" || !plan.Batches[0].DeterministicMessage {
		t.Fatalf("batch without the deterministic fallback: %+v", plan.Batches[0])
	}
}

// TestRunSlicePlanAutomaticBypassIndivisible covers the indivisible class:
// a unit that cannot be subdivided by atoms gets the bypass from the plan
// itself (no exit 3 nor question), notified in automatic_decisions.
func TestRunSlicePlanAutomaticBypassIndivisible(t *testing.T) {
	prepareRepoForPlan(t)
	content := strings.Repeat("// filler line\n", 600)
	if err := os.WriteFile("giant.go", []byte(content), 0644); err != nil {
		t.Fatalf("could not write giant.go: %v", err)
	}

	var out bytes.Buffer
	if code := runSlicePlan(&out, []string{"--json"}); code != 0 {
		t.Fatalf("exit = %d, expected 0. Output: %s", code, out.String())
	}

	var plan struct {
		PlanID           string `json:"plan_id"`
		PendingDecisions []struct {
			File string `json:"file"`
		} `json:"pending_decisions"`
		AutomaticDecisions []struct {
			File   string `json:"file"`
			Reason string `json:"reason"`
		} `json:"automatic_decisions"`
	}
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
		t.Fatalf("the --json output is not valid JSON: %v", err)
	}
	if plan.PlanID == "" {
		t.Error("the emitted plan carries no plan_id")
	}
	if len(plan.PendingDecisions) != 0 {
		t.Errorf("pending decisions = %+v, expected empty", plan.PendingDecisions)
	}
	if len(plan.AutomaticDecisions) != 1 || plan.AutomaticDecisions[0].File != "giant.go" {
		t.Fatalf("automatic decisions = %+v", plan.AutomaticDecisions)
	}
	if plan.AutomaticDecisions[0].Reason != "unit not divisible by diff atoms" {
		t.Errorf("reason = %q", plan.AutomaticDecisions[0].Reason)
	}
}

// TestSlicePlanAndApplyFullPath walks the two-step flow of T0.10: plan ->
// user answers -> apply, without any stdin read.
func TestSlicePlanAndApplyFullPath(t *testing.T) {
	prepareRepoForPlan(t)
	if err := os.WriteFile("app.go", []byte("package app\n"), 0644); err != nil {
		t.Fatalf("could not write app.go: %v", err)
	}

	var planOut bytes.Buffer
	if code := runSlicePlan(&planOut, []string{"--json"}); code != 0 {
		t.Fatalf("plan exit = %d, expected 0. Output: %s", code, planOut.String())
	}
	if err := os.WriteFile("plan.json", planOut.Bytes(), 0644); err != nil {
		t.Fatalf("could not write plan.json: %v", err)
	}

	var plan struct {
		PlanID string `json:"plan_id"`
	}
	if err := json.Unmarshal(planOut.Bytes(), &plan); err != nil {
		t.Fatalf("plan not serializable: %v", err)
	}
	answers := []byte(`{"plan_id":"` + plan.PlanID + `","answers":{}}`)
	if err := os.WriteFile("answers.json", answers, 0644); err != nil {
		t.Fatalf("could not write answers.json: %v", err)
	}

	// Writing the approval artifacts inside the repo does NOT invalidate the
	// plan: the tree hash is bound to the plan's paths (app.go), not to the
	// whole worktree.
	var applyOut bytes.Buffer
	if code := runSliceApply(&applyOut, []string{"--plan", "plan.json", "--answers", "answers.json"}); code != 0 {
		t.Fatalf("apply exit = %d, expected 0. Output: %s", code, applyOut.String())
	}
	if !strings.Contains(applyOut.String(), "commits created") {
		t.Errorf("no commits were created: %s", applyOut.String())
	}

	// Reapplying the same plan is no longer possible: app.go got committed,
	// so the state of its paths changed.
	var repeatOut bytes.Buffer
	if code := runSliceApply(&repeatOut, []string{"--plan", "plan.json", "--answers", "answers.json"}); code != 1 {
		t.Fatalf("reapply exit = %d, expected 1. Output: %s", code, repeatOut.String())
	}
}

func TestRunSlicePlanCapturesDeclaredIntentAndHumanOutput(t *testing.T) {
	prepareRepoForPlan(t)
	if err := os.WriteFile("app.go", []byte("package app\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var jsonOut bytes.Buffer
	if code := runSlicePlan(&jsonOut, []string{"--json", "--intent", "  protect\n the   release "}); code != 0 {
		t.Fatalf("JSON plan exit = %d: %s", code, jsonOut.String())
	}
	var plan git.SerializedPlan
	if err := json.Unmarshal(jsonOut.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Intent != "protect the release" || plan.IntentSource != "declared" {
		t.Fatalf("plan intent = %+v", plan)
	}
	if len(plan.Warnings) != 0 {
		t.Fatalf("declared intent warnings = %v", plan.Warnings)
	}

	var humanOut bytes.Buffer
	if code := runSlicePlan(&humanOut, []string{"--intent", "protect the release"}); code != 0 {
		t.Fatalf("human plan exit = %d: %s", code, humanOut.String())
	}
	if !strings.Contains(humanOut.String(), "🎯 Intent (declared): protect the release") {
		t.Fatalf("human plan omitted intent: %s", humanOut.String())
	}
}

func prepareRepoForPlan(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
	previous := newAgentAdapterForMessage
	newAgentAdapterForMessage = func(string) (agentadapter.AgentAdapter, error) {
		return nil, errors.New("agent not available in test")
	}
	t.Cleanup(func() { newAgentAdapterForMessage = previous })
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@vas.sentinel"},
		{"config", "user.name", "VAS Sentinel Test"},
		{"config", "core.hooksPath", ""},
	} {
		if err := exec.Command("git", args...).Run(); err != nil {
			t.Fatalf("setup %v failed: %v", args, err)
		}
	}
	if err := os.WriteFile("base.txt", []byte("base\n"), 0644); err != nil {
		t.Fatalf("could not write base.txt: %v", err)
	}
	for _, args := range [][]string{{"add", "base.txt"}, {"commit", "-m", "chore: base"}} {
		if err := exec.Command("git", args...).Run(); err != nil {
			t.Fatalf("setup %v failed: %v", args, err)
		}
	}
}

func writeDiffRequest(t *testing.T, allowed bool) {
	t.Helper()
	if err := os.MkdirAll(".vas_sentinel", 0755); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf("request_external_agent_diff: %t\n", allowed)
	if err := os.WriteFile(filepath.Join(".vas_sentinel", "vassentinel.yml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func grantExternalDiffForPlan(t *testing.T) {
	t.Helper()
	writeDiffRequest(t, true)
	for _, args := range [][]string{{"add", ".vas_sentinel/vassentinel.yml"}, {"commit", "-m", "chore: request external diff"}} {
		if err := exec.Command("git", args...).Run(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := consent.GrantExternalDiff("."); err != nil {
		t.Fatal(err)
	}
}

// The transcript path was withdrawn from piece 1 on 2026-09-10. Both flags are
// refused by name rather than reported as unknown options, so an operator or a
// script that used them learns what happened instead of guessing at a typo.
func TestRunSlicePlanRefusesTheWithdrawnTranscriptFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--intent-transcript", "notes.txt"},
		{"--transcript-consent"},
		{"--json", "--intent-transcript", "notes.txt", "--transcript-consent"},
	} {
		var out bytes.Buffer
		if code := runSlicePlan(&out, args); code != 1 {
			t.Fatalf("runSlicePlan(%v) = %d, want 1", args, code)
		}
		printed := out.String()
		if !strings.Contains(printed, "withdrawn") {
			t.Fatalf("runSlicePlan(%v) did not say the flag was withdrawn: %q", args, printed)
		}
		if strings.Contains(printed, "Unknown option") {
			t.Fatalf("runSlicePlan(%v) reported a withdrawn flag as unknown: %q", args, printed)
		}
	}
}
