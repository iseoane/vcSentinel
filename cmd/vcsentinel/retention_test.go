// T9.5 acceptance: event-driven retention collects the execution streams
// of published commits while leaving measurement byte-identical.
//
// The fixture pins every half of the contract at once: a published commit
// whose run is measured is collected with its snapshot surviving; an
// unpublished commit keeps its run; a published commit whose run has no
// snapshot keeps it (absence is unknown, never zero); and the published
// record carries a confirmed finding, so any implementation that deleted
// records instead of streams would move the metrics bytes it must preserve.
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

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vcSentinel/internal/gate"
	"github.com/ISeoane-Quental/vcSentinel/internal/git"
	"github.com/ISeoane-Quental/vcSentinel/internal/metrics"
	"github.com/ISeoane-Quental/vcSentinel/internal/review"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
)

func retentionGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
		return ""
	} else {
		return string(out)
	}
}

func TestRetentionCollectsPublishedRunsAndKeepsMeasurementIdentical(t *testing.T) {
	worktree := t.TempDir()
	retentionGit(t, worktree, "init", "-q", "-b", "main")
	retentionGit(t, worktree, "config", "user.email", "retention@test")
	retentionGit(t, worktree, "config", "user.name", "retention")
	retentionGit(t, worktree, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(worktree, "a.txt"), []byte("a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	retentionGit(t, worktree, "add", "a.txt")
	retentionGit(t, worktree, "commit", "-q", "-m", "published")
	shaPublished := retentionGit(t, worktree, "rev-parse", "HEAD")
	shaPublished = strings.TrimSpace(shaPublished)
	retentionGit(t, worktree, "update-ref", "refs/remotes/origin/main", shaPublished)
	retentionGit(t, worktree, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(worktree, "b.txt"), []byte("b\n"), 0644); err != nil {
		t.Fatal(err)
	}
	retentionGit(t, worktree, "add", "b.txt")
	retentionGit(t, worktree, "commit", "-q", "-m", "unpublished")
	shaUnpublished := strings.TrimSpace(retentionGit(t, worktree, "rev-parse", "HEAD"))

	backing := pruneRepoStore(t, worktree)
	commonDir, err := git.GetGitCommonDir(worktree)
	if err != nil {
		t.Fatal(err)
	}
	ancient := time.Unix(1000000000, 0).UTC()
	seedMeasured := func(candidate string) (string, string) {
		t.Helper()
		runID, invocationID := seedPruneTerminalRun(t, backing, candidate, ancient)
		if err := backing.SaveExecutionMetrics(store.ExecutionMetrics{Version: store.ExecutionMetricsSchemaVersion, RunID: runID}); err != nil {
			t.Fatalf("SaveExecutionMetrics() error = %v", err)
		}
		return runID, invocationID
	}
	runPublished, invPublished := seedMeasured("candidate:published")
	runUnpublished, invUnpublished := seedMeasured("candidate:unpublished")
	runUnmeasured, invUnmeasured := seedPruneTerminalRun(t, backing, "candidate:unmeasured", ancient)
	// A failed published run with a faithful finalize fold (the outcome
	// cast plus one semantic class): the breakdown must survive
	// collection exactly, which is what catches double counting.
	runFailed, invFailed := seedPruneFailedRun(t, backing, ancient)
	// A contradictory published run: terminal success with a failing
	// snapshot (the corrective-retry shape). Agreement, not mere
	// presence, authorizes collection, so this one stays.
	runContradicted, invContradicted := seedPruneTerminalRun(t, backing, "candidate:contradicted", ancient)
	if err := backing.SaveExecutionMetrics(store.ExecutionMetrics{
		Version:  store.ExecutionMetricsSchemaVersion,
		RunID:    runContradicted,
		Failures: []store.ExecutionFailure{{Class: store.FailureInvalidOutput}},
	}); err != nil {
		t.Fatalf("SaveExecutionMetrics() error = %v", err)
	}

	ledger := review.NewLedger(commonDir)
	saveRecord := func(sha, invocation string) {
		t.Helper()
		if err := ledger.SaveRevision(sha[:40], "fixture", "bucket", "model", review.Revision{
			At:     ancient,
			Result: "ok",
			// AggregatedFindings carries the durable v2 finding whose
			// InvocationID is the provenance prune/retention must keep.
			AggregatedFindings: []review.Finding{{
				Fingerprint:  "retention-finding-" + sha[:8],
				Dimension:    "logic",
				Status:       review.StatusConfirmed,
				Description:  "fixture confirmed finding",
				InvocationID: invocation,
			}},
			Dims: []review.DimensionResult{{
				Dim: "logic", Verdict: "ok",
				InvocationID: invocation,
				// Findings carries the metrics-visible raw finding;
				// AggregatedFindings above carries provenance. Both cite
				// the run.
				Findings: []review.ReviewFinding{{
					File:        "a.go",
					Line:        10,
					Severity:    review.SevCritical,
					Description: "fixture confirmed finding",
					Status:      review.StatusConfirmed,
				}},
			}},
		}); err != nil {
			t.Fatalf("SaveRevision(%s) error = %v", sha, err)
		}
	}
	saveRecord(shaPublished, invPublished)
	saveRecord(shaUnpublished, invUnpublished)
	saveRecord(shaPublished, invUnmeasured)
	saveRecord(shaPublished, invFailed)
	saveRecord(shaPublished, invContradicted)

	metricsBefore, err := metrics.AggregateStore(commonDir)
	if err != nil {
		t.Fatalf("AggregateStore() before retention error = %v", err)
	}
	before, err := json.Marshal(metricsBefore)
	if err != nil {
		t.Fatal(err)
	}
	if metricsBefore.Findings.Observed == 0 || metricsBefore.Findings.Confirmed == 0 || metricsBefore.Executions.MeasuredRuns != 4 {
		t.Fatalf("fixture is evidence-free (findings=%d confirmed=%d measured=%d): the invariance check would pass vacuously",
			metricsBefore.Findings.Observed, metricsBefore.Findings.Confirmed, metricsBefore.Executions.MeasuredRuns)
	}

	report, published, undecidable, err := retainPublishedDetail(worktree)
	if err != nil {
		t.Fatalf("retainPublishedDetail() error = %v", err)
	}
	if len(published) != 1 || published[0] != shaPublished[:40] {
		t.Fatalf("published = %v, want exactly [%s]", published, shaPublished)
	}
	if len(undecidable) != 0 {
		t.Fatalf("undecidable = %v, want none: every fixture SHA resolves", undecidable)
	}
	if report.Pruned != 2 {
		t.Fatalf("pruned = %d, want 2 (the measured published runs): %+v", report.Pruned, report.Decisions)
	}

	stillThere := func(runID string) bool {
		t.Helper()
		ids, err := backing.ListExecutionIDs()
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range ids {
			if id == runID {
				return true
			}
		}
		return false
	}
	if stillThere(runPublished) {
		t.Fatalf("measured published run %s was not collected", runPublished)
	}
	if stillThere(runFailed) {
		t.Fatalf("measured published failed run %s was not collected", runFailed)
	}
	if !stillThere(runUnpublished) {
		t.Fatalf("unpublished run %s was collected", runUnpublished)
	}
	if !stillThere(runUnmeasured) {
		t.Fatalf("unmeasured published run %s was collected without a snapshot", runUnmeasured)
	}
	if !stillThere(runContradicted) {
		t.Fatalf("contradicted published run %s was collected despite disagreeing evidence", runContradicted)
	}
	for _, collected := range []string{runPublished, runFailed} {
		if snapshot, err := backing.ReadExecutionMetrics(collected); err != nil || snapshot == nil {
			t.Fatalf("snapshot of collected run %s must survive: got=%v err=%v", collected, snapshot, err)
		}
	}
	for _, sha := range []string{shaPublished, shaUnpublished} {
		if record, err := ledger.ReadRecord(sha[:40]); err != nil || record == nil {
			t.Fatalf("record %s must survive retention: record=%v err=%v", sha, record, err)
		}
	}

	metricsAfter, err := metrics.AggregateStore(commonDir)
	if err != nil {
		t.Fatalf("AggregateStore() after retention error = %v", err)
	}
	after, err := json.Marshal(metricsAfter)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("metrics moved under retention:\nbefore %s\nafter  %s", before, after)
	}
}

func TestRetentionSkipsUndecidableRepositories(t *testing.T) {
	worktree := t.TempDir()
	if _, _, _, err := retainPublishedDetail(worktree); err == nil {
		t.Fatal("retention outside a repository must fail closed, not collect nothing with success")
	} else if !strings.Contains(err.Error(), "usable") {
		t.Fatalf("undecidable repository must fail on the usability anchor, got: %v", err)
	}
}

// TestGateTriggersRetentionOnlyOnPrePush pins the trigger boundary: the
// retention pass runs after a pre-push gate decision and never for other
// stages. The fixture run is uncited and measured, so the only thing that
// can keep it under pre-commit is the stage gate itself.
func TestGateTriggersRetentionOnlyOnPrePush(t *testing.T) {
	worktree := t.TempDir()
	retentionGit(t, worktree, "init", "-q", "-b", "main")
	retentionGit(t, worktree, "config", "user.email", "retention@test")
	retentionGit(t, worktree, "config", "user.name", "retention")
	retentionGit(t, worktree, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(worktree, "a.txt"), []byte("a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	retentionGit(t, worktree, "add", "a.txt")
	retentionGit(t, worktree, "commit", "-q", "-m", "base")
	retentionGit(t, worktree, "update-ref", "refs/remotes/origin/main", "HEAD")

	backing := pruneRepoStore(t, worktree)
	runID, _ := seedPruneTerminalRun(t, backing, "candidate:trigger", time.Unix(1000000000, 0).UTC())
	if err := backing.SaveExecutionMetrics(store.ExecutionMetrics{Version: store.ExecutionMetricsSchemaVersion, RunID: runID}); err != nil {
		t.Fatalf("SaveExecutionMetrics() error = %v", err)
	}
	listed := func() bool {
		t.Helper()
		ids, err := backing.ListExecutionIDs()
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range ids {
			if id == runID {
				return true
			}
		}
		return false
	}

	var out bytes.Buffer
	if code := finalizeGateWithDetails(&out, worktree, "pre-commit", "PASS", nil, ""); code != 0 {
		t.Fatalf("pre-commit gate exit = %d, want 0", code)
	}
	if !listed() {
		t.Fatal("pre-commit gate collected execution detail: retention must trigger on pre-push only")
	}
	out.Reset()
	if code := finalizeGateWithDetails(&out, worktree, "pre-push", "PASS", nil, ""); code != 0 {
		t.Fatalf("pre-push gate exit = %d, want 0", code)
	}
	if listed() {
		t.Fatalf("pre-push gate did not collect the uncited measured run %s", runID)
	}
	if !strings.Contains(out.String(), "retention: collected 1 execution stream(s)") {
		t.Fatalf("pre-push gate output missing the retention line: %q", out.String())
	}
	// A blocking verdict still collects already-published detail: the
	// predicate covers only ancestors of origin/main, never the rejected
	// HEAD. Verdict-independence is the documented trigger contract.
	runBlocked, _ := seedPruneTerminalRun(t, backing, "candidate:blocked-verdict", time.Unix(1000000000, 0).UTC())
	if err := backing.SaveExecutionMetrics(store.ExecutionMetrics{Version: store.ExecutionMetricsSchemaVersion, RunID: runBlocked}); err != nil {
		t.Fatalf("SaveExecutionMetrics() error = %v", err)
	}
	out.Reset()
	wantCode := gate.ExitCode(gate.StateValidationFailed)
	if code := finalizeGateWithDetails(&out, worktree, "pre-push", gate.StateValidationFailed, nil, ""); code != wantCode {
		t.Fatalf("blocking pre-push gate exit = %d, want %d", code, wantCode)
	}
	stillListed := false
	ids, err := backing.ListExecutionIDs()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if id == runBlocked {
			stillListed = true
		}
	}
	if stillListed {
		t.Fatalf("blocking pre-push gate kept the uncited measured run %s", runBlocked)
	}
	if snapshot, err := backing.ReadExecutionMetrics(runID); err != nil || snapshot == nil {
		t.Fatalf("snapshot of the collected run must survive: got=%v err=%v", snapshot, err)
	}
}

// TestRetentionKeepsUndecidableRecordsWithoutVetoingThePass proves the
// per-record fail-closed rule: a record whose commit cannot be resolved
// keeps its streams protected while the rest of the pass still collects.
// SaveRevision never validates the SHA, so a record can cite an object
// the object store does not have — exactly the shape that must not veto
// collection of its decidable siblings.
func TestRetentionKeepsUndecidableRecordsWithoutVetoingThePass(t *testing.T) {
	worktree := t.TempDir()
	retentionGit(t, worktree, "init", "-q", "-b", "main")
	retentionGit(t, worktree, "config", "user.email", "retention@test")
	retentionGit(t, worktree, "config", "user.name", "retention")
	retentionGit(t, worktree, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(worktree, "a.txt"), []byte("a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	retentionGit(t, worktree, "add", "a.txt")
	retentionGit(t, worktree, "commit", "-q", "-m", "published")
	shaPublished := strings.TrimSpace(retentionGit(t, worktree, "rev-parse", "HEAD"))
	retentionGit(t, worktree, "update-ref", "refs/remotes/origin/main", shaPublished)
	const shaUnresolvable = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

	backing := pruneRepoStore(t, worktree)
	commonDir, err := git.GetGitCommonDir(worktree)
	if err != nil {
		t.Fatal(err)
	}
	ancient := time.Unix(1000000000, 0).UTC()
	seedMeasured := func(candidate string) (string, string) {
		t.Helper()
		runID, invocationID := seedPruneTerminalRun(t, backing, candidate, ancient)
		if err := backing.SaveExecutionMetrics(store.ExecutionMetrics{Version: store.ExecutionMetricsSchemaVersion, RunID: runID}); err != nil {
			t.Fatalf("SaveExecutionMetrics() error = %v", err)
		}
		return runID, invocationID
	}
	runProtected, invProtected := seedMeasured("candidate:undecidable")
	runCollected, invCollected := seedMeasured("candidate:published")

	ledger := review.NewLedger(commonDir)
	cite := func(sha, invocation string) {
		t.Helper()
		if err := ledger.SaveRevision(sha, "fixture", "bucket", "model", review.Revision{
			At:     ancient,
			Result: "ok",
			Dims: []review.DimensionResult{{
				Dim: "logic", Verdict: "ok",
				InvocationID: invocation,
			}},
		}); err != nil {
			t.Fatalf("SaveRevision(%s) error = %v", sha, err)
		}
	}
	cite(shaUnresolvable, invProtected)
	cite(shaPublished, invCollected)

	report, published, undecidable, err := retainPublishedDetail(worktree)
	if err != nil {
		t.Fatalf("one undecidable record must not veto the pass: %v", err)
	}
	if len(published) != 1 || published[0] != shaPublished {
		t.Fatalf("published = %v, want [%s]", published, shaPublished)
	}
	if len(undecidable) != 1 || undecidable[0] != shaUnresolvable {
		t.Fatalf("undecidable = %v, want [%s]", undecidable, shaUnresolvable)
	}
	if report.Pruned != 1 {
		t.Fatalf("pruned = %d, want 1 (the decidable published run)", report.Pruned)
	}
	listed := func(runID string) bool {
		t.Helper()
		ids, err := backing.ListExecutionIDs()
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range ids {
			if id == runID {
				return true
			}
		}
		return false
	}
	if !listed(runProtected) {
		t.Fatalf("run cited by the undecidable record %s was released", runProtected)
	}
	if listed(runCollected) {
		t.Fatalf("decidable published run %s was not collected", runCollected)
	}
}

// TestRetentionAbortsOnCorruptRecord proves the other half of the failure
// taxonomy: an unreadable record (as opposed to an undecidable commit) has
// no references to annotate, so the pass aborts instead of collecting
// under partially known provenance.
func TestRetentionAbortsOnCorruptRecord(t *testing.T) {
	worktree := t.TempDir()
	retentionGit(t, worktree, "init", "-q", "-b", "main")
	retentionGit(t, worktree, "config", "user.email", "retention@test")
	retentionGit(t, worktree, "config", "user.name", "retention")
	retentionGit(t, worktree, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(worktree, "a.txt"), []byte("a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	retentionGit(t, worktree, "add", "a.txt")
	retentionGit(t, worktree, "commit", "-q", "-m", "base")
	sha := strings.TrimSpace(retentionGit(t, worktree, "rev-parse", "HEAD"))
	retentionGit(t, worktree, "update-ref", "refs/remotes/origin/main", sha)

	backing := pruneRepoStore(t, worktree)
	commonDir, err := git.GetGitCommonDir(worktree)
	if err != nil {
		t.Fatal(err)
	}
	ledger := review.NewLedger(commonDir)
	runID, invocationID := seedPruneTerminalRun(t, backing, "candidate:corrupt-record", time.Unix(1000000000, 0).UTC())
	if err := ledger.SaveRevision(sha, "fixture", "bucket", "model", review.Revision{
		At:     time.Now(),
		Result: "ok",
		Dims:   []review.DimensionResult{{Dim: "logic", Verdict: "ok", InvocationID: invocationID}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ledger.RecordPath(sha), []byte("{corrupt"), 0644); err != nil {
		t.Fatal(err)
	}

	if _, _, _, err := retainPublishedDetail(worktree); err == nil {
		t.Fatal("corrupt record must abort the pass, not collect under partial provenance")
	} else if !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("corrupt record error = %v, want the unreadable-record refusal", err)
	}
	ids, err := backing.ListExecutionIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != runID {
		t.Fatalf("aborted pass removed evidence: surviving = %v", ids)
	}
}

// TestRetentionSkipNoteReachesTheCallerWriter proves the skip path reports
// through the supplied writer: with no repository to answer, the pass
// fails closed and the caller still sees why.
func TestRetentionSkipNoteReachesTheCallerWriter(t *testing.T) {
	var out bytes.Buffer
	tryRetentionAfterPublish(&out, t.TempDir())
	if !strings.Contains(out.String(), "retention skipped") {
		t.Fatalf("skip note missing from caller writer: %q", out.String())
	}
}

// seedPruneFailedRun admits a run through the real CreateRun/AppendEvent
// machinery and settles its single attempt as failed, with a metrics
// snapshot faithful to what finalization would fold for it: the outcome
// cast plus one semantic class. It returns the run and invocation
// identities, the latter being what review provenance records cite.
func seedPruneFailedRun(t *testing.T, backing *store.Store, start time.Time) (string, string) {
	t.Helper()
	job := agentrun.NewLogicalJob(agentrun.NewRunRequest(
		agentrun.Candidate("candidate:failed"), agentrun.Prompt("prune fixture"), nil))
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
		{agentrun.StateRunning, agentrun.StateFailed, agentrun.DecisionNone},
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
	snapshot := store.ExecutionMetrics{
		Version: store.ExecutionMetricsSchemaVersion,
		RunID:   runID,
		Failures: []store.ExecutionFailure{
			{InvocationID: invocation.InvocationID().String(), Class: store.FailureClass(agentrun.OutcomeFailure)},
			{InvocationID: invocation.InvocationID().String(), Class: store.FailureInvalidOutput},
		},
	}
	if err := backing.SaveExecutionMetrics(snapshot); err != nil {
		t.Fatalf("SaveExecutionMetrics() error = %v", err)
	}
	return runID, invocation.InvocationID().String()
}
