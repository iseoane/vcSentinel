package durableruns_e2e

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vcSentinel/internal/execution"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
)

// Scenario a — crash mid-run. Two honest crash shapes are proven:
//
//  1. A plain running head whose worker process died between frames: the next
//     observation classifies the outcome as unknown (operator-required), no
//     completion is fabricated, and Recover refuses without writing.
//  2. Owner death during cancellation (escalation transitions recorded, no
//     settlement): the R8 reconciled view reads canceled-orphaned without any
//     writer having appended a fabricated terminal frame.

func TestCrashMidRunRunningHeadClassifiesHonestlyWithoutFabricating(t *testing.T) {
	backing := newE2EStore(t)
	adapter := newBlockingAdapter()
	controller := execution.NewControllerWithClock(backing, adapter, fixedClock())

	handle, err := controller.Start(context.Background(), e2eRequest("crash-running-head"), e2ePolicy())
	if err != nil {
		t.Fatal(err)
	}
	waitStarted(t, adapter.started)
	// Start appends the whole admission chain synchronously, so the durable
	// head is already running: the "process" now dies by abandonment.
	runID := string(handle.RunID)

	// Teardown lets the abandoned worker exit BEFORE temp-dir removal so its
	// late bookkeeping never races the store directory deletion.
	t.Cleanup(func() {
		adapter.Release()
		cleanupContext, cancelCleanup := context.WithTimeout(context.Background(), observationBudget)
		defer cancelCleanup()
		_, _ = handle.Wait(cleanupContext)
	})

	observer := execution.NewController(backing, nil)
	inspection := assertRunsInspection(t, backing, observer, runID,
		agentrun.StateRunning, agentrun.TerminalNone)

	if len(inspection.Outcomes) != 0 {
		t.Fatalf("outcomes = %+v, want none: a dead worker admits no outcome evidence", inspection.Outcomes)
	}
	last := inspection.Events[len(inspection.Events)-1]
	if last.To != agentrun.StateRunning {
		t.Fatalf("stream head = %s->%s, want an honest running head with no fabricated completion", last.From, last.To)
	}

	entries, err := store.ScanRecoveries(backing)
	if err != nil || len(entries) != 1 {
		t.Fatalf("recovery scan = %+v, %v; want exactly one entry", entries, err)
	}
	if entries[0].Class != store.RecoveryOperatorRequired {
		t.Fatalf("recovery class = %q, want operator_required", entries[0].Class)
	}
	if !strings.Contains(entries[0].Reason, "outcome unknown") {
		t.Fatalf("recovery reason = %q, want the exact missing evidence named", entries[0].Reason)
	}

	fresh := execution.NewControllerWithClock(backing, &failingAdapter{detail: "unused"}, fixedClock())
	before, err := backing.ReadEvents(runID, 0, 128)
	if err != nil {
		t.Fatal(err)
	}
	if _, recoverErr := fresh.Recover(context.Background(), handle.RunID, 0); !errors.Is(recoverErr, execution.ErrRunNotRecoverable) {
		t.Fatalf("Recover(running head) = %v, want ErrRunNotRecoverable", recoverErr)
	}
	after, err := backing.ReadEvents(runID, 0, 128)
	if err != nil || len(after.Events) != len(before.Events) {
		t.Fatalf("refused recovery mutated the stream: %d events after %d", len(after.Events), len(before.Events))
	}
}

