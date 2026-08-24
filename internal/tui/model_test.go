package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbletea"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/daemon"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// Message- and session-state transition coverage pairing with model.go:
// status-line landing, post-action refresh scheduling, input-buffer edits,
// the terminal freeze/poll interplay, and the detach relay. Keyboard-routing
// coverage lives in update_test.go; the doubles live in fakes_test.go.

// TestTypingQInsideRespondInputDoesNotQuit proves the input consumes keys
// that are actions elsewhere.
func TestTypingQInsideRespondInputDoesNotQuit(t *testing.T) {
	m := newTestModel(t, newScriptedProvider(), 0, runningView())
	m, _ = update(t, m, keyMsg("e"))
	m, cmd := update(t, m, keyMsg("q"))
	if cmd != nil {
		t.Fatalf("typing q inside the input scheduled %v, want text entry", cmd)
	}
	if got := m.input.value(); got != "q" {
		t.Fatalf("typed buffer = %q, want %q", got, "q")
	}
}

// TestActionErrorLandsInStatusLine proves semantic rejections surface in the
// status line without crashing or auto-retrying.
func TestActionErrorLandsInStatusLine(t *testing.T) {
	m := newTestModel(t, newScriptedProvider(), 0, runningView())
	rejected := errors.New("run not active")
	m, cmd := update(t, m, actionResultMsg{Kind: "abort", Err: rejected})
	if cmd != nil {
		t.Fatalf("rejected action scheduled %v, want quiet status update", cmd)
	}
	want := "❌ abort rejected: run not active"
	if m.status != want {
		t.Fatalf("status = %q, want %q", m.status, want)
	}
	if m.conn != stateAttached {
		t.Fatalf("semantic rejection flipped conn state to %d, want attached", m.conn)
	}
}

// TestConnectionLevelActionErrorClassification pins the classifier that
// routes action errors: the daemon transport surfaces (closed-connection
// post-mortem text plus every mid-exchange path that markConnDead) are
// connection-level, while decoded server rejections and domain sentinels are
// semantic. The closed-connection match is a documented equivalence check on
// the daemon package's unexported errConnClosed sentinel text.
func TestConnectionLevelActionErrorClassification(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		connLevel bool
	}{
		{"nil error is nothing", nil, false},
		{"closed-connection post-mortem", errors.New("daemon: connection is closed"), true},
		{"mid-exchange write failure", errors.New("daemon: cannot send the apply request: broken pipe"), true},
		{"mid-exchange read failure", errors.New("daemon: cannot read the retry response: EOF"), true},
		{"corrupt response frame", errors.New("daemon: malformed apply response frame: junk"), true},
		{"empty response body", errors.New("daemon: the apply response carries no body"), true},
		{"decoded server rejection", &daemon.RemoteError{Code: "unknown.code", Message: "stale revision"}, false},
		{"plain semantic sentinel", errors.New("stale revision: competing writer"), false},
		{"context cancellation", context.DeadlineExceeded, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isConnectionLevelActionFailure(tt.err); got != tt.connLevel {
				t.Fatalf("isConnectionLevelActionFailure(%v) = %v, want %v", tt.err, got, tt.connLevel)
			}
		})
	}
}

