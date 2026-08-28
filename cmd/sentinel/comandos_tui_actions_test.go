package main

// Unit coverage for the control-center session run actions (slice 12): a/r
// dispatch flows through the same daemon-preferred host plumbing the runs CLI
// uses, restricted to the session worktree, carrying unique authenticated
// idempotency identities, skipping the CLI settle wait, applying the
// idempotent-head fallback on rejection, and refusing foreign repositories
// with the documented error command. Completion messages are asserted
// black-box: each one is fed into a primed control model and observed through
// PendingAction()/Err(), which also proves wire compatibility with the
// control package's private result channel.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/bubbletea"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/overview"
	"github.com/ISeoane-Quental/vas.sentinel/internal/presence"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
	"github.com/ISeoane-Quental/vas.sentinel/internal/tui/control"
)

const (
	tuiActionsRunID      = "run-under-cursor"
	tuiActionsPrincipal  = "operator-tui"
	foreignRepoErrorText = "actions are limited to the session repository"
)

// tuiActionsSpy is the minimal control.RunActions double used only to prime a
// pending marker inside deliverTuiActionResult.
type tuiActionsSpy struct {
	calls int
}

func (s *tuiActionsSpy) Abort(string, string) tea.Cmd { return s.record() }
func (s *tuiActionsSpy) Retry(string, string) tea.Cmd { return s.record() }

func (s *tuiActionsSpy) record() tea.Cmd {
	s.calls++
	return func() tea.Msg { return struct{}{} }
}

// tuiActionsFakeHost records every repository-host exchange relevant to the
// session actions and stages rejection outcomes for Apply, Retry, and
// Inspect.
type tuiActionsFakeHost struct {
	mu         sync.Mutex
	applies    []execution.ApplyRequest
	retries    []execution.RetryRequest
	inspects   []execution.InspectRequest
	applyErr   error
	retryErr   error
	inspect    execution.Inspection
	inspectErr error
}

func (h *tuiActionsFakeHost) Start(context.Context, execution.StartRequest) (execution.Handle, error) {
	return execution.Handle{}, nil
}

func (h *tuiActionsFakeHost) Subscribe(context.Context, execution.SubscribeRequest) (store.EventPage, error) {
	return store.EventPage{}, nil
}

func (h *tuiActionsFakeHost) Recover(context.Context, execution.RecoverRequest) (execution.Handle, error) {
	return execution.Handle{}, nil
}

func (h *tuiActionsFakeHost) Apply(_ context.Context, req execution.ApplyRequest) (execution.ApplyResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.applies = append(h.applies, req)
	return execution.ApplyResult{RunID: req.RunID, Accepted: true}, h.applyErr
}

func (h *tuiActionsFakeHost) Retry(_ context.Context, req execution.RetryRequest) (execution.Handle, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.retries = append(h.retries, req)
	return execution.Handle{RunID: req.RunID}, h.retryErr
}

func (h *tuiActionsFakeHost) Inspect(_ context.Context, req execution.InspectRequest) (execution.Inspection, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.inspects = append(h.inspects, req)
	return h.inspect, h.inspectErr
}

func (h *tuiActionsFakeHost) snapshot() (applies, retries, inspects int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.applies), len(h.retries), len(h.inspects)
}

// stubTuiActionSeams pins a deterministic principal and swaps the controller
// and host seams for the given fakes, counting host teardowns. Restores both
// seams through t.Cleanup.
func stubTuiActionSeams(t *testing.T, host *tuiActionsFakeHost) *int {
	t.Helper()
	t.Setenv("USERNAME", "")
	t.Setenv("USER", tuiActionsPrincipal)
	teardowns := 0
	originalController, originalHost := tuiSessionController, tuiSessionHost
	tuiSessionController = func(string) (*execution.Controller, error) {
		return execution.NewController(store.NuevoStore(t.TempDir()), nil), nil
	}
	tuiSessionHost = func(string, *execution.Controller) (execution.RepositoryHost, func()) {
		return host, func() { teardowns++ }
	}
	t.Cleanup(func() { tuiSessionController, tuiSessionHost = originalController, originalHost })
	return &teardowns
}

