// Guard-chain retention tests for PruneExecutions: every kept/pruned decision
// class (terminal-old, terminal-recent, non-terminal, corrupt, orphaned,
// provenance-referenced, parent-protection) plus the doc-pin test that locks
// the exported prune reason constants to their documented literals. Shared
// fixtures live in execution_prune_removal_test.go.
package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

// TestPruneExecutionsAppliesRetentionGuards proves the core behavior table:
// only terminal-old-unreferenced records are removed; every other class is
// kept with its stable reason.
func TestPruneExecutionsAppliesRetentionGuards(t *testing.T) {
	s := NuevoStore(t.TempDir())
	cutoff := time.Now().Add(-24 * time.Hour)

	oldRun, _ := seedPruneRun(t, s, "old-terminal", "", pruneTerminalSuccess(), pruneAncientTime)
	recentRun, _ := seedPruneRun(t, s, "recent-terminal", "", pruneTerminalSuccess(), time.Now())
	runningRun, _ := seedPruneRun(t, s, "still-running", "", pruneRunningHead(), pruneAncientTime)

	report, err := s.PruneExecutions(cutoff, nil)
	if err != nil {
		t.Fatalf("PruneExecutions() error = %v", err)
	}

	if report.Examined != 3 || report.Pruned != 1 || report.Kept != 2 {
		t.Fatalf("report counts = examined %d, pruned %d, kept %d; want 3/1/2", report.Examined, report.Pruned, report.Kept)
	}
	if decision := decisionFor(t, report, oldRun); decision.Action != PruneActionPruned {
		t.Fatalf("old terminal run action = %q (%s), want pruned", decision.Action, decision.Reason)
	}
	assertKept(t, report, recentRun, "terminal-recent")
	assertKept(t, report, runningRun, "non-terminal")

	if executionDirectoryExists(t, s, oldRun) {
		t.Fatalf("pruned run %s directory still exists", oldRun)
	}
	if !executionDirectoryExists(t, s, recentRun) || !executionDirectoryExists(t, s, runningRun) {
		t.Fatalf("kept run directories were removed")
	}
	ids, listErr := s.ListExecutionIDs()
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(ids) != 2 {
		t.Fatalf("ListExecutionIDs() after prune = %v, want the two kept runs", ids)
	}
	for _, id := range ids {
		if id == oldRun {
			t.Fatalf("pruned run %s still listed", oldRun)
		}
	}
}

// TestPruneExecutionsRefusesCorruptRecords proves corrupt bytes are never
// destroyed by retention: both an unreadable line and an incomplete final
// tail keep the record with an explicit corrupt reason.
func TestPruneExecutionsRefusesCorruptRecords(t *testing.T) {
	t.Run("garbage line", func(t *testing.T) {
		s := NuevoStore(t.TempDir())
		runID, _ := seedPruneRun(t, s, "corrupt-line", "", pruneTerminalSuccess(), pruneAncientTime)
		directory, _ := s.executionDir(runID)
		logPath := filepath.Join(directory, "events.jsonl")
		file, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString("{not json\n"); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}

		report, err := s.PruneExecutions(time.Now().Add(-24*time.Hour), nil)
		if err != nil {
			t.Fatal(err)
		}
		assertKept(t, report, runID, "corrupt:")
		if !executionDirectoryExists(t, s, runID) {
			t.Fatal("corrupt record was removed")
		}
	})

	t.Run("incomplete final tail", func(t *testing.T) {
		s := NuevoStore(t.TempDir())
		runID, _ := seedPruneRun(t, s, "torn-tail", "", pruneTerminalSuccess(), pruneAncientTime)
		directory, _ := s.executionDir(runID)
		logPath := filepath.Join(directory, "events.jsonl")
		data, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(logPath, data[:len(data)-1], 0600); err != nil {
			t.Fatal(err)
		}

		report, err := s.PruneExecutions(time.Now().Add(-24*time.Hour), nil)
		if err != nil {
			t.Fatal(err)
		}
		assertKept(t, report, runID, "incomplete final event tail")
	})
}