// TestTransportFailureDuringFrozenActionUnfreezesIntoReconnect proves the
// R2 fix end to end: an abort left in flight when the terminal projection
// freezes the session comes back as a connection-level failure — the model
// unfreezes into bounded reconnecting with the provider reset and a backoff
// tick scheduled, and the later successful redial delivers a fresh observe.
// The run may not be truly settled if the session lost contact mid-action,
// so the frozen state must not survive a lost exchange.
func TestTransportFailureDuringFrozenActionUnfreezesIntoReconnect(t *testing.T) {
	closedConn := &fakeHost{applyErr: errors.New("daemon: connection is closed")}
	good := &fakeHost{inspection: execution.Inspection{Projection: runningProjection()}}
	provider := newScriptedProvider(closedConn, good) // action resolves the dying host; reconnect resolves the fresh one
	m := newTestModel(t, provider, 0, runningView())

	// Abort is dispatched while attached; the terminal arrival freezes the
	// session WHILE the action command is still in flight.
	m, abortCmd := update(t, m, keyMsg("a"))
	m, _ = update(t, m, pagesAppliedMsg{View: failedView(9)})
	if !m.Frozen() {
		t.Fatal("setup: terminal arrival did not freeze the session")
	}

	result := runCmd(t, "abort in flight against a dead connection", abortCmd)
	arrival, ok := result.(actionResultMsg)
	if !ok || arrival.Err == nil {
		t.Fatalf("in-flight abort produced %T (%v), want a failed actionResultMsg", result, result)
	}

	m, tickCmd := update(t, m, arrival)
	if m.frozen {
		t.Fatal("connection-level action failure did not unfreeze the frozen session")
	}
	if m.conn != stateReconnecting || m.attempt != 1 {
		t.Fatalf("conn/attempt = %d/%d, want reconnecting at attempt 1", m.conn, m.attempt)
	}
	if got := provider.resetCount(); got != 1 {
		t.Fatalf("provider reset count = %d, want exactly 1 after the transport failure", got)
	}
	if tickCmd == nil {
		t.Fatal("reconnecting flow scheduled no backoff tick")
	}

	// The later successful redial delivers a fresh observe through the
	// replacement host.
	m, observeCmd := update(t, m, backoffTickMsg{})
	applied, ok := runCmd(t, "post-failure reconnect observe", observeCmd).(pagesAppliedMsg)
	if !ok {
		t.Fatal("reconnect attempt did not produce pagesAppliedMsg")
	}
	m, _ = update(t, m, applied)
	if m.conn != stateAttached {
		t.Fatalf("successful redial left conn=%d, want attached", m.conn)
	}
	if applied.View.State != agentrun.StateRunning || applied.View.Sequence != 5 {
		t.Fatalf("fresh observe = %s (sequence %d), want the live running projection",
			applied.View.State, applied.View.Sequence)
	}
}

// TestSemanticRejectionDuringFreezeStaysFrozenStatusLineOnly proves the other
// classification branch: a semantic rejection arriving on a frozen session
// keeps the freeze untouched, stays attached, never resets the provider, and
// lands purely in the status line.
func TestSemanticRejectionDuringFreezeStaysFrozenStatusLineOnly(t *testing.T) {
	provider := newScriptedProvider(&fakeHost{})
	m := newTestModelWithView(t, provider, failedView(7)) // boots frozen over the settled run

	rejected := errors.New("stale revision: a competing writer moved the stream head")
	m, cmd := update(t, m, actionResultMsg{Kind: "retry", Err: rejected})
	if cmd != nil {
		t.Fatalf("semantic rejection scheduled %v, want status-line-only handling", cmd)
	}
	if !m.Frozen() {
		t.Fatal("semantic rejection unfroze a frozen session")
	}
	if m.conn != stateAttached {
		t.Fatalf("semantic rejection flipped conn to %d, want attached", m.conn)
	}
	if got := provider.resetCount(); got != 0 {
		t.Fatalf("semantic rejection reset the provider %d times, want 0", got)
	}
	want := "❌ retry rejected: stale revision: a competing writer moved the stream head"
	if m.status != want {
		t.Fatalf("status = %q, want %q", m.status, want)
	}
}

// TestSuccessfulActionTriggersImmediateObserve proves an accepted action
// schedules a refresh instead of waiting out the poll interval.
func TestSuccessfulActionTriggersImmediateObserve(t *testing.T) {
	host := &fakeHost{inspection: execution.Inspection{Projection: runningProjection()}}
	provider := newScriptedProvider(host, host)
	m := newTestModel(t, provider, 0, runningView())

	m, cmd := update(t, m, actionResultMsg{Kind: "respond"})
	if m.status != "✅ respond accepted" {
		t.Fatalf("status = %q, want acceptance line", m.status)
	}
	msg := runCmd(t, "post-action observe", cmd)
	if _, ok := msg.(pagesAppliedMsg); !ok {
		t.Fatalf("post-action observe produced %T, want pagesAppliedMsg", msg)
	}
}

