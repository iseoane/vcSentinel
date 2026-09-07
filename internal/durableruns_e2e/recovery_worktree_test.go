package durableruns_e2e

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// Scenario g — recovery. An orphaned-canceled run (owner died during its own
// cancellation) is recovered explicitly through the R8 two-step relaunch: the
// reconciled canceled settlement is materialized first, then a fresh
// controller relaunches under a new invocation identity, the interrupted
// attempt keeps its durable record, and the post-recovery scan is clean.

func TestRecoveryOrphanedCanceledRelaunchesExplicitlyAndScanCleansUp(t *testing.T) {
	backing := newE2EStore(t)
	job, interruptedInvocation, seeded := seedOrphanedCancellationStream(t, backing, "recovery-owner-loss")
	runID := job.RunID()

	entries, err := store.ScanRecoveries(backing)
	if err != nil || len(entries) != 1 || entries[0].Class != store.RecoveryOrphanedCanceled {
		t.Fatalf("pre-recovery scan = %+v, %v; want exactly one orphaned_canceled entry", entries, err)
	}

	// The explicit operator recovery runs on a fresh controller over the same
	// store: nothing about the original process survives.
	fresh := execution.NewControllerWithClock(backing, successOutputAdapter("recovered output"), fixedClock())
	handle, err := fresh.Recover(context.Background(), runID, 0)
	if err != nil {
		t.Fatalf("Recover(orphaned-canceled): %v", err)
	}
	if handle.InvocationID == interruptedInvocation {
		t.Fatalf("recovered invocation %s reuses the interrupted attempt's identity", handle.InvocationID)
	}
	completion, err := handle.Wait(context.Background())
	if err != nil || completion.State != agentrun.StateSucceeded {
		t.Fatalf("relaunched completion = %+v, %v; want success", completion, err)
	}

	host := execution.NewInProcessHost(fresh)
	inspection, err := host.Inspect(context.Background(), execution.InspectRequest{
		RunID:       runID,
		AuthContext: execution.AuthContext{Principal: "operator"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Old-attempt preservation: the interrupted attempt keeps a canceled
	// outcome carrying the reconciliation detail, and the seeded frames stay
	// an untouched append-only prefix.
	var canceledOutcome, resumedOutcome bool
	for _, outcome := range inspection.Outcomes {
		switch {
		case outcome.InvocationID == string(interruptedInvocation) &&
			outcome.Class == agentrun.OutcomeCancellation &&
			outcome.Error == "owner death during cancellation was reconciled at explicit operator recovery":
			canceledOutcome = true
		case outcome.InvocationID == string(handle.InvocationID) &&
			outcome.Class == agentrun.OutcomeSuccess:
			resumedOutcome = true
		}
	}
	if !canceledOutcome || !resumedOutcome {
		t.Fatalf("outcomes = %+v, want the preserved reconciled cancellation AND the fresh attempt's success", inspection.Outcomes)
	}
	if len(inspection.Events) <= len(seeded) {
		t.Fatalf("events = %d, want settlement and relaunch appended after the %d seeded frames",
			len(inspection.Events), len(seeded))
	}
	for index, frame := range seeded {
		if inspection.Events[index].ContentHash != frame.ContentHash {
			t.Fatalf("seeded frame %d was rewritten during recovery", frame.Sequence)
		}
	}

	assertRunsInspection(t, backing, fresh, string(runID),
		agentrun.StateSucceeded, agentrun.TerminalSuccess)
	if verification, err := fresh.Verify(context.Background(), runID); err != nil || !verification.Valid {
		t.Fatalf("post-recovery verification = %+v, %v; want an intact hash chain across both invocations", verification, err)
	}
	remaining, err := store.ScanRecoveries(backing)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("post-recovery scan = %+v, %v; want nothing left to recover", remaining, err)
	}
	assertSingleRun(t, backing, string(runID))
}

// Linked-worktree consistency: a durable run admitted from one worktree of a
// repository is observable with IDENTICAL state from a linked worktree,
// because both resolve their store through <git-common-dir>/vas-sentinel.

func TestLinkedWorktreesObserveIdenticalRunStateThroughCommonDirStore(t *testing.T) {
	repoRoot := t.TempDir()
	runGit(t, repoRoot, "init", "-q")
	runGit(t, repoRoot, "config", "user.email", "e2e@vas.sentinel")
	runGit(t, repoRoot, "config", "user.name", "durable-runs-e2e")
	writeRepoFile(t, repoRoot, "tracked.txt", "tracked\n")
	runGit(t, repoRoot, "add", "tracked.txt")
	runGit(t, repoRoot, "commit", "-q", "-m", "initial")

	linkedWorktree := filepath.Join(t.TempDir(), "linked-worktree")
	runGit(t, repoRoot, "worktree", "add", "-q", linkedWorktree, "-b", "linked")

	commonA, err := git.GetGitCommonDir(repoRoot)
	if err != nil {
		t.Fatalf("worktree A common dir: %v", err)
	}
	commonB, err := git.GetGitCommonDir(linkedWorktree)
	if err != nil {
		t.Fatalf("worktree B common dir: %v", err)
	}
	if commonA != commonB {
		t.Fatalf("common directories differ: %s != %s", commonA, commonB)
	}

	// Worktree A admits and settles a durable review run.
	storeA := store.NewStore(commonA)
	writer := execution.NewControllerWithClock(storeA, successOutputAdapter("review from worktree A"), fixedClock())
	handle, err := writer.Start(context.Background(), e2eRequest("linked-worktree-run"), e2ePolicy())
	if err != nil {
		t.Fatal(err)
	}
	completion, err := handle.Wait(context.Background())
	if err != nil || completion.State != agentrun.StateSucceeded {
		t.Fatalf("run from worktree A = %+v, %v; want succeeded", completion, err)
	}
	runID := handle.RunID

	// Worktree B observes through its own store resolution path.
	storeB := store.NewStore(commonB)
	reader := execution.NewControllerWithClock(storeB, nil, fixedClock())

	idsA, err := storeA.ListExecutionIDs()
	if err != nil {
		t.Fatal(err)
	}
	idsB, err := storeB.ListExecutionIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(idsB) != 1 || idsB[0] != string(runID) || !reflect.DeepEqual(idsA, idsB) {
		t.Fatalf("ListExecutionIDs differ: worktree A = %v, worktree B = %v", idsA, idsB)
	}

	fromA, err := writer.Inspect(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	fromB, err := reader.Inspect(context.Background(), runID)
	if err != nil {
		t.Fatalf("worktree B cannot observe the run: %v", err)
	}
	if !reflect.DeepEqual(fromA, fromB) {
		t.Fatalf("inspection differs between worktrees:\nA: %+v\nB: %+v", fromA, fromB)
	}

	reconciledA, err := storeA.ReadReconciledProjection(string(runID))
	if err != nil {
		t.Fatal(err)
	}
	reconciledB, err := storeB.ReadReconciledProjection(string(runID))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reconciledA, reconciledB) {
		t.Fatalf("reconciled projections differ:\nA: %+v\nB: %+v", reconciledA, reconciledB)
	}
	if reconciledB.State != agentrun.StateSucceeded || reconciledB.Terminal != agentrun.TerminalSuccess {
		t.Fatalf("observed projection = %s/%s, want succeeded/success",
			reconciledB.State, reconciledB.Terminal)
	}
}

func TestRecoverRefusesUnknownRunWithoutFabricatingAnything(t *testing.T) {
	backing := newE2EStore(t)
	fresh := execution.NewControllerWithClock(backing, successOutputAdapter("unused"), fixedClock())
	_, err := fresh.Recover(context.Background(), agentrun.Identity("never-admitted"), 0)
	if !errors.Is(err, execution.ErrRunNotActive) && err == nil {
		t.Fatalf("Recover(unknown run) = %v; want an explicit refusal", err)
	}
	ids, listErr := backing.ListExecutionIDs()
	if listErr != nil || len(ids) != 0 {
		t.Fatalf("ListExecutionIDs after refusal = %v, %v; want an empty store", ids, listErr)
	}
}