// TestPruneExecutionsKeepsOrphanedCanceled proves owner-death evidence stays
// inspectable: its reconciled canceled settlement exists only as derived
// evidence over those exact bytes until an explicit recover settles it.
func TestPruneExecutionsKeepsOrphanedCanceled(t *testing.T) {
	s := NuevoStore(t.TempDir())
	runID, _ := seedPruneRun(t, s, "orphaned-canceled", "", pruneOrphanedEscalation(), pruneAncientTime)

	projection, err := s.ReadReconciledProjection(runID)
	if err != nil {
		t.Fatal(err)
	}
	if !projection.OrphanedCancellation {
		t.Fatalf("fixture is not orphaned-canceled: %+v", projection)
	}

	report, err := s.PruneExecutions(time.Now().Add(-24*time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
	assertKept(t, report, runID, "orphaned-canceled recovery evidence")
}

// TestPruneExecutionsKeepsProvenanceReferencedStreams proves the reference
// guard: a terminal-old run whose invocation produced review findings is
// retained even though it would otherwise be prunable.
func TestPruneExecutionsKeepsProvenanceReferencedStreams(t *testing.T) {
	s := NuevoStore(t.TempDir())
	referencedRun, frames := seedPruneRun(t, s, "referenced", "", pruneTerminalSuccess(), pruneAncientTime)
	freeRun, _ := seedPruneRun(t, s, "unreferenced", "", pruneTerminalSuccess(), pruneAncientTime)
	references := map[string]bool{frames[0].InvocationID: true}

	report, err := s.PruneExecutions(time.Now().Add(-24*time.Hour), references)
	if err != nil {
		t.Fatal(err)
	}
	assertKept(t, report, referencedRun, "review provenance references invocation")
	if decision := decisionFor(t, report, freeRun); decision.Action != PruneActionPruned {
		t.Fatalf("unreferenced run action = %q (%s), want pruned", decision.Action, decision.Reason)
	}
}

// TestPruneExecutionsKeepsFindingReferencedStreams wires the persisted
// finding provenance lookup into the guard: findings that carry an
// invocation_id protect the producing stream.
func TestPruneExecutionsKeepsFindingReferencedStreams(t *testing.T) {
	s := NuevoStore(t.TempDir())
	runID, frames := seedPruneRun(t, s, "finding-referenced", "", pruneTerminalSuccess(), pruneAncientTime)
	hallazgo := &review.Hallazgo{
		Fingerprint:  "prune-fixture-fingerprint",
		Dimension:    "logic",
		Description:  "fixture finding",
		InvocationID: frames[0].InvocationID,
	}
	if err := s.GuardarHallazgo(hallazgo); err != nil {
		t.Fatal(err)
	}
	references, err := s.ReferencedInvocationIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(references) != 1 {
		t.Fatalf("ReferencedInvocationIDs() = %v, want exactly one reference", references)
	}

	report, err := s.PruneExecutions(time.Now().Add(-24*time.Hour), references)
	if err != nil {
		t.Fatal(err)
	}
	assertKept(t, report, runID, "review provenance references invocation")
}

// TestReferencedInvocationIDsEmptyAndCorrupt proves the lookup fails closed
// on unreadable evidence instead of letting a prune run while provenance is
// unreadable.
func TestReferencedInvocationIDsEmptyAndCorrupt(t *testing.T) {
	t.Run("empty store", func(t *testing.T) {
		s := NuevoStore(t.TempDir())
		references, err := s.ReferencedInvocationIDs()
		if err != nil || len(references) != 0 {
			t.Fatalf("references = %v, err = %v; want empty with no error", references, err)
		}
	})
	t.Run("corrupt finding fails closed", func(t *testing.T) {
		s := NuevoStore(t.TempDir())
		if err := os.MkdirAll(filepath.Join(s.dir, subdirFindings), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(s.dir, subdirFindings, "broken.json"), []byte("{"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReferencedInvocationIDs(); err == nil {
			t.Fatal("corrupt finding record must fail closed")
		}
	})
}

// TestPruneExecutionsProtectsParentOfSurvivingRun proves the linkage guard:
// a gate root referenced as parent_run_id by a surviving child survives even
// when it is itself terminal and old.
func TestPruneExecutionsProtectsParentOfSurvivingRun(t *testing.T) {
	s := NuevoStore(t.TempDir())
	parentRun, _ := seedPruneRun(t, s, "gate-root", "", pruneTerminalSuccess(), pruneAncientTime)
	childRun, _ := seedPruneRun(t, s, "review-child", parentRun, pruneTerminalSuccess(), time.Now())

	report, err := s.PruneExecutions(time.Now().Add(-24*time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
	assertKept(t, report, parentRun, "parent of surviving run "+childRun)
	// The child itself survives on its own recency — that survival is what
	// triggers the linkage guard on its parent.
	assertKept(t, report, childRun, "terminal-recent")
	if !executionDirectoryExists(t, s, parentRun) {
		t.Fatal("protected parent was removed")
	}
}

// TestPruneExecutionsIsIdempotent proves repeated pruning is safe: once the
// removable records are gone, a second pass keeps everything and removes
// nothing.
func TestPruneExecutionsIsIdempotent(t *testing.T) {
	s := NuevoStore(t.TempDir())
	seedPruneRun(t, s, "first-pass", "", pruneTerminalSuccess(), pruneAncientTime)
	survivor, _ := seedPruneRun(t, s, "survivor", "", pruneRunningHead(), pruneAncientTime)

	first, err := s.PruneExecutions(time.Now().Add(-24*time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Pruned != 1 {
		t.Fatalf("first pass pruned %d runs, want 1", first.Pruned)
	}
	second, err := s.PruneExecutions(time.Now().Add(-24*time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Pruned != 0 || second.Examined != 1 {
		t.Fatalf("second pass = examined %d, pruned %d; want 1/0", second.Examined, second.Pruned)
	}
	assertKept(t, second, survivor, "non-terminal")
}

// TestPruneExecutionsKeepsIncompleteAdmissionRecord proves a listed
// directory without its immutable request record is never touched.
func TestPruneExecutionsKeepsIncompleteAdmissionRecord(t *testing.T) {
	s := NuevoStore(t.TempDir())
	stray := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	directory := filepath.Join(s.dir, "executions", "v1", stray)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}

	report, err := s.PruneExecutions(time.Now().Add(-24*time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
	assertKept(t, report, stray, "incomplete or corrupt admission record")
	if !executionDirectoryExists(t, s, stray) {
		t.Fatal("partial admission record was removed")
	}
}

// TestPruneReasonConstantsMatchDocumentedContract pins every exported prune
// constant to its documented literal: docs/runs-cli.md quotes these strings
// verbatim in the prune-decision tables, so any drift on either side must
// fail here first.
func TestPruneReasonConstantsMatchDocumentedContract(t *testing.T) {
	cases := map[string]string{
		"PruneActionPruned":              PruneActionPruned,
		"PruneActionKept":                PruneActionKept,
		"PruneReasonPruned":              PruneReasonPruned,
		"PruneReasonTerminalRecent":      PruneReasonTerminalRecent,
		"PruneReasonNonTerminal":         PruneReasonNonTerminal,
		"PruneReasonCorruptTail":         PruneReasonCorruptTail,
		"PruneReasonIncompleteAdmission": PruneReasonIncompleteAdmission,
		"PruneReasonOrphanedCanceled":    PruneReasonOrphanedCanceled,
		"PruneReasonRemovalRemnant":      PruneReasonRemovalRemnant,
		"PruneReasonUnreadableFmt":       PruneReasonUnreadableFmt,
		"PruneReasonCorruptFmt":          PruneReasonCorruptFmt,
		"PruneReasonProvenanceFmt":       PruneReasonProvenanceFmt,
		"PruneReasonParentOfSurvivorFmt": PruneReasonParentOfSurvivorFmt,
		"PruneReasonRemovalFailedFmt":    PruneReasonRemovalFailedFmt,
	}
	expected := map[string]string{
		"PruneActionPruned":              "pruned",
		"PruneActionKept":                "kept",
		"PruneReasonPruned":              "pruned",
		"PruneReasonTerminalRecent":      "terminal-recent",
		"PruneReasonNonTerminal":         "non-terminal",
		"PruneReasonCorruptTail":         "corrupt: incomplete final event tail",
		"PruneReasonIncompleteAdmission": "incomplete or corrupt admission record",
		"PruneReasonOrphanedCanceled":    "orphaned-canceled recovery evidence",
		"PruneReasonRemovalRemnant":      "prunable-remnant",
		"PruneReasonUnreadableFmt":       "unreadable: %v",
		"PruneReasonCorruptFmt":          "corrupt: %v",
		"PruneReasonProvenanceFmt":       "review provenance references invocation %s",
		"PruneReasonParentOfSurvivorFmt": "parent of surviving run %s",
		"PruneReasonRemovalFailedFmt":    "removal failed: %v",
	}
	for name, got := range cases {
		want, ok := expected[name]
		if !ok {
			t.Fatalf("constant %s has no pinned expectation; add it to both maps", name)
		}
		if got != want {
			t.Errorf("constant %s = %q, want documented literal %q (update docs/runs-cli.md together)", name, got, want)
		}
	}
}
