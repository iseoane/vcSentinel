// Package tui hosts the Bubble Tea attach program for one durable run. It is
// a thin renderer and command dispatcher over the pure attach read-model: the
// ReplayCollector/RunView pipeline is consumed unchanged, every keyboard
// action routes through the daemon-preferred repository host with the same
// idempotency and revision discipline as the CLI twins, endpoint loss flips
// into bounded reconnect with cursor-resumed replay, and a terminal
// projection freezes the view instead of polling forever.
//
// Model.Update is deliberately pure (no tea.Program contact beyond returned
// commands), so the whole state machine is testable headlessly without
// spawning a terminal.
package tui

import (
	"context"
	"sync"
	"time"

	"github.com/charmbracelet/bubbletea"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/attach"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
)

// PollInterval spaces automatic repolls of an attached run; slow enough to
// stay quiet, fast enough that a settling run is caught sub-second.
const PollInterval = 250 * time.Millisecond

// Reconnect policy: bounded exponential backoff with a hard attempt cap.
// Each attempt re-resolves the host through the provider and resumes the
// replay strictly after the collector cursor. Exhaustion freezes the view
// into an honest lost-contact state instead of looping forever.
const MaxReconnectAttempts = 8

// MaxObservePages bounds one observe cycle's Subscribe pagination so a
// broken stream cannot spin forever.
const MaxObservePages = 10000

// subscribePageLimit matches the CLI observation page size.
const subscribePageLimit = 100

// HostProvider resolves a repository host on demand so reconnect attempts can
// re-resolve (a fresh dial after endpoint loss) instead of reusing a dead
// connection. Implementations must be safe for concurrent use by command
// goroutines.
type HostProvider func() (execution.RepositoryHost, error)

// connState tracks the exchange health of the attach session.
type connState int

const (
	stateAttached connState = iota
	stateReconnecting
	stateLost // bounded reconnects exhausted: frozen honest failure
)

// Messages driving the model. Everything the host pipeline produces lands in
// one of these, so Update stays a pure transition function.

type pagesAppliedMsg struct {
	View attach.RunView

	// The slice-1 text loop used this change flag to suppress reprinting an
	// unchanged view. That reprint contract became structural in the TUI
	// world: bubbletea re-renders on every Update and diffs frames itself,
	// so no render-count logic is needed (or wanted) here.
}

type hostErrMsg struct {
	Err error
	Op  string
}

type backoffTickMsg struct{}

type pollTickMsg struct{}

type actionResultMsg struct {
	Kind string
	Err  error
}

// detachMsg fires when the process-level detach signal (SIGINT/SIGTERM)
// fired; it quits cleanly exactly like the old text follow loop did.
type detachMsg struct{}

// Model is the Bubble Tea attach model over one durable run.
type Model struct {
	identity  agentrun.Identity
	principal string
	provider  HostProvider
	collector *attach.ReplayCollector

	// ctx carries the process detach context; Init watches it so SIGTERM
	// detaches as cleanly as pressing q. Stored here because bubbletea
	// copies the model by value through Update.
	ctx context.Context

	view     attach.RunView
	haveView bool

	conn    connState
	attempt int

	input respondInput

	frozen bool
	status string

	// backoff serializes the single reconnect backoff chain across bubbletea's
	// model copies. It lives behind a pointer for the same reason gate does:
	// bubbletea copies the model by value through Update.
	backoff *backoffTracker

	// gate serializes observation exchanges across concurrent command
	// goroutines. It lives behind a pointer because bubbletea copies models.
	gate *observeGate
}

// observeGate serializes observe cycles: only one Subscribe/Inspect exchange
// may run at a time against the shared collector.
type observeGate struct{ mu sync.Mutex }

// New builds the attach model from the already-observed initial snapshot the
// CLI took before launching the program, so first paint is instant and
// --after resume works unchanged.
func New(identity agentrun.Identity, principal string, provider HostProvider, collector *attach.ReplayCollector, initialView attach.RunView, detach context.Context) Model {
	return Model{
		identity:  identity,
		principal: principal,
		provider:  provider,
		collector: collector,
		ctx:       detach,
		view:      initialView,
		haveView:  initialView.RunID != "",
		backoff:   &backoffTracker{},
		gate:      &observeGate{},
	}
}

// Accessors pinning the construction contract for the CLI seam and tests.
// Snapshot exposes the last observed read-model; View is reserved by
// bubbletea for the renderer.

func (m Model) RunID() agentrun.Identity { return m.identity }
func (m Model) Principal() string        { return m.principal }
func (m Model) Provider() HostProvider   { return m.provider }
func (m Model) Snapshot() attach.RunView { return m.view }
func (m Model) Frozen() bool             { return m.frozen }
func (m Model) LostContact() bool        { return m.conn == stateLost }

// Init starts the poll loop (unless the run already settled) and watches the
// detach context.
func (m Model) Init() tea.Cmd {
	if m.view.IsTerminal() {
		return nil
	}
	cmds := []tea.Cmd{pollTickCmd()}
	if m.ctx != nil {
		cmds = append(cmds, waitForDetach(m.ctx))
	}
	return tea.Batch(cmds...)
}

// waitForDetach converts the process detach signal into a clean quit.
func waitForDetach(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		<-ctx.Done()
		return detachMsg{}
	}
}

// statusText renders the current status line content (also used by the view
// footer when nothing else is pending).
func (m Model) statusText() string {
	switch {
	case m.conn == stateLost:
		return "host unreachable"
	case m.frozen:
		return "run reached a terminal state"
	case m.status != "":
		return m.status
	default:
		return ""
	}
}
