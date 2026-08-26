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

// Action kinds shared by the pending marker and the result messages. The
// keyboard dispatch, the RunActions hooks, and every producer of completion
// results must use these exact strings.
const (
	KindAbort = "abort"
	KindRetry = "retry"
)

// RunActions dispatches operator-initiated durable-run actions off the UI
// thread. Each hook receives the repository path and run id under the runs
// cursor and returns the command that performs the request in its own
// goroutine; that command must deliver its outcome by yielding the message
// built through ActionResult (a nil Err reports quiet success). A nil
// interface disables the a/r keys entirely — static New models never carry
// one.
type RunActions interface {
	// Abort requests cooperative cancellation of runID in repoPath.
	Abort(repoPath, runID string) tea.Cmd
	// Retry relaunches the last failed attempt of runID inside repoPath.
	Retry(repoPath, runID string) tea.Cmd
}

// ActionResult builds the completion command RunActions implementations yield
// for a dispatched action: executing it produces the private result message
// Update consumes to clear the matching pending marker and record a rejection
// on Err. The concrete message stays unexported so external code cannot forge
// malformed completions; it can only travel through this constructor.
func ActionResult(kind, repoPath, runID string, err error) tea.Cmd {
	return func() tea.Msg {
		return actionResultMsg{Kind: kind, RepoPath: repoPath, RunID: runID, Err: err}
	}
}

// pendingAction is the in-flight marker stored when a key dispatches an
// action and cleared only by the matching result. Comparing kind, repository,
// and run makes stale completions (answers to superseded markers) inert
// instead of clearing the wrong dispatch.
type pendingAction struct {
	Kind     string
	RepoPath string
	RunID    string
}

// actionResultMsg carries one dispatched run action's outcome from the hook's
// command goroutine back to Update. Only a message matching the current
// pending marker is consumed: stale results are ignored whole. Failure lands
// on the same Err() channel as refresh failures and overwrites whatever was
// recorded there (action feedback wins; it never touches repo.Error);
// success stays silent because the next refreshed snapshot shows the
// transition.
type actionResultMsg struct {
	Kind     string
	RepoPath string
	RunID    string
	Err      error
}

// Model is the Bubble Tea control-center model over one overview snapshot.
type Model struct {
	width    int
	repos    []overview.Repo
	selected int // deprecated: use treeCursor.Repo
	quitting bool

	// Dual-pane navigation state. The zero values encode the initial
	// contract: tree focus, runs cursor at the first render-order position,
	// no open run detail, help closed.
	focus      art.FocusPane
	treeCursor art.TreePos
	runCursor  art.RunPos
	openRunID  string
	help       bool
	filter     string
	filtering  bool

	// Keyboard-dispatched run actions. actions is nil on static models and
	// whenever the caller wants a/r inert; pending marks one dispatched
	// action until its matching result message arrives. Cursor moves, help
	// toggles, and snapshot swaps never touch it.
	actions RunActions
	pending *pendingAction

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
	return Model{width: DefaultWidth, repos: repos, treeCursor: art.TreePos{Worktree: -1}}
}

// NewLive builds the control-center model with the live refresh loop
// enabled: every interval the model collects a fresh overview snapshot off
// the UI thread through a command, swaps it in on success, or keeps the last
// good snapshot and records the failure on error.
//
// actions wires the a/r keyboard hooks onto the operator's durable-run
// actions (abort/retry of the focused run of healthy repositories). A nil
// interface leaves those keys permanently inert; static New models never
// carry one. Outcomes travel back through Err() and PendingAction(): a
// rejection overwrites any recorded refresh error, success stays silent, and
// only the matching result clears the pending marker.
//
// refresh must be non-nil whenever the loop is enabled: calling NewLive with
// a nil refresh panics by contract, because a loop with nothing to collect
// is a programming error, not a runtime condition. Callers pass a positive
// interval; a non-positive one is clamped defensively to
// DefaultRefreshInterval so the loop always runs on a sane cadence.
func NewLive(repos []overview.Repo, refresh func() ([]overview.Repo, error), interval time.Duration, actions RunActions) Model {
	if refresh == nil {
		panic("control.NewLive: refresh must be non-nil")
	}
	if interval <= 0 {
		interval = DefaultRefreshInterval
	}
	return Model{width: DefaultWidth, repos: repos, treeCursor: art.TreePos{Worktree: -1}, refresh: refresh, interval: interval, actions: actions}
}

