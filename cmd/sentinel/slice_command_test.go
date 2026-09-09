package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/consent"
	git "github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/intent"
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

func TestRunSlicePlanSummarizesTranscriptOnlyWithConsent(t *testing.T) {
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
	if err := os.WriteFile("app.go", []byte("package app\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("transcript.txt", []byte("human wanted a protected release"), 0644); err != nil {
		t.Fatal(err)
	}
	fake := &adapterSlicePlanFake{promptResponse: "Protect the release"}
	previous := newAgentAdapterForMessage
	newAgentAdapterForMessage = func(string) (agentadapter.AgentAdapter, error) { return fake, nil }
	t.Cleanup(func() { newAgentAdapterForMessage = previous })

	var out bytes.Buffer
	if code := runSlicePlan(&out, []string{"--json", "--intent-transcript", "transcript.txt", "--transcript-consent"}); code != 0 {
		t.Fatalf("transcript plan exit = %d: %s", code, out.String())
	}
	var plan git.SerializedPlan
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Intent != "Protect the release" || plan.IntentSource != "conversation" {
		t.Fatalf("transcript intent = %+v", plan)
	}
	if len(plan.Warnings) != 1 || plan.Warnings[0] != "Transcript sent to auto under this repository's external-diff consent, acknowledged with --transcript-consent." {
		t.Fatalf("transcript warnings = %v", plan.Warnings)
	}
	if !strings.Contains(out.String(), "Transcript sent to auto under this repository's external-diff consent, acknowledged with --transcript-consent.") {
		t.Fatalf("transcript disclosure missing: %s", out.String())
	}
	if !strings.Contains(fake.prompt, intentTranscriptMarkerForTest()) {
		t.Fatalf("adapter prompt did not contain transcript fences: %q", fake.prompt)
	}
	for _, change := range plan.Changes {
		if change.Path == "transcript.txt" || change.OldPath == "transcript.txt" {
			t.Fatalf("transcript remained in CLI plan changes: %+v", plan.Changes)
		}
	}
	for _, batch := range plan.Batches {
		for _, path := range batch.Paths {
			if path == "transcript.txt" {
				t.Fatalf("transcript remained in CLI batch: %+v", plan.Batches)
			}
		}
	}
	if strings.Contains(fake.diff, "transcript.txt") || strings.Contains(fake.diff, "human wanted a protected release") {
		t.Fatalf("transcript reached the CLI adapter micro-diff: %q", fake.diff)
	}
}

func TestRunSlicePlanDoesNotSendTranscriptWithoutConsent(t *testing.T) {
	prepareRepoForPlan(t)
	if err := os.WriteFile("app.go", []byte("package app\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("transcript.txt", []byte("human wanted a protected release"), 0644); err != nil {
		t.Fatal(err)
	}
	invoked := false
	previous := newAgentAdapterForMessage
	newAgentAdapterForMessage = func(string) (agentadapter.AgentAdapter, error) {
		invoked = true
		return &adapterSlicePlanFake{promptResponse: "must not run"}, nil
	}
	t.Cleanup(func() { newAgentAdapterForMessage = previous })

	var out bytes.Buffer
	if code := runSlicePlan(&out, []string{"--json", "--intent-transcript", "transcript.txt"}); code != 0 {
		t.Fatalf("transcript plan exit = %d: %s", code, out.String())
	}
	var plan git.SerializedPlan
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if invoked {
		t.Fatal("transcript was sent without external-diff consent")
	}
	if plan.Intent != "" || plan.IntentSource != "" || len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0], "not sent") {
		t.Fatalf("no-consent transcript plan = %+v", plan)
	}
}

