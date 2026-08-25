// Package control hosts the Bubble Tea control-center model: it renders a
// real overview snapshot through the approved art layout engine and moves a
// repository cursor with the keyboard, replacing the hardcoded "first
// repository" right pane with the operator-selected one.
//
// Like the attach TUI, Model.Update is deliberately pure (no tea.Program
// contact beyond returned commands), so the whole state machine is testable
// headlessly without spawning a terminal. Models built through New stay fully
// passive; NewLive layers a deterministic live-refresh loop on top: an
// injected interval scheduler drives periodic snapshot collection through
// commands. There is no daemon lifecycle and no exec beyond the injected
// refresh closure.
package control

import (
	"time"

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

	// DefaultRefreshInterval is the defensive cadence for live models whose
	// caller passed a non-positive interval: callers are expected to pass a
	// positive one, but the loop must never run on a degenerate timer.
	DefaultRefreshInterval = 2 * time.Second
)

// Messages driving the live refresh loop. Both are private: only this
// package's commands produce them.

// tickMsg fires once per interval on live models; receiving it consumes one
// scheduling slot, so it maps to exactly one reschedule plus exactly one
// snapshot-collection command.
type tickMsg struct{}

// snapshotMsg carries one refresh result from the command goroutine back to
// Update: the fetched repositories plus an optional error.
type snapshotMsg struct {
	repos []overview.Repo
	err   error
}

// Model is the Bubble Tea control-center model over one overview snapshot.
type Model struct {
	width    int
	repos    []overview.Repo
	selected int
	quitting bool

	// Live-refresh loop state. Every field stays zero on static models built
	// through New, which is exactly what keeps them passive: a nil refresh
	// disables Init scheduling and makes stray ticks inert. schedule may be
	// nil even on live models: scheduleCmd then falls back to tea.Tick.
	refresh  func() ([]overview.Repo, error)
	interval time.Duration
	schedule func(time.Duration) tea.Cmd
	err      string
}

// New builds the control-center model over the given snapshot. Width starts
// at DefaultWidth (80 columns) until the first window-size message arrives;
// selected starts at the first repository. The model is static: no refresh
// loop is enabled, Init schedules nothing, and stray ticks are ignored.
func New(repos []overview.Repo) Model {
	return Model{width: DefaultWidth, repos: repos}
}

// NewLive builds the control-center model with the live refresh loop
// enabled: every interval the model collects a fresh overview snapshot off
// the UI thread through a command, swaps it in on success, or keeps the last
// good snapshot and records the failure on error.
//
// refresh must be non-nil whenever the loop is enabled: calling NewLive with
// a nil refresh panics by contract, because a loop with nothing to collect
// is a programming error, not a runtime condition. Callers pass a positive
// interval; a non-positive one is clamped defensively to
// DefaultRefreshInterval so the loop always runs on a sane cadence.
func NewLive(repos []overview.Repo, refresh func() ([]overview.Repo, error), interval time.Duration) Model {
	if refresh == nil {
		panic("control.NewLive: refresh must be non-nil")
	}
	if interval <= 0 {
		interval = DefaultRefreshInterval
	}
	return Model{width: DefaultWidth, repos: repos, refresh: refresh, interval: interval}
}

// Accessors pinning the construction contract for tests.

func (m Model) Width() int             { return m.width }
func (m Model) Selected() int          { return m.selected }
func (m Model) Quitting() bool         { return m.quitting }
func (m Model) Repos() []overview.Repo { return m.repos }

// Err reports the last snapshot-collection failure as text; empty while the
// loop is healthy (and always, on static models).
func (m Model) Err() string { return m.err }

// Init schedules the first tick when the live refresh loop is enabled and
// nothing otherwise: static models stay fully passive.
func (m Model) Init() tea.Cmd {
	if m.refresh == nil {
		return nil
	}
	return m.scheduleCmd(m.interval)
}

// Update is the pure state-machine transition: messages in, next model plus
// at most one scheduled command out. Window resizes set the render width
// with a floor at minWidth; up/k and down/j move the repository cursor
// clamped to [0, len(repos)-1]; q and ctrl+c flag quitting; on live models a
// tick maps to exactly one reschedule plus one snapshot collection, a
// successful snapshot replaces the repositories (clamping the selection back
// into range), and a failed snapshot keeps the last good snapshot while
// recording the error; everything else leaves the model untouched.
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
	case tickMsg:
		if m.refresh == nil {
			// Static models never enabled the loop: a stray tick is inert.
			return m, nil
		}
		// One tick consumes its scheduling slot: exactly one reschedule plus
		// exactly one refresh. Batch keeps both effects; bubbletea gives no
		// ordering guarantees between them, and none are needed because they
		// are independent.
		return m, tea.Batch(m.scheduleCmd(m.interval), m.refreshSnapshotCmd())
	case snapshotMsg:
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.repos = msg.repos
		if last := len(m.repos) - 1; m.selected > last {
			m.selected = last
		}
		if m.selected < 0 {
			m.selected = 0
		}
		m.err = ""
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

// scheduleCmd resolves the tick scheduler: live models use their injected
// scheduler when set (it runs eagerly inside Update — a test seam only),
// falling back to tea.Tick otherwise, so a model that never went through
// NewLive can never panic on a stray tick either.
func (m Model) scheduleCmd(d time.Duration) tea.Cmd {
	if m.schedule != nil {
		return m.schedule(d)
	}
	return tea.Tick(d, func(time.Time) tea.Msg { return tickMsg{} })
}

// refreshSnapshotCmd runs the injected collector inside the command goroutine
// and wraps its result as its own message, keeping Update pure: the snapshot
// lands later than the reschedule it raced with, in either order.
func (m Model) refreshSnapshotCmd() tea.Cmd {
	refresh := m.refresh
	return func() tea.Msg {
		repos, err := refresh()
		return snapshotMsg{repos: repos, err: err}
	}
}
