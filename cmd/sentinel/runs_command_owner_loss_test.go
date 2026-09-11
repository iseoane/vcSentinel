package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// Ticket 10 slice 3: CLI-level proof that recovering an orphaned-canceled
// owner-loss stream creates a NEW invocation identity while preserving the
// interrupted attempt's record, that the operator-required class stays a
// zero-write surface end to end, and that --expected-revision is rejected
// outside the resume path.

func TestRunsRecoverOwnerLossCreatesFreshInvocationEndToEnd(t *testing.T) {
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	commonDir, err := git.GetGitCommonDir(worktree)
	if err != nil {
		t.Fatal(err)
	}
	backing := store.NewStore(commonDir)
	runID := string(appendReconciledFixtureStream(t, backing, "candidate:owner-loss", terminatingExtra()))
	page, err := backing.ReadEvents(runID, 0, 128)
	if err != nil {
		t.Fatal(err)
	}
	interruptedFrames := page.Events
	interruptedInvocation := interruptedFrames[len(interruptedFrames)-1].InvocationID

	replaceRunsAgent(t, &fakeRunsAgent{output: "resumed output"})
	output, code := captureRunsOutput(t, func(w io.Writer) int {
		return executeRuns(w, worktree, []string{"recover", "--run", runID, "--json"})
	})
	if code != runExitSuccess {
		t.Fatalf("recover exit = %d, want %d, output:\n%s", code, runExitSuccess, output)
	}
	result := decodeRunsJSON(t, output)
	freshInvocation, _ := result["invocation_id"].(string)
	if freshInvocation == "" || freshInvocation == interruptedInvocation {
		t.Fatalf("recover result = %v, want a fresh invocation identity distinct from %s", result, interruptedInvocation)
	}
	if result["state"] != "succeeded" || result["run_id"] != runID {
		t.Fatalf("recover result = %v, want the resumed run settled successfully", result)
	}
	metrics, metricsErr := backing.ReadExecutionMetrics(runID)
	if metricsErr != nil {
		t.Fatal(metricsErr)
	}
	if metrics == nil || metrics.RunID != runID {
		t.Fatalf("recover metrics = %+v, want immutable snapshot after eventual success", metrics)
	}

	// Old-attempt preservation through a fresh inspection: the interrupted
	// attempt keeps its reconciled canceled outcome and every original frame
	// keeps its content hash.
	fresh := execution.NewController(store.NewStore(commonDir), nil)
	inspection, inspectErr := fresh.Inspect(context.Background(), agentrun.Identity(runID))
	if inspectErr != nil {
		t.Fatal(inspectErr)
	}
	foundCanceled := false
	for _, outcome := range inspection.Outcomes {
		if outcome.InvocationID == interruptedInvocation {
			foundCanceled = outcome.Class == agentrun.OutcomeCancellation &&
				strings.Contains(outcome.Error, "reconciled")
		}
	}
	if !foundCanceled {
		t.Fatalf("outcomes = %+v, want the interrupted attempt's preserved reconciled cancellation", inspection.Outcomes)
	}
	if len(inspection.Events) <= len(interruptedFrames) {
		t.Fatalf("events = %d, want appended evidence after the %d original frames",
			len(inspection.Events), len(interruptedFrames))
	}
	for index, frame := range interruptedFrames {
		if inspection.Events[index].ContentHash != frame.ContentHash {
			t.Fatalf("original frame #%d was rewritten by the recovery flow", frame.Sequence)
		}
	}

	verifyOut, verifyCode := captureRunsOutput(t, func(w io.Writer) int {
		return executeRuns(w, worktree, []string{"verify", "--run", runID})
	})
	if verifyCode != runExitSuccess || !strings.Contains(verifyOut, "valid") {
		t.Fatalf("post-recovery verify = (%d) %s, want a valid verdict", verifyCode, verifyOut)
	}
	scanOut, scanCode := captureRunsOutput(t, func(w io.Writer) int {
		return executeRuns(w, worktree, []string{"recover"})
	})
	if scanCode != runExitSuccess || strings.Contains(scanOut, runID) {
		t.Fatalf("post-recovery scan = (%d) %s, want the settled run gone", scanCode, scanOut)
	}
}

