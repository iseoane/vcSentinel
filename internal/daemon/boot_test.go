package daemon

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// Boot reconciliation (ticket 14 slice 3b) is seeded through the same store
// primitives the cmd-level recovery fixtures use: a real admission record,
// real appended event frames, and snapshot removal to simulate the crash
// window the classifier proves.

type bootFixtureTransition = struct {
	from     agentrun.LifecycleState
	to       agentrun.LifecycleState
	decision agentrun.Decision
}

func bootSuccessExtra() []bootFixtureTransition {
	return []bootFixtureTransition{
		{agentrun.StateRunning, agentrun.StateSucceeded, agentrun.DecisionComplete},
	}
}

func bootAwaitingExtra() []bootFixtureTransition {
	return []bootFixtureTransition{
		{agentrun.StateRunning, agentrun.StateAwaitingDecision, agentrun.DecisionNone},
	}
}

// appendBootFixtureStream writes one synthetic stream and returns its run id.
// baseTransitions always reach StateRunning; extra appends the outcome frames.
func appendBootFixtureStream(t *testing.T, backing *store.Store, candidate string, extra []bootFixtureTransition) string {
	t.Helper()
	job := agentrun.NewLogicalJob(agentrun.NewRunRequest(agentrun.Candidate(candidate), agentrun.Prompt("fixture"), nil))
	if err := backing.CreateRun(job, store.RunPolicy{ID: "policy:boot-test"}); err != nil {
		t.Fatal(err)
	}
	invocation, err := agentrun.NewRootInvocation(job, 1, agentrun.DecisionStart)
	if err != nil {
		t.Fatal(err)
	}
	transitions := []bootFixtureTransition{
		{agentrun.StateCreated, agentrun.StateQueued, agentrun.DecisionStart},
		{agentrun.StateQueued, agentrun.StateAdmitted, agentrun.DecisionStart},
		{agentrun.StateAdmitted, agentrun.StateRunning, agentrun.DecisionStart},
	}
	appended := append(transitions, extra...)
	for revision, transition := range appended {
		event, eventErr := agentrun.NewNormalizedEvent(invocation, transition.from, transition.to, transition.decision, time.Unix(1700000000+int64(revision), 0).UTC())
		if eventErr != nil {
			t.Fatal(eventErr)
		}
		if _, appendErr := backing.AppendEvent(string(job.RunID()), event, uint64(revision)); appendErr != nil {
			t.Fatal(appendErr)
		}
	}
	return string(job.RunID())
}

// snapshotPathFor locates state.json so tests can simulate the crash window
// by removing the snapshot after a real append (same layout knowledge as the
// cmd-level repair fixtures).
func snapshotPathFor(root, runID string) string {
	return filepath.Join(root, "vas-sentinel", "executions", "v1", runID, "state.json")
}

func TestReconcileOnBootSettlesUnprojectedAndSurfacesOperatorRequired(t *testing.T) {
	root := t.TempDir()
	backing := store.NuevoStore(root)

	unprojected := appendBootFixtureStream(t, backing, "candidate:boot-unprojected", bootSuccessExtra())
	if err := os.Remove(snapshotPathFor(root, unprojected)); err != nil {
		t.Fatal(err)
	}
	operatorRequired := appendBootFixtureStream(t, backing, "candidate:boot-operator", nil)
	recoverable := appendBootFixtureStream(t, backing, "candidate:boot-recoverable", bootAwaitingExtra())

	controller := execution.NewController(backing, nil)
	var out bytes.Buffer
	if err := ReconcileOnBoot(controller, backing, &out); err != nil {
		t.Fatalf("ReconcileOnBoot error = %v, output:\n%s", err, out.String())
	}
	text := out.String()

	// The auto-settleable class was repaired: the action line names the
	// before/after transition exactly like the CLI repair surface does.
	if !strings.Contains(text, "settled run "+unprojected) ||
		!strings.Contains(text, "terminal_unprojected → settled") {
		t.Fatalf("boot output did not record the repair of %s:\n%s", unprojected, text)
	}
	if _, err := os.Stat(snapshotPathFor(root, unprojected)); err != nil {
		t.Fatalf("repaired snapshot missing after boot: %v", err)
	}

	// operator_required stays untouched but is surfaced with its class and
	// exact missing-evidence reason.
	if !strings.Contains(text, "needs an operator decision (class operator_required)") ||
		!strings.Contains(text, operatorRequired) ||
		!strings.Contains(text, "outcome unknown") {
		t.Fatalf("boot output did not surface the operator_required reason:\n%s", text)
	}

	// Non-actionable classes are logged informationally, never repaired.
	if !strings.Contains(text, "left untouched (class recoverable)") ||
		!strings.Contains(text, recoverable) {
		t.Fatalf("boot output did not report the untouched recoverable stream:\n%s", text)
	}

	entries, scanErr := store.ScanRecoveries(backing)
	if scanErr != nil {
		t.Fatal(scanErr)
	}
	classes := map[string]store.RecoveryClass{}
	for _, entry := range entries {
		classes[entry.RunID] = entry.Class
	}
	if classes[unprojected] != "" || classes[operatorRequired] != store.RecoveryOperatorRequired ||
		classes[recoverable] != store.RecoveryRecoverable {
		t.Fatalf("post-boot classification drifted: %+v", entries)
	}
}

func TestReconcileOnBootIsIdempotent(t *testing.T) {
	root := t.TempDir()
	backing := store.NuevoStore(root)

	unprojected := appendBootFixtureStream(t, backing, "candidate:boot-idempotent", bootSuccessExtra())
	if err := os.Remove(snapshotPathFor(root, unprojected)); err != nil {
		t.Fatal(err)
	}
	controller := execution.NewController(backing, nil)

	first := &bytes.Buffer{}
	if err := ReconcileOnBoot(controller, backing, first); err != nil {
		t.Fatalf("first ReconcileOnBoot error = %v:\n%s", err, first.String())
	}
	snapshot, err := os.ReadFile(snapshotPathFor(root, unprojected))
	if err != nil {
		t.Fatal(err)
	}

	second := &bytes.Buffer{}
	if err := ReconcileOnBoot(controller, backing, second); err != nil {
		t.Fatalf("second ReconcileOnBoot error = %v:\n%s", err, second.String())
	}
	text := second.String()
	if !strings.Contains(text, "no interrupted durable runs require attention") {
		t.Fatalf("second boot did not report an empty scan:\n%s", text)
	}
	if strings.Contains(text, "repaired projection") || strings.Contains(text, "settled run") {
		t.Fatalf("second boot performed settlement actions; want zero actions:\n%s", text)
	}
	repaired, err := os.ReadFile(snapshotPathFor(root, unprojected))
	if err != nil {
		t.Fatal(err)
	}
	if string(repaired) != string(snapshot) {
		t.Fatal("second boot rewrote the already-repaired snapshot")
	}
}

func TestReconcileOnBootEmptyScanPrintsQuietLine(t *testing.T) {
	backing := store.NuevoStore(t.TempDir())
	controller := execution.NewController(backing, nil)
	var out bytes.Buffer
	if err := ReconcileOnBoot(controller, backing, &out); err != nil {
		t.Fatalf("ReconcileOnBoot error = %v", err)
	}
	if !strings.Contains(out.String(), "no interrupted durable runs require attention") {
		t.Fatalf("quiet boot output = %q", out.String())
	}
}
