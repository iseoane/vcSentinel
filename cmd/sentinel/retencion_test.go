// T9.5 acceptance: event-driven retention collects the execution streams
// of published commits while leaving measurement byte-identical.
//
// The fixture pins every half of the contract at once: a published commit
// whose run is measured is collected with its snapshot surviving; an
// unpublished commit keeps its run; a published commit whose run has no
// snapshot keeps it (absence is unknown, never zero); and the published
// ficha carries a confirmed finding, so any implementation that deleted
// fichas instead of streams would move the metrics bytes it must preserve.
package main

import (
	"bytes"
	"encoding/json"
	"github.com/ISeoane-Quental/vas.sentinel/internal/gate"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/metrics"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func retencionGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
		return ""
	} else {
		return string(out)
	}
}

func TestRetencionCollectsPublishedRunsAndKeepsMeasurementIdentical(t *testing.T) {
	worktree := t.TempDir()
	retencionGit(t, worktree, "init", "-q", "-b", "main")
	retencionGit(t, worktree, "config", "user.email", "retencion@test")
	retencionGit(t, worktree, "config", "user.name", "retencion")
	retencionGit(t, worktree, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(worktree, "a.txt"), []byte("a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	retencionGit(t, worktree, "add", "a.txt")
	retencionGit(t, worktree, "commit", "-q", "-m", "published")
	shaPublished := retencionGit(t, worktree, "rev-parse", "HEAD")
	shaPublished = strings.TrimSpace(shaPublished)
	retencionGit(t, worktree, "update-ref", "refs/remotes/origin/main", shaPublished)
	retencionGit(t, worktree, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(worktree, "b.txt"), []byte("b\n"), 0644); err != nil {
		t.Fatal(err)
	}
	retencionGit(t, worktree, "add", "b.txt")
	retencionGit(t, worktree, "commit", "-q", "-m", "unpublished")
	shaUnpublished := strings.TrimSpace(retencionGit(t, worktree, "rev-parse", "HEAD"))

	backing := pruneRepoStore(t, worktree)
	commonDir, err := git.ObtenerGitCommonDir(worktree)
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

	ledger := review.NuevoLedger(commonDir)
	guardarFicha := func(sha, invocation string) {
		t.Helper()
		if err := ledger.GuardarRevision(sha[:40], "fixture", "bucket", "model", review.Revision{
			At:     ancient,
			Result: "ok",
			Dims: []review.DimensionResult{{
				Dim: "logic", Verdict: "ok",
				InvocationID: invocation,
				Hallazgos: []review.Hallazgo{{
					Fingerprint:  "retencion-finding-" + sha[:8],
					Dimension:    "logic",
					Status:       review.StatusConfirmed,
					Description:  "fixture confirmed finding",
					InvocationID: invocation,
				}},
				// Findings carries the metrics-visible raw finding;
				// Hallazgos above carries provenance. Both cite the run.
				Findings: []review.ReviewFinding{{
					File:        "a.go",
					Line:        10,
					Severity:    review.SevCritical,
					Description: "fixture confirmed finding",
					Status:      review.StatusConfirmed,
				}},
			}},
		}); err != nil {
			t.Fatalf("GuardarRevision(%s) error = %v", sha, err)
		}
	}
	guardarFicha(shaPublished, invPublished)
	guardarFicha(shaUnpublished, invUnpublished)
	guardarFicha(shaPublished, invUnmeasured)

	metricsBefore, err := metrics.AggregateStore(commonDir)
	if err != nil {
		t.Fatalf("AggregateStore() before retention error = %v", err)
	}
	before, err := json.Marshal(metricsBefore)
	if err != nil {
		t.Fatal(err)
	}
	if metricsBefore.Findings.Observed == 0 || metricsBefore.Findings.Confirmed == 0 || metricsBefore.Executions.MeasuredRuns != 2 {
		t.Fatalf("fixture is evidence-free (findings=%d confirmed=%d measured=%d): the invariance check would pass vacuously",
			metricsBefore.Findings.Observed, metricsBefore.Findings.Confirmed, metricsBefore.Executions.MeasuredRuns)
	}

	report, published, undecidable, err := retenerDetallePublicado(worktree)
	if err != nil {
		t.Fatalf("retenerDetallePublicado() error = %v", err)
	}
	if len(published) != 1 || published[0] != shaPublished[:40] {
		t.Fatalf("published = %v, want exactly [%s]", published, shaPublished)
	}
	if len(undecidable) != 0 {
		t.Fatalf("undecidable = %v, want none: every fixture SHA resolves", undecidable)
	}
	if report.Pruned != 1 {
		t.Fatalf("pruned = %d, want 1 (only the measured published run): %+v", report.Pruned, report.Decisions)
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
	if !stillThere(runUnpublished) {
		t.Fatalf("unpublished run %s was collected", runUnpublished)
	}
	if !stillThere(runUnmeasured) {
		t.Fatalf("unmeasured published run %s was collected without a snapshot", runUnmeasured)
	}
	if snapshot, err := backing.ReadExecutionMetrics(runPublished); err != nil || snapshot == nil {
		t.Fatalf("snapshot of collected run must survive: got=%v err=%v", snapshot, err)
	}
	for _, sha := range []string{shaPublished, shaUnpublished} {
		if ficha, err := ledger.LeerFicha(sha[:40]); err != nil || ficha == nil {
			t.Fatalf("ficha %s must survive retention: ficha=%v err=%v", sha, ficha, err)
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

func TestRetencionSkipsUndecidableRepositories(t *testing.T) {
	worktree := t.TempDir()
	if _, _, _, err := retenerDetallePublicado(worktree); err == nil {
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
	retencionGit(t, worktree, "init", "-q", "-b", "main")
	retencionGit(t, worktree, "config", "user.email", "retencion@test")
	retencionGit(t, worktree, "config", "user.name", "retencion")
	retencionGit(t, worktree, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(worktree, "a.txt"), []byte("a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	retencionGit(t, worktree, "add", "a.txt")
	retencionGit(t, worktree, "commit", "-q", "-m", "base")
	retencionGit(t, worktree, "update-ref", "refs/remotes/origin/main", "HEAD")

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
	if code := finalizeGateWithDetails(&out, worktree, "pre-commit", "PASS", nil, "", nil); code != 0 {
		t.Fatalf("pre-commit gate exit = %d, want 0", code)
	}
	if !listed() {
		t.Fatal("pre-commit gate collected execution detail: retention must trigger on pre-push only")
	}
	out.Reset()
	if code := finalizeGateWithDetails(&out, worktree, "pre-push", "PASS", nil, "", nil); code != 0 {
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
	wantCode := gate.CodigoSalida(gate.EstadoCodeReviewFailed)
	if code := finalizeGateWithDetails(&out, worktree, "pre-push", gate.EstadoCodeReviewFailed, nil, "", nil); code != wantCode {
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

// TestRetencionKeepsUndecidableRecordsWithoutVetoingThePass proves the
// per-record fail-closed rule: a ficha whose commit cannot be resolved
// keeps its streams protected while the rest of the pass still collects.
// GuardarRevision never validates the SHA, so a ficha can cite an object
// the object store does not have — exactly the shape that must not veto
// collection of its decidable siblings.
func TestRetencionKeepsUndecidableRecordsWithoutVetoingThePass(t *testing.T) {
	worktree := t.TempDir()
	retencionGit(t, worktree, "init", "-q", "-b", "main")
	retencionGit(t, worktree, "config", "user.email", "retencion@test")
	retencionGit(t, worktree, "config", "user.name", "retencion")
	retencionGit(t, worktree, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(worktree, "a.txt"), []byte("a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	retencionGit(t, worktree, "add", "a.txt")
	retencionGit(t, worktree, "commit", "-q", "-m", "published")
	shaPublished := strings.TrimSpace(retencionGit(t, worktree, "rev-parse", "HEAD"))
	retencionGit(t, worktree, "update-ref", "refs/remotes/origin/main", shaPublished)
	const shaUnresolvable = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

	backing := pruneRepoStore(t, worktree)
	commonDir, err := git.ObtenerGitCommonDir(worktree)
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

	ledger := review.NuevoLedger(commonDir)
	citar := func(sha, invocation string) {
		t.Helper()
		if err := ledger.GuardarRevision(sha, "fixture", "bucket", "model", review.Revision{
			At:     ancient,
			Result: "ok",
			Dims: []review.DimensionResult{{
				Dim: "logic", Verdict: "ok",
				InvocationID: invocation,
			}},
		}); err != nil {
			t.Fatalf("GuardarRevision(%s) error = %v", sha, err)
		}
	}
	citar(shaUnresolvable, invProtected)
	citar(shaPublished, invCollected)

	report, published, undecidable, err := retenerDetallePublicado(worktree)
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

// TestRetencionAbortsOnCorruptFicha proves the other half of the failure
// taxonomy: an unreadable ficha (as opposed to an undecidable commit) has
// no references to annotate, so the pass aborts instead of collecting
// under partially known provenance.
func TestRetencionAbortsOnCorruptFicha(t *testing.T) {
	worktree := t.TempDir()
	retencionGit(t, worktree, "init", "-q", "-b", "main")
	retencionGit(t, worktree, "config", "user.email", "retencion@test")
	retencionGit(t, worktree, "config", "user.name", "retencion")
	retencionGit(t, worktree, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(worktree, "a.txt"), []byte("a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	retencionGit(t, worktree, "add", "a.txt")
	retencionGit(t, worktree, "commit", "-q", "-m", "base")
	sha := strings.TrimSpace(retencionGit(t, worktree, "rev-parse", "HEAD"))
	retencionGit(t, worktree, "update-ref", "refs/remotes/origin/main", sha)

	backing := pruneRepoStore(t, worktree)
	commonDir, err := git.ObtenerGitCommonDir(worktree)
	if err != nil {
		t.Fatal(err)
	}
	ledger := review.NuevoLedger(commonDir)
	runID, invocationID := seedPruneTerminalRun(t, backing, "candidate:corrupt-ficha", time.Unix(1000000000, 0).UTC())
	if err := ledger.GuardarRevision(sha, "fixture", "bucket", "model", review.Revision{
		At:     time.Now(),
		Result: "ok",
		Dims:   []review.DimensionResult{{Dim: "logic", Verdict: "ok", InvocationID: invocationID}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ledger.RutaFicha(sha), []byte("{corrupt"), 0644); err != nil {
		t.Fatal(err)
	}

	if _, _, _, err := retenerDetallePublicado(worktree); err == nil {
		t.Fatal("corrupt ficha must abort the pass, not collect under partial provenance")
	} else if !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("corrupt ficha error = %v, want the unreadable-ficha refusal", err)
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
	intentarRetencionTrasPublicacion(&out, t.TempDir())
	if !strings.Contains(out.String(), "retention skipped") {
		t.Fatalf("skip note missing from caller writer: %q", out.String())
	}
}
