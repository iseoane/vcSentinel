package tui

import (
	"testing"

	"github.com/charmbracelet/bubbletea"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vcSentinel/internal/attach"
	"github.com/ISeoane-Quental/vcSentinel/internal/execution"
)

// Keyboard-routing, action, retry, and respond-input coverage pairing with
// update.go. Message-transition coverage lives in model_test.go; the doubles
// live in fakes_test.go.

func TestKeyRoutingTable(t *testing.T) {
	tests := []struct {
		name   string
		model  func(*testing.T) Model
		key    string
		assert func(t *testing.T, m Model, cmd tea.Cmd)
	}{
		{
			name: "q quits from attached state",
			model: func(t *testing.T) Model {
				return newTestModel(t, newScriptedProvider(&fakeHost{}), 0, runningView())
			},
			key: "q",
			assert: func(t *testing.T, m Model, cmd tea.Cmd) {
				if _, ok := runCmd(t, "q", cmd).(tea.QuitMsg); !ok {
					t.Fatalf("q did not produce a quit command")
				}
			},
		},
		{
			name: "r schedules an observe while attached",
			model: func(t *testing.T) Model {
				host := &fakeHost{inspection: execution.Inspection{Projection: runningProjection()}}
				return newTestModel(t, newScriptedProvider(host), 0, runningView())
			},
			key: "r",
			assert: func(t *testing.T, m Model, cmd tea.Cmd) {
				msg := runCmd(t, "r", cmd)
				applied, ok := msg.(pagesAppliedMsg)
				if !ok {
					t.Fatalf("r produced %T, want pagesAppliedMsg", msg)
				}
				if applied.View.State != agentrun.StateRunning {
					t.Fatalf("observe produced state %q, want running", applied.View.State)
				}
			},
		},
		{
			name: "e activates the respond input without scheduling anything",
			model: func(t *testing.T) Model {
				return newTestModel(t, newScriptedProvider(), 0, runningView())
			},
			key: "e",
			assert: func(t *testing.T, m Model, cmd tea.Cmd) {
				if !m.input.active {
					t.Fatalf("e did not activate the respond input")
				}
				if cmd != nil {
					t.Fatalf("e scheduled %v, want nothing", cmd)
				}
			},
		},
		{
			name: "y is inert on a non-retryable running view",
			model: func(t *testing.T) Model {
				return newTestModel(t, newScriptedProvider(), 0, runningView())
			},
			key: "y",
			assert: func(t *testing.T, m Model, cmd tea.Cmd) {
				if cmd != nil {
					t.Fatalf("y on a running view scheduled %v, want nothing", cmd)
				}
			},
		},
		{
			name: "y fires on a failed view even after terminal freeze",
			model: func(t *testing.T) Model {
				m := newTestModelWithView(t, newScriptedProvider(&fakeHost{}), failedView(7))
				m, _ = update(t, m, pagesAppliedMsg{View: failedView(7)})
				if !m.Frozen() {
					t.Fatalf("terminal arrival did not freeze the model")
				}
				return m
			},
			key: "y",
			assert: func(t *testing.T, m Model, cmd tea.Cmd) {
				runCmd(t, "retry after freeze", cmd)
			},
		},
		{
			name: "unknown keys are inert",
			model: func(t *testing.T) Model {
				return newTestModel(t, newScriptedProvider(), 0, runningView())
			},
			key: "z",
			assert: func(t *testing.T, m Model, cmd tea.Cmd) {
				if cmd != nil {
					t.Fatalf("unmapped key z scheduled %v", cmd)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.model(t)
			next, cmd := update(t, m, keyMsg(tt.key))
			tt.assert(t, next, cmd)
		})
	}
}

// TestCtrlCQuitsEvenInsideRespondInput pins the escape hatch: the input
// sub-state consumes every other key, but ctrl+c always quits.
func TestCtrlCQuitsEvenInsideRespondInput(t *testing.T) {
	m := newTestModel(t, newScriptedProvider(), 0, runningView())
	m, _ = update(t, m, keyMsg("e"))
	if !m.input.active {
		t.Fatalf("input did not activate")
	}
	_, cmd := update(t, m, keyMsg("ctrl+c"))
	if _, ok := runCmd(t, "ctrl+c", cmd).(tea.QuitMsg); !ok {
		t.Fatalf("ctrl+c inside the input did not produce a quit command")
	}
}

// TestAbortStampsFreshIdentities mirrors the CLI contract: every press of a
// sends abort with its own idempotency identity following the same
// kind:timestamp-pid-counter pattern as comandos_runs_actions.go.
func TestAbortStampsFreshIdentities(t *testing.T) {
	host := &fakeHost{}
	provider := newScriptedProvider(host, host, host)
	m := newTestModel(t, provider, 0, runningView())

	m, cmd := update(t, m, keyMsg("a"))
	runCmd(t, "abort 1", cmd)
	m, cmd = update(t, m, keyMsg("a"))
	runCmd(t, "abort 2", cmd)

	ids := host.appliedActions()
	if len(ids) != 2 {
		t.Fatalf("two abort presses produced %d Apply calls:\n%v", len(ids), ids)
	}
	for _, id := range ids {
		if len(id) < len("action:abort:") || id[:len("action:abort:")] != "action:abort:" {
			t.Fatalf("abort identity %q does not follow the CLI stamping pattern", id)
		}
	}
	if ids[0] == ids[1] {
		t.Fatalf("repeated aborts reused idempotency identity %q", ids[0])
	}
	if host.applies[0].AuthContext.Principal != "tester" {
		t.Fatalf("abort envelope principal = %q, want the model principal", host.applies[0].AuthContext.Principal)
	}
}

// TestRespondInputStateMachine walks the full sub-state: activate, type,
// submit, cancel, and the empty-submit guard.
func TestRespondInputStateMachine(t *testing.T) {
	t.Run("esc cancels and clears", func(t *testing.T) {
		m := newTestModel(t, newScriptedProvider(), 0, runningView())
		m, _ = update(t, m, keyMsg("e"))
		m, _ = update(t, m, keyMsg("o"))
		m, _ = update(t, m, keyMsg("k"))
		m, cmd := update(t, m, keyMsg("esc"))
		if cmd != nil {
			t.Fatalf("cancel scheduled %v", cmd)
		}
		if m.input.active || m.input.value() != "" {
			t.Fatalf("cancel left input active=%v buffer=%q", m.input.active, m.input.value())
		}
	})

	t.Run("enter submits the typed response with a fresh identity", func(t *testing.T) {
		host := &fakeHost{}
		provider := newScriptedProvider(host, host)
		m := newTestModel(t, provider, 0, runningView())
		for _, key := range []string{"e", "g", "o"} {
			m, _ = update(t, m, keyMsg(key))
		}
		m, cmd := update(t, m, keyMsg("enter"))
		msg := runCmd(t, "respond submit", cmd)
		if _, ok := msg.(actionResultMsg); !ok {
			t.Fatalf("submit produced %T, want actionResultMsg", msg)
		}
		applies := host.appliedActions()
		if len(applies) != 1 || len(applies[0]) < len("action:respond:") || applies[0][:len("action:respond:")] != "action:respond:" {
			t.Fatalf("respond identities = %v, want one CLI-pattern action:respond: identity", applies)
		}
		if host.applies[0].Action.Response != "go" {
			t.Fatalf("submitted response = %q, want typed text verbatim", host.applies[0].Action.Response)
		}
		if m.input.active || m.input.value() != "" {
			t.Fatalf("submit did not deactivate the input")
		}
	})

	t.Run("enter on an empty buffer stays active with a hint", func(t *testing.T) {
		m := newTestModel(t, newScriptedProvider(), 0, runningView())
		m, _ = update(t, m, keyMsg("e"))
		m, cmd := update(t, m, keyMsg("enter"))
		if cmd != nil {
			t.Fatalf("empty submit scheduled %v", cmd)
		}
		if !m.input.active {
			t.Fatalf("empty submit deactivated the input")
		}
		if m.status == "" {
			t.Fatalf("empty submit gave no guidance")
		}
	})

	t.Run("backspace edits the buffer", func(t *testing.T) {
		m := newTestModel(t, newScriptedProvider(), 0, runningView())
		m, _ = update(t, m, keyMsg("e"))
		m, _ = update(t, m, keyMsg("a"))
		m, _ = update(t, m, keyMsg("b"))
		m, _ = update(t, m, keyMsg("backspace"))
		if got := m.input.value(); got != "a" {
			t.Fatalf("buffer after backspace = %q, want %q", got, "a")
		}
	})
}

// TestRetryCarriesExpectedRevisionFromObservedHead pins the revision-aware
// mapping: retry pins ExpectedRevision from the last observed head revision,
// exactly the value the server compares against its durable stream head.
func TestRetryCarriesExpectedRevisionFromObservedHead(t *testing.T) {
	host := &fakeHost{}
	provider := newScriptedProvider(host, host)
	view := failedView(7)
	m := newTestModelWithView(t, provider, view)

	m, cmd := update(t, m, keyMsg("y"))
	runCmd(t, "retry", cmd)
	host.mu.Lock()
	retries := append([]execution.RetryRequest(nil), host.retries...)
	host.mu.Unlock()
	if len(retries) != 1 {
		t.Fatalf("retry produced %d host.Retry calls, want 1", len(retries))
	}
	if retries[0].ExpectedRevision != 7 {
		t.Fatalf("ExpectedRevision = %d, want the observed head revision 7", retries[0].ExpectedRevision)
	}
	if retries[0].AuthContext.Principal != "tester" {
		t.Fatalf("retry principal = %q, want the model principal", retries[0].AuthContext.Principal)
	}
	if retries[0].RunID != "run-tui-test" {
		t.Fatalf("retry run = %q, want the attached run", retries[0].RunID)
	}
}

// TestRetryableGateMatrix pins exactly which terminal projections unlock y.
func TestRetryableGateMatrix(t *testing.T) {
	tests := []struct {
		name     string
		state    agentrun.LifecycleState
		wantFire bool
	}{
		{"failed retries", agentrun.StateFailed, true},
		{"canceled retries", agentrun.StateCanceled, true},
		{"timed_out retries", agentrun.StateTimedOut, true},
		{"succeeded stays final", agentrun.StateSucceeded, false},
		{"unavailable stays final", agentrun.StateUnavailable, false},
		{"running is not retryable", agentrun.StateRunning, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			view := attach.RunView{
				RunID: "run-tui-test", State: tt.state,
				Invocations: []attach.InvocationEvidence{}, Responses: []attach.ResponseRecord{},
			}
			view.Terminal = tt.state.TerminalClass()
			m := newTestModelWithView(t, newScriptedProvider(&fakeHost{}), view)
			_, cmd := update(t, m, keyMsg("y"))
			if tt.wantFire && cmd == nil {
				t.Fatalf("y on %s did not schedule the retry", tt.state)
			}
			if !tt.wantFire && cmd != nil {
				t.Fatalf("y on %s scheduled %v, want nothing", tt.state, cmd)
			}
			if !tt.wantFire {
				return
			}
			runCmd(t, "retry "+string(tt.state), cmd)
		})
	}
}
