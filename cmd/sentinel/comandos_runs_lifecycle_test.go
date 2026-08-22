package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
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
