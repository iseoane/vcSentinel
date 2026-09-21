package daemon

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vcSentinel/internal/execution"
)

// awaitingThenFinalAdapter awaits a decision on the first physical attempt
// and settles successfully after the respond continuation.
func awaitingThenFinalAdapter() funcAdapter {
	var mu sync.Mutex
	calls := 0
	return funcAdapter(func(context.Context, agentrun.LogicalJob, agentrun.InvocationEnvelope, string) (execution.AdapterResult, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			return execution.AdapterResult{Output: "need-input", AwaitingDecision: true}, nil
		}
		return execution.AdapterResult{Output: "final"}, nil
	})
}

func TestServerStartSettlesAndInspectMatchesDirectController(t *testing.T) {
	controller := newTestController(t, immediateAdapter("done"))
	ep := startTestServer(t, controller)
	conn := connectClient(t, ep, testFingerprint(), "")

	response := callOp(t, conn, OpStart, testStartEnvelope())
	if !response.OK {
		t.Fatalf("wire start failed: %+v", response.Error)
	}
	var handle execution.Handle
	decodeBodyInto(t, response.Body, &handle)
	if handle.RunID == "" {
		t.Fatal("wire start returned an empty run identity")
	}

	waitForStateViaWire(t, conn, handle.RunID, agentrun.StateSucceeded)

	wireInspect := callOp(t, conn, OpInspect, execution.InspectRequest{RunID: handle.RunID, AuthContext: testAuth()})
	if !wireInspect.OK {
		t.Fatalf("wire inspect failed: %+v", wireInspect.Error)
	}
	var remoteInspection execution.Inspection
	decodeBodyInto(t, wireInspect.Body, &remoteInspection)

	directInspection, err := controller.Inspect(context.Background(), handle.RunID)
	if err != nil {
		t.Fatalf("direct inspect: %v", err)
	}
	if !reflect.DeepEqual(remoteInspection, directInspection) {
		t.Fatal("wire inspection differs from direct-controller inspection of the same durable evidence")
	}

	// Parity against InProcessHost on an identical twin store/controller fed
	// the same envelope: the terminal projection must agree.
	twinHost := execution.NewInProcessHost(newTestController(t, immediateAdapter("done")))
	twinHandle, err := twinHost.Start(context.Background(), testStartEnvelope())
	if err != nil {
		t.Fatalf("twin start: %v", err)
	}
	if _, err := twinHandle.Wait(context.Background()); err != nil {
		t.Fatalf("twin wait: %v", err)
	}
	twinInspection, err := twinHost.Inspect(context.Background(), execution.InspectRequest{
		RunID: twinHandle.RunID, AuthContext: testAuth(),
	})
	if err != nil {
		t.Fatalf("twin inspect: %v", err)
	}
	if remoteInspection.Projection.State != twinInspection.Projection.State ||
		remoteInspection.Projection.Terminal != twinInspection.Projection.Terminal {
		t.Fatalf("terminal projections diverge: wire %+v twin %+v",
			remoteInspection.Projection, twinInspection.Projection)
	}
}

func TestServerRespondReachesAwaitingAndAbortSettlesCanceled(t *testing.T) {
	fingerprint := testFingerprint()

	// Respond flow over the wire: start, await, respond, settle success.
	controller := newTestController(t, awaitingThenFinalAdapter())
	ep := startTestServer(t, controller)
	conn := connectClient(t, ep, fingerprint, "")

	started := callOp(t, conn, OpStart, testStartEnvelope())
	if !started.OK {
		t.Fatalf("wire start failed: %+v", started.Error)
	}
	var handle execution.Handle
	decodeBodyInto(t, started.Body, &handle)
	waitForStateViaWire(t, conn, handle.RunID, agentrun.StateAwaitingDecision)

	responsed := callOp(t, conn, OpApply, execution.ApplyRequest{
		RunID:       handle.RunID,
		Action:      execution.ControlAction{Kind: execution.ActionRespond, Response: "continue"},
		ActionID:    "respond-1",
		AuthContext: testAuth(),
	})
	if !responsed.OK {
		t.Fatalf("wire respond failed: %+v", responsed.Error)
	}
	var result execution.ApplyResult
	decodeBodyInto(t, responsed.Body, &result)
	if !result.Accepted {
		t.Fatal("wire respond was not accepted")
	}
	waitForStateViaWire(t, conn, handle.RunID, agentrun.StateSucceeded)

	// Abort flow over the wire on a separate server/run: cancel settles even
	// though the fake adapter is still blocked on its channel.
	release := make(chan struct{})
	defer close(release)
	abortController := newTestController(t, blockingAdapter(release))
	abortEp := startTestServer(t, abortController)
	abortConn := connectClient(t, abortEp, fingerprint, "")

	abortStarted := callOp(t, abortConn, OpStart, testStartEnvelope())
	if !abortStarted.OK {
		t.Fatalf("wire abort-flow start failed: %+v", abortStarted.Error)
	}
	var abortHandle execution.Handle
	decodeBodyInto(t, abortStarted.Body, &abortHandle)
	waitForStateViaWire(t, abortConn, abortHandle.RunID, agentrun.StateRunning)

	aborted := callOp(t, abortConn, OpApply, execution.ApplyRequest{
		RunID:       abortHandle.RunID,
		Action:      execution.ControlAction{Kind: execution.ActionAbort},
		ActionID:    "abort-1",
		AuthContext: testAuth(),
	})
	if !aborted.OK {
		t.Fatalf("wire abort failed: %+v", aborted.Error)
	}
	waitForStateViaWire(t, abortConn, abortHandle.RunID, agentrun.StateCanceled)
}
