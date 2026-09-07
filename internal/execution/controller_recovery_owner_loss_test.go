package execution

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// Ticket 10 slice 3: resume-after-owner-loss proof path. An orphaned-canceled
// stream (owner died mid-cancellation, R7 reconciled view) must recover
// through the existing Recover/Retry machinery under a NEW invocation
// identity while the interrupted attempt keeps its durable record. A plain
// running head (operator-required) must keep refusing without any write.

// seedOrphanedCancellationStream appends a valid owner-loss stream through the
// real store machinery: admission progress followed by a terminating
// escalation transition and no canceled settlement frame.
func seedOrphanedCancellationStream(t *testing.T, backing *store.Store, candidate string) (agentrun.LogicalJob, agentrun.Identity, []store.EventFrame) {
	t.Helper()
	request := agentrun.NewRunRequest(agentrun.Candidate(candidate), agentrun.Prompt("owner-loss fixture"), nil)
	job := agentrun.NewLogicalJob(request)
	if err := backing.CreateRun(job, testPolicy()); err != nil {
		t.Fatal(err)
	}
	invocation, err := agentrun.NewRootInvocation(job, 1, agentrun.DecisionStart)
	if err != nil {
		t.Fatal(err)
	}
	transitions := []struct {
		from, to agentrun.LifecycleState
	}{
		{agentrun.StateCreated, agentrun.StateQueued},
		{agentrun.StateQueued, agentrun.StateAdmitted},
		{agentrun.StateAdmitted, agentrun.StateRunning},
		{agentrun.StateRunning, agentrun.StateTerminating},
	}
	for offset, transition := range transitions {
		event, eventErr := agentrun.NewNormalizedEvent(invocation, transition.from, transition.to, agentrun.DecisionNone, time.Unix(1700000000+int64(offset), 0).UTC())
		if eventErr != nil {
			t.Fatal(eventErr)
		}
		if _, appendErr := backing.AppendRunEvent(string(job.RunID()), event, uint64(offset)); appendErr != nil {
			t.Fatal(appendErr)
		}
	}
	frames := originalFrames(t, backing, job.RunID())
	return job, invocation.InvocationID(), frames
}

func originalFrames(t *testing.T, backing *store.Store, runID agentrun.Identity) []store.EventFrame {
	t.Helper()
	page, err := backing.ReadEvents(string(runID), 0, 128)
	if err != nil {
		t.Fatal(err)
	}
	return page.Events
}

