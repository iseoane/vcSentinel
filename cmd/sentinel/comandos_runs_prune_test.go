package main

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// newPruneTestRepo creates a real temporary git worktree so the command
// resolves its common-dir store exactly like production does.
func newPruneTestRepo(t *testing.T) string {
	t.Helper()
	worktree := t.TempDir()
	if out, err := exec.Command("git", "-C", worktree, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return worktree
}

func pruneRepoStore(t *testing.T, worktree string) *store.Store {
	t.Helper()
	commonDir, err := git.ObtenerGitCommonDir(worktree)
	if err != nil {
		t.Fatal(err)
	}
	return store.NuevoStore(commonDir)
}

// seedPruneTerminalRun admits a run through the real CreateRun/AppendEvent
// machinery and settles it as succeeded, with events timestamped from the
// given instant. It returns the run identity and its first invocation
// identity, which is what review provenance records cite.
func seedPruneTerminalRun(t *testing.T, backing *store.Store, candidate string, start time.Time) (string, string) {
	t.Helper()
	job := agentrun.NewLogicalJob(agentrun.NewRunRequest(
		agentrun.Candidate(candidate), agentrun.Prompt("prune fixture"), nil))
	if err := backing.CreateRun(job, store.RunPolicy{ID: "policy:prune"}); err != nil {
		t.Fatal(err)
	}
	runID := string(job.RunID())
	invocation, err := agentrun.NewRootInvocation(job, 1, agentrun.DecisionStart)
	if err != nil {
		t.Fatal(err)
	}
	transitions := []struct {
		from     agentrun.LifecycleState
		to       agentrun.LifecycleState
		decision agentrun.Decision
	}{
		{agentrun.StateCreated, agentrun.StateQueued, agentrun.DecisionStart},
		{agentrun.StateQueued, agentrun.StateAdmitted, agentrun.DecisionStart},
		{agentrun.StateAdmitted, agentrun.StateRunning, agentrun.DecisionStart},
		{agentrun.StateRunning, agentrun.StateSucceeded, agentrun.DecisionComplete},
	}
	for revision, transition := range transitions {
		event, eventErr := agentrun.NewNormalizedEvent(invocation,
			transition.from, transition.to, transition.decision, start.Add(time.Duration(revision)*time.Second))
		if eventErr != nil {
			t.Fatal(eventErr)
		}
		if _, appendErr := backing.AppendEvent(runID, event, uint64(revision)); appendErr != nil {
			t.Fatal(appendErr)
		}
	}
	return runID, invocation.InvocationID().String()
}

// TestRunsPruneUsageErrors proves every malformed invocation exits through
// the documented usage code without touching any store.
func TestRunsPruneUsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"missing flag", nil},
		{"empty value", []string{"--older-than", ""}},
		{"invalid duration", []string{"--older-than", "not-a-duration"}},
		{"negative duration", []string{"--older-than", "-5h"}},
		{"zero duration", []string{"--older-than", "0s"}},
		{"unknown flag", []string{"--older-than", "720h", "--unexpected"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var out bytes.Buffer
			if code := executeRunsPrune(&out, t.TempDir(), testCase.args); code != runExitUsage {
				t.Fatalf("exit = %d, want %d (output: %s)", code, runExitUsage, out.String())
			}
		})
	}
}

// TestRunsPruneRemovesOnlyOldTerminalUnreferenced proves the command end to
// end on a real repository: JSON report shape, honest per-run decisions,
// and the actual removal of exactly the prunable record.
func TestRunsPruneRemovesOnlyOldTerminalUnreferenced(t *testing.T) {
	worktree := newPruneTestRepo(t)
	backing := pruneRepoStore(t, worktree)
	oldRun, _ := seedPruneTerminalRun(t, backing, "candidate:old-terminal", time.Unix(1000000000, 0).UTC())
	recentRun, _ := seedPruneTerminalRun(t, backing, "candidate:recent-terminal", time.Now())

	var out bytes.Buffer
	if code := executeRunsPrune(&out, worktree, []string{"--older-than", "720h", "--json"}); code != runExitSuccess {
		t.Fatalf("exit = %d (output: %s)", code, out.String())
	}
	var report runsPruneOutput
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("prune output is not valid JSON: %v\n%s", err, out.String())
	}
	if report.Examined != 2 || report.Pruned != 1 || report.Kept != 1 {
		t.Fatalf("report = examined %d, pruned %d, kept %d; want 2/1/1", report.Examined, report.Pruned, report.Kept)
	}
	if len(report.Decisions) != 2 || report.Decisions[0].Action == "" {
		t.Fatalf("decisions incomplete: %+v", report.Decisions)
	}

	ids, listErr := backing.ListExecutionIDs()
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(ids) != 1 || ids[0] != recentRun {
		t.Fatalf("surviving executions = %v, want only the recent run %s", ids, recentRun)
	}
	for _, id := range ids {
		if id == oldRun {
			t.Fatalf("old terminal run %s was not pruned", oldRun)
		}
	}
}

// TestRunsPruneKeepsLedgerReferencedStreams proves the composition guard:
// a terminal-old run whose invocation is cited by an append-only review
// ledger ficha survives pruning with its stable reason.
func TestRunsPruneKeepsLedgerReferencedStreams(t *testing.T) {
	worktree := newPruneTestRepo(t)
	backing := pruneRepoStore(t, worktree)
	runID, invocationID := seedPruneTerminalRun(t, backing, "candidate:ledger-referenced", time.Unix(1000000000, 0).UTC())
	commonDir, err := git.ObtenerGitCommonDir(worktree)
	if err != nil {
		t.Fatal(err)
	}
	ledger := review.NuevoLedger(commonDir)
	err = ledger.GuardarRevision("fixtursha00000000000000000000000000000000", "fixture", "bucket", "model", review.Revision{
		At:     time.Now(),
		Result: "warn",
		Dims: []review.DimensionResult{{
			Dim: "logic", Verdict: "warn",
			InvocationID: invocationID,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if code := executeRunsPrune(&out, worktree, []string{"--older-than", "720h", "--json"}); code != runExitSuccess {
		t.Fatalf("exit = %d (output: %s)", code, out.String())
	}
	if !strings.Contains(out.String(), "review provenance references invocation") {
		t.Fatalf("kept reason missing from report: %s", out.String())
	}
	ids, listErr := backing.ListExecutionIDs()
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(ids) != 1 || ids[0] != runID {
		t.Fatalf("referenced execution was pruned; surviving = %v", ids)
	}
}

// TestRunsPruneHumanOutputListsEveryDecision proves the plain-text surface
// reports one line per examined record, removals and refusals alike.
func TestRunsPruneHumanOutputListsEveryDecision(t *testing.T) {
	worktree := newPruneTestRepo(t)
	backing := pruneRepoStore(t, worktree)
	appendReconciledFixtureStream(t, backing, "candidate:human-old", []struct {
		from     agentrun.LifecycleState
		to       agentrun.LifecycleState
		decision agentrun.Decision
	}{{
		from:     agentrun.StateRunning,
		to:       agentrun.StateSucceeded,
		decision: agentrun.DecisionComplete,
	}})

	var out bytes.Buffer
	if code := executeRunsPrune(&out, worktree, []string{"--older-than", "720h"}); code != runExitSuccess {
		t.Fatalf("exit = %d (output: %s)", code, out.String())
	}
	for _, fragment := range []string{"examined 1", "pruned 1, kept 0", "pruned ("} {
		if !strings.Contains(out.String(), fragment) {
			t.Fatalf("human output missing %q: %s", fragment, out.String())
		}
	}
}
