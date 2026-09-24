package cli_e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCLIRunsPublicLifecycle(t *testing.T) {
	runner := newCLIRunner(t)
	before := captureCLIExternalState(t, runner)
	defer assertCLIExternalStateUnchanged(t, runner, before)

	seedCLIRepository(t, runner, "test: seed runs fixture")
	runPublicInit(t, runner)
	stageRunsFakeAgent(t, runner)
	writeProjectConfig(t, runner, runsPublicConfig())

	startResult := runner.run("runs", "start", "--prompt", "deterministic prompt", "--json")
	var start cliRunsStartResult
	decodeCLIRunsJSON(t, startResult, &start)
	if start.RunID == "" || start.JobID == "" || start.InvocationID == "" {
		t.Fatalf("runs start omitted a durable identity: %+v", start)
	}
	if start.State != "succeeded" {
		t.Fatalf("runs start state = %q, want succeeded: %s", start.State, startResult.Diagnostic())
	}

	statusResult := runner.run("runs", "status", "--run", start.RunID, "--json")
	var status cliRunsStatusResult
	decodeCLIRunsJSON(t, statusResult, &status)
	if status.RunID != start.RunID || status.JobID != start.JobID || status.State != start.State {
		t.Fatalf("runs status identity/state = %+v, want run=%q job=%q state=%q", status, start.RunID, start.JobID, start.State)
	}
	if status.EventCount == 0 || len(status.Outcomes) == 0 {
		t.Fatalf("runs status has no durable events or outcomes: %+v", status)
	}
	if len(status.Outcomes) != 1 || status.Outcomes[0].InvocationID != start.InvocationID || status.Outcomes[0].Class != "success" {
		t.Fatalf("runs status outcomes = %+v, want one successful invocation %q", status.Outcomes, start.InvocationID)
	}

	logsResult := runner.run("runs", "logs", "--run", start.RunID, "--json")
	var logs cliRunsLogsResult
	decodeCLIRunsJSON(t, logsResult, &logs)
	if logs.RunID != start.RunID || len(logs.Events) == 0 {
		t.Fatalf("runs logs = %+v, want run %q with events", logs, start.RunID)
	}
	if len(logs.Events) != status.EventCount {
		t.Fatalf("runs logs returned %d events, status reported %d", len(logs.Events), status.EventCount)
	}
	for _, event := range logs.Events {
		if event.RunID != start.RunID || event.JobID != start.JobID || event.InvocationID != start.InvocationID {
			t.Fatalf("runs log event identity = %+v, want run=%q job=%q invocation=%q", event, start.RunID, start.JobID, start.InvocationID)
		}
	}
	if got := logs.Events[len(logs.Events)-1].To; got != start.State {
		t.Fatalf("runs logs final transition = %q, want %q", got, start.State)
	}

	verifyResult := runner.run("runs", "verify", "--run", start.RunID, "--json")
	var verification cliRunsVerificationResult
	decodeCLIRunsJSON(t, verifyResult, &verification)
	if !verification.Valid || verification.Events != len(logs.Events) {
		t.Fatalf("runs verify = %+v, want valid=true and %d events", verification, len(logs.Events))
	}

	attachResult := runner.run("runs", "attach", "--run", start.RunID)
	if attachResult.ExitCode != 0 {
		t.Fatalf("runs attach failed:\n%s", attachResult.Diagnostic())
	}
	for _, vocabulary := range []string{
		"🔎 Run " + start.RunID,
		"   job " + start.JobID,
		"   state succeeded ",
		"   🏁 outcome success",
		"   invocations 1, responses 0",
		"decision=start outcome=success",
	} {
		if !strings.Contains(attachResult.Stdout, vocabulary) {
			t.Fatalf("runs attach omitted stable vocabulary %q:\n%s", vocabulary, attachResult.Stdout)
		}
	}

	pruneResult := runner.run("runs", "prune", "--older-than", "1ms", "--json")
	var prune cliRunsPruneResult
	decodeCLIRunsJSON(t, pruneResult, &prune)
	if prune.Examined != 1 || prune.Pruned != 1 || prune.Kept != 0 {
		t.Fatalf("runs prune = %+v, want one pruned run", prune)
	}
	if len(prune.Decisions) != 1 || prune.Decisions[0].RunID != start.RunID || prune.Decisions[0].Action != "pruned" {
		t.Fatalf("runs prune decisions = %+v, want %q pruned", prune.Decisions, start.RunID)
	}

	missingResult := runner.run("runs", "status", "--run", start.RunID, "--json")
	if missingResult.ExitCode != 2 {
		t.Fatalf("status after prune exit code = %d, want 2:\n%s", missingResult.ExitCode, missingResult.Diagnostic())
	}
	missingOutput := missingResult.Stdout + "\n" + missingResult.Stderr
	if !strings.Contains(missingOutput, start.RunID) || !strings.Contains(missingOutput, "does not exist") {
		t.Fatalf("status after prune omitted the public not-found diagnostic:\n%s", missingResult.Diagnostic())
	}
}

