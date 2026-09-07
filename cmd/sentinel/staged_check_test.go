package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/change"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
)

func TestStagedCheckAcceptsStagedCandidateWhileUnstagedWorkExceedsBudget(t *testing.T) {
	prepareStagedCheckRepository(t)
	writeTestLines(t, "staged.go", 120)
	runGit(t, "add", "staged.go")
	writeTestLines(t, "unfinished.go", 450)

	var output bytes.Buffer
	if exitCode := runStagedCheckWith(&output, ".", false, git.MeasureStagedVolume, noOpStagedCohesion); exitCode != 0 {
		t.Fatalf("staged check exit code = %d, want 0\n%s", exitCode, output.String())
	}
	if !strings.Contains(output.String(), "within the review budget") {
		t.Fatalf("staged check did not accept the candidate: %s", output.String())
	}
}

func TestStagedCheckRejectsOverBudgetWithoutChangingIndexOrHistory(t *testing.T) {
	prepareStagedCheckRepository(t)
	writeTestLines(t, "large.go", 401)
	runGit(t, "add", "large.go")
	indexBefore := gitOutput(t, "diff", "--cached", "--binary")
	headBefore := gitOutput(t, "rev-parse", "HEAD")

	var output bytes.Buffer
	if exitCode := runStagedCheckWith(&output, ".", false, git.MeasureStagedVolume, noOpStagedCohesion); exitCode != 1 {
		t.Fatalf("staged check exit code = %d, want 1\n%s", exitCode, output.String())
	}
	if !strings.Contains(output.String(), "Staged commit rejected") {
		t.Fatalf("normal staged rejection was not reported: %s", output.String())
	}
	if strings.Contains(output.String(), "measurement failed") {
		t.Fatalf("normal rejection was confused with measurement failure: %s", output.String())
	}
	if got := gitOutput(t, "diff", "--cached", "--binary"); got != indexBefore {
		t.Fatal("staged rejection changed the index")
	}
	if got := gitOutput(t, "rev-parse", "HEAD"); got != headBefore {
		t.Fatal("staged rejection changed repository history")
	}
}

func TestStagedCheckBlocksMeasurementFailureDistinctly(t *testing.T) {
	var output bytes.Buffer
	if exitCode := runStagedCheckWith(
		&output,
		"worktree",
		false,
		func() (git.PendingVolume, error) {
			return git.PendingVolume{State: "ERROR"}, errors.New("git index diff failed")
		},
		noOpStagedCohesion,
	); exitCode != 1 {
		t.Fatalf("measurement failure exit code = %d, want 1", exitCode)
	}
	if !strings.Contains(output.String(), "Staged measurement failed") {
		t.Fatalf("measurement failure was not reported distinctly: %s", output.String())
	}
	if strings.Contains(output.String(), "Staged commit rejected") {
		t.Fatalf("measurement failure was reported as a normal rejection: %s", output.String())
	}
}

