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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/metrics"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
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

	report, published, err := retenerDetallePublicado(worktree)
	if err != nil {
		t.Fatalf("retenerDetallePublicado() error = %v", err)
	}
	if len(published) != 1 || published[0] != shaPublished[:40] {
		t.Fatalf("published = %v, want exactly [%s]", published, shaPublished)
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
	if _, _, err := retenerDetallePublicado(worktree); err == nil {
		t.Fatal("retention outside a repository must fail closed, not collect nothing with success")
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
	if code := finalizeGateWithDetails(&out, worktree, "pre-push", "PASS", nil, "", nil); code != 0 {
		t.Fatalf("pre-push gate exit = %d, want 0", code)
	}
	if listed() {
		t.Fatalf("pre-push gate did not collect the uncited measured run %s", runID)
	}
	if !strings.Contains(out.String(), "retention: collected 1 execution stream(s)") {
		t.Fatalf("pre-push gate output missing the retention line: %q", out.String())
	}
	if snapshot, err := backing.ReadExecutionMetrics(runID); err != nil || snapshot == nil {
		t.Fatalf("snapshot of the collected run must survive: got=%v err=%v", snapshot, err)
	}
}
