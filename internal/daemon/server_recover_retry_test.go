package daemon

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vcSentinel/internal/execution"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
)

// failTimesThenSucceed fails the first n attempts with an ordinary error —
// which settles the run as retryably failed — and succeeds on every later
// attempt, so a relaunch scenario can drive failure then recovery through
// one adapter.
func failTimesThenSucceed(n int) funcAdapter {
	var mu sync.Mutex
	calls := 0
	return funcAdapter(func(context.Context, agentrun.LogicalJob, agentrun.InvocationEnvelope, string) (execution.AdapterResult, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls <= n {
			return execution.AdapterResult{}, errors.New("attempt failed")
		}
		return execution.AdapterResult{Output: "relaunched"}, nil
	})
}

// TestServerRetryAndRecoverParityOverWire proves, against the REAL socket,
// that the two relaunch operations carry sentinel identity across the wire:
// every refusal decodes into a *RemoteError whose Unwrap resolves errors.Is
// against the very sentinel the server-side failure carried, and the happy
// path relaunches the run inside its original identity. Local parity rides
// the same twin fixtures the rest of the vocabulary parity suite uses.
func TestServerRetryAndRecoverParityOverWire(t *testing.T) {
	fingerprint := testFingerprint()

	t.Run("retrying a succeeded run is not retryable", func(t *testing.T) {
		controller := newTestController(t, immediateAdapter("done"))
		ep := startTestServer(t, controller)
		conn := connectClient(t, ep, fingerprint, "")

		started := callOp(t, conn, OpStart, testStartEnvelope())
		if !started.OK {
			t.Fatalf("wire start failed: %+v", started.Error)
		}
		var handle execution.Handle
		decodeBodyInto(t, started.Body, &handle)
		waitForStateViaWire(t, conn, handle.RunID, agentrun.StateSucceeded)

		assertRemoteSentinel(t, callOp(t, conn, OpRetry, execution.RetryRequest{
			RunID: handle.RunID, AuthContext: testAuth(),
		}), execution.ErrRunNotRetryable)

		host := execution.NewInProcessHost(newTestController(t, immediateAdapter("done")))
		twinHandle, err := host.Start(context.Background(), testStartEnvelope())
		if err != nil {
			t.Fatalf("twin start: %v", err)
		}
		if _, err := twinHandle.Wait(context.Background()); err != nil {
			t.Fatalf("twin wait: %v", err)
		}
		if _, twinErr := host.Retry(context.Background(), execution.RetryRequest{
			RunID: twinHandle.RunID, AuthContext: testAuth(),
		}); !errors.Is(twinErr, execution.ErrRunNotRetryable) {
			t.Fatalf("twin retry error = %v, want ErrRunNotRetryable", twinErr)
		}
	})

	t.Run("recovering a final head is not recoverable", func(t *testing.T) {
		controller := newTestController(t, immediateAdapter("done"))
		ep := startTestServer(t, controller)
		conn := connectClient(t, ep, fingerprint, "")

		started := callOp(t, conn, OpStart, testStartEnvelope())
		if !started.OK {
			t.Fatalf("wire start failed: %+v", started.Error)
		}
		var handle execution.Handle
		decodeBodyInto(t, started.Body, &handle)
		waitForStateViaWire(t, conn, handle.RunID, agentrun.StateSucceeded)

		assertRemoteSentinel(t, callOp(t, conn, OpRecover, execution.RecoverRequest{
			RunID: handle.RunID, AuthContext: testAuth(),
		}), execution.ErrRunNotRecoverable)

		host := execution.NewInProcessHost(newTestController(t, immediateAdapter("done")))
		twinHandle, err := host.Start(context.Background(), testStartEnvelope())
		if err != nil {
			t.Fatalf("twin start: %v", err)
		}
		if _, err := twinHandle.Wait(context.Background()); err != nil {
			t.Fatalf("twin wait: %v", err)
		}
		if _, twinErr := host.Recover(context.Background(), execution.RecoverRequest{
			RunID: twinHandle.RunID, AuthContext: testAuth(),
		}); !errors.Is(twinErr, execution.ErrRunNotRecoverable) {
			t.Fatalf("twin recover error = %v, want ErrRunNotRecoverable", twinErr)
		}
	})

	t.Run("retry with a stale revision fails explicitly", func(t *testing.T) {
		controller := newTestController(t, failTimesThenSucceed(1))
		ep := startTestServer(t, controller)
		conn := connectClient(t, ep, fingerprint, "")

		started := callOp(t, conn, OpStart, testStartEnvelope())
		if !started.OK {
			t.Fatalf("wire start failed: %+v", started.Error)
		}
		var handle execution.Handle
		decodeBodyInto(t, started.Body, &handle)
		waitForStateViaWire(t, conn, handle.RunID, agentrun.StateFailed)

		inspected := callOp(t, conn, OpInspect, execution.InspectRequest{RunID: handle.RunID, AuthContext: testAuth()})
		if !inspected.OK {
			t.Fatalf("wire inspect failed: %+v", inspected.Error)
		}
		var inspection execution.Inspection
		decodeBodyInto(t, inspected.Body, &inspection)

		stale := execution.RetryRequest{
			RunID: handle.RunID, ExpectedRevision: inspection.Projection.Revision + 1,
			AuthContext: testAuth(),
		}
		assertRemoteSentinel(t, callOp(t, conn, OpRetry, stale), execution.ErrStaleRevision)

		_, localErr := controller.Retry(context.Background(), stale.RunID, stale.ExpectedRevision)
		if !errors.Is(localErr, execution.ErrStaleRevision) {
			t.Fatalf("local error = %v, want ErrStaleRevision chain", localErr)
		}
	})

	t.Run("retry relaunches a failed run and it progresses to success", func(t *testing.T) {
		controller := newTestController(t, failTimesThenSucceed(1))
		ep := startTestServer(t, controller)
		conn := connectClient(t, ep, fingerprint, "")

		started := callOp(t, conn, OpStart, testStartEnvelope())
		if !started.OK {
			t.Fatalf("wire start failed: %+v", started.Error)
		}
		var handle execution.Handle
		decodeBodyInto(t, started.Body, &handle)
		waitForStateViaWire(t, conn, handle.RunID, agentrun.StateFailed)

		retried := callOp(t, conn, OpRetry, execution.RetryRequest{
			RunID: handle.RunID, AuthContext: testAuth(),
		})
		if !retried.OK {
			t.Fatalf("wire retry failed: %+v", retried.Error)
		}
		var relaunch execution.Handle
		decodeBodyInto(t, retried.Body, &relaunch)
		if relaunch.RunID != handle.RunID || relaunch.JobID != handle.JobID {
			t.Fatalf("wire retry identity = %s/%s, want the original run %s/%s",
				relaunch.RunID, relaunch.JobID, handle.RunID, handle.JobID)
		}
		if relaunch.InvocationID == handle.InvocationID {
			t.Fatal("a wire retry must launch a new attempt envelope")
		}
		waitForStateViaWire(t, conn, relaunch.RunID, agentrun.StateSucceeded)

		final := callOp(t, conn, OpInspect, execution.InspectRequest{RunID: relaunch.RunID, AuthContext: testAuth()})
		if !final.OK {
			t.Fatalf("final wire inspect failed: %+v", final.Error)
		}
		var after execution.Inspection
		decodeBodyInto(t, final.Body, &after)
		if len(after.Outcomes) != 2 || after.Outcomes[0].Class != agentrun.OutcomeFailure ||
			after.Outcomes[1].Class != agentrun.OutcomeSuccess {
			t.Fatalf("durable outcomes = %+v, want the preserved failure followed by the retry success", after.Outcomes)
		}
	})
}

