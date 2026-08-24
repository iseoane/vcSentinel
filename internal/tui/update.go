package tui

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/charmbracelet/bubbletea"
)

// Backoff ladder bounds for the reconnect policy capped by
// MaxReconnectAttempts (declared in model.go).
const (
	baseReconnectBackoff = 500 * time.Millisecond
	maxReconnectBackoff  = 8 * time.Second
)

// Update is the pure state-machine transition: messages in, next model plus
// at most one scheduled command out.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tea.WindowSizeMsg:
		return m, nil
	case detachMsg:
		return m, tea.Quit
	case pollTickMsg:
		if m.frozen || m.conn != stateAttached {
			return m, nil
		}
		return m, m.observeCmd()
	case backoffTickMsg:
		// The tick is being consumed: release the scheduler slot before the
		// observe runs, so a resulting hostErrMsg may claim it again.
		m.releaseBackoff()
		if m.conn != stateReconnecting {
			return m, nil
		}
		return m, m.observeCmd()
	case pagesAppliedMsg:
		m.view, m.haveView = msg.View, true
		m.conn, m.attempt = stateAttached, 0
		// Success ends any reconnect chain: no tick may still claim scheduling.
		m.releaseBackoff()
		if msg.View.IsTerminal() {
			// Terminal freeze stops scheduling polls; the footer takes over
			// with the exit hint. A later successful retry clears this.
			m.frozen = true
			m.status = ""
			return m, nil
		}
		m.frozen = false
		return m, pollTickCmd()
	case hostErrMsg:
		return m.handleHostError(msg)
	case actionResultMsg:
		if msg.Err != nil {
			if isConnectionLevelActionFailure(msg.Err) {
				// A transport failure during abort/respond/retry means the
				// session may have lost contact mid-action, so the run cannot
				// be treated as settled even when the last projection read
				// terminal: the durable stream may have moved past a view we
				// can no longer refresh. Interpretation: such a failure
				// UNFREEZES a frozen session into the bounded reconnect flow
				// (unless contact was already declared lost, where the freeze
				// is moot and exhaustion semantics stay authoritative), while
				// semantic rejections keep the status-line-only landing below.
				if m.conn != stateLost {
					m.frozen = false
				}
				return m.handleHostError(hostErrMsg{Err: msg.Err, Op: msg.Kind})
			}
			m.status = fmt.Sprintf("❌ %s rejected: %v", msg.Kind, msg.Err)
			return m, nil
		}
		m.status = fmt.Sprintf("✅ %s accepted", msg.Kind)
		if msg.Kind == "retry" {
			// The relaunch makes the run live again; the follow-up observe
			// confirms it and resumes polling.
			m.frozen = false
		}
		return m, m.observeCmd()
	default:
		return m, nil
	}
}

// handleKey routes keyboard input. While the respond input sub-state is
// active it consumes every key except ctrl+c, so typing a response that
// contains q or a never triggers those actions. While the session is not
// attached (reconnecting or lost contact) the action keys r/a/e/y are inert:
// each one resolves a host and observes, so firing one mid-backoff would
// schedule a second tick chain and multiply the attempt burn rate.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" {
		return m, tea.Quit
	}
	if m.input.active {
		return m.updateRespondInput(key, msg.Runes)
	}
	if key == "q" {
		return m, tea.Quit
	}
	if m.conn != stateAttached {
		switch key {
		case "y":
			// Retry stays live through terminal freeze by design: the frozen
			// session is exactly what y exists for. Everything else waits for
			// the connection to come back.
			if !m.frozen {
				return m, nil
			}
		default:
			return m, nil
		}
	}
	switch key {
	case "r":
		if m.conn == stateLost {
			m.status = "nothing to refresh: host unreachable"
			return m, nil
		}
		return m, m.observeCmd()
	case "a":
		if m.frozen {
			return m, nil
		}
		return m, m.abortCmd()
	case "e":
		if m.frozen || m.input.active {
			return m, nil
		}
		m.input.active = true
		m.input.buffer = nil
		return m, nil
	case "y":
		// Retry is gated on the projection saying retryable (failed,
		// canceled, timed_out): success and unavailable evidence stay final.
		// It is the one action that stays live through terminal freeze — a
		// settled-but-retryable run is exactly what y exists for — and its
		// success unfreezes the session through the follow-up observe.
		if !m.view.State.Retryable() {
			return m, nil
		}
		return m, m.retryCmd()
	default:
		return m, nil
	}
}