// Accessors pinning the construction contract for tests.

func (m Model) Width() int             { return m.width }
func (m Model) Selected() int          { return m.selected }
func (m Model) Quitting() bool         { return m.quitting }
func (m Model) Repos() []overview.Repo { return m.repos }

// Focus reports the pane that currently owns keyboard navigation.
func (m Model) Focus() art.FocusPane { return m.focus }

// RunCursorPosition reports the run-row position the runs cursor points at.
func (m Model) RunCursorPosition() art.RunPos { return m.runCursor }

// OpenRun reports the identifier of the expanded run detail; empty when no
// run is open.
func (m Model) OpenRun() string { return m.openRunID }

// HelpVisible reports whether the KEYS overlay replaces the pane content.
func (m Model) HelpVisible() bool { return m.help }

// PendingAction reports the dispatched-but-unresolved run action: its kind
// ("abort" or "retry"), the target repository path, and the run id. ok is
// false while nothing is in flight — always, on static models. Only the
// matching action-result message clears the marker; cursor movement, help
// toggles, and snapshot swaps leave it alone.
func (m Model) PendingAction() (kind, repoPath, runID string, ok bool) {
	if m.pending == nil {
		return "", "", "", false
	}
	return m.pending.Kind, m.pending.RepoPath, m.pending.RunID, true
}

