package tui

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbletea"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/attach"
)

// Golden views pin the exact bytes of Model.View() for the states the ticket
// names: running, awaiting_decision (with a recorded response), succeeded,
// canceled with an orphaned reason, reconnecting attempt N, and the distinct
// lost-contact exhaustion state. Every capture renders through RenderPlain so
// the lipgloss color profile is pinned to Ascii instead of trusting lazy
// environment detection: these files stay byte-stable under CLICOLOR_FORCE,
// exotic TERM values, and NO_COLOR alike.
//
// Regenerate with:
//
//	go test ./internal/tui -run TestGoldenViews -update

var updateGolden = flag.Bool("update", false, "rewrite the golden view files in internal/tui/testdata from current rendering")

// assertGolden compares got against testdata/<name>.golden byte for byte,
// failing with a line-level diff, and rewrites the file under -update.
func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	if strings.ContainsRune(got, 0x1b) {
		t.Fatalf("golden %s contains ANSI escape sequences: the pinned Ascii color profile leaked into rendering", name)
	}
	path := filepath.Join("testdata", name+".golden")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("create testdata: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", name, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run with -update to regenerate): %v", name, err)
	}
	if got == string(want) {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "golden %s drifted from the pinned view\n", name)
	gotLines, wantLines := strings.Split(got, "\n"), strings.Split(string(want), "\n")
	for i := 0; i < len(gotLines) || i < len(wantLines); i++ {
		g, w := "<missing>", "<missing>"
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if g != w {
			fmt.Fprintf(&b, "  line %d:\n    want: %q\n    got:  %q\n", i+1, w, g)
		}
	}
	t.Fatal(b.String())
}

// apply feeds one message through Update and hands back the resulting model.
func apply(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	next, _ := m.Update(msg)
	typed, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want tui.Model", next)
	}
	return typed
}

func TestGoldenViews(t *testing.T) {
	run := func(name string, build func(t *testing.T) Model) {
		t.Run(name, func(t *testing.T) {
			assertGolden(t, name, RenderPlain(build(t)))
		})
	}

	// running: an in-flight run with one admitted invocation and no outcome.
	run("running", func(t *testing.T) Model {
		view := runningView()
		view.Invocations = []attach.InvocationEvidence{{
			InvocationID: "inv-running-1", Order: 1, Decision: agentrun.DecisionStart,
			FirstSequence: 4, LastSequence: 4,
		}}
		return apply(t, newTestModel(t, newScriptedProvider(), 0, attach.RunView{}), pagesAppliedMsg{View: view})
	})

	// awaiting_decision: a decision pending on the root invocation with its
	// recorded response hash visible.
	run("awaiting_decision", func(t *testing.T) Model {
		view := attach.RunView{
			RunID: "run-tui-test", State: agentrun.StateAwaitingDecision,
			Sequence: 7, Revision: 7,
			Invocations: []attach.InvocationEvidence{{
				InvocationID: "inv-awaiting-1", Order: 1, Decision: agentrun.DecisionStart,
				FirstSequence: 4, LastSequence: 5,
			}},
			Responses: []attach.ResponseRecord{{
				InvocationID: "inv-awaiting-1", ResponseHash: "hash:awaited-answer", Sequence: 6,
			}},
		}
		return apply(t, newTestModel(t, newScriptedProvider(), 0, attach.RunView{}), pagesAppliedMsg{View: view})
	})

	// succeeded: a terminal projection frozen with the exit hint and one
	// invocation whose evidence records success with an output hash present.
	run("succeeded", func(t *testing.T) Model {
		view := attach.RunView{
			RunID: "run-tui-test", State: agentrun.StateSucceeded,
			Terminal: agentrun.TerminalSuccess, Sequence: 6, Revision: 6,
			Outcome: agentrun.OutcomeSuccess,
			Invocations: []attach.InvocationEvidence{{
				InvocationID: "inv-success-1", Order: 1, Decision: agentrun.DecisionStart,
				OutcomeClass: agentrun.OutcomeSuccess, HasOutputHash: true,
				FirstSequence: 4, LastSequence: 5,
			}},
		}
		// The freeze comes from the real transition: a terminal projection
		// arriving through pagesAppliedMsg stops polling and pins the hint.
		return apply(t, newTestModel(t, newScriptedProvider(), 0, attach.RunView{}), pagesAppliedMsg{View: view})
	})

	// canceled_orphaned: the canceled terminal variant whose settlement
	// carries the orphaned reason text of a termination that could not be
	// confirmed — the honest wording controller-authored escalation writes.
	run("canceled_orphaned", func(t *testing.T) Model {
		reason := "aborted while running; process tree orphaned after termination attempt (pid 4242 unconfirmed)"
		view := attach.RunView{
			RunID: "run-tui-test", State: agentrun.StateCanceled,
			Terminal: agentrun.TerminalCancellation, Sequence: 9, Revision: 9,
			Outcome: agentrun.OutcomeCancellation, Error: reason,
			Invocations: []attach.InvocationEvidence{{
				InvocationID: "inv-cancel-1", Order: 1, Decision: agentrun.DecisionStart,
				OutcomeClass: agentrun.OutcomeCancellation, Error: reason,
				FirstSequence: 4, LastSequence: 8,
			}},
		}
		return apply(t, newTestModel(t, newScriptedProvider(), 0, attach.RunView{}), pagesAppliedMsg{View: view})
	})

	// reconnecting: endpoint loss mid-run after three failed exchanges; the
	// status band shows the bounded attempt counter against the cap.
	run("reconnecting_attempt_3", func(t *testing.T) Model {
		m := apply(t, newTestModel(t, newScriptedProvider(), 0, attach.RunView{}), pagesAppliedMsg{View: runningView()})
		failure := hostErrMsg{Err: errors.New("dial tcp: connection refused"), Op: "observe"}
		for i := 0; i < 3; i++ {
			m = apply(t, m, failure)
		}
		return m
	})

	// lost_contact: the distinct state after the bounded budget exhausts —
	// MaxReconnectAttempts failures plus the one that trips the cap.
	run("lost_contact", func(t *testing.T) Model {
		m := apply(t, newTestModel(t, newScriptedProvider(), 0, attach.RunView{}), pagesAppliedMsg{View: runningView()})
		failure := hostErrMsg{Err: errors.New("dial tcp: connection refused"), Op: "observe"}
		for i := 0; i < MaxReconnectAttempts+1; i++ {
			m = apply(t, m, failure)
		}
		return m
	})
}
