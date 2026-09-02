package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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

// TestCollectProvenanceReferencesIncludesRefutedFindingInvocations drives
// the real collector over a real seeded ledger ficha (ticket 13 acceptance
// criterion 3): a dimension result carries its producer invocation while one
// of its raw v2 hallazgos carries the DIFFERENT admitted refuter invocation
// stamped at finalization. Aggregation drops refuted findings, so only the
// raw Dims scan can protect the refuter stream — both identities must land
// in the reference set.
func TestCollectProvenanceReferencesIncludesRefutedFindingInvocations(t *testing.T) {
	worktree := newPruneTestRepo(t)
	backing := pruneRepoStore(t, worktree)
	commonDir, err := git.ObtenerGitCommonDir(worktree)
	if err != nil {
		t.Fatal(err)
	}
	producerInvocation := "inv-producer-dim-1"
	refuterInvocation := "inv-refuter-finding-2"
	ledger := review.NuevoLedger(commonDir)
	err = ledger.GuardarRevision("fixtursha00000000000000000000000000000001", "fixture", "bucket", "model", review.Revision{
		At:     time.Now(),
		Result: "warn",
		Dims: []review.DimensionResult{{
			Dim:          "logic",
			Verdict:      "warn",
			InvocationID: producerInvocation,
			Hallazgos: []review.Hallazgo{{
				Fingerprint:  "prune-refutation-fixture-fingerprint",
				Dimension:    "logic",
				Status:       review.StatusRefuted,
				Description:  "fixture refuted finding",
				InvocationID: refuterInvocation,
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	references, refsErr := collectProvenanceReferences(worktree, backing)
	if refsErr != nil {
		t.Fatalf("collectProvenanceReferences() error = %v", refsErr)
	}
	for _, invocation := range []string{producerInvocation, refuterInvocation} {
		if !references[invocation] {
			t.Fatalf("collectProvenanceReferences() = %v, missing cited identity %q", references, invocation)
		}
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

// TestCollectProvenanceReferencesIncludesLinkedWorktreeLedgers is FU-12 stated
// as an oracle rather than as prose. The collector exists so that a prune never
// destroys an execution stream that review evidence still cites, and its own
// contract is to fail closed when provenance is unreadable, "because that is
// exactly how referenced streams get destroyed".
//
// A ficha written from a linked worktree is not an unreadable read. It is an
// absent one. review.NuevoLedger is anchored at the checkout's gitDir, so the
// main checkout writes to <gitCommonDir>/vas-sentinel only because its two
// paths coincide, while a linked worktree writes to
// <gitCommonDir>/worktrees/<name>/vas-sentinel. The collector reads the common
// directory alone, so the identity below is cited by real review evidence and
// invisible to the guard, and the stream it protects is prunable.
//
// Delegating implementation to a writer in a dedicated worktree is the
// mandated workflow here, so this is the normal path and not an edge case.
func TestCollectProvenanceReferencesIncludesLinkedWorktreeLedgers(t *testing.T) {
	worktree := newPruneTestRepo(t)
	backing := pruneRepoStore(t, worktree)

	// A commit is required before a linked worktree can be added.
	correr := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", worktree}, args...)...)
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"), "HOME=" + worktree,
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(worktree, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	correr("add", "a.txt")
	correr("commit", "-qm", "first")

	enlazado := filepath.Join(t.TempDir(), "linked")
	correr("worktree", "add", "-q", "--detach", enlazado)

	// The ledger the linked worktree writes to, obtained the way the review
	// command obtains it: from its own gitDir, not from the common directory.
	gitDirEnlazado, err := git.ObtenerGitDirDe(enlazado)
	if err != nil {
		t.Fatal(err)
	}
	commonDir, err := git.ObtenerGitCommonDir(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if gitDirEnlazado == commonDir {
		t.Fatalf("the linked worktree shares the common directory (%q); the fixture no longer exercises the split", commonDir)
	}

	invocacionEnlazada := "inv-producer-from-linked-worktree"
	ledger := review.NuevoLedger(gitDirEnlazado)
	if err := ledger.GuardarRevision("fixtursha00000000000000000000000000000002", "fixture", "bucket", "model", review.Revision{
		At:     time.Now(),
		Result: "warn",
		Dims: []review.DimensionResult{{
			Dim:          "logic",
			Verdict:      "warn",
			InvocationID: invocacionEnlazada,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	references, refsErr := collectProvenanceReferences(worktree, backing)
	if refsErr != nil {
		t.Fatalf("collectProvenanceReferences() error = %v", refsErr)
	}
	if !references[invocacionEnlazada] {
		t.Fatalf("collectProvenanceReferences() = %v, missing %q cited by a ficha in the linked worktree ledger %q; a prune would destroy the stream it protects",
			references, invocacionEnlazada, gitDirEnlazado)
	}
}
