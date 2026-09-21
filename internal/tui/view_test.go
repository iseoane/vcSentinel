package tui

import (
	"errors"
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vcSentinel/internal/attach"
)

// Help-footer gating coverage pairing with view.go: the footer advertises
// exactly the keys handleKey would honor in the current session state, so it
// never offers a dead key — r disappears while reconnecting or lost, a/e
// disappear on frozen views, and y appears only when the projection is
// retryable.

// lostModel drives a reconnecting session past the attempt cap.
func lostModel(t *testing.T) Model {
	t.Helper()
	m := enterReconnecting(t)
	for i := 0; i < MaxReconnectAttempts; i++ {
		m, _ = update(t, m, hostErrMsg{Err: errors.New("still down"), Op: "observe"})
	}
	if m.conn != stateLost {
		t.Fatalf("setup: conn=%d, want lost contact", m.conn)
	}
	return m
}

func TestHelpFooterGating(t *testing.T) {
	tests := []struct {
		name  string
		model func(t *testing.T) Model
		want  string
	}{
		{
			name: "attached and live",
			model: func(t *testing.T) Model {
				return newTestModelWithView(t, newScriptedProvider(), runningView())
			},
			want: "q quit · r refresh · a abort · e respond",
		},
		{
			name:  "reconnecting hides every inert action key",
			model: enterReconnecting,
			want:  "q quit",
		},
		{
			name:  "lost contact leaves only q",
			model: lostModel,
			want:  "q quit",
		},
		{
			name: "frozen non-retryable terminal keeps r only",
			model: func(t *testing.T) Model {
				m := newTestModel(t, newScriptedProvider(), 0, attach.RunView{})
				applied, _ := update(t, m, pagesAppliedMsg{View: succeededView()})
				return applied
			},
			want: "q quit · r refresh",
		},
		{
			name: "frozen retryable terminal keeps r and y",
			model: func(t *testing.T) Model {
				m := newTestModel(t, newScriptedProvider(), 0, attach.RunView{})
				applied, _ := update(t, m, pagesAppliedMsg{View: failedView(9)})
				return applied
			},
			want: "q quit · r refresh · y retry",
		},
		{
			name: "boot-frozen retryable terminal from New matches the live freeze",
			model: func(t *testing.T) Model {
				return newTestModelWithView(t, newScriptedProvider(&fakeHost{}), failedView(7))
			},
			want: "q quit · r refresh · y retry",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.model(t).helpFooter(); got != tt.want {
				t.Fatalf("helpFooter() = %q, want %q", got, tt.want)
			}
		})
	}
}

// succeededView is a final-success terminal projection.
func succeededView() attach.RunView {
	return attach.RunView{
		RunID: "run-tui-test", State: agentrun.StateSucceeded,
		Terminal: agentrun.TerminalSuccess, Outcome: agentrun.OutcomeSuccess,
		Sequence: 6, Revision: 6,
	}
}