// hashSentinelTree fingerprints every file under the repository's
// vas-sentinel directory so any CLI-level write changes at least one digest.
func hashSentinelTree(t *testing.T, root string) map[string]string {
	t.Helper()
	digests := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		digests[filepath.ToSlash(rel)] = hex.EncodeToString(digest[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return digests
}

func digestsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

func TestRunsRecoverScanOperatorRequiredNeverWritesAtCliLevel(t *testing.T) {
	root := t.TempDir()
	backing := store.NewStore(root)
	appendReconciledFixtureStream(t, backing, "candidate:cli-running-head", nil)
	if err := backing.CreateRun(agentrun.NewLogicalJob(
		agentrun.NewRunRequest(agentrun.Candidate("candidate:cli-zero-events"), agentrun.Prompt("never admitted"), nil)),
		store.RunPolicy{ID: "policy:test"}); err != nil {
		t.Fatal(err)
	}

	before := hashSentinelTree(t, root)

	var textOut bytes.Buffer
	if code := listRecoveries(&textOut, backing, false); code != runExitInvalidState {
		t.Fatalf("scan exit = %d, want %d, output:\n%s", code, runExitInvalidState, textOut.String())
	}
	if !strings.Contains(textOut.String(), "class=operator_required") ||
		!strings.Contains(textOut.String(), "outcome unknown") {
		t.Fatalf("scan did not name the exact missing evidence:\n%s", textOut.String())
	}

	var jsonOut bytes.Buffer
	if code := listRecoveries(&jsonOut, backing, true); code != runExitInvalidState {
		t.Fatalf("JSON scan exit = %d, want %d", code, runExitInvalidState)
	}
	var decoded runsRecoveryOutput
	if err := json.Unmarshal(jsonOut.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Recoveries) != 2 {
		t.Fatalf("JSON recoveries = %+v, want both operator-required rows", decoded.Recoveries)
	}
	for _, row := range decoded.Recoveries {
		// Both operator-required shapes name their exact missing evidence:
		// a running head reports its unknown outcome, a zero-event stream
		// reports the missing admission evidence.
		namesEvidence := strings.Contains(row.Reason, "outcome unknown") ||
			strings.Contains(row.Reason, "missing admission evidence")
		if row.Class != string(store.RecoveryOperatorRequired) || !namesEvidence {
			t.Fatalf("row %+v lost the operator-required class or exact evidence", row)
		}
	}

	after := hashSentinelTree(t, root)
	if !digestsEqual(before, after) {
		t.Fatalf("the recovery listing wrote to the store:\nbefore %v\nafter  %v", before, after)
	}
}

func TestRunsRecoverRejectsExpectedRevisionOutsideResumePath(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		wantIn string
	}{
		{
			name:   "scan mode pins nothing",
			args:   []string{"--expected-revision", "4"},
			wantIn: "read-only scan pins nothing",
		},
		{
			name:   "repair replays the whole verified stream",
			args:   []string{"--repair", "some-run", "--expected-revision", "4"},
			wantIn: "rejects --expected-revision with --repair",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			if code := executeRunsRecover(&out, t.TempDir(), tt.args); code != runExitUsage {
				t.Fatalf("executeRunsRecover(%v) exit = %d, want usage 1, output:\n%s", tt.args, code, out.String())
			}
			if !strings.Contains(out.String(), tt.wantIn) {
				t.Fatalf("usage output = %q, want it to contain %q", out.String(), tt.wantIn)
			}
		})
	}
}