// TestRemoteClientRetryAndRecoverCarrySentinelIdentity spot-checks, through
// the CLIENT adapter rather than raw frames, that the two relaunch methods
// preserve errors.Is identity over the wire: ErrRunNotRetryable and
// ErrRunNotRecoverable on final outcomes, and ErrStaleRevision under a
// wrong ExpectedRevision pin.
func TestRemoteClientRetryAndRecoverCarrySentinelIdentity(t *testing.T) {
	doneController := newTestController(t, immediateAdapter("done"))
	doneEp := startTestServerForRemote(t, doneController)
	doneHost := dialRemoteHostForTest(t, doneEp)

	settled, err := doneHost.Start(context.Background(), execution.StartRequest{
		Candidate:   "candidate:remote-relaunch",
		Prompt:      "prompt:remote-relaunch",
		Policy:      store.RunPolicy{ID: "wire-relaunch-policy"},
		AuthContext: testAuth(),
	})
	if err != nil {
		t.Fatalf("remote Start: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		inspection, inspectErr := doneHost.Inspect(context.Background(), execution.InspectRequest{
			RunID: settled.RunID, AuthContext: testAuth(),
		})
		if inspectErr == nil && inspection.Projection.State == agentrun.StateSucceeded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("run never reached StateSucceeded through the remote client")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if _, retryErr := doneHost.Retry(context.Background(), execution.RetryRequest{
		RunID: settled.RunID, AuthContext: testAuth(),
	}); !errors.Is(retryErr, execution.ErrRunNotRetryable) {
		t.Fatalf("remote Retry on a succeeded run = %v, want errors.Is ErrRunNotRetryable", retryErr)
	}
	if _, recoverErr := doneHost.Recover(context.Background(), execution.RecoverRequest{
		RunID: settled.RunID, AuthContext: testAuth(),
	}); !errors.Is(recoverErr, execution.ErrRunNotRecoverable) {
		t.Fatalf("remote Recover on a succeeded run = %v, want errors.Is ErrRunNotRecoverable", recoverErr)
	}

	failedController := newTestController(t, failTimesThenSucceed(1))
	failedEp := startTestServerForRemote(t, failedController)
	failedHost := dialRemoteHostForTest(t, failedEp)

	failed, err := failedHost.Start(context.Background(), execution.StartRequest{
		Candidate:   "candidate:remote-stale",
		Prompt:      "prompt:remote-stale",
		Policy:      store.RunPolicy{ID: "wire-relaunch-policy"},
		AuthContext: testAuth(),
	})
	if err != nil {
		t.Fatalf("remote Start: %v", err)
	}
	var revision uint64
	deadline = time.Now().Add(5 * time.Second)
	for {
		inspection, inspectErr := failedHost.Inspect(context.Background(), execution.InspectRequest{
			RunID: failed.RunID, AuthContext: testAuth(),
		})
		if inspectErr == nil && inspection.Projection.State == agentrun.StateFailed {
			revision = inspection.Projection.Revision
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("run never reached StateFailed through the remote client")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if _, staleErr := failedHost.Retry(context.Background(), execution.RetryRequest{
		RunID: failed.RunID, ExpectedRevision: revision + 1, AuthContext: testAuth(),
	}); !errors.Is(staleErr, execution.ErrStaleRevision) {
		t.Fatalf("remote Retry with a stale revision = %v, want errors.Is ErrStaleRevision", staleErr)
	}
}
