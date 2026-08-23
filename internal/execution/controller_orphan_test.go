package execution

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// The orphaning tests pin ticket 14 slice 3a's reuse contract: settlements
// authored by OrphanActiveRuns carry the caller's reason, leave ScanRecoveries
// with no operator_required residue, and never touch already-terminal runs.

const orphanReason = "orphaned by daemon shutdown"

func TestOrphanActiveRunsSettlesLiveRunningRun(t *testing.T) {
	st := store.NuevoStore(t.TempDir())
	controller := NewControllerWithClock(st, &blockingAdapter{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}, fixedClock())

	handle, err := controller.Start(context.Background(), testRequest("orphan-live"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	waitForState(t, controller, handle.RunID, agentrun.StateRunning)

	orphaned, err := controller.OrphanActiveRuns(orphanReason)
	if err != nil {
		t.Fatalf("OrphanActiveRuns() error = %v", err)
	}
	if len(orphaned) != 1 || orphaned[0] != handle.RunID {
		t.Fatalf("orphaned = %v, want exactly %s", orphaned, handle.RunID)
	}

	inspection, err := controller.Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Projection.State != agentrun.StateCanceled || inspection.Projection.Terminal != agentrun.TerminalCancellation {
		t.Fatalf("projection = %+v, want canceled terminal settlement", inspection.Projection)
	}
	if len(inspection.Outcomes) == 0 || inspection.Outcomes[len(inspection.Outcomes)-1].Class != agentrun.OutcomeCancellation ||
		!strings.Contains(inspection.Outcomes[len(inspection.Outcomes)-1].Error, orphanReason) {
		t.Fatalf("outcomes = %+v, want cancellation carrying %q", inspection.Outcomes, orphanReason)
	}

	entries, err := store.ScanRecoveries(st)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("ScanRecoveries = %+v, want clean classification after shutdown orphaning", entries)
	}
}

func TestOrphanActiveRunsSkipsAlreadyTerminalRuns(t *testing.T) {
	controller := NewControllerWithClock(store.NuevoStore(t.TempDir()), &scriptedAdapter{
		result: AdapterResult{Output: "finished before shutdown"},
	}, fixedClock())

	handle, err := controller.Start(context.Background(), testRequest("orphan-terminal"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}

	orphaned, err := controller.OrphanActiveRuns(orphanReason)
	if err != nil {
		t.Fatalf("OrphanActiveRuns() error = %v", err)
	}
	if len(orphaned) != 0 {
		t.Fatalf("orphaned = %v, want nothing settled for a terminal run", orphaned)
	}
}

func TestOrphanActiveRunsSettlesQuiescentAwaitingHead(t *testing.T) {
	st := store.NuevoStore(t.TempDir())
	controller := NewControllerWithClock(st, &scriptedAdapter{
		result: AdapterResult{AwaitingDecision: true},
	}, fixedClock())

	handle, err := controller.Start(context.Background(), testRequest("orphan-awaiting"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	waitForState(t, controller, handle.RunID, agentrun.StateAwaitingDecision)

	orphaned, err := controller.OrphanActiveRuns(orphanReason)
	if err != nil {
		t.Fatalf("OrphanActiveRuns() error = %v", err)
	}
	if len(orphaned) != 1 || orphaned[0] != handle.RunID {
		t.Fatalf("orphaned = %v, want exactly %s", orphaned, handle.RunID)
	}

	inspection, err := controller.Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	last := inspection.Outcomes[len(inspection.Outcomes)-1]
	if inspection.Projection.Terminal != agentrun.TerminalCancellation || last.Class != agentrun.OutcomeCancellation ||
		!strings.Contains(last.Error, orphanReason) {
		t.Fatalf("durable evidence = %+v / %+v, want orphaned cancellation", inspection.Projection, last)
	}

	entries, err := store.ScanRecoveries(st)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("ScanRecoveries = %+v, want clean classification after orphaning an awaiting head", entries)
	}

	if busy := controller.WaitForActiveRuns(time.Second); busy != 0 {
		t.Fatalf("WaitForActiveRuns = %d after everything settled, want 0", busy)
	}
}

func TestOrphanActiveRunsRefusesBlankReasonAndNilStore(t *testing.T) {
	controller := NewControllerWithClock(store.NuevoStore(t.TempDir()), &scriptedAdapter{}, fixedClock())
	if _, err := controller.OrphanActiveRuns("   "); err == nil {
		t.Fatal("OrphanActiveRuns with a blank reason must fail explicitly")
	}

	bare := Controller{}
	if _, err := bare.OrphanActiveRuns(orphanReason); err == nil {
		t.Fatal("OrphanActiveRuns without a store must fail with ErrControllerNotReady")
	}
}

// TestOrphanActiveRunsSettlesCrossProcessResidueWithoutLiveState reaches the
// durable-head branch the live-settlement path never touches: a stream whose
// owner died in a previous process (seeded directly through store
// primitives, exactly like the R8 recovery fixtures) must be settled by a
// fresh controller that never held live state for it.
func TestOrphanActiveRunsSettlesCrossProcessResidueWithoutLiveState(t *testing.T) {
	st := store.NuevoStore(t.TempDir())
	job, _, _ := seedOrphanedCancellationStream(t, st, "orphan-residue")
	runID := job.RunID()

	fresh := NewControllerWithClock(st, &scriptedAdapter{}, fixedClock())
	orphaned, err := fresh.OrphanActiveRuns(orphanReason)
	if err != nil {
		t.Fatalf("OrphanActiveRuns() error = %v", err)
	}
	if len(orphaned) != 1 || orphaned[0] != runID {
		t.Fatalf("orphaned = %v, want exactly %s", orphaned, runID)
	}

	inspection, err := fresh.Inspect(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	last := inspection.Outcomes[len(inspection.Outcomes)-1]
	if inspection.Projection.Terminal != agentrun.TerminalCancellation || last.Class != agentrun.OutcomeCancellation ||
		!strings.Contains(last.Error, orphanReason) {
		t.Fatalf("residue = %+v / %+v, want a canceled terminal settlement carrying %q", inspection.Projection, last, orphanReason)
	}

	entries, err := store.ScanRecoveries(st)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("ScanRecoveries = %+v, want clean classification after settling cross-process residue", entries)
	}
}

// TestOrphanActiveRunsRefusesUndecidableDurableHead pins fail-closed
// behavior on evidence no automatic settlement may touch: an empty stream
// offers no head to reconstruct, so the sweep refuses with the mapped
// ErrRunNotRecoverable sentinel and leaves every durable byte untouched.
func TestOrphanActiveRunsRefusesUndecidableDurableHead(t *testing.T) {
	controller := NewControllerWithClock(store.NuevoStore(t.TempDir()), &scriptedAdapter{}, fixedClock())
	runID := createEmptyRun(t, controller, "orphan-undecidable")

	orphaned, err := controller.OrphanActiveRuns(orphanReason)
	if !errors.Is(err, ErrRunNotRecoverable) {
		t.Fatalf("OrphanActiveRuns() error = %v, want the ErrRunNotRecoverable chain", err)
	}
	if !strings.Contains(err.Error(), string(runID)) {
		t.Fatalf("aggregated error %q must name the failed run identity", err)
	}
	if len(orphaned) != 0 {
		t.Fatalf("orphaned = %v, want nothing settled for undecidable evidence", orphaned)
	}
	inspection, err := controller.Inspect(context.Background(), runID)
	if err != nil || len(inspection.Events) != 0 {
		t.Fatalf("refused orphaning must not mutate durable evidence: %v, %+v", err, inspection.Events)
	}
}

// TestOrphanActiveRunsSkipsRunsInsideBoundedEscalation pins the documented
// no-double-settlement promise: a run mid-escalation is left entirely to its
// escalation goroutine — never raced through the durable head, whose losing
// writer would abort the whole sweep with ErrStaleRevision — while another
// active run still settles.
func TestOrphanActiveRunsSkipsRunsInsideBoundedEscalation(t *testing.T) {
	blocker := &blockingAdapter{started: make(chan struct{}), release: make(chan struct{})}
	var safeRelease sync.Once
	defer safeRelease.Do(func() { close(blocker.release) })
	controller := NewControllerWithClock(store.NuevoStore(t.TempDir()), blocker, fixedClock())

	escalating, err := controller.Start(context.Background(), testRequest("orphan-escalating"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	waitForState(t, controller, escalating.RunID, agentrun.StateRunning)

	neighbor, err := controller.Start(context.Background(), testRequest("orphan-neighbor"), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	waitForState(t, controller, neighbor.RunID, agentrun.StateRunning)

	// White-box: park the first run exactly where abortRunning leaves it
	// while its escalation goroutine owns the pending settlement.
	controller.mu.Lock()
	escalatingState := controller.runs[string(escalating.RunID)]
	controller.mu.Unlock()
	escalatingState.mu.Lock()
	escalatingState.aborting = true
	escalatingState.mu.Unlock()

	orphaned, err := controller.OrphanActiveRuns(orphanReason)
	if err != nil {
		t.Fatalf("OrphanActiveRuns() error = %v", err)
	}
	if len(orphaned) != 1 || orphaned[0] != neighbor.RunID {
		t.Fatalf("orphaned = %v, want only the neighbor %s settled", orphaned, neighbor.RunID)
	}
	inspection, err := controller.Inspect(context.Background(), escalating.RunID)
	if err != nil || inspection.Projection.Terminal != agentrun.TerminalNone || inspection.Projection.State != agentrun.StateRunning {
		t.Fatalf("mid-escalation run = %+v / %v, want it untouched inside its escalation", inspection.Projection, err)
	}

	// Cleanup: hand the settlement back to the normal completion path so
	// both workers finish before teardown instead of being dropped by the
	// aborting guard forever.
	escalatingState.mu.Lock()
	escalatingState.aborting = false
	escalatingState.mu.Unlock()
	safeRelease.Do(func() { close(blocker.release) })
	if _, err := escalating.Wait(context.Background()); err != nil {
		t.Fatalf("escalating cleanup wait = %v, want a normal success settlement", err)
	}
	if _, err := neighbor.Wait(context.Background()); err != nil {
		t.Fatalf("neighbor cleanup wait = %v, want its canceled completion", err)
	}
}