// TestRunsAbortDoesNotClaimSuccessOnALiveHead pins the honesty of the abort
// exit contract. idempotentHeadOf is shared by abort, retry and recover, and it
// accepts a RUNNING head as "the goal already holds". That is true for retry and
// recover — there is already a live attempt — and exactly false for abort: a
// running head is what the abort failed to stop.
//
// Observed in operation on three orphaned runs: `sentinel runs abort` printed
// "✅ accepted=true / resulting state running" and exited 0 while writing
// nothing at all, telling the operator a run was canceled while it was not.
func TestRunsAbortDoesNotClaimSuccessOnALiveHead(t *testing.T) {
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	commonDir, err := git.GetGitCommonDir(worktree)
	if err != nil {
		t.Fatal(err)
	}
	backing := store.NewStore(commonDir)
	// No extra transitions: the head stays running with no live owner in this
	// process, which is the shape of an orphaned run.
	runID := string(appendReconciledFixtureStream(t, backing, "candidate:orphaned-running", nil))
	before, err := backing.ReadEvents(runID, 0, 128)
	if err != nil {
		t.Fatal(err)
	}

	output, code := captureRunsOutput(t, func(w io.Writer) int {
		return executeRuns(w, worktree, []string{"abort", "--run", runID})
	})

	if code == runExitSuccess {
		t.Errorf("abort exit = %d (success) on a run whose head is still running; an operator reading this believes the run was canceled. Output:\n%s", code, output)
	}
	if strings.Contains(output, "✅") {
		t.Errorf("abort rendered a success mark for a run it did not settle:\n%s", output)
	}
	after, err := backing.ReadEvents(runID, 0, 128)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Events) != len(before.Events) {
		t.Errorf("durable events went from %d to %d: a refused abort must write nothing", len(before.Events), len(after.Events))
	}
}

// TestRunsAbortOrphanedSettlesAnOwnerlessRun closes the operator gap the honest
// refusal exposed: a run whose head is running with no live owner had NO way to
// be retired. The recovery classifier is read-only and refuses to guess an
// outcome, and `runs prune` skips non-terminal records, so such a run stayed in
// the scan forever.
//
// The settlement is not invented evidence. It goes through the same
// appendOrphanedCancellationSettlement the daemon-shutdown sweep already uses:
// a canceled frame authored from the verified stream, revision-pinned, and
// carrying a mandatory non-empty reason — the codebase requires that reason
// precisely so an orphaned settlement can never be mistaken for an ordinary
// abort. What the flag adds is the operator decision the classifier was waiting
// for.
func TestRunsAbortOrphanedSettlesAnOwnerlessRun(t *testing.T) {
	worktree := t.TempDir()
	replaceRunsAgent(t, &fakeRunsAgent{})
	initGitRepo(t, worktree)
	commonDir, err := git.GetGitCommonDir(worktree)
	if err != nil {
		t.Fatal(err)
	}
	backing := store.NewStore(commonDir)
	runID := string(appendReconciledFixtureStream(t, backing, "candidate:orphaned-settle", nil))
	before, err := backing.ReadEvents(runID, 0, 128)
	if err != nil {
		t.Fatal(err)
	}

	output, code := captureRunsOutput(t, func(w io.Writer) int {
		return executeRuns(w, worktree, []string{"abort", "--run", runID, "--orphaned", "--reason", "the owning process no longer exists"})
	})
	if code != runExitSuccess {
		t.Fatalf("abort --orphaned exit = %d, want %d; output:\n%s", code, runExitSuccess, output)
	}

	if strings.Contains(output, "invocation )") {
		t.Errorf("the result renders an empty invocation identity:\n%s", output)
	}
	projection, err := backing.ReadDerivedProjection(runID)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Terminal == agentrun.TerminalNone {
		t.Errorf("run is still non-terminal after an orphaned abort: state=%q terminal=%q", projection.State, projection.Terminal)
	}
	after, err := backing.ReadEvents(runID, 0, 128)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Events) != len(before.Events)+1 {
		t.Errorf("durable events went from %d to %d, want exactly one appended settlement", len(before.Events), len(after.Events))
	}

	// The reason must be recorded, or the settlement is indistinguishable from
	// an ordinary abort.
	raw, err := os.ReadFile(filepath.Join(commonDir, "vas-sentinel", "executions", "v1", runID, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "orphan") {
		t.Error("the appended settlement records no orphaning reason")
	}
	// The operator's own words and the provenance disclaimer must both survive
	// into the record: without them the frame is indistinguishable from an
	// ordinary abort, and its basis is invisible.
	if !strings.Contains(string(raw), "the owning process no longer exists") {
		t.Error("the operator reason is not recorded in the settlement")
	}
	if !strings.Contains(string(raw), "no owner-liveness evidence was available") {
		t.Error("the settlement does not record that it rests on an unverified assertion")
	}
}