func TestCrashMidRunOwnerDeathDuringCancellationReadsCanceledOrphaned(t *testing.T) {
	backing := newE2EStore(t)
	job, interruptedInvocation, seeded := seedOrphanedCancellationStream(t, backing, "crash-owner-loss")
	runID := string(job.RunID())

	reconciled, err := backing.ReadReconciledProjection(runID)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.State != agentrun.StateCanceled || reconciled.Terminal != agentrun.TerminalCancellation || !reconciled.OrphanedCancellation {
		t.Fatalf("reconciled view = %+v, want the canceled-orphaned verdict", reconciled)
	}

	entries, err := store.ScanRecoveries(backing)
	if err != nil || len(entries) != 1 {
		t.Fatalf("recovery scan = %+v, %v; want exactly one entry", entries, err)
	}
	if entries[0].Class != store.RecoveryOrphanedCanceled || !entries[0].Reconciled {
		t.Fatalf("recovery entry = %+v, want a reconciled orphaned_canceled verdict", entries[0])
	}
	if entries[0].HeadSequence != seeded[len(seeded)-1].Sequence {
		t.Fatalf("head sequence = %d, want %d", entries[0].HeadSequence, seeded[len(seeded)-1].Sequence)
	}

	// No fabricated completion anywhere in the raw stream: observation is
	// read-only and never appends the missing canceled settlement frame.
	inspection, err := execution.NewController(backing, nil).Inspect(context.Background(), job.RunID())
	if err != nil {
		t.Fatal(err)
	}
	if len(inspection.Events) != len(seeded) {
		t.Fatalf("events = %d, want the %d seeded frames untouched", len(inspection.Events), len(seeded))
	}
	if got := countTransitions(t, inspection.Events, agentrun.StateTerminating, agentrun.StateCanceled); got != 0 {
		t.Fatalf("found %d fabricated terminating->canceled settlements; owner death must not invent one", got)
	}
	for _, outcome := range inspection.Outcomes {
		if outcome.InvocationID == string(interruptedInvocation) && outcome.Class == agentrun.OutcomeCancellation {
			t.Fatal("a canceled outcome record exists before explicit recovery; it must only be materialized by Recover/Retry")
		}
	}
	assertSingleRun(t, backing, runID)
}

// Scenario b — timeout. The adapter's own budget expires; the run settles
// timed_out honestly, whether the provider was an in-process call or a real
// sleeping child killed through its context deadline.

func TestTimeoutSettlesTimedOutHonestly(t *testing.T) {
	backing := newE2EStore(t)
	controller := execution.NewControllerWithClock(backing, deadlineAdapter{budget: 20 * time.Millisecond}, fixedClock())

	handle, err := controller.Start(context.Background(), e2eRequest("timeout-in-process"), e2ePolicy())
	if err != nil {
		t.Fatal(err)
	}
	completion, err := handle.Wait(context.Background())
	if err != nil {
		t.Fatalf("handle.Wait(): %v", err)
	}
	if completion.State != agentrun.StateTimedOut || completion.Outcome != agentrun.OutcomeTimeout {
		t.Fatalf("completion = %s/%s, want timed_out/timeout", completion.State, completion.Outcome)
	}
	if !strings.Contains(completion.Error, "budget expired") {
		t.Fatalf("completion error = %q, want the adapter's own timeout detail preserved", completion.Error)
	}

	inspection := assertRunsInspection(t, backing, controller, string(handle.RunID),
		agentrun.StateTimedOut, agentrun.TerminalTimeout)
	if len(inspection.Outcomes) != 1 || inspection.Outcomes[0].Class != agentrun.OutcomeTimeout {
		t.Fatalf("outcomes = %+v, want exactly one timeout outcome", inspection.Outcomes)
	}
	if !strings.Contains(inspection.Outcomes[0].Error, "deadline exceeded") {
		t.Fatalf("outcome error = %q, want the wrapped context.DeadlineExceeded text", inspection.Outcomes[0].Error)
	}
}

func TestTimeoutSubprocessChildKilledAtDeadlineIsDurableEvidence(t *testing.T) {
	requireLinux(t)
	backing := newE2EStore(t)
	adapter := subprocessDeadlineAdapter{budget: 150 * time.Millisecond}
	controller := execution.NewControllerWithClock(backing, adapter, fixedClock())

	handle, err := controller.Start(context.Background(), e2eRequest("timeout-subprocess"), e2ePolicy())
	if err != nil {
		t.Fatal(err)
	}
	completion, err := handle.Wait(context.Background())
	if err != nil {
		t.Fatalf("handle.Wait(): %v", err)
	}
	if completion.State != agentrun.StateTimedOut || completion.Outcome != agentrun.OutcomeTimeout {
		t.Fatalf("completion = %s/%s, want timed_out/timeout", completion.State, completion.Outcome)
	}

	// The sleeping child named in the durable error text is really gone.
	detail := completion.Error
	at := strings.Index(detail, "child ")
	if at < 0 {
		t.Fatalf("completion error = %q, want the child pid named", detail)
	}
	pid, err := strconv.Atoi(strings.Fields(detail[at+6:])[0])
	if err != nil {
		t.Fatalf("pid unparsable from %q: %v", detail, err)
	}
	deadline := time.Now().Add(observationBudget)
	for procAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("sleep child %d survived its context deadline", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}

	inspection := assertRunsInspection(t, backing, controller, string(handle.RunID),
		agentrun.StateTimedOut, agentrun.TerminalTimeout)
	if len(inspection.Outcomes) != 1 || inspection.Outcomes[0].Class != agentrun.OutcomeTimeout {
		t.Fatalf("outcomes = %+v, want exactly one timeout outcome", inspection.Outcomes)
	}
}

