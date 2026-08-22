package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

func TestRunsRespondReachesTerminalOnAwaitingRun(t *testing.T) {
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	runID := seedDurableRun(t, worktree, "awaiting-run",
		runsSeedAdapter{result: execution.AdapterResult{AwaitingDecision: true}})
	replaceRunsAgent(t, &fakeRunsAgent{output: "clarified answer"})

	output, code := captureRunsOutput(t, func(w io.Writer) int {
		return executeRuns(w, worktree, []string{"respond", "--run", string(runID), "--text", "use plan B", "--json"})
	})
	if code != runExitSuccess {
		t.Fatalf("respond exit code = %d, want %d: %s", code, runExitSuccess, output)
	}
	result := decodeRunsJSON(t, output)
	if result["accepted"] != true || result["state"] != "succeeded" {
		t.Fatalf("respond result = %v, want accepted=true ending succeeded", result)
	}

	_, detailCode := captureRunsOutput(t, func(w io.Writer) int {
		return executeRuns(w, worktree, []string{"status", "--run", string(runID), "--json"})
	})
	if detailCode != runExitSuccess {
		t.Fatalf("post-respond status exit code = %d, want success", detailCode)
	}
	// A second respond on the same run is no longer pending: explicit refusal.
	_, repeatCode := captureRunsOutput(t, func(w io.Writer) int {
		return executeRuns(w, worktree, []string{"respond", "--run", string(runID), "--text", "again"})
	})
	if repeatCode != runExitInvalidState {
		t.Fatalf("repeat respond exit code = %d, want %d (invalid state)", repeatCode, runExitInvalidState)
	}
}

func TestRunsRetryRelaunchesFailedRunInsideSameIdentity(t *testing.T) {
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	runID := seedDurableRun(t, worktree, "failed-run",
		runsSeedAdapter{err: errors.New("attempt blew up")})
	replaceRunsAgent(t, &fakeRunsAgent{output: "recovered output"})

	staleRevision := "1"
	_, staleCode := captureRunsOutput(t, func(w io.Writer) int {
		return executeRuns(w, worktree, []string{"retry", "--run", string(runID), "--expected-revision", staleRevision})
	})
	if staleCode != runExitStaleRevision {
		t.Fatalf("stale-revision retry exit code = %d, want %d", staleCode, runExitStaleRevision)
	}

	output, code := captureRunsOutput(t, func(w io.Writer) int {
		return executeRuns(w, worktree, []string{"retry", "--run", string(runID), "--json"})
	})
	if code != runExitSuccess {
		t.Fatalf("retry exit code = %d, want %d: %s", code, runExitSuccess, output)
	}
	result := decodeRunsJSON(t, output)
	if result["state"] != "succeeded" || result["run_id"] != string(runID) {
		t.Fatalf("retry result = %v, want the original run relaunched to succeeded", result)
	}

	// The retried attempt lives in the same run: two outcomes, preserved failure.
	commonDir, err := git.ObtenerGitCommonDir(worktree)
	if err != nil {
		t.Fatal(err)
	}
	fresh := execution.NewController(store.NuevoStore(commonDir), nil)
	inspection, err := fresh.Inspect(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(inspection.Outcomes) != 2 ||
		inspection.Outcomes[0].Class != agentrun.OutcomeFailure ||
		inspection.Outcomes[1].Class != agentrun.OutcomeSuccess {
		t.Fatalf("outcomes = %+v, want failure followed by the retry success", inspection.Outcomes)
	}
}

func TestRunsTerminalStateExitCodesEndToEnd(t *testing.T) {
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	succeededID := seedDurableRun(t, worktree, "terminal-success",
		runsSeedAdapter{result: execution.AdapterResult{Output: "done"}})

	tests := []struct {
		name string
		args []string
		want int
	}{
		{
			name: "missing run is not found",
			args: []string{"status", "--run", "never-admitted"},
			want: runExitRunNotFound,
		},
		{
			name: "retrying a succeeded run is invalid state",
			args: []string{"retry", "--run", string(succeededID)},
			want: runExitInvalidState,
		},
		{
			name: "recovering a final outcome is refused",
			args: []string{"recover", "--run", string(succeededID)},
			want: runExitInvalidState,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			replaceRunsAgent(t, &fakeRunsAgent{output: "unused"})
			output, code := captureRunsOutput(t, func(w io.Writer) int {
				return executeRuns(w, worktree, tt.args)
			})
			if code != tt.want {
				t.Fatalf("exit code = %d, want %d: %s", code, tt.want, output)
			}
		})
	}
}

func TestRunsVerifyDetectsTamperedEventLog(t *testing.T) {
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	intactID := seedDurableRun(t, worktree, "intact-run",
		runsSeedAdapter{result: execution.AdapterResult{Output: "clean"}})
	tamperedID := seedDurableRun(t, worktree, "tampered-run",
		runsSeedAdapter{result: execution.AdapterResult{Output: "will be corrupted"}})

	output, code := captureRunsOutput(t, func(w io.Writer) int {
		return executeRuns(w, worktree, []string{"verify", "--run", string(intactID), "--json"})
	})
	if code != runExitSuccess {
		t.Fatalf("intact verify exit code = %d, want %d: %s", code, runExitSuccess, output)
	}
	verdict := decodeRunsJSON(t, output)
	if verdict["valid"] != true {
		t.Fatalf("intact verdict = %v, want valid", verdict)
	}

	commonDir, err := git.ObtenerGitCommonDir(worktree)
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(commonDir, "vas-sentinel", "executions", "v1", string(tamperedID), "events.jsonl")
	file, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{\"this-line\":\"is not a normalized event\"\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	tamperedOutput, tamperedCode := captureRunsOutput(t, func(w io.Writer) int {
		return executeRuns(w, worktree, []string{"verify", "--run", string(tamperedID), "--json"})
	})
	if tamperedCode != runExitInfrastructure {
		t.Fatalf("tampered verify exit code = %d, want %d: %s", tamperedCode, runExitInfrastructure, tamperedOutput)
	}
	tamperedVerdict := decodeRunsJSON(t, tamperedOutput)
	if tamperedVerdict["valid"] != false || tamperedVerdict["reason"] == "" {
		t.Fatalf("tampered verdict = %v, want valid=false with a concrete reason", tamperedVerdict)
	}
}

