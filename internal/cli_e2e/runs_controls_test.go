package cli_e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vcSentinel/internal/execution"
	"github.com/ISeoane-Quental/vcSentinel/internal/git"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
)

const cliRunsFakeAgentCountEnv = "VCSENTINEL_CLI_FAKE_RUN_AGENT_COUNT_FILE"

func TestCLIRunsRespondAndAbortPublicBinary(t *testing.T) {
	runner := newCLIRunner(t)
	before := captureCLIExternalState(t, runner)
	defer assertCLIExternalStateUnchanged(t, runner, before)

	seedCLIRepository(t, runner, "test: seed runs control fixture")
	runPublicInit(t, runner)
	stageRunsFakeAgent(t, runner)
	writeProjectConfig(t, runner, runsPublicConfig())

	respondID := seedCLIRunsAwaitingRun(t, runner, "respond")
	respondResult := runner.run("runs", "respond", "--run", respondID, "--text", "continue", "--json")
	var respond cliRunsApplyResult
	decodeCLIRunsJSON(t, respondResult, &respond)
	if respond.RunID != respondID || !respond.Accepted || respond.InvocationID == "" {
		t.Fatalf("runs respond result = %+v, want accepted response for run %q: %s", respond, respondID, respondResult.Diagnostic())
	}
	if respond.State == nil || *respond.State != string(agentrun.StateSucceeded) {
		t.Fatalf("runs respond state = %v, want succeeded: %s", respond.State, respondResult.Diagnostic())
	}

	abortID := seedCLIRunsAwaitingRun(t, runner, "abort")
	abortResult := runner.run("runs", "abort", "--run", abortID, "--json")
	var abort cliRunsApplyResult
	decodeCLIRunsJSON(t, abortResult, &abort)
	if abort.RunID != abortID || !abort.Accepted || abort.InvocationID == "" {
		t.Fatalf("runs abort result = %+v, want accepted cancellation for run %q: %s", abort, abortID, abortResult.Diagnostic())
	}

	statusResult := runner.run("runs", "status", "--run", abortID, "--json")
	var status cliRunsStatusResult
	decodeCLIRunsJSON(t, statusResult, &status)
	if status.RunID != abortID || status.State != string(agentrun.StateCanceled) {
		t.Fatalf("runs abort status = %+v, want canceled run %q: %s", status, abortID, statusResult.Diagnostic())
	}
	if len(status.Outcomes) != 1 || status.Outcomes[0].Class != string(agentrun.OutcomeCancellation) {
		t.Fatalf("runs abort outcomes = %+v, want one cancellation outcome: %s", status.Outcomes, statusResult.Diagnostic())
	}
}

func TestCLIRunsRetryAndRecoverPublicBinary(t *testing.T) {
	runner := newCLIRunner(t)
	before := captureCLIExternalState(t, runner)
	defer assertCLIExternalStateUnchanged(t, runner, before)

	seedCLIRepository(t, runner, "test: seed runs relaunch fixture")
	runPublicInit(t, runner)
	countPath := filepath.Join(t.TempDir(), "fake-run-agent-count")
	if err := os.WriteFile(countPath, []byte("0\n"), 0o600); err != nil {
		t.Fatalf("initialize fake agent count file: %v", err)
	}
	// Keep the state-file binding inside the isolated child-process environment;
	// the real user's environment is never changed by this fixture.
	runner.env = append(runner.env, cliRunsFakeAgentCountEnv+"="+countPath)
	stageCLISequencedFakeAgent(t, runner)
	writeProjectConfig(t, runner, runsPublicConfig())

	firstResult := runner.run("runs", "start", "--prompt", "retry first attempt", "--json")
	var first cliRunsStartResult
	decodeCLIRunsJSON(t, firstResult, &first)
	if first.State != string(agentrun.StateFailed) || first.RunID == "" || first.InvocationID == "" {
		t.Fatalf("first runs start = %+v, want exit-0 failed run: %s", first, firstResult.Diagnostic())
	}

	retryResult := runner.run("runs", "retry", "--run", first.RunID, "--json")
	var retried cliRunsStartResult
	decodeCLIRunsJSON(t, retryResult, &retried)
	if retried.RunID != first.RunID || retried.State != string(agentrun.StateSucceeded) || retried.InvocationID == "" || retried.InvocationID == first.InvocationID {
		t.Fatalf("runs retry = %+v, want same run succeeded with a fresh invocation after %+v: %s", retried, first, retryResult.Diagnostic())
	}

	if err := os.WriteFile(countPath, []byte("0\n"), 0o600); err != nil {
		t.Fatalf("reset fake agent count file: %v", err)
	}
	secondResult := runner.run("runs", "start", "--prompt", "recover first attempt", "--json")
	var second cliRunsStartResult
	decodeCLIRunsJSON(t, secondResult, &second)
	if second.State != string(agentrun.StateFailed) || second.RunID == "" || second.InvocationID == "" {
		t.Fatalf("second runs start = %+v, want exit-0 failed run: %s", second, secondResult.Diagnostic())
	}

	recoverResult := runner.run("runs", "recover", "--run", second.RunID, "--json")
	var recovered cliRunsStartResult
	decodeCLIRunsJSON(t, recoverResult, &recovered)
	if recovered.RunID != second.RunID || recovered.State != string(agentrun.StateSucceeded) || recovered.InvocationID == "" || recovered.InvocationID == second.InvocationID {
		t.Fatalf("runs recover = %+v, want same run succeeded with a fresh invocation after %+v: %s", recovered, second, recoverResult.Diagnostic())
	}

	statusResult := runner.run("runs", "status", "--run", second.RunID, "--json")
	var status cliRunsStatusResult
	decodeCLIRunsJSON(t, statusResult, &status)
	if status.State != string(agentrun.StateSucceeded) || len(status.Outcomes) != 2 {
		t.Fatalf("runs recover status = %+v, want two-attempt success: %s", status, statusResult.Diagnostic())
	}
	if status.Outcomes[0].Class != string(agentrun.OutcomeFailure) || status.Outcomes[1].Class != string(agentrun.OutcomeSuccess) || status.Outcomes[1].InvocationID != recovered.InvocationID {
		t.Fatalf("runs recover outcomes = %+v, want failure followed by recovered invocation %q", status.Outcomes, recovered.InvocationID)
	}
}