// updateRespondInput edits the respond buffer: enter submits, esc cancels.
func (m Model) updateRespondInput(key string, runes []rune) (tea.Model, tea.Cmd) {
	switch key {
	case "enter":
		text := m.input.value()
		if text == "" {
			m.status = "empty response: type text or press esc to cancel"
			return m, nil
		}
		m.input.active = false
		m.input.buffer = nil
		return m, m.respondCmd(text)
	case "esc":
		m.input.active = false
		m.input.buffer = nil
		m.status = ""
		return m, nil
	case "backspace":
		m.input.backspace()
		return m, nil
	case "ctrl+u":
		m.input.buffer = nil
		return m, nil
	default:
		if len(runes) == 1 && runes[0] >= ' ' {
			m.input.push(runes[0])
		}
		return m, nil
	}
}

// handleHostError enters bounded reconnect: attempt 1 schedules the first
// backoff tick, later failures escalate the delay until exhaustion freezes
// the session into the honest lost-contact state. It can fire from a poll, a
// manual refresh, or an action whose host resolution failed — or whose
// exchange failed at connection level (classified in actions.go), which
// additionally unfreezes a frozen session into this flow. The failed host
// is dropped from any provider cache first, so the reconnect attempt redials
// instead of reusing the connection that just died; implementations close
// that replaced connection only at the replacing resolution — poisoned until
// then, released via its own Close only after the swap — never under a
// concurrent exchange. Only one tick chain may be in flight at a time: if a
// backoffTickMsg is already pending, the failure updates the attempt counter
// and status but does not schedule a second scheduler — the pending tick
// drives the next attempt.
func (m Model) handleHostError(msg hostErrMsg) (tea.Model, tea.Cmd) {
	if m.provider != nil {
		m.provider.Reset()
	}
	if m.conn == stateLost {
		return m, nil
	}
	if m.conn != stateReconnecting {
		m.conn = stateReconnecting
		m.attempt = 0
	}
	m.attempt++
	if m.attempt > MaxReconnectAttempts {
		m.conn = stateLost
		m.releaseBackoff() // exhaustion ends the chain; nothing more schedules
		m.status = fmt.Sprintf(
			"lost contact with the repository host after %d reconnect attempts (%v); the last view is preserved — press q to quit",
			MaxReconnectAttempts, msg.Err)
		return m, nil
	}
	m.status = fmt.Sprintf("⚠️ %s failed: %v — reconnecting (attempt %d/%d)",
		msg.Op, msg.Err, m.attempt, MaxReconnectAttempts)
	if !m.claimBackoff() {
		return m, nil
	}
	return m, backoffCmd(m.attempt)
}

// backoffTracker pins whether a backoffTickMsg chain is already in flight.
// Without it, every failed exchange during reconnecting (manual refresh, an
// action whose host resolution died) scheduled its OWN tick chain while one
// was pending, multiplying the attempt burn rate. Claim/release keep exactly
// one scheduler alive; the tracker is mutex-guarded because command
// goroutines and Update run concurrently.
type backoffTracker struct {
	mu      sync.Mutex
	pending bool
}

func (b *backoffTracker) claim() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pending {
		return false
	}
	b.pending = true
	return true
}

func (b *backoffTracker) release() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pending = false
}

// claimBackoff reserves the single backoff-tick scheduling slot; nil-safe for
// zero-value models that never went through New.
func (m Model) claimBackoff() bool {
	if m.backoff == nil {
		return true
	}
	return m.backoff.claim()
}

// releaseBackoff frees the backoff-tick scheduling slot; nil-safe like
// claimBackoff.
func (m Model) releaseBackoff() {
	if m.backoff != nil {
		m.backoff.release()
	}
}

// reconnectBackoff doubles per attempt from base up to max.
func reconnectBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := baseReconnectBackoff << (attempt - 1)
	if delay <= 0 || delay > maxReconnectBackoff {
		return maxReconnectBackoff
	}
	return delay
}

func pollTickCmd() tea.Cmd {
	return tea.Tick(PollInterval, func(time.Time) tea.Msg { return pollTickMsg{} })
}

func backoffCmd(attempt int) tea.Cmd {
	delay := reconnectBackoff(attempt)
	return tea.Tick(delay, func(time.Time) tea.Msg { return backoffTickMsg{} })
}

// observeCmd runs one full observe cycle against a freshly resolved host.
// Both poll ticks and reconnect backoff ticks land here: on success the
// replay resumes strictly after the collector cursor, so nothing is
// duplicated or skipped across reconnects.
func (m Model) observeCmd() tea.Cmd {
	identity, principal, provider, collector, gate := m.identity, m.principal, m.provider, m.collector, m.gate
	ctx := m.ctx
	if ctx == nil { // zero-value models never went through New
		ctx = context.Background()
	}
	return func() tea.Msg {
		host, err := provider.Host()
		if err != nil {
			return hostErrMsg{Err: err, Op: "observe"}
		}
		gate.mu.Lock()
		defer gate.mu.Unlock()
		view, _, err := ObserveSnapshot(ctx, host, identity, principal, collector)
		if err != nil {
			return hostErrMsg{Err: err, Op: "observe"}
		}
		return pagesAppliedMsg{View: view}
	}
}
