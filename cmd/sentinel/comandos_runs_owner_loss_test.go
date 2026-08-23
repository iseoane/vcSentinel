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
	commonDir, err := git.ObtenerGitCommonDir(worktree)
	if err != nil {
		t.Fatal(err)
	}
	backing := store.NuevoStore(commonDir)
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

	// Old-attempt preservation through a fresh inspection: the interrupted
	// attempt keeps its reconciled canceled outcome and every original frame
	// keeps its content hash.
	fresh := execution.NewController(store.NuevoStore(commonDir), nil)
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
	backing := store.NuevoStore(root)
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