// runeKey builds a rune KeyMsg the way a real terminal would deliver it.
func runeKey(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

// deliverTuiActionResult primes a live control model over one repository
// (runs focus, cursor on its only run, pending marker raised for the wanted
// kind) and delivers msg to Update. The returned model lets tests observe the
// outcome through PendingAction()/Err().
func deliverTuiActionResult(t *testing.T, msg tea.Msg, kind, repoPath, runID string) control.Model {
	t.Helper()
	repos := []overview.Repo{{
		Path: repoPath, Name: "session", Enabled: true,
		Runs: []presence.RunSummary{{RunID: runID, State: agentrun.StateRunning}},
	}}
	spy := &tuiActionsSpy{}
	m := control.NewLive(repos, func() ([]overview.Repo, error) { return repos, nil }, time.Second, spy)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // tree -> runs focus
	m = next.(control.Model)
	key := runeKey('a')
	if kind == control.KindRetry {
		key = runeKey('r')
	}
	next, _ = m.Update(key)
	m = next.(control.Model)
	if _, _, _, ok := m.PendingAction(); !ok {
		t.Fatalf("priming %s dispatch did not raise a marker (spy calls=%d)", kind, spy.calls)
	}
	next, _ = m.Update(msg)
	return next.(control.Model)
}

// TestSessionRunActionsForeignRepositoryRefused pins the session-only gate:
// paths other than the resolved session root yield an immediate command
// carrying the documented error without resolving principals, controllers, or
// hosts.
func TestSessionRunActionsForeignRepositoryRefused(t *testing.T) {
	host := &tuiActionsFakeHost{}
	stubTuiActionSeams(t, host)
	worktree := t.TempDir()
	foreign := t.TempDir()
	actions := newSessionRunActions(worktree)

	tests := []struct {
		name  string
		kind  string
		call  func(string, string) tea.Cmd
		prime string
	}{
		{name: "abort", kind: control.KindAbort, call: actions.Abort, prime: "a"},
		{name: "retry", kind: control.KindRetry, call: actions.Retry, prime: "r"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := tt.call(foreign, tuiActionsRunID)
			if cmd == nil {
				t.Fatalf("foreign-path %s returned no command at all", tt.name)
			}
			m := deliverTuiActionResult(t, cmd(), tt.kind, foreign, tuiActionsRunID)
			if got := m.Err(); got != foreignRepoErrorText {
				t.Errorf("Err() = %q, want exactly %q", got, foreignRepoErrorText)
			}
			if _, _, _, ok := m.PendingAction(); ok {
				t.Error("the refusal did not clear the pending marker")
			}
			applies, retries, inspects := host.snapshot()
			if applies+retries+inspects != 0 {
				t.Errorf("a foreign-path action reached the host: applies=%d retries=%d inspects=%d",
					applies, retries, inspects)
			}
		})
	}
}