func TestRunSlicePlanTranscriptConsentMatrix(t *testing.T) {
	for _, tc := range []struct {
		name           string
		grantExternal  bool
		acknowledge    bool
		wantExit       int
		wantPromptCall int
	}{
		{name: "without external consent", wantExit: 0},
		{name: "without external consent but acknowledged", acknowledge: true, wantExit: 0},
		{name: "with external consent without acknowledgement", grantExternal: true, wantExit: 1},
		{name: "with both consent conditions", grantExternal: true, acknowledge: true, wantPromptCall: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prepareRepoForPlan(t)
			if tc.grantExternal {
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
			if err := os.WriteFile("app.go", []byte("package app\n"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile("transcript.txt", []byte("human wanted a protected release"), 0644); err != nil {
				t.Fatal(err)
			}
			fake := &adapterSlicePlanFake{promptResponse: "Protect the release"}
			previous := newAgentAdapterForMessage
			newAgentAdapterForMessage = func(string) (agentadapter.AgentAdapter, error) { return fake, nil }
			t.Cleanup(func() { newAgentAdapterForMessage = previous })

			args := []string{"--json", "--intent-transcript", "transcript.txt"}
			if tc.acknowledge {
				args = append(args, "--transcript-consent")
			}
			var out bytes.Buffer
			if code := runSlicePlan(&out, args); code != tc.wantExit {
				t.Fatalf("exit = %d, want %d: %s", code, tc.wantExit, out.String())
			}
			if fake.promptCalls != tc.wantPromptCall {
				t.Fatalf("summarizer calls = %d, want %d", fake.promptCalls, tc.wantPromptCall)
			}
			if tc.grantExternal && !tc.acknowledge {
				wantCommand := "sentinel slice plan --json --intent-transcript " + quoteCommandArgument("transcript.txt") + " --transcript-consent"
				if !strings.HasSuffix(strings.TrimSpace(out.String()), wantCommand) {
					t.Fatalf("missing exact repeat command in output: %s", out.String())
				}
				if !strings.Contains(out.String(), "transcript.txt") || !strings.Contains(out.String(), "auto") {
					t.Fatalf("missing transcript disclosure target: %s", out.String())
				}
			}
			if tc.grantExternal && tc.acknowledge && !strings.Contains(out.String(), "Transcript sent to auto under this repository's external-diff consent, acknowledged with --transcript-consent.") {
				t.Fatalf("missing acknowledgement disclosure: %s", out.String())
			}
		})
	}
}

func TestRunSlicePlanDoesNotReadTranscriptBeforeBothConsentGates(t *testing.T) {
	prepareRepoForPlan(t)
	grantExternalDiffForPlan(t)
	fake := &adapterSlicePlanFake{promptResponse: "must not run"}
	previous := newAgentAdapterForMessage
	newAgentAdapterForMessage = func(string) (agentadapter.AgentAdapter, error) {
		return fake, nil
	}
	t.Cleanup(func() { newAgentAdapterForMessage = previous })

	if err := os.WriteFile("transcript.txt", []byte("human wanted a protected release"), 0644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := runSlicePlan(&out, []string{"--json", "--intent-transcript", "transcript.txt"}); code != 1 {
		t.Fatalf("exit = %d, want 1: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "would be sent") || strings.Contains(out.String(), "Could not read") {
		t.Fatalf("transcript was read before acknowledgement gate: %s", out.String())
	}
	if fake.promptCalls != 0 {
		t.Fatalf("summarizer calls = %d, want 0", fake.promptCalls)
	}
}

func TestRunSlicePlanResolvesTranscriptFromRepositoryRoot(t *testing.T) {
	prepareRepoForPlan(t)
	grantExternalDiffForPlan(t)
	if err := os.WriteFile("app.go", []byte("package app\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("transcript.txt", []byte("human wanted a protected release"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir("nested", 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir("nested")

	fake := &adapterSlicePlanFake{promptResponse: "Protect the release"}
	previous := newAgentAdapterForMessage
	newAgentAdapterForMessage = func(string) (agentadapter.AgentAdapter, error) { return fake, nil }
	t.Cleanup(func() { newAgentAdapterForMessage = previous })

	var out bytes.Buffer
	if code := runSlicePlan(&out, []string{"--json", "--intent-transcript", "transcript.txt", "--transcript-consent"}); code != 0 {
		t.Fatalf("exit = %d: %s", code, out.String())
	}
	var plan git.SerializedPlan
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Intent != "Protect the release" || fake.promptCalls != 1 {
		t.Fatalf("root-relative transcript plan = %+v, prompt calls = %d", plan, fake.promptCalls)
	}
	for _, change := range plan.Changes {
		if change.Path == "transcript.txt" || change.OldPath == "transcript.txt" {
			t.Fatalf("root-relative transcript remained in plan: %+v", plan.Changes)
		}
	}
}

func TestRunSlicePlanLeavesInRepoTranscriptUncommittedAfterApply(t *testing.T) {
	prepareRepoForPlan(t)
	grantExternalDiffForPlan(t)
	if err := os.WriteFile("app.go", []byte("package app\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("transcript.txt", []byte("human wanted a protected release"), 0644); err != nil {
		t.Fatal(err)
	}
	fake := &adapterSlicePlanFake{promptResponse: "Protect the release"}
	previous := newAgentAdapterForMessage
	newAgentAdapterForMessage = func(string) (agentadapter.AgentAdapter, error) { return fake, nil }
	t.Cleanup(func() { newAgentAdapterForMessage = previous })

	var planOut bytes.Buffer
	if code := runSlicePlan(&planOut, []string{"--json", "--intent-transcript", "transcript.txt", "--transcript-consent"}); code != 0 {
		t.Fatalf("plan exit = %d: %s", code, planOut.String())
	}
	var plan git.SerializedPlan
	if err := json.Unmarshal(planOut.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("plan.json", planOut.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
	answers, err := json.Marshal(git.PlanAnswers{PlanID: plan.PlanID, Answers: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("answers.json", answers, 0644); err != nil {
		t.Fatal(err)
	}
	var applyOut bytes.Buffer
	if code := runSliceApply(&applyOut, []string{"--plan", "plan.json", "--answers", "answers.json"}); code != 0 {
		t.Fatalf("apply exit = %d: %s", code, applyOut.String())
	}
	latest, err := exec.Command("git", "show", "--format=", "--name-only", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(latest), "transcript.txt") {
		t.Fatalf("applied commit included the transcript: %s", latest)
	}
	status, err := exec.Command("git", "status", "--porcelain").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(status), "transcript.txt") {
		t.Fatalf("transcript was not left in the worktree: %s", status)
	}
}

func TestRunSlicePlanRejectsInvalidTranscriptInputsAfterConsent(t *testing.T) {
	for _, tc := range []struct {
		name string
		make func(t *testing.T)
		path string
	}{
		{name: "missing", path: "missing.txt"},
		{name: "non-regular", path: "directory.txt", make: func(t *testing.T) {
			if err := os.Mkdir("directory.txt", 0755); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "empty", path: "empty.txt", make: func(t *testing.T) {
			if err := os.WriteFile("empty.txt", nil, 0644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "oversized", path: "oversized.txt", make: func(t *testing.T) {
			if err := os.WriteFile("oversized.txt", []byte(strings.Repeat("x", intent.MaxTranscriptBytes+1)), 0644); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prepareRepoForPlan(t)
			grantExternalDiffForPlan(t)
			if tc.make != nil {
				tc.make(t)
			}
			fake := &adapterSlicePlanFake{promptResponse: "must not run"}
			previous := newAgentAdapterForMessage
			newAgentAdapterForMessage = func(string) (agentadapter.AgentAdapter, error) { return fake, nil }
			t.Cleanup(func() { newAgentAdapterForMessage = previous })

			var out bytes.Buffer
			if code := runSlicePlan(&out, []string{"--json", "--intent-transcript", tc.path, "--transcript-consent"}); code != 1 {
				t.Fatalf("exit = %d, want 1: %s", code, out.String())
			}
			if fake.promptCalls != 0 {
				t.Fatalf("summarizer calls = %d, want 0", fake.promptCalls)
			}
		})
	}
}

func TestRunSlicePlanTranscriptFailuresPreserveDecisionExitCode(t *testing.T) {
	for _, tc := range []struct {
		name     string
		pending  bool
		wantExit int
	}{
		{name: "normal plan", wantExit: 0},
		{name: "pending decisions", pending: true, wantExit: pendingDecisionsExitCode},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			if err := os.WriteFile("app.go", []byte("package app\n"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile("transcript.txt", []byte("human wanted a protected release"), 0644); err != nil {
				t.Fatal(err)
			}
			fake := &adapterSlicePlanFake{promptErr: errors.New("summary failed")}
			previousAdapter := newAgentAdapterForMessage
			newAgentAdapterForMessage = func(string) (agentadapter.AgentAdapter, error) { return fake, nil }
			t.Cleanup(func() { newAgentAdapterForMessage = previousAdapter })
			previousBuilder := buildSlicePlan
			buildSlicePlan = func(_ git.CommitMessageGenerator, _ git.SemanticSliceOptions) (*git.SerializedPlan, error) {
				plan := &git.SerializedPlan{PlanID: "plan-id", WorktreeState: "worktree-state"}
				if tc.pending {
					plan.PendingDecisions = []git.PendingDecision{{ID: "decision", File: "app.go", Options: []string{git.AnswerBypass, git.AnswerAbort}}}
				}
				return plan, nil
			}
			t.Cleanup(func() { buildSlicePlan = previousBuilder })

			var out bytes.Buffer
			if code := runSlicePlan(&out, []string{"--json", "--intent-transcript", "transcript.txt", "--transcript-consent"}); code != tc.wantExit {
				t.Fatalf("exit = %d, want %d: %s", code, tc.wantExit, out.String())
			}
			if fake.promptCalls != 1 {
				t.Fatalf("summarizer calls = %d, want 1", fake.promptCalls)
			}
			var plan git.SerializedPlan
			if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
				t.Fatal(err)
			}
			if plan.Intent != "" || plan.IntentSource != "" {
				t.Fatalf("failed summary retained intent = %+v", plan)
			}
		})
	}
}

func TestRunSlicePlanTranscriptFailuresWarnWithoutIntent(t *testing.T) {
	for _, tc := range []struct {
		name       string
		factoryErr error
		promptErr  error
	}{
		{name: "adapter unavailable", factoryErr: errors.New("adapter unavailable")},
		{name: "summary failed", promptErr: errors.New("summary failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			if err := os.WriteFile("app.go", []byte("package app\n"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile("transcript.txt", []byte("human wanted a protected release"), 0644); err != nil {
				t.Fatal(err)
			}
			previous := newAgentAdapterForMessage
			newAgentAdapterForMessage = func(string) (agentadapter.AgentAdapter, error) {
				return &adapterSlicePlanFake{promptErr: tc.promptErr}, tc.factoryErr
			}
			t.Cleanup(func() { newAgentAdapterForMessage = previous })

			var out bytes.Buffer
			if code := runSlicePlan(&out, []string{"--json", "--intent-transcript", "transcript.txt", "--transcript-consent"}); code != 0 {
				t.Fatalf("transcript plan exit = %d: %s", code, out.String())
			}
			var plan git.SerializedPlan
			if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
				t.Fatal(err)
			}
			if plan.Intent != "" || plan.IntentSource != "" {
				t.Fatalf("failed summary retained intent = %+v", plan)
			}
			if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0], "no intent") {
				t.Fatalf("failure warnings = %v", plan.Warnings)
			}
		})
	}
}

func TestRunSlicePlanRejectsTranscriptUsageErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "missing file", args: []string{"--intent-transcript", "missing.txt"}},
		{name: "unreadable file", args: []string{"--intent-transcript", "unreadable.txt"}},
		{name: "empty file", args: []string{"--intent-transcript", "empty.txt"}},
		{name: "exclusive flags", args: []string{"--intent", "protect", "--intent-transcript", "transcript.txt"}},
		{name: "acknowledgement without transcript", args: []string{"--transcript-consent"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prepareRepoForPlan(t)
			if tc.name == "unreadable file" {
				if err := os.Mkdir("unreadable.txt", 0755); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "empty file" {
				if err := os.WriteFile("empty.txt", nil, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "exclusive flags" {
				if err := os.WriteFile("transcript.txt", []byte("transcript"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			if code := runSlicePlan(&out, tc.args); code != 1 {
				t.Fatalf("exit = %d, want usage error 1: %s", code, out.String())
			}
		})
	}
}

func intentTranscriptMarkerForTest() string {
	return intent.TranscriptBeginMarker
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

func TestTranscriptRepeatCommandKeepsPathAsOneArgument(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "metacharacter-was-executed")
	path := filepath.Join(t.TempDir(), "transcript with spaces O'Reilly")
	if runtime.GOOS == "windows" {
		path += " & echo retry-command-injected"
	} else {
		path += "; touch " + marker
	}
	command := transcriptRepeatCommand(true, path)

	binDir := t.TempDir()
	capture := filepath.Join(t.TempDir(), "arguments")
	if runtime.GOOS == "windows" {
		sentinel := filepath.Join(binDir, "sentinel.cmd")
		script := "@echo off\r\n> \"%CAPTURE%\" echo \"%~5\"\r\n"
		if err := os.WriteFile(sentinel, []byte(script), 0644); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command("cmd.exe", "/d", "/s", "/c", command)
		cmd.Env = append(os.Environ(), "CAPTURE="+capture, "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("retry command failed: %v (%s)", err, output)
		}
		if strings.Contains(string(output), "retry-command-injected") {
			t.Fatal("retry command evaluated the path's shell metacharacters")
		}
		captured, err := os.ReadFile(capture)
		if err != nil {
			t.Fatal(err)
		}
		want := `"` + path + `"` + "\r\n"
		if string(captured) != want {
			t.Fatalf("retry command argument = %q, want %q", captured, want)
		}
		return
	}

	sentinel := filepath.Join(binDir, "sentinel")
	script := "#!/bin/sh\nprintf '%s\\n' \"$#\" > \"$CAPTURE\"\nfor arg do printf '%s\\n' \"$arg\" >> \"$CAPTURE\"; done\n"
	if err := os.WriteFile(sentinel, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("sh", "-c", command)
	cmd.Env = append(os.Environ(), "CAPTURE="+capture, "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("retry command failed: %v (%s)", err, output)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("retry command evaluated the path's shell metacharacters")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checking injection marker: %v", err)
	}

	captured, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{"6", "slice", "plan", "--json", "--intent-transcript", path, "--transcript-consent", ""}, "\n")
	if string(captured) != want {
		t.Fatalf("retry command arguments = %q, want %q", captured, want)
	}
}