// Err reports the last failure recorded on the model: a snapshot-collection
// failure or a rejected run action — whichever arrived most recently. The
// channels share one slot by contract: an action outcome overwrites a prior
// refresh error string (first-error-wins deliberately does not apply here)
// and a later successful refresh clears the text entirely. Empty while
// healthy (and always, on static models). Repository-level degradation never
// lands here: it travels in each Repo's Error field.
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
// with a floor at minWidth. Keys route through the focused pane: q and
// ctrl+c quit from any state; "?" toggles the help overlay; any other key
// while help is open closes it and is otherwise swallowed. In the tree,
// up/k and down/j move the repository cursor clamped to [0, len(repos)-1]
// and enter moves focus to the runs pane without changing the selection. In
// the runs pane, up/k and down/j walk every visible run row of the current
// snapshot in render order with clamping (an empty run list is a no-op),
// enter toggles the open-run detail under the cursor, any actual cursor
// movement clears an open detail, and a/r dispatch the wired RunActions hook
// for the visible run under the cursor when its repository is enabled,
// non-missing, and error-free — every unmet gate (including a nil hook) is a
// pure no-op. tab and shift+tab switch focus (a clamped no-op on an empty
// registry). On live models a tick maps to exactly one
// reschedule plus one snapshot collection, a successful snapshot replaces
// the repositories (clamping the selection back into range), and a failed
// snapshot keeps the last good snapshot while recording the error; an
// action-result message clears the pending marker only when kind, repository,
// and run match the latest dispatch, records rejections on Err with overwrite
// semantics, and stays silent on success; everything else leaves the model
// untouched.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		if m.width < minWidth {
			m.width = minWidth
		}
		return m, nil
	case tea.KeyMsg:
		var cmd tea.Cmd
		// Filter input has priority over normal shortcuts: q and ? are valid
		// query characters, while ctrl+c remains the global quit escape.
		if m.filtering {
			switch msg.String() {
			case "ctrl+c":
				m.quitting = true
				return m, tea.Quit
			case "enter", "esc":
				m.filtering = false
				return m, nil
			case "backspace", "ctrl+h":
				runes := []rune(m.filter)
				if len(runes) > 0 {
					m.filter = string(runes[:len(runes)-1])
				}
				return m, nil
			default:
				if msg.Type == tea.KeyRunes {
					m.filter += string(msg.Runes)
				} else if msg.Type == tea.KeySpace {
					m.filter += " "
				}
				return m, nil
			}
		}
		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			// The quit flag stays for headless tests; the command is what a
			// real tea.Program consumes to end the session.
			return m, tea.Quit
		case "?":
			m.help = !m.help
		default:
			if m.help {
				// Any other key closes help first and is otherwise swallowed.
				m.help = false
				return m, nil
			}
			switch msg.String() {
			case "tab", "shift+tab":
				m.toggleFocus()
			case "up", "k":
				if m.focus == art.FocusRuns {
					m.moveRunCursor(-1)
				} else {
					m.moveTreeCursor(-1)
				}
			case "down", "j":
				if m.focus == art.FocusRuns {
					m.moveRunCursor(1)
				} else {
					m.moveTreeCursor(1)
				}
			case "enter":
				if m.focus == art.FocusTree {
					m.focus = art.FocusRuns
				} else {
					m.toggleOpenRun()
				}
			case "a", "r":
				cmd = m.requestRunAction(msg.String())
			case "/":
				m.filtering = true
				m.filter = ""
				return m, nil
			}
		}
		return m, cmd
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
		if len(m.repos) == 0 {
			m.selected = 0
			m.treeCursor = art.TreePos{Worktree: -1}
		} else {
			last := len(m.repos) - 1
			if m.treeCursor.Repo < 0 {
				m.treeCursor.Repo = 0
			}
			if m.treeCursor.Repo > last {
				m.treeCursor.Repo = last
			}
			if m.treeCursor.Worktree >= len(m.repos[m.treeCursor.Repo].Worktrees) {
				m.treeCursor.Worktree = -1
			}
			m.selected = m.treeCursor.Repo
		}
		m.err = ""
		return m, nil
	case actionResultMsg:
		if m.pending == nil ||
			m.pending.Kind != msg.Kind ||
			m.pending.RepoPath != msg.RepoPath ||
			m.pending.RunID != msg.RunID {
			// Stale or unexpected completion: ignored whole, pending marker
			// included — only the answer to the LATEST dispatch may clear it.
			return m, nil
		}
		m.pending = nil
		if msg.Err != nil {
			// Action feedback overwrites any prior refresh error string; see
			// Err for the shared-slot contract.
			m.err = msg.Err.Error()
		}
		// Success is silent: the next refreshed snapshot shows the transition.
		return m, nil
	default:
		return m, nil
	}
}

// View renders the snapshot through the approved layout engine. The renderer
// clamps an out-of-range repository cursor (and keeps the dash placeholder
// on an empty registry), so View never panics regardless of navigation state.
func (m Model) View() string {
	// Keep selected in sync with treeCursor for backward compat with tests
	// that still read Selected().
	treeCursor := m.treeCursor
	if treeCursor.Repo == 0 && treeCursor.Worktree == 0 && m.selected != 0 {
		treeCursor = art.TreePos{Repo: m.selected, Worktree: -1}
	}
	return art.RenderOverview(m.width, art.ViewState{
		Repos:         m.repos,
		TreeCursor:    treeCursor,
		TreeCursorSet: true,
		Focus:         m.focus,
		RunCursor:     m.runCursor,
		OpenRunID:     m.openRunID,
		Help:          m.help,
		Filter:        m.filter,
		Filtering:     m.filtering,
	})
}

// toggleFocus switches between the tree and the runs pane; an empty registry
// has no runs side to focus, so switching is a clamped no-op there.
func (m *Model) toggleFocus() {
	if len(m.repos) == 0 {
		return
	}
	if m.focus == art.FocusTree {
		m.focus = art.FocusRuns
		return
	}
	m.focus = art.FocusTree
}