// TestSessionRunActionsAbortSendsUniqueAuthenticatedApplies pins the abort
// envelope: two aborts produce two Apply requests with the session worktree
// as target, the deterministic principal, distinct unique ActionIDs sharing
// the runs-CLI format, no Inspect contact on the happy path, and one host
// teardown per dispatch (closed inside the command goroutine).
func TestSessionRunActionsAbortSendsUniqueAuthenticatedApplies(t *testing.T) {
	host := &tuiActionsFakeHost{}
	teardowns := stubTuiActionSeams(t, host)
	worktree := t.TempDir()
	actions := newSessionRunActions(worktree)

	first, second := actions.Abort(worktree, tuiActionsRunID), actions.Abort(worktree, tuiActionsRunID)
	for _, cmd := range []tea.Cmd{first, second} {
		m := deliverTuiActionResult(t, cmd(), control.KindAbort, worktree, tuiActionsRunID)
		if got := m.Err(); got != "" {
			t.Errorf("successful abort surfaced %q on Err()", got)
		}
	}

	applies, _, inspects := host.snapshot()
	if applies != 2 {
		t.Fatalf("applies = %d, want 2", applies)
	}
	if inspects != 0 {
		t.Errorf("inspects = %d, want none on the happy path", inspects)
	}
	if *teardowns != 2 {
		t.Errorf("host teardowns = %d, want one per dispatch", *teardowns)
	}
	ids := map[string]bool{}
	for i, req := range host.applies {
		if req.Action.Kind != execution.ActionAbort || req.Action.Response != "" {
			t.Errorf("apply[%d] action = %+v, want a bare abort", i, req.Action)
		}
		if req.RunID != agentrun.Identity(tuiActionsRunID) {
			t.Errorf("apply[%d] run = %q, want %q", i, req.RunID, tuiActionsRunID)
		}
		if req.AuthContext.Principal != tuiActionsPrincipal {
			t.Errorf("apply[%d] principal = %q, want %q", i, req.AuthContext.Principal, tuiActionsPrincipal)
		}
		wantPrefix := fmt.Sprintf("action:%s:", execution.ActionAbort)
		if !strings.HasPrefix(req.ActionID, wantPrefix) {
			t.Errorf("apply[%d] ActionID = %q, want the %q format", i, req.ActionID, wantPrefix)
		}
		if ids[req.ActionID] {
			t.Errorf("apply[%d] reused ActionID %q across aborts", i, req.ActionID)
		}
		ids[req.ActionID] = true
	}
}

// TestSessionRunActionsRetrySkipsSettleWait pins the retry envelope: one
// authenticated Retry request without a revision pin, zero controller/host
// observation traffic (no settle wait anywhere), and the usual teardown.
func TestSessionRunActionsRetrySkipsSettleWait(t *testing.T) {
	host := &tuiActionsFakeHost{}
	teardowns := stubTuiActionSeams(t, host)
	worktree := t.TempDir()

	cmd := newSessionRunActions(worktree).Retry(worktree, tuiActionsRunID)
	m := deliverTuiActionResult(t, cmd(), control.KindRetry, worktree, tuiActionsRunID)
	if got := m.Err(); got != "" {
		t.Errorf("successful retry surfaced %q on Err()", got)
	}

	applies, retries, inspects := host.snapshot()
	if retries != 1 {
		t.Fatalf("retries = %d, want exactly one", retries)
	}
	req := host.retries[0]
	if req.RunID != agentrun.Identity(tuiActionsRunID) {
		t.Errorf("retry run = %q, want %q", req.RunID, tuiActionsRunID)
	}
	if req.ExpectedRevision != 0 {
		t.Errorf("retry pinned revision %d, want the skip-the-pin default", req.ExpectedRevision)
	}
	if req.AuthContext.Principal != tuiActionsPrincipal {
		t.Errorf("retry principal = %q, want %q", req.AuthContext.Principal, tuiActionsPrincipal)
	}
	if applies != 0 || inspects != 0 {
		t.Errorf("settle-wait evidence found: applies=%d inspects=%d, want zeros", applies, inspects)
	}
	if *teardowns != 1 {
		t.Errorf("host teardowns = %d, want exactly one", *teardowns)
	}
}

