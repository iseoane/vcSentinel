package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbletea"

	"github.com/ISeoane-Quental/vas.sentinel/internal/daemon"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
)

// tuiActionSequence keeps TUI control-action identities unique within this
// process, mirroring the CLI pattern in comandos_runs_actions.go exactly:
// kind prefix, nanosecond timestamp, pid, and a process-local counter.
var tuiActionSequence atomic.Uint64

func stampActionIdentity(kind string) string {
	return fmt.Sprintf("action:%s:%d-%d-%06d",
		kind, time.Now().UnixNano(), os.Getpid(), tuiActionSequence.Add(1))
}

// daemonClosedConnText is the documented equivalence check for the daemon
// package's unexported errConnClosed sentinel ("connection is closed"): the
// sentinel is deliberately package-private, so callers outside internal/daemon
// cannot errors.Is against it and can only match its stable surface text —
// which every post-mortem call on a dead RemoteHost wraps verbatim as
// "daemon: connection is closed".
const daemonClosedConnText = "connection is closed"

// isConnectionLevelActionFailure classifies an abort/respond/retry error into
// the two landings Update distinguishes. CONNECTION-LEVEL failures mean the
// transport or session died mid-action (or afterwards): they route through
// handleHostError semantics (provider Reset + reconnecting state + backoff).
// SEMANTIC rejections are server-classified answers about the run itself;
// they stay status-line-only because the burned idempotency identity makes
// auto-retry wrong. Classification rules, in order:
//
//  1. *daemon.RemoteError (errors.As) is by definition a decoded,
//     server-classified rejection — semantic, never transport.
//  2. The daemon closed-connection surface text (equivalence check above).
//  3. Mid-exchange transport surfaces of the framed client — EVERY path in
//     daemon.RemoteHost.call that calls markConnDead before returning:
//     request-write failure ("cannot send the "), response-read failure
//     ("cannot read the "), corrupt response frame ("response frame"),
//     ok=false answer with no classified error ("operation failed without
//     a classified error"), empty response body ("carries no body"), and
//     result-body decode failure ("cannot decode the "). Each leaves the
//     cached connection unusable for any later retry. When client.go gains
//     a new markConnDead call site, its surface text must be re-audited and
//     added here.
//
// Everything else (domain sentinels like stale-revision, context
// cancellation, in-process host validation errors) is semantic.
func isConnectionLevelActionFailure(err error) bool {
	if err == nil {
		return false
	}
	var rejected *daemon.RemoteError
	if errors.As(err, &rejected) {
		return false
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, daemonClosedConnText):
		return true
	case strings.Contains(msg, "cannot send the "),
		strings.Contains(msg, "cannot read the "),
		strings.Contains(msg, "response frame"),
		strings.Contains(msg, "operation failed without a classified error"),
		strings.Contains(msg, "carries no body"),
		strings.Contains(msg, "cannot decode the "):
		return true
	default:
		return false
	}
}

// actionCmd routes one control action through the provider host. Host
// resolution failures surface as hostErrMsg (reconnectable); every perform
// error lands as actionResultMsg carrying Err, and Update classifies it:
// connection-level failures route through handleHostError semantics, while
// semantic rejections stay in the status line without crashing or
// auto-retrying — a failed action burned its idempotency identity, so the
// operator decides what happens next.
func (m Model) actionCmd(kind string, perform func(context.Context, execution.RepositoryHost) error) tea.Cmd {
	provider := m.provider
	ctx := m.ctx
	if ctx == nil { // zero-value models never went through New
		ctx = context.Background()
	}
	return func() tea.Msg {
		host, err := provider.Host()
		if err != nil {
			return hostErrMsg{Err: err, Op: kind}
		}
		if err := perform(ctx, host); err != nil {
			return actionResultMsg{Kind: kind, Err: err}
		}
		return actionResultMsg{Kind: kind}
	}
}

// abortCmd mirrors the CLI abort twin: no confirmation, fresh idempotency
// identity, cooperative cancellation left to settle on its own (the next
// observe picks the settlement up).
func (m Model) abortCmd() tea.Cmd {
	kind := string(execution.ActionAbort)
	actionID := stampActionIdentity(kind)
	perform := func(ctx context.Context, host execution.RepositoryHost) error {
		_, err := host.Apply(ctx, execution.ApplyRequest{
			RunID:       m.identity,
			Action:      execution.ControlAction{Kind: execution.ActionAbort},
			ActionID:    actionID,
			AuthContext: execution.AuthContext{Principal: m.principal},
		})
		return err
	}
	return m.actionCmd(kind, perform)
}

// respondCmd mirrors the CLI respond twin: fresh idempotency identity, the
// typed answer travels verbatim in the control action.
func (m Model) respondCmd(text string) tea.Cmd {
	kind := string(execution.ActionRespond)
	actionID := stampActionIdentity(kind)
	perform := func(ctx context.Context, host execution.RepositoryHost) error {
		_, err := host.Apply(ctx, execution.ApplyRequest{
			RunID:       m.identity,
			Action:      execution.ControlAction{Kind: execution.ActionRespond, Response: text},
			ActionID:    actionID,
			AuthContext: execution.AuthContext{Principal: m.principal},
		})
		return err
	}
	return m.actionCmd(kind, perform)
}

// retryCmd relaunches a retryable terminal run carrying ExpectedRevision
// pinned from the last observed head revision, exactly like the CLI retry
// twin's --expected-revision contract: the server rejects the relaunch with
// ErrStaleRevision when a competing writer moved the stream head first.
func (m Model) retryCmd() tea.Cmd {
	expected := m.view.Revision
	perform := func(ctx context.Context, host execution.RepositoryHost) error {
		_, err := host.Retry(ctx, execution.RetryRequest{
			RunID:            m.identity,
			ExpectedRevision: expected,
			AuthContext:      execution.AuthContext{Principal: m.principal},
		})
		return err
	}
	// "retry" labels the action result; retry is a dedicated host operation
	// (host.Retry), not an Apply control action, so it has no Action constant.
	return m.actionCmd("retry", perform)
}