// TestTerminalFreezeStopsPolling proves the terminal projection halts the
// poll loop while leaving manual refresh available.
func TestTerminalFreezeStopsPolling(t *testing.T) {
	host := &fakeHost{inspection: execution.Inspection{Projection: runningProjection()}}
	m := newTestModel(t, newScriptedProvider(host, host), 0, runningView())

	_, cmd := update(t, m, pollTickMsg{})
	runCmd(t, "poll", cmd) // live run keeps scheduling polls

	terminal := failedView(9)
	m, cmd = update(t, m, pagesAppliedMsg{View: terminal})
	if !m.Frozen() {
		t.Fatalf("terminal arrival did not freeze the model")
	}
	if cmd != nil {
		t.Fatalf("terminal freeze scheduled %v, want the poll loop stopped", cmd)
	}
	_, cmd = update(t, m, pollTickMsg{})
	if cmd != nil {
		t.Fatalf("poll tick after freeze scheduled %v", cmd)
	}
	// Manual refresh stays truthful on a frozen view: it may only rebuild an
	// identical terminal projection.
	host.inspection.Projection = store.RunProjection{
		RunID: "run-tui-test", State: agentrun.StateFailed,
		Terminal: agentrun.TerminalFailure, Sequence: 9, Revision: 9,
	}
	m, cmd = update(t, m, keyMsg("r"))
	msg := runCmd(t, "manual refresh after freeze", cmd)
	if applied, ok := msg.(pagesAppliedMsg); !ok || !applied.View.IsTerminal() {
		t.Fatalf("refresh after freeze produced %T (%v), want a terminal pagesAppliedMsg", msg, msg)
	}
}

// TestDetachMessageQuitsCleanly pins the SIGTERM relay: the detach message
// quits exactly like pressing q.
func TestDetachMessageQuitsCleanly(t *testing.T) {
	m := newTestModel(t, newScriptedProvider(), 0, runningView())
	_, cmd := update(t, m, detachMsg{})
	if _, ok := runCmd(t, "detach", cmd).(tea.QuitMsg); !ok {
		t.Fatalf("detach message did not produce a quit command")
	}
}

// TestFollowOverTerminalRunBootsFrozen pins the --follow-over-a-settled-run
// contract: a model constructed from an already-terminal initial view boots
// frozen — the exit hint renders, abort/respond are inert, and retry stays
// live exactly as after a live terminal arrival — while the detach signal
// still quits cleanly.
func TestFollowOverTerminalRunBootsFrozen(t *testing.T) {
	m := newTestModelWithView(t, newScriptedProvider(&fakeHost{}), failedView(7))
	if !m.Frozen() {
		t.Fatal("a follow session over an already-terminal run did not boot frozen")
	}
	if view := m.View(); !strings.Contains(view, "press q to exit") {
		t.Fatalf("boot-frozen view is missing the exit hint:\n%s", view)
	}
	if _, cmd := update(t, m, keyMsg("a")); cmd != nil {
		t.Fatalf("abort on a boot-frozen session scheduled %v, want inert", cmd)
	}
	m, cmd := update(t, m, keyMsg("e"))
	if cmd != nil || m.input.active {
		t.Fatalf("respond on a boot-frozen session activated (input=%v cmd=%v), want inert", m.input.active, cmd)
	}
	// y stays live through terminal freeze by design: the failed view is
	// retryable even though the run settled before the session started.
	_, cmd = update(t, m, keyMsg("y"))
	runCmd(t, "retry on a boot-frozen run", cmd)
	_, cmd = update(t, m, detachMsg{})
	if _, ok := runCmd(t, "detach from frozen session", cmd).(tea.QuitMsg); !ok {
		t.Fatal("detach message did not quit cleanly on a boot-frozen session")
	}
}

// TestInitAlwaysInstallsDetachWatcher pins the Init contract: the detach
// watcher is installed for EVERY session — terminal-booted ones included —
// so SIGINT/SIGTERM always translate into a clean detach; only live sessions
// additionally schedule the poll loop.
func TestInitAlwaysInstallsDetachWatcher(t *testing.T) {
	t.Run("terminal-booted session keeps the clean-detach contract", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		m := newTestModelWithDetach(t, newScriptedProvider(), failedView(7), ctx)
		cmd := m.Init()
		if cmd == nil {
			t.Fatal("terminal-booted session installed no init commands: SIGINT/SIGTERM would not detach cleanly")
		}
		cancel()
		msg := runCmd(t, "detach watcher of a frozen session", cmd)
		if _, ok := msg.(detachMsg); !ok {
			t.Fatalf("frozen-session init produced %T (%v), want detachMsg once the signal fires", msg, msg)
		}
	})
	t.Run("live session schedules poll loop plus watcher", func(t *testing.T) {
		live := newTestModelWithDetach(t, newScriptedProvider(), runningView(), context.Background())
		batch, ok := runCmd(t, "live init", live.Init()).(tea.BatchMsg)
		if !ok || len(batch) != 2 {
			t.Fatalf("live init did not batch exactly two commands (poll loop + detach watcher): %T", batch)
		}
	})
}