// TestSessionRunActionsIdempotentHeadFallbackAccepts pins the fallback shape
// on both hooks: an ErrRunNotActive rejection consults the host's durable
// evidence EXACTLY ONCE, and a head that already satisfies the action's goal
// turns the rejection into quiet success.
//
// The abort fixture changed. It was named settledHead while carrying
// StateRunning, and asserted that aborting a RUNNING run is quiet success —
// which is the defect this test now guards against, not a contract: abort's
// goal is a stopped run, so a running head means the abort did not happen. A
// live head still satisfies RETRY, whose goal is "there is a live attempt", so
// that half is unchanged and only its fixture is named honestly.
func TestSessionRunActionsIdempotentHeadFallbackAccepts(t *testing.T) {
	worktree := t.TempDir()
	cabezaViva := execution.Inspection{
		Projection: store.RunProjection{State: agentrun.StateRunning},
		Events:     []store.EventFrame{{JobID: "job-1"}, {InvocationID: "inv-2"}},
	}
	cabezaAsentada := execution.Inspection{
		Projection: store.RunProjection{State: agentrun.StateCanceled},
		Events:     []store.EventFrame{{JobID: "job-1"}, {InvocationID: "inv-2"}},
	}

	t.Run("abort fallback accepts an already settled head", func(t *testing.T) {
		host := &tuiActionsFakeHost{inspect: cabezaAsentada}
		stubTuiActionSeams(t, host)
		host.applyErr = fmt.Errorf("controller refused: %w", execution.ErrRunNotActive)
		cmd := newSessionRunActions(worktree).Abort(worktree, tuiActionsRunID)
		m := deliverTuiActionResult(t, cmd(), control.KindAbort, worktree, tuiActionsRunID)
		if got := m.Err(); got != "" {
			t.Errorf("fallback surfaced %q on Err(), want quiet acceptance", got)
		}
		applies, _, inspects := host.snapshot()
		if applies != 1 || inspects != 1 {
			t.Errorf("applies=%d inspects=%d, want one rejected apply plus exactly one fallback inspect",
				applies, inspects)
		}
	})
	t.Run("abort fallback refuses a still running head", func(t *testing.T) {
		host := &tuiActionsFakeHost{inspect: cabezaViva}
		stubTuiActionSeams(t, host)
		host.applyErr = fmt.Errorf("controller refused: %w", execution.ErrRunNotActive)
		cmd := newSessionRunActions(worktree).Abort(worktree, tuiActionsRunID)
		m := deliverTuiActionResult(t, cmd(), control.KindAbort, worktree, tuiActionsRunID)
		if m.Err() == "" {
			t.Error("a run still running was reported as aborted: the operator would believe it stopped")
		}
	})
	t.Run("retry fallback accepts the live attempt", func(t *testing.T) {
		host := &tuiActionsFakeHost{inspect: cabezaViva}
		stubTuiActionSeams(t, host)
		host.retryErr = fmt.Errorf("controller refused: %w", execution.ErrRunNotActive)
		cmd := newSessionRunActions(worktree).Retry(worktree, tuiActionsRunID)
		m := deliverTuiActionResult(t, cmd(), control.KindRetry, worktree, tuiActionsRunID)
		if got := m.Err(); got != "" {
			t.Errorf("fallback surfaced %q on Err(), want quiet acceptance", got)
		}
		_, retries, inspects := host.snapshot()
		if retries != 1 || inspects != 1 {
			t.Errorf("retries=%d inspects=%d, want one rejected retry plus exactly one fallback inspect",
				retries, inspects)
		}
	})
}

// TestSessionRunActionsRejectionSurfacesError pins the honest-failure path: a
// rejection that carries no settled idempotent head never consults Inspect
// and lands verbatim on Err().
func TestSessionRunActionsRejectionSurfacesError(t *testing.T) {
	worktree := t.TempDir()
	t.Run("abort rejection", func(t *testing.T) {
		host := &tuiActionsFakeHost{applyErr: errors.New("daemon offline")}
		stubTuiActionSeams(t, host)
		cmd := newSessionRunActions(worktree).Abort(worktree, tuiActionsRunID)
		m := deliverTuiActionResult(t, cmd(), control.KindAbort, worktree, tuiActionsRunID)
		if got := m.Err(); !strings.Contains(got, "daemon offline") {
			t.Errorf("Err() = %q, want the apply rejection", got)
		}
		if _, _, inspects := host.snapshot(); inspects != 0 {
			t.Errorf("inspects = %d, want no fallback attempt for a plain failure", inspects)
		}
	})
	t.Run("retry rejection", func(t *testing.T) {
		host := &tuiActionsFakeHost{retryErr: errors.New("not retryable")}
		stubTuiActionSeams(t, host)
		cmd := newSessionRunActions(worktree).Retry(worktree, tuiActionsRunID)
		m := deliverTuiActionResult(t, cmd(), control.KindRetry, worktree, tuiActionsRunID)
		if got := m.Err(); !strings.Contains(got, "not retryable") {
			t.Errorf("Err() = %q, want the retry rejection", got)
		}
		if _, _, inspects := host.snapshot(); inspects != 0 {
			t.Errorf("inspects = %d, want no fallback attempt for a plain failure", inspects)
		}
	})
}
