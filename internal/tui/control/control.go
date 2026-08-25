// Package control hosts the Bubble Tea control-center model: it renders a
// real overview snapshot through the approved art layout engine and moves a
// repository cursor with the keyboard, replacing the hardcoded "first
// repository" right pane with the operator-selected one.
//
// Like the attach TUI, Model.Update is deliberately pure (no tea.Program
// contact beyond returned commands), so the whole state machine is testable
// headlessly without spawning a terminal. This slice intentionally has no
// refresh loop, no daemon lifecycle, no exec, and no time ticks; later
// slices layer those on.
package control

import (
	"github.com/charmbracelet/bubbletea"

	"github.com/ISeoane-Quental/vas.sentinel/internal/overview"
	"github.com/ISeoane-Quental/vas.sentinel/internal/tui/art"
)

const (
	// DefaultWidth renders before any tea.WindowSizeMsg arrives: wide enough
	// for the side-by-side layout contract without knowing the terminal.
	DefaultWidth = 80

	// minWidth floors the render width so a tiny terminal still produces the
	// stacked full-width panes instead of a crushed side-by-side frame.
	minWidth = 40
)

// Model is the Bubble Tea control-center model over one overview snapshot.
type Model struct {
	width    int
	repos    []overview.Repo
	selected int
	quitting bool
}

// New builds the control-center model over the given snapshot. Width starts
// at DefaultWidth (80 columns) until the first window-size message arrives;
// selected starts at the first repository.
func New(repos []overview.Repo) Model {
	return Model{width: DefaultWidth, repos: repos}
}

// Accessors pinning the construction contract for tests.

func (m Model) Width() int             { return m.width }
func (m Model) Selected() int          { return m.selected }
func (m Model) Quitting() bool         { return m.quitting }
func (m Model) Repos() []overview.Repo { return m.repos }

// Init schedules nothing yet: this slice has no refresh loop or daemon
// lifecycle, so there is no command to run up front.
func (m Model) Init() tea.Cmd { return nil }

// Update is the pure state-machine transition: messages in, next model plus
// no scheduled command out (nothing in this slice schedules work). Window
// resizes set the render width with a floor at minWidth; up/k and down/j
// move the repository cursor clamped to [0, len(repos)-1]; q and ctrl+c flag
// quitting; everything else leaves the model untouched.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		if m.width < minWidth {
			m.width = minWidth
		}
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.selected > 0 {
				m.selected--
			}
		case "down", "j":
			// len(repos)-1 is -1 on an empty registry, so the comparison is
			// false and the cursor cannot move: no move when empty.
			if m.selected < len(m.repos)-1 {
				m.selected++
			}
		case "q", "ctrl+c":
			m.quitting = true
		}
		return m, nil
	default:
		return m, nil
	}
}

// View renders the snapshot through the approved layout engine. The renderer
// clamps an out-of-range selected (and keeps the dash placeholder on an empty
// registry), so View never panics regardless of cursor state.
func (m Model) View() string {
	return art.RenderOverview(m.width, m.repos, m.selected)
}