func TestStagedCheckWarnsOnIndependentClustersWithoutMutation(t *testing.T) {
	prepareStagedCheckRepository(t)
	if err := os.MkdirAll(filepath.Join("left"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join("right"), 0755); err != nil {
		t.Fatal(err)
	}
	writeTestLines(t, filepath.Join("left", "change.go"), 20)
	writeTestLines(t, filepath.Join("right", "change.go"), 20)
	runGit(t, "add", "left/change.go", "right/change.go")
	indexBefore := gitOutput(t, "diff", "--cached", "--binary")
	headBefore := gitOutput(t, "rev-parse", "HEAD")

	var output bytes.Buffer
	if exitCode := runStagedCheckWith(&output, ".", false, git.MeasureStagedVolume, computeStagedCohesion); exitCode != 0 {
		t.Fatalf("staged check exit code = %d, want 0\n%s", exitCode, output.String())
	}
	if !strings.Contains(output.String(), "independent cohesion clusters") {
		t.Fatalf("cohesion warning missing: %s", output.String())
	}
	if got := gitOutput(t, "diff", "--cached", "--binary"); got != indexBefore {
		t.Fatal("cohesion warning changed the index")
	}
	if got := gitOutput(t, "rev-parse", "HEAD"); got != headBefore {
		t.Fatal("cohesion warning changed repository history")
	}
}

func TestHookInvokesStagedContractWithPortablePath(t *testing.T) {
	if testing.Short() {
		t.Skip("skips shell hook integration in short mode")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not available in PATH")
	}

	dir := t.TempDir()
	argsPath := filepath.Join(dir, "hook args.txt")
	executable := filepath.Join(dir, "sentinel binary")
	hook := filepath.Join(dir, "pre-commit")
	fake := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ARGS_FILE\"\n"
	if err := os.WriteFile(executable, []byte(fake), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hook, []byte(generateHookScriptFor(executable)), 0755); err != nil {
		t.Fatal(err)
	}

	command := exec.Command("sh", hook)
	command.Env = append(os.Environ(), "ARGS_FILE="+argsPath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("hook failed: %v\n%s", err, output)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("hook did not invoke the sentinel executable: %v", err)
	}
	if got := string(args); got != "check\n--staged\n" {
		t.Fatalf("hook arguments = %q, want check --staged", got)
	}
	if !strings.Contains(generateHookScriptFor(filepath.FromSlash("C:/Program Files/sentinel")), "C:/Program Files/sentinel") {
		t.Fatal("hook script did not normalize Windows paths")
	}
}

func TestStagedCheckJSONDistinguishesRejectionAndMeasurementFailure(t *testing.T) {
	var rejection bytes.Buffer
	if exitCode := runStagedCheckWith(
		&rejection,
		"worktree",
		true,
		func() (git.PendingVolume, error) {
			return git.PendingVolume{Blocking: 401, State: "CRITICAL"}, nil
		},
		noOpStagedCohesion,
	); exitCode != 1 {
		t.Fatalf("rejection exit code = %d, want 1", exitCode)
	}
	var rejectedReport stagedCheckReport
	if err := json.Unmarshal(rejection.Bytes(), &rejectedReport); err != nil {
		t.Fatal(err)
	}
	if !rejectedReport.Rejected || rejectedReport.MeasurementFailed || rejectedReport.Error != "" {
		t.Fatalf("rejection report = %+v", rejectedReport)
	}

	var failure bytes.Buffer
	if exitCode := runStagedCheckWith(
		&failure,
		"worktree",
		true,
		func() (git.PendingVolume, error) {
			return git.PendingVolume{State: "ERROR"}, errors.New("index unavailable")
		},
		noOpStagedCohesion,
	); exitCode != 1 {
		t.Fatalf("failure exit code = %d, want 1", exitCode)
	}
	var failureReport stagedCheckReport
	if err := json.Unmarshal(failure.Bytes(), &failureReport); err != nil {
		t.Fatal(err)
	}
	if failureReport.Rejected || !failureReport.MeasurementFailed || failureReport.Error != "index unavailable" {
		t.Fatalf("failure report = %+v", failureReport)
	}
}

func noOpStagedCohesion([]string) (change.CohesionResult, error) {
	return change.CohesionResult{Clusters: 1}, nil
}

func prepareStagedCheckRepository(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
	runGit(t, "init", "-q", "-b", "main")
	runGit(t, "config", "user.email", "test@vas.sentinel")
	runGit(t, "config", "user.name", "VAS Sentinel Test")
	runGit(t, "config", "core.hooksPath", "no-hooks")
	if err := os.WriteFile("base.txt", []byte("base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, "add", "base.txt")
	runGit(t, "commit", "-q", "-m", "chore: base")
}

func writeTestLines(t *testing.T, path string, count int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("// staged check line\n", count)), 0644); err != nil {
		t.Fatalf("could not write %s: %v", path, err)
	}
}

func runGit(t *testing.T, args ...string) string {
	t.Helper()
	output, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func gitOutput(t *testing.T, args ...string) string {
	t.Helper()
	return runGit(t, args...)
}