type cliRunsStartResult struct {
	RunID        string `json:"run_id"`
	JobID        string `json:"job_id"`
	InvocationID string `json:"invocation_id"`
	State        string `json:"state"`
}

type cliRunsStatusResult struct {
	RunID      string            `json:"run_id"`
	JobID      string            `json:"job_id"`
	State      string            `json:"state"`
	EventCount int               `json:"event_count"`
	Outcomes   []cliRunsOutcome  `json:"outcomes"`
	Responses  []json.RawMessage `json:"responses"`
}

type cliRunsOutcome struct {
	RunID        string `json:"run_id"`
	JobID        string `json:"job_id"`
	InvocationID string `json:"invocation_id"`
	Class        string `json:"class"`
}

type cliRunsLogsResult struct {
	RunID  string         `json:"run_id"`
	Events []cliRunsEvent `json:"events"`
}

type cliRunsEvent struct {
	Sequence     uint64 `json:"sequence"`
	Revision     uint64 `json:"revision"`
	RunID        string `json:"run_id"`
	JobID        string `json:"job_id"`
	InvocationID string `json:"invocation_id"`
	From         string `json:"from"`
	To           string `json:"to"`
	Decision     string `json:"decision"`
}

type cliRunsVerificationResult struct {
	Valid  bool   `json:"valid"`
	Events int    `json:"events"`
	Reason string `json:"reason"`
}

type cliRunsPruneResult struct {
	Examined  int                    `json:"examined"`
	Pruned    int                    `json:"pruned"`
	Kept      int                    `json:"kept"`
	Decisions []cliRunsPruneDecision `json:"decisions"`
}

type cliRunsPruneDecision struct {
	RunID  string `json:"run_id"`
	Action string `json:"action"`
	Reason string `json:"reason"`
}

func decodeCLIRunsJSON(t *testing.T, result commandResult, target any) {
	t.Helper()
	if result.ExitCode != 0 {
		t.Fatalf("durable-runs JSON command failed:\n%s", result.Diagnostic())
	}
	if strings.TrimSpace(result.Stderr) != "" {
		t.Fatalf("durable-runs JSON command wrote unexpected stderr: %q", result.Stderr)
	}
	if err := json.Unmarshal([]byte(result.Stdout), target); err != nil {
		t.Fatalf("durable-runs JSON is invalid: %v\n%s", err, result.Stdout)
	}
}

func stageRunsFakeAgent(t *testing.T, runner *cliRunner) {
	t.Helper()
	directory := t.TempDir()
	if runtime.GOOS == "windows" {
		sourcePath := filepath.Join(directory, "fake_run_agent.go")
		const source = `package main

import "fmt"

func main() {
	fmt.Println("stable fake-run-agent output")
}
`
		if err := os.WriteFile(sourcePath, []byte(source), 0o644); err != nil {
			t.Fatalf("write fake runs agent source: %v", err)
		}
		output := filepath.Join(directory, "fake-run-agent.exe")
		buildEnv := append([]string(nil), runner.env...)
		buildEnv = append(buildEnv,
			"GO111MODULE=off",
			"GOCACHE="+t.TempDir(),
			"GOPROXY=off",
			"GOSUMDB=off",
			"GOTOOLCHAIN=local",
		)
		build := exec.Command(lookupTool(t, "go"), "build", "-o", output, sourcePath)
		build.Dir = directory
		build.Env = buildEnv
		if combined, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build fake runs agent: %v\n%s", err, combined)
		}
	} else {
		path := filepath.Join(directory, "fake-run-agent")
		const script = "#!/bin/sh\nprintf '%s\\n' 'stable fake-run-agent output'\n"
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			t.Fatalf("write fake runs agent: %v", err)
		}
	}
	prependRunnerPath(t, runner, directory)
}

func runsPublicConfig() string {
	return "version: \"2.0\"\nactive_agent: \"fake-run-agent\"\nagents:\n  fake-run-agent:\n    model: \"fake-model\"\n    reasoning_effort: \"low\"\nreview:\n  timeout: 5\n  parallel: 1\n"
}