func TestRecoverOwnerLossRelaunchesUnderAFreshInvocationIdentity(t *testing.T) {
	backingStore := store.NewStore(t.TempDir())
	job, interruptedInvocation, interruptedFrames := seedOrphanedCancellationStream(t, backingStore, "recover-owner-loss")
	runID := job.RunID()

	fresh := NewControllerWithClock(backingStore, &scriptedAdapter{result: AdapterResult{Output: "resumed output"}}, fixedClock())
	handle, err := fresh.Recover(context.Background(), runID, 0)
	if err != nil {
		t.Fatalf("Recover(orphaned-canceled) error = %v, want a fresh-identity relaunch", err)
	}
	if handle.InvocationID == interruptedInvocation {
		t.Fatalf("recovered invocation %s reuses the interrupted attempt's identity; want a fresh one", handle.InvocationID)
	}

	completion, err := handle.Wait(context.Background())
	if err != nil || completion.State != agentrun.StateSucceeded {
		t.Fatalf("relaunched completion = %+v, %v; want success", completion, err)
	}

	// The resumed work is inspectable through the repository host seam with
	// the same principal contract as every other lifecycle read.
	host := NewInProcessHost(fresh)
	inspection, err := host.Inspect(context.Background(), InspectRequest{
		RunID:       runID,
		AuthContext: AuthContext{Principal: "operator"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Old-attempt preservation: the interrupted attempt keeps its canceled
	// outcome record with the reconciliation detail, and the pre-recovery
	// frames stay byte-honest (append-only prefix).
	var canceledOutcome, resumedOutcome *store.AttemptOutcome
	for index := range inspection.Outcomes {
		switch inspection.Outcomes[index].InvocationID {
		case string(interruptedInvocation):
			canceledOutcome = &inspection.Outcomes[index]
		case string(handle.InvocationID):
			resumedOutcome = &inspection.Outcomes[index]
		}
	}
	if canceledOutcome == nil || canceledOutcome.Class != agentrun.OutcomeCancellation ||
		canceledOutcome.Error != "owner death during cancellation was reconciled at explicit operator recovery" {
		t.Fatalf("interrupted attempt outcome = %+v, want the preserved reconciled cancellation record", canceledOutcome)
	}
	if resumedOutcome == nil || resumedOutcome.Class != agentrun.OutcomeSuccess {
		t.Fatalf("resumed attempt outcome = %+v, want the fresh attempt's success", resumedOutcome)
	}
	if len(inspection.Events) <= len(interruptedFrames) {
		t.Fatalf("events = %d, want the settlement and relaunch appended after the %d original frames", len(inspection.Events), len(interruptedFrames))
	}
	for index, frame := range interruptedFrames {
		if inspection.Events[index].ContentHash != frame.ContentHash {
			t.Fatalf("original frame %d was rewritten during recovery", frame.Sequence)
		}
	}

	if verification, err := fresh.Verify(context.Background(), runID); err != nil || !verification.Valid {
		t.Fatalf("post-recovery verification = %+v, %v; want an intact hash chain across both invocations", verification, err)
	}

	// The settled relaunch leaves no recovery work behind.
	entries, err := store.ScanRecoveries(backingStore)
	if err != nil || len(entries) != 0 {
		t.Fatalf("post-recovery scan = %+v, %v; want nothing left to recover", entries, err)
	}
}

// A cross-process race can surface the reconciled orphaned-canceled verdict
// while the evidence frames have already been re-read as empty. The
// settlement must refuse with the typed not-recoverable error instead of
// indexing an empty frame slice.
func TestOrphanedCancellationSettlementRefusesZeroFrameEvidence(t *testing.T) {
	backingStore := store.NewStore(t.TempDir())
	controller := NewControllerWithClock(backingStore, &scriptedAdapter{}, fixedClock())

	receipt, err := controller.appendOrphanedCancellationSettlement(nil, &store.RunProjection{Revision: 1}, reconciledOwnerDeathReason)
	if !errors.Is(err, ErrRunNotRecoverable) {
		t.Fatalf("settlement(zero frames) error = %v, want ErrRunNotRecoverable", err)
	}
	if !strings.Contains(err.Error(), "orphaned cancellation evidence vanished between reads") {
		t.Fatalf("settlement error = %q, want the explicit vanished-evidence reason", err)
	}
	if (receipt != store.EventReceipt{}) {
		t.Fatalf("settlement receipt = %+v, want zero value on refusal", receipt)
	}
}

func TestRetryAcceptsOwnerLossThroughTheSameContract(t *testing.T) {
	backingStore := store.NewStore(t.TempDir())
	job, interruptedInvocation, _ := seedOrphanedCancellationStream(t, backingStore, "retry-owner-loss")

	// Operators may call either entry point for an orphaned-canceled run:
	// direct Retry materializes the same reconciled settlement before the
	// fresh-identity relaunch.
	fresh := NewControllerWithClock(backingStore, &scriptedAdapter{result: AdapterResult{Output: "resumed via retry"}}, fixedClock())
	handle, err := fresh.Retry(context.Background(), job.RunID(), 0)
	if err != nil {
		t.Fatalf("Retry(orphaned-canceled) error = %v, want acceptance through the retry contract", err)
	}
	if handle.InvocationID == interruptedInvocation {
		t.Fatalf("retried invocation %s reuses the interrupted attempt's identity", handle.InvocationID)
	}
	if completion, err := handle.Wait(context.Background()); err != nil || completion.State != agentrun.StateSucceeded {
		t.Fatalf("retried completion = %+v, %v; want success", completion, err)
	}
	if verification, err := fresh.Verify(context.Background(), job.RunID()); err != nil || !verification.Valid {
		t.Fatalf("post-retry verification = %+v, %v; want valid", verification, err)
	}
}

func TestRecoverRefusesOperatorRequiredHeadWithoutWriting(t *testing.T) {
	backingStore := store.NewStore(t.TempDir())
	request := agentrun.NewRunRequest(agentrun.Candidate("recover-operator-required"), agentrun.Prompt("running head"), nil)
	job := agentrun.NewLogicalJob(request)
	if err := backingStore.CreateRun(job, testPolicy()); err != nil {
		t.Fatal(err)
	}
	invocation, err := agentrun.NewRootInvocation(job, 1, agentrun.DecisionStart)
	if err != nil {
		t.Fatal(err)
	}
	for offset, transition := range [][2]agentrun.LifecycleState{
		{agentrun.StateCreated, agentrun.StateQueued},
		{agentrun.StateQueued, agentrun.StateAdmitted},
		{agentrun.StateAdmitted, agentrun.StateRunning},
	} {
		event, eventErr := agentrun.NewNormalizedEvent(invocation, transition[0], transition[1], agentrun.DecisionStart, time.Unix(1700000100+int64(offset), 0).UTC())
		if eventErr != nil {
			t.Fatal(eventErr)
		}
		if _, appendErr := backingStore.AppendRunEvent(string(job.RunID()), event, uint64(offset)); appendErr != nil {
			t.Fatal(appendErr)
		}
	}
	page, err := backingStore.ReadEvents(string(job.RunID()), 0, 128)
	if err != nil {
		t.Fatal(err)
	}

	entries, err := store.ScanRecoveries(backingStore)
	if err != nil || len(entries) != 1 || entries[0].Class != store.RecoveryOperatorRequired {
		t.Fatalf("scan = %+v, %v; want exactly one operator-required entry", entries, err)
	}
	if !strings.Contains(entries[0].Reason, "outcome unknown") {
		t.Fatalf("operator-required reason = %q, want the exact missing evidence named", entries[0].Reason)
	}

	fresh := NewControllerWithClock(backingStore, &scriptedAdapter{}, fixedClock())
	if _, recoverErr := fresh.Recover(context.Background(), job.RunID(), 0); !errors.Is(recoverErr, ErrRunNotRecoverable) {
		t.Fatalf("Recover(operator-required) error = %v, want ErrRunNotRecoverable", recoverErr)
	}
	after, err := backingStore.ReadEvents(string(job.RunID()), 0, 128)
	if err != nil || len(after.Events) != len(page.Events) {
		t.Fatalf("refused recovery mutated the stream: %d events after %d", len(after.Events), len(page.Events))
	}
}
