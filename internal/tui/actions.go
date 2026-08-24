package tui

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbletea"

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

// actionCmd routes one control action through the provider host. Host
// resolution failures surface as hostErrMsg (reconnectable); semantic
// rejections surface as actionResultMsg and land in the status line without
// crashing or auto-retrying — a failed action burned its idempotency
// identity, so the operator decides what happens next.
func (m Model) actionCmd(kind string, perform func(context.Context, execution.RepositoryHost) error) tea.Cmd {
	provider := m.provider
	ctx := m.ctx
	if ctx == nil { // zero-value models never went through New
		ctx = context.Background()
	}
	return func() tea.Msg {
		host, err := provider()
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