func procAlive(pid int) bool {
	_, err := os.Stat("/proc/" + strconv.Itoa(pid))
	return err == nil
}

// Scenario c — abort. The controller authors the canceled settlement itself;
// a late adapter result that ignored cancellation cannot overwrite it, and a
// repeated abort stays idempotent.

func TestAbortSettlesControllerAuthoredAndDropsLateResults(t *testing.T) {
	backing := newE2EStore(t)
	adapter := newBlockingAdapter()
	controller := execution.NewControllerWithClock(backing, adapter, fixedClock())
	host := execution.NewInProcessHost(controller)

	handle, err := host.Start(context.Background(), execution.StartRequest{
		Request:     e2eRequest("abort-late-result"),
		Policy:      e2ePolicy(),
		AuthContext: execution.AuthContext{Principal: "operator"},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitStarted(t, adapter.started)

	result, err := host.Apply(context.Background(), execution.ApplyRequest{
		RunID:       handle.RunID,
		Action:      execution.ControlAction{Kind: execution.ActionAbort},
		ActionID:    "abort-1",
		AuthContext: execution.AuthContext{Principal: "operator"},
	})
	if err != nil || !result.Accepted {
		t.Fatalf("host Apply(abort) = %+v, %v; want accepted", result, err)
	}
	completion, err := handle.Wait(context.Background())
	if err != nil || completion.State != agentrun.StateCanceled {
		t.Fatalf("completion = %+v, %v; want a controller-authored canceled settlement", completion, err)
	}
	if completion.Error != "aborted while running" {
		t.Fatalf("settlement detail = %q, want the plain controller-authored abort reason", completion.Error)
	}

	// The late provider result arrives after the settlement and must change
	// nothing: finish() finds the run already done.
	adapter.Release()
	adapter.waitReturned(t)

	runID := string(handle.RunID)
	inspection := assertRunsInspection(t, backing, controller, runID,
		agentrun.StateCanceled, agentrun.TerminalCancellation)

	if len(inspection.Outcomes) != 1 {
		t.Fatalf("outcomes = %+v, want exactly the single canceled settlement", inspection.Outcomes)
	}
	outcome := inspection.Outcomes[0]
	if outcome.Class != agentrun.OutcomeCancellation ||
		outcome.InvocationID != string(completion.InvocationID) ||
		outcome.Error != "aborted while running" {
		t.Fatalf("outcome = %+v, want the preserved canceled record for %s", outcome, completion.InvocationID)
	}
	if got := countTransitions(t, inspection.Events, agentrun.StateRunning, agentrun.StateCanceled); got != 1 {
		t.Fatalf("canceled settlements = %d, want exactly once", got)
	}
	last := inspection.Events[len(inspection.Events)-1]
	if last.From != agentrun.StateRunning || last.To != agentrun.StateCanceled || last.Decision != agentrun.DecisionAbort {
		t.Fatalf("terminal event = %s->%s/%s, want running->canceled by abort",
			last.From, last.To, last.Decision)
	}
	if strings.Contains(last.OutputHash+last.OutcomeError, "late provider output") {
		t.Fatal("late adapter output leaked into the durable settlement")
	}

	// Repeated abort stays idempotent: accepted again, still one settlement.
	repeat, err := host.Apply(context.Background(), execution.ApplyRequest{
		RunID:       handle.RunID,
		Action:      execution.ControlAction{Kind: execution.ActionAbort},
		ActionID:    "abort-2",
		AuthContext: execution.AuthContext{Principal: "operator"},
	})
	if err != nil || !repeat.Accepted {
		t.Fatalf("repeated host Apply(abort) = %+v, %v; want idempotent acceptance", repeat, err)
	}
	reread, err := backing.ReadEvents(runID, 0, 128)
	if err != nil {
		t.Fatal(err)
	}
	if len(reread.Events) != len(inspection.Events) {
		t.Fatalf("events = %d after repeated abort, want unchanged %d", len(reread.Events), len(inspection.Events))
	}
}