// moveRunCursor walks the visible run rows of the CURRENT snapshot in render
// order, clamping at both ends; an empty run list is a no-op. A stale cursor
// left behind by a snapshot swap re-anchors from the top of the walk in the
// pressed direction. Any actual movement clears an open run detail.
func (m *Model) moveRunCursor(delta int) {
	visible := art.VisibleRuns(art.ViewState{Repos: m.repos})
	if len(visible) == 0 {
		return
	}
	current := 0
	for i, pos := range visible {
		if pos == m.runCursor {
			current = i
			break
		}
	}
	next := current + delta
	if next < 0 {
		next = 0
	}
	if last := len(visible) - 1; next > last {
		next = last
	}
	if next == current && visible[current] == m.runCursor {
		return // clamped against a valid position: nothing moved
	}
	m.runCursor = visible[next]
	m.openRunID = ""
}

// moveTreeCursor walks the visible tree rows of the current snapshot in
// render order, clamping at both ends. An empty registry is a no-op.
func (m *Model) moveTreeCursor(delta int) {
	visible := art.VisibleTreePositions(art.ViewState{Repos: m.repos, TreeCursor: m.treeCursor})
	if len(visible) == 0 {
		return
	}
	current := 0
	for i, pos := range visible {
		if pos == m.treeCursor {
			current = i
			break
		}
	}
	next := current + delta
	if next < 0 {
		next = 0
	}
	if last := len(visible) - 1; next > last {
		next = last
	}
	if next == current && visible[current] == m.treeCursor {
		return
	}
	m.treeCursor = visible[next]
	m.selected = m.treeCursor.Repo
}

// toggleOpenRun expands or collapses the run detail under the runs cursor;
// pressing enter on anything that is not a currently visible run row leaves
// the model untouched.
func (m *Model) toggleOpenRun() {
	for _, pos := range art.VisibleRuns(art.ViewState{Repos: m.repos}) {
		if pos != m.runCursor {
			continue
		}
		id := m.repos[pos.Repo].Runs[pos.Run].RunID
		if m.openRunID == id {
			m.openRunID = ""
		} else {
			m.openRunID = id
		}
		return
	}
}

// requestRunAction gates and dispatches the a/r keys. The action fires only
// when an actions hook is wired, the runs pane owns focus, the cursor rests
// on a currently visible run row (the exact walk the ACTIVITY pane draws),
// and that row's repository is enabled, non-missing, and error-free; every
// unmet gate — including a nil hook — is a pure no-op. When the gate opens,
// the pending marker goes up before the hook command is handed out, and only
// the matching result message clears it afterwards.
func (m *Model) requestRunAction(key string) tea.Cmd {
	if m.actions == nil || m.focus != art.FocusRuns {
		return nil
	}
	pos, ok := focusedVisibleRun(m.repos, m.runCursor)
	if !ok {
		return nil
	}
	target := m.repos[pos.Repo]
	if !target.Enabled || target.Missing || target.Error != "" {
		return nil
	}
	runID := target.Runs[pos.Run].RunID
	if key == "r" {
		m.pending = &pendingAction{Kind: KindRetry, RepoPath: target.Path, RunID: runID}
		return m.actions.Retry(target.Path, runID)
	}
	m.pending = &pendingAction{Kind: KindAbort, RepoPath: target.Path, RunID: runID}
	return m.actions.Abort(target.Path, runID)
}

// focusedVisibleRun reports whether pos names a currently visible run row of
// the snapshot, reusing the render-order walk VisibleRuns publishes.
func focusedVisibleRun(repos []overview.Repo, pos art.RunPos) (art.RunPos, bool) {
	for _, visible := range art.VisibleRuns(art.ViewState{Repos: repos}) {
		if visible == pos {
			return visible, true
		}
	}
	return art.RunPos{}, false
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
