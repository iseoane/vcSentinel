package tui

import (
	"errors"
	"testing"

	"github.com/charmbracelet/bubbletea"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
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
