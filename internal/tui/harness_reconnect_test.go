package tui

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/attach"
	"github.com/ISeoane-Quental/vas.sentinel/internal/daemon"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
)

// This file carries the harness scenario for connection loss: bounded
// reconnect after the client's wire dies, and replay resumption that lands
// every stream event in the collector exactly once, in order. It reuses the
// daemon fixtures and driving helpers defined in harness_test.go unchanged.

// recordingProvider dials through the harness base provider, records every
// dialed host so the test can kill the client connections, and injects a
// bounded number of forced resolution failures to drive the reconnect path.
type recordingProvider struct {
	mu       sync.Mutex
	base     HostProvider
	dialed   []execution.RepositoryHost
	failures int // remaining forced failures before the next good dial
}

func (p *recordingProvider) Host() (execution.RepositoryHost, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failures > 0 {
		p.failures--
		return nil, errors.New("forced endpoint loss")
	}
	host, err := p.base.Host()
	if err != nil {
		return nil, err
	}
	p.dialed = append(p.dialed, host)
	return host, nil
}

// Reset is part of the HostProvider seam; this double's dial sequence is
// driven explicitly by the test, so there is no cache to drop.
func (p *recordingProvider) Reset() {}

func (p *recordingProvider) forceFailures(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failures += n
}

func (p *recordingProvider) closeDialed() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, host := range p.dialed {
		if closer, ok := host.(*daemon.RemoteHost); ok {
			_ = closer.Close()
		}
	}
	p.dialed = nil
}

// frameSequences extracts the applied stream's sequence numbers.
func frameSequences(collector *attach.ReplayCollector) []uint64 {
	frames := collector.Frames()
	sequences := make([]uint64, 0, len(frames))
	for _, frame := range frames {
		sequences = append(sequences, frame.Sequence)
	}
	return sequences
}

// TestHarnessReconnectResumesReplayExactlyOnce is AC2+AC4 evidence: killing
// the client connection flips the session into bounded reconnect, durable
// progress happens while disconnected, and the redialed observe resumes
// strictly after the last applied cursor so every event lands in the
// collector exactly once, in order — proven against a fresh full replay of
// the same stream as ground truth.
func TestHarnessReconnectResumesReplayExactlyOnce(t *testing.T) {
	gate := newGateAdapter()
	harness := startHarnessDaemon(t, gate)
	wrapped := &recordingProvider{base: harness.provider}
	harness.provider = wrapped

	runID, seedHost := startHarnessRun(t, harness)
	defer func() {
		if closer, ok := seedHost.(*daemon.RemoteHost); ok {
			_ = closer.Close()
		}
	}()

	model := New(runID, harnessPrincipal, harness.provider, attach.NewReplayCollector(0), attach.RunView{}, context.Background())
	model = driveUntilRunning(t, model)
	beforeLoss := frameSequences(model.collector)

	// Kill every client connection server-tracked so far, then let durable
	// progress happen while nothing is attached. The ground-truth replay runs
	// on its own collector and its own dialed hosts, never through the model.
	wrapped.closeDialed()
	gate.open()
	groundTruth := attach.NewReplayCollector(0)
	ctx := context.Background()
	deadline := time.Now().Add(5 * time.Second)
	for {
		host, err := harness.provider.Host()
		if err == nil {
			_, _, err := ObserveSnapshot(ctx, host, runID, harnessPrincipal, groundTruth)
			if closer, ok := host.(*daemon.RemoteHost); ok {
				_ = closer.Close()
			}
			if err == nil && groundTruth.Cursor() > beforeLoss[len(beforeLoss)-1] {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the released run never made durable progress")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Force the next model-driven resolution to fail so the observe lands as
	// a host error and the session enters bounded reconnect.
	wrapped.forceFailures(1)

	// The next observe must fail into reconnecting: attempt 1 scheduled.
	failed, scheduledCmd := step(t, model, pollTickMsg{})
	msg := scheduledCmd() // observeCmd runs: forced failure surfaces
	errMsg, ok := msg.(hostErrMsg)
	if !ok {
		t.Fatalf("expected a host error into reconnecting, got %T", msg)
	}
	reconnecting, tickCmd := step(t, failed, errMsg)
	if reconnecting.conn != stateReconnecting || reconnecting.attempt != 1 {
		t.Fatalf("conn/attempt = %v/%d, want reconnecting/1", reconnecting.conn, reconnecting.attempt)
	}
	if tickCmd == nil {
		t.Fatal("reconnecting without scheduling a backoff tick")
	}
	// Synthesize the tick instead of sleeping out the real 500ms backoff:
	// the transition under test consumes the message identically.
	resumed, observeCmd := step(t, reconnecting, backoffTickMsg{})
	pageMsg := observeCmd()
	applied, ok := pageMsg.(pagesAppliedMsg)
	if !ok {
		t.Fatalf("post-redial observe produced %T (%v), want pagesAppliedMsg", pageMsg, pageMsg)
	}
	final, _ := step(t, resumed, pageMsg)

	if final.conn != stateAttached {
		t.Fatalf("conn = %v after successful redial, want attached", final.conn)
	}
	if !final.Frozen() || applied.View.State != agentrun.StateSucceeded {
		t.Fatalf("final view = %s frozen=%v, want succeeded frozen", applied.View.State, final.Frozen())
	}

	// Exactly-once ordering: the collector's applied sequences must equal the
	// fresh full replay byte for byte AND be strictly increasing (no duplicate
	// or skipped cursor position across the reconnect boundary).
	live := frameSequences(model.collector)
	truth := frameSequences(groundTruth)
	if len(live) != len(truth) {
		t.Fatalf("applied %d frames across reconnect, want the full stream's %d", len(live), len(truth))
	}
	for i := range truth {
		if live[i] != truth[i] {
			t.Fatalf("applied sequence drifted at position %d: live=%d ground-truth=%d", i, live[i], truth[i])
		}
		if i > 0 && live[i] <= live[i-1] {
			t.Fatalf("applied sequence not strictly increasing at %d: %v", i, live)
		}
	}

	// Teardown drain: the released run settled succeeded while detached, but
	// its worker may still be finishing durable writes. Require store
	// quiescence before returning so cleanup never races a settlement writer.
	awaitRunQuiescence(t, harness, runID)
}
