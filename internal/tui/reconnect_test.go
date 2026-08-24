package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/attach"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// Reconnect-policy coverage: bounded backoff progression, honest
// exhaustion, cursor-resumed replay after simulated endpoint recovery, and
// the single-scheduler guards that stop tick-chain multiplication.

// enterReconnecting seeds a model into stateReconnecting with exactly one
// backoff tick claimed and pending.
func enterReconnecting(t *testing.T) Model {
	t.Helper()
	dead := &fakeHost{
		inspectErr:    errors.New("connection is closed"),
		subscribeErrs: []error{errors.New("connection is closed")},
	}
	m := newTestModel(t, newScriptedProvider(dead), 0, runningView())
	m, first := update(t, m, hostErrMsg{Err: errors.New("dial failed"), Op: "observe"})
	if m.conn != stateReconnecting || m.attempt != 1 || first == nil {
		t.Fatalf("setup: conn=%d attempt=%d cmd=%v, want reconnecting at attempt 1 with one tick scheduled",
			m.conn, m.attempt, first)
	}
	return m
}

// TestRefreshDuringReconnectingIsInert pins key-routing guard (a): pressing
// r mid-backoff must neither schedule a second observe chain nor move the
// attempt counter — the single pending backoff tick keeps driving attempts,
// so a manual refresh would otherwise double the burn rate.
func TestRefreshDuringReconnectingIsInert(t *testing.T) {
	m := enterReconnecting(t)
	attemptBefore := m.attempt

	for _, key := range []string{"r", "a", "e", "y"} {
		_, cmd := update(t, m, keyMsg(key))
		if cmd != nil {
			t.Fatalf("%s during reconnecting scheduled %v, want nothing", key, cmd)
		}
	}
	if m.attempt != attemptBefore {
		t.Fatalf("inert keys moved attempt %d -> %d", attemptBefore, m.attempt)
	}
	if m.conn != stateReconnecting {
		t.Fatalf("inert keys flipped conn to %d, want still reconnecting", m.conn)
	}
}

// TestActionFailureDuringReconnectingKeepsSingleTickChain pins scheduler
// guard (b): an action whose host exchange failed during reconnecting lands
// in the status line and advances the attempt counter, but must not schedule
// a second backoff tick while one is already pending.
func TestActionFailureDuringReconnectingKeepsSingleTickChain(t *testing.T) {
	m := enterReconnecting(t)
	attemptBefore := m.attempt

	m, cmd := update(t, m, hostErrMsg{Err: errors.New("provider script exhausted"), Op: "abort"})
	if cmd != nil {
		t.Fatalf("failed action during reconnecting scheduled %v, want the pending tick to stay the only scheduler", cmd)
	}
	if m.conn != stateReconnecting {
		t.Fatalf("failed action flipped conn to %d, want still reconnecting", m.conn)
	}
	if m.attempt != attemptBefore+1 {
		t.Fatalf("failed action left attempt at %d, want %d", m.attempt, attemptBefore+1)
	}
	if !strings.Contains(m.status, "abort") {
		t.Fatalf("status = %q, want the abort failure surfaced in the status line", m.status)
	}

	// The pre-existing pending tick still owns scheduling: when it fires, it
	// releases the slot, observes, and its failure may claim exactly one new
	// tick — never two.
	m, tickCmd := update(t, m, backoffTickMsg{})
	msg := runCmd(t, "pending reconnect attempt", tickCmd)
	failure, ok := msg.(hostErrMsg)
	if !ok {
		t.Fatalf("reconnect attempt produced %T (%v), want hostErrMsg", msg, msg)
	}
	m, next := update(t, m, failure)
	if m.conn != stateReconnecting || next == nil {
		t.Fatalf("post-pending failure: conn=%d cmd=%v, want reconnecting with exactly one new tick", m.conn, next)
	}
}

