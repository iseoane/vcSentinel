package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// respondInput is the minimal text-input sub-state for composing a decision
// response. It is deliberately hand-rolled: the `bubbles` textinput would
// add a dependency this slice does not need, and the state machine (buffer,
// backspace, enter, esc) is small enough to pin with tests.
type respondInput struct {
	active bool
	buffer []rune
}

func (i *respondInput) value() string { return string(i.buffer) }
func (i *respondInput) push(r rune)   { i.buffer = append(i.buffer, r) }
func (i *respondInput) backspace()    { i.buffer = trimLastRune(i.buffer) }
func (i *respondInput) placeholder() string {
	return "type the response…"
}

// trimLastRune drops the last rune without splitting a multi-byte character.
func trimLastRune(runes []rune) []rune {
	if len(runes) == 0 {
		return runes
	}
	return runes[:len(runes)-1]
}

// Minimal lipgloss styling. Styles degrade to plain text automatically when
// the terminal has no color support, so golden views captured headlessly are
// byte-identical plain strings.
var (
	headerStyle = lipgloss.NewStyle().Bold(true)
	dimStyle    = lipgloss.NewStyle().Faint(true)
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
)

// View renders the deterministic multi-line attach layout. No wall-clock
// timestamps or durations ever appear here — timings stay in the data-layer
// fields that slice 3's golden views omit — so View() output from a fixed
// model state is a stable plain string.
//
// Layout: header (run id/state/outcome), invocations with their creating
// decisions and evidence presence lines, responses, then the status band
// (reconnect progress / action errors / input sub-state), then the help
// footer listing every key.
func (m Model) View() string {
	var b strings.Builder
	b.WriteString(headerStyle.Render(fmt.Sprintf("🔎 Run %s", m.identity)))
	if m.view.JobID != "" {
		fmt.Fprintf(&b, "\n   job %s", m.view.JobID)
	}
	if m.haveView && m.view.RunID != "" {
		fmt.Fprintf(&b, "\n   state %s (sequence %d, revision %d)", m.view.State, m.view.Sequence, m.view.Revision)
	}
	if m.haveView {
		if m.view.IsTerminal() {
			fmt.Fprintf(&b, "\n   🏁 outcome %s", m.view.Outcome)
			if m.view.Error != "" {
				fmt.Fprintf(&b, "\n%s", errStyle.Render("      error: "+m.view.Error))
			}
		} else {
			fmt.Fprint(&b, "\n   ⏳ still in flight")
		}
		fmt.Fprintf(&b, "\n   invocations %d, responses %d", len(m.view.Invocations), len(m.view.Responses))
		for _, invocation := range m.view.Invocations {
			outcome := string(invocation.OutcomeClass)
			if outcome == "" {
				outcome = "-"
			}
			line := fmt.Sprintf("   • #%d %s decision=%s outcome=%s",
				invocation.Order, invocation.InvocationID, invocation.Decision, outcome)
			if invocation.ParentInvocationID != "" {
				line += fmt.Sprintf(" parent=%s", invocation.ParentInvocationID)
			}
			if invocation.HasOutputHash {
				line += " output-hash=present"
			}
			fmt.Fprintln(&b)
			b.WriteString(line)
			if invocation.Error != "" {
				fmt.Fprintf(&b, "\n%s", errStyle.Render(fmt.Sprintf("       error: %s", invocation.Error)))
			}
		}
		for _, response := range m.view.Responses {
			fmt.Fprintf(&b, "\n   • response → invocation=%s hash=%s", response.InvocationID, response.ResponseHash)
		}
	}
	for _, line := range m.statusBand() {
		fmt.Fprintf(&b, "\n%s", line)
	}
	fmt.Fprintf(&b, "\n%s", dimStyle.Render(m.helpFooter()))
	return b.String()
}

// statusBand renders the mutable lines between the data section and the
// footer: the respond input when active, the reconnect banner, action
// feedback, and the terminal exit hint.
func (m Model) statusBand() []string {
	var lines []string
	switch {
	case m.conn == stateLost:
		lines = append(lines, errStyle.Render("⛔ host unreachable — reconnect gave up"))
	case m.conn == stateReconnecting:
		lines = append(lines, fmt.Sprintf("⟳ reconnecting attempt %d/%d …", m.attempt, MaxReconnectAttempts))
	}
	if m.input.active {
		text := m.input.value()
		if text == "" {
			text = m.input.placeholder()
		}
		lines = append(lines, fmt.Sprintf("respond ❯ %s▏  (enter send · esc cancel)", text))
	}
	if m.frozen {
		lines = append(lines, okStyle.Render("🏁 run reached a terminal state — press q to exit"))
	}
	if m.statusText() != "" && m.conn != stateLost {
		lines = append(lines, m.status)
	}
	return lines
}

// helpFooter lists the keys that currently do something: r/a/e are inert
// while the session is reconnecting or lost contact (handleKey refuses them
// so a mid-backoff action cannot schedule a competing chain), and y appears
// only when the projection says retryable — live through terminal freeze by
// design — so the footer never advertises a dead key.
func (m Model) helpFooter() string {
	keys := []string{"q quit", "r refresh"}
	if !m.frozen && m.conn == stateAttached {
		keys = append(keys, "a abort", "e respond")
	}
	if m.view.State.Retryable() {
		keys = append(keys, "y retry")
	}
	return strings.Join(keys, " · ")
}