// TestRunsAbortOrphanedRequiresAReason pins the guard the security review asked
// for. The CLI cannot prove the owner process is gone — no per-run owner
// identity is recorded anywhere in the store, and ErrRunNotActive only speaks
// for the consulted host — so the settlement must never rest on an unrecorded
// assumption. It rests on a named assertion or it does not happen.
func TestRunsAbortOrphanedRequiresAReason(t *testing.T) {
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	commonDir, err := git.GetGitCommonDir(worktree)
	if err != nil {
		t.Fatal(err)
	}
	backing := store.NewStore(commonDir)
	runID := string(appendReconciledFixtureStream(t, backing, "candidate:orphaned-no-reason", nil))
	before, err := backing.ReadEvents(runID, 0, 128)
	if err != nil {
		t.Fatal(err)
	}

	// A blank reason must be refused exactly like an absent one: a settlement
	// whose recorded assertion is whitespace records nothing.
	for _, c := range []struct {
		name string
		args []string
	}{
		{"reason absent", []string{"abort", "--run", runID, "--orphaned"}},
		{"reason blank", []string{"abort", "--run", runID, "--orphaned", "--reason", "   "}},
	} {
		t.Run(c.name, func(t *testing.T) {
			output, code := captureRunsOutput(t, func(w io.Writer) int {
				return executeRuns(w, worktree, c.args)
			})
			if code != runExitUsage {
				t.Errorf("exit = %d, want %d (usage): any other refusal would also pass a mere not-success assertion:\n%s", code, runExitUsage, output)
			}
			if !strings.Contains(output, "--reason") {
				t.Errorf("the refusal does not name the missing flag:\n%s", output)
			}
			after, err := backing.ReadEvents(runID, 0, 128)
			if err != nil {
				t.Fatal(err)
			}
			if len(after.Events) != len(before.Events) {
				t.Errorf("durable events went from %d to %d: a refused settlement must write nothing", len(before.Events), len(after.Events))
			}
		})
	}

	// The pairing holds both ways: --reason without --orphaned would discard
	// the operator's words silently, which is what the flag exists to prevent.
	output, code := captureRunsOutput(t, func(w io.Writer) int {
		return executeRuns(w, worktree, []string{"abort", "--run", runID, "--reason", "without orphaned"})
	})
	if code != runExitUsage {
		t.Errorf("exit = %d, want %d (usage): --reason without --orphaned would discard the operator's words:\n%s", code, runExitUsage, output)
	}

	// The pairing is validated BEFORE any run lookup, controller, host or
	// action identity exists. An identity that does not exist would fail with
	// "run not found" from any path past that block, so a usage exit here is
	// what proves the ordering — the previous assertions passed equally well
	// against the old code, which only checked inside settleOrphanedRun.
	for _, c := range []struct {
		name string
		args []string
	}{
		{"orphaned without reason", []string{"abort", "--run", "does-not-exist", "--orphaned"}},
		{"reason without orphaned", []string{"abort", "--run", "does-not-exist", "--reason", "x"}},
	} {
		t.Run(c.name+" is refused before the run is looked up", func(t *testing.T) {
			out, code := captureRunsOutput(t, func(w io.Writer) int {
				return executeRuns(w, worktree, c.args)
			})
			if code != runExitUsage {
				t.Errorf("exit = %d, want %d (usage): the pairing is checked after the run lookup, so a live run would ignore it silently:\n%s", code, runExitUsage, out)
			}
		})
	}
}

// TestRunsAbortRefusalNamesTheOrphanedExit is the discoverability half: the
// honest refusal must tell the operator how to retire the run, or the fix is
// only reachable by reading the source.
func TestRunsAbortRefusalNamesTheOrphanedExit(t *testing.T) {
	worktree := t.TempDir()
	replaceRunsAgent(t, &fakeRunsAgent{})
	initGitRepo(t, worktree)
	commonDir, err := git.GetGitCommonDir(worktree)
	if err != nil {
		t.Fatal(err)
	}
	backing := store.NewStore(commonDir)
	runID := string(appendReconciledFixtureStream(t, backing, "candidate:orphaned-hint", nil))

	output, code := captureRunsOutput(t, func(w io.Writer) int {
		return executeRuns(w, worktree, []string{"abort", "--run", runID})
	})
	if code == runExitSuccess {
		t.Fatalf("plain abort still reports success on an ownerless run:\n%s", output)
	}
	if !strings.Contains(output, "--orphaned") {
		t.Errorf("the refusal does not name the exit:\n%s", output)
	}
}