type cliRunsApplyResult struct {
	RunID        string  `json:"run_id"`
	InvocationID string  `json:"invocation_id"`
	Accepted     bool    `json:"accepted"`
	State        *string `json:"state,omitempty"`
}

type cliRunsAwaitingAdapter struct{}

func (cliRunsAwaitingAdapter) Execute(context.Context, agentrun.LogicalJob, agentrun.InvocationEnvelope, string) (execution.AdapterResult, error) {
	return execution.AdapterResult{AwaitingDecision: true}, nil
}

func seedCLIRunsAwaitingRun(t *testing.T, runner *cliRunner, name string) string {
	t.Helper()
	commonDir, err := git.GetGitCommonDir(runner.repository)
	if err != nil {
		t.Fatalf("resolve Git common directory for durable seed: %v", err)
	}
	backing := store.NewStore(commonDir)
	controller := execution.NewController(backing, cliRunsAwaitingAdapter{})
	request := agentrun.NewRunRequest(
		agentrun.Candidate("cli-e2e-awaiting:"+name),
		agentrun.Prompt("cli e2e awaiting "+name),
		nil,
	)
	handle, err := controller.Start(context.Background(), request, store.RunPolicy{ID: "policy:cli-e2e"})
	if err != nil {
		t.Fatalf("seed awaiting run %q: %v", name, err)
	}
	waitForCLIRunsProjection(t, backing, controller, handle.RunID, agentrun.StateAwaitingDecision)
	return string(handle.RunID)
}

func waitForCLIRunsProjection(t *testing.T, backing *store.Store, controller *execution.Controller, runID agentrun.Identity, want agentrun.LifecycleState) store.RunProjection {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last store.RunProjection
	var lastStoreErr, lastInspectErr error
	for time.Now().Before(deadline) {
		projection, storeErr := backing.ReadDerivedProjection(string(runID))
		inspection, inspectErr := controller.Inspect(context.Background(), runID)
		lastStoreErr, lastInspectErr = storeErr, inspectErr
		if projection != nil {
			last = *projection
		}
		if storeErr == nil && inspectErr == nil && projection != nil && projection.State == want && inspection.Projection.State == want {
			return *projection
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("durable projection for %s never reached %q: store=%+v store_error=%v inspect_error=%v", runID, want, last, lastStoreErr, lastInspectErr)
	return store.RunProjection{}
}

func stageCLISequencedFakeAgent(t *testing.T, runner *cliRunner) {
	t.Helper()
	directory := t.TempDir()
	if runtime.GOOS == "windows" {
		sourcePath := filepath.Join(directory, "fake_run_agent.go")
		const source = `package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func main() {
	path := os.Getenv("VCSENTINEL_CLI_FAKE_RUN_AGENT_COUNT_FILE")
	if path == "" {
		fmt.Fprintln(os.Stderr, "count-file environment variable is missing")
		os.Exit(90)
	}
	count := 0
	data, err := os.ReadFile(path)
	if err == nil {
		count, err = strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			fmt.Fprintln(os.Stderr, "count-file contents are invalid")
			os.Exit(91)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(os.Stderr, "count-file read failed:", err)
		os.Exit(92)
	}
	count++
	if err := os.WriteFile(path, []byte(strconv.Itoa(count)+"\n"), 0600); err != nil {
		fmt.Fprintln(os.Stderr, "count-file write failed:", err)
		os.Exit(93)
	}
	if count == 1 {
		fmt.Fprintln(os.Stderr, "deterministic fake-run-agent failure")
		os.Exit(23)
	}
	fmt.Println("stable fake-run-agent output")
}
`
		if err := os.WriteFile(sourcePath, []byte(source), 0o644); err != nil {
			t.Fatalf("write sequenced fake agent source: %v", err)
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
			t.Fatalf("build sequenced fake agent: %v\n%s", err, combined)
		}
	} else {
		path := filepath.Join(directory, "fake-run-agent")
		const script = `#!/bin/sh
state=${VCSENTINEL_CLI_FAKE_RUN_AGENT_COUNT_FILE:?count-file environment variable is missing}
count=0
if [ -f "$state" ]; then
  count=$(cat "$state") || exit 92
fi
case "$count" in
  ''|*[!0-9]*) exit 91 ;;
esac
count=$((count + 1))
printf '%s\n' "$count" > "$state" || exit 93
if [ "$count" -eq 1 ]; then
  printf '%s\n' 'deterministic fake-run-agent failure' >&2
  exit 23
fi
printf '%s\n' 'stable fake-run-agent output'
`
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			t.Fatalf("write sequenced fake agent: %v", err)
		}
	}
	prependRunnerPath(t, runner, directory)
}