func TestReconnectBackoffProgressionAndExhaustion(t *testing.T) {
	dead := &fakeHost{
		inspectErr:    errors.New("connection is closed"),
		subscribeErrs: []error{errors.New("connection is closed")},
	}
	provider := newScriptedProvider(dead) // every resolution fails afterwards
	m := newTestModel(t, provider, 0, runningView())

	m, cmd := update(t, m, hostErrMsg{Err: errors.New("dial failed"), Op: "observe"})
	if m.conn != stateReconnecting || m.attempt != 1 {
		t.Fatalf("first failure: conn=%d attempt=%d, want reconnecting/1", m.conn, m.attempt)
	}
	if cmd == nil {
		t.Fatalf("first failure scheduled no backoff tick")
	}

	attemptsSeen := []int{1}
	for m.conn == stateReconnecting {
		_, cmd = update(t, m, backoffTickMsg{})
		if cmd == nil {
			t.Fatalf("attempt %d stopped scheduling before exhaustion", m.attempt)
		}
		msg := runCmd(t, "reconnect attempt", cmd)
		failure, ok := msg.(hostErrMsg)
		if !ok {
			t.Fatalf("reconnect attempt produced %T (%v), want hostErrMsg while the endpoint is down", msg, msg)
		}
		m, _ = update(t, m, failure)
		attemptsSeen = append(attemptsSeen, m.attempt)
		if m.attempt > MaxReconnectAttempts+1 {
			t.Fatalf("attempts ran past the cap: %v", attemptsSeen)
		}
	}
	if m.conn != stateLost {
		t.Fatalf("exhaustion left conn=%d, want lost contact", m.conn)
	}
	last := attemptsSeen[len(attemptsSeen)-1]
	if last != MaxReconnectAttempts+1 {
		t.Fatalf("exhausted at attempt %d, want %d\nattempts=%v", last, MaxReconnectAttempts+1, attemptsSeen)
	}
	for i := 1; i < len(attemptsSeen); i++ {
		if attemptsSeen[i] != attemptsSeen[i-1]+1 {
			t.Fatalf("backoff progression broke at %v", attemptsSeen)
		}
	}
	if m.status == "" || m.conn != stateLost {
		t.Fatalf("exhaustion message missing: %q", m.status)
	}
	// After giving up, nothing schedules anymore.
	_, cmd = update(t, m, backoffTickMsg{})
	if cmd != nil {
		t.Fatalf("backoff tick after exhaustion scheduled %v", cmd)
	}
	_, cmd = update(t, m, pollTickMsg{})
	if cmd != nil {
		t.Fatalf("poll tick after exhaustion scheduled %v", cmd)
	}
}

// TestCursorResumesStrictlyAfterLastApplied proves reconnect replay resumes
// strictly after the collector cursor: duplicate redelivery below the cursor
// is dropped, new frames apply exactly once, and the session returns to
// attached.
func TestCursorResumesStrictlyAfterLastApplied(t *testing.T) {
	good := &fakeHost{
		inspection: execution.Inspection{Projection: runningProjection()},
		page: store.EventPage{Events: []store.EventFrame{
			{Sequence: 3, InvocationID: "inv-1"}, // late duplicate below the cursor
			{Sequence: 4, InvocationID: "inv-1"},
			{Sequence: 5, InvocationID: "inv-1"},
		}},
	}
	provider := newScriptedProvider(good, good)
	m := newTestModel(t, provider, 3, runningView()) // cursor already at #3

	// Endpoint loss flips to reconnecting...
	m, cmd := update(t, m, hostErrMsg{Err: errors.New("connection died"), Op: "observe"})
	if m.conn != stateReconnecting {
		t.Fatalf("failure did not enter reconnecting: conn=%d", m.conn)
	}
	// ...and the synthesized backoff tick performs the reconnect attempt
	// through the provider, resuming strictly after cursor 3.
	_, cmd = update(t, m, backoffTickMsg{})
	msg := runCmd(t, "reconnect attempt", cmd)
	applied, ok := msg.(pagesAppliedMsg)
	if !ok {
		t.Fatalf("reconnect produced %T (%v), want pagesAppliedMsg", msg, msg)
	}
	// Feeding the message back through Update is what transitions the model:
	// reconnecting -> attached with the cursor-resumed view applied.
	m, _ = update(t, m, applied)
	if m.conn != stateAttached {
		t.Fatalf("successful reconnect left conn=%d, want attached", m.conn)
	}
	hostRequests := good.subscribes
	if len(hostRequests) == 0 || hostRequests[len(hostRequests)-1].AfterCursor != 3 {
		t.Fatalf("reconnect subscribed %+v, want AfterCursor 3 (strictly after the last applied frame)", hostRequests)
	}
	snapshot := m.Snapshot()
	if snapshot.Sequence != 5 {
		t.Fatalf("snapshot sequence = %d, want 5 after applying frames 4 and 5", snapshot.Sequence)
	}
	if len(snapshot.Invocations) != 1 {
		t.Fatalf("invocations = %d, want the single deduplicated invocation", len(snapshot.Invocations))
	}
}

// newTestModelWithView builds a model pre-seeded with an observed view.
func newTestModelWithView(t *testing.T, provider HostProvider, view attach.RunView) Model {
	t.Helper()
	return New("run-tui-test", "tester", provider, attach.NewReplayCollector(0), view, context.Background())
}

// TestReconnectBackoffDelaysGrow documents the delay ladder the scheduler
// uses: doubling from the base up to the cap.
func TestReconnectBackoffDelaysGrow(t *testing.T) {
	delays := make([]int64, 0, MaxReconnectAttempts)
	for attempt := 1; attempt <= MaxReconnectAttempts+2; attempt++ {
		delay := reconnectBackoff(attempt)
		delays = append(delays, int64(delay))
		if delay < 0 || delay > maxReconnectBackoff {
			t.Fatalf("attempt %d delay %s escapes the cap", attempt, delay)
		}
	}
	for i := 1; i < len(delays); i++ {
		if delays[i] < delays[i-1] && delays[i-1] < int64(maxReconnectBackoff) {
			t.Fatalf("delays regressed before reaching the cap: %v", delays)
		}
	}
	if delays[MaxReconnectAttempts+1] != int64(maxReconnectBackoff) {
		t.Fatalf("cap-clamped delay = %d, want %d", delays[MaxReconnectAttempts+1], maxReconnectBackoff)
	}
}