func TestRunsRepeatActionsReportIdempotentSuccess(t *testing.T) {
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	replaceRunsAgent(t, &fakeRunsAgent{output: "unused"})

	// Abort twice: the second abort finds a settled run and reports
	// idempotent success instead of invalid state.
	runID := seedDurableRun(t, worktree, "abort-twice",
		runsSeedAdapter{result: execution.AdapterResult{AwaitingDecision: true}})
	out, code := captureRunsOutput(t, func(out io.Writer) int {
		return executeRuns(out, worktree, []string{"abort", "--run", string(runID)})
	})
	if code != 0 {
		t.Fatalf("first abort exit = %d, out %s", code, out)
	}
	out, code = captureRunsOutput(t, func(out io.Writer) int {
		return executeRuns(out, worktree, []string{"abort", "--run", string(runID), "--json"})
	})
	if code != 0 || !strings.Contains(out, `"accepted": true`) || !strings.Contains(out, "canceled") {
		t.Fatalf("repeat abort = (%d) %s, want idempotent success with canceled state", code, out)
	}

	// Retry while the relaunched attempt is still live: the goal already
	// holds, so the repeat is idempotent success.
	blocking := &runsBlockAfterCrash{started: make(chan struct{}), release: make(chan struct{})}
	commonDir, err := git.ObtenerGitCommonDir(worktree)
	if err != nil {
		t.Fatal(err)
	}
	controller := execution.NewController(store.NuevoStore(commonDir), blocking)
	request := agentrun.NewRunRequest(
		agentrun.Candidate("seed:retry-live"), agentrun.Prompt("retry-live seed"), nil)
	handle, err := controller.Start(context.Background(), request, store.RunPolicy{ID: "policy:test"})
	if err != nil {
		t.Fatal(err)
	}
	if completion, err := handle.Wait(context.Background()); err != nil || completion.State != agentrun.StateFailed {
		t.Fatalf("seed attempt = %+v, %v; want failure", completion, err)
	}
	retried, err := controller.Retry(context.Background(), handle.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	<-blocking.started
	t.Cleanup(func() {
		close(blocking.release)
		// Drain the relaunched attempt so its terminal writes land before
		// TempDir removal races with them.
		_, _ = retried.Wait(context.Background())
	})

	out, code = captureRunsOutput(t, func(out io.Writer) int {
		return executeRuns(out, worktree, []string{"retry", "--run", string(handle.RunID), "--json"})
	})
	if code != 0 || !strings.Contains(out, "running") {
		t.Fatalf("repeat retry while live = (%d) %s, want idempotent success with running state", code, out)
	}
}

// runsBlockAfterCrash fails its first invocation and then blocks the retry
// mid-flight so a second operator retry observes a live run.
type runsBlockAfterCrash struct {
	started chan struct{}
	release chan struct{}
	blocked bool
}

func (a *runsBlockAfterCrash) Execute(_ context.Context, _ agentrun.LogicalJob, _ agentrun.InvocationEnvelope, _ string) (execution.AdapterResult, error) {
	if !a.blocked {
		a.blocked = true
		return execution.AdapterResult{}, errors.New("crash")
	}
	close(a.started)
	<-a.release
	return execution.AdapterResult{Output: "late"}, nil
}

func TestRunsLogsAndVerifyJsonShapesAreStable(t *testing.T) {
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	runID := seedDurableRun(t, worktree, "stable-shapes",
		runsSeedAdapter{result: execution.AdapterResult{Output: "done"}})

	firstLogs, code1 := captureRunsOutput(t, func(out io.Writer) int {
		return executeRuns(out, worktree, []string{"logs", "--run", string(runID), "--json"})
	})
	secondLogs, code2 := captureRunsOutput(t, func(out io.Writer) int {
		return executeRuns(out, worktree, []string{"logs", "--run", string(runID), "--json"})
	})
	if code1 != 0 || code2 != 0 || firstLogs != secondLogs {
		t.Fatalf("logs --json not byte-stable across repeats (%d/%d)", code1, code2)
	}
	if strings.Contains(firstLogs, `"events": null`) {
		t.Fatal("exhausted logs must emit an empty array, never null")
	}

	firstVerify, code1 := captureRunsOutput(t, func(out io.Writer) int {
		return executeRuns(out, worktree, []string{"verify", "--run", string(runID), "--json"})
	})
	secondVerify, code2 := captureRunsOutput(t, func(out io.Writer) int {
		return executeRuns(out, worktree, []string{"verify", "--run", string(runID), "--json"})
	})
	if code1 != 0 || code2 != 0 || firstVerify != secondVerify {
		t.Fatalf("verify --json not byte-stable across repeats (%d/%d)", code1, code2)
	}
}
