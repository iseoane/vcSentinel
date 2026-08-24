package tui

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/charmbracelet/bubbletea"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/attach"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// Test doubles for the attach model tests: a scriptable fake
// execution.RepositoryHost and a provider that serves hosts in order, plus
// the small builders every transition test shares.

// responses and recorded requests, so tests can stage successes, failures,
// and recovery sequences deterministically.
type fakeHost struct {
	mu sync.Mutex

	inspection    execution.Inspection
	inspectErr    error
	page          store.EventPage
	subscribeErrs []error // consumed per Subscribe call; empty slice repeats nil
	subscribes    []execution.SubscribeRequest

	applyErr   error
	applies    []execution.ApplyRequest
	retryErr   error
	retries    []execution.RetryRequest
	startErr   error
	recoverErr error
}

func (h *fakeHost) Start(ctx context.Context, request execution.StartRequest) (execution.Handle, error) {
	return execution.Handle{}, h.startErr
}

func (h *fakeHost) Inspect(ctx context.Context, request execution.InspectRequest) (execution.Inspection, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.inspection, h.inspectErr
}

func (h *fakeHost) Subscribe(ctx context.Context, request execution.SubscribeRequest) (store.EventPage, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.subscribes = append(h.subscribes, request)
	var page store.EventPage
	if len(h.subscribeErrs) > 0 {
		err, rest := h.subscribeErrs[0], h.subscribeErrs[1:]
		h.subscribeErrs = rest
		if err != nil {
			return page, err
		}
	}
	if h.page.Events != nil || h.page.HasMore {
		page = h.page
	}
	return page, nil
}

func (h *fakeHost) Apply(ctx context.Context, request execution.ApplyRequest) (execution.ApplyResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.applies = append(h.applies, request)
	return execution.ApplyResult{RunID: request.RunID}, h.applyErr
}

func (h *fakeHost) Recover(ctx context.Context, request execution.RecoverRequest) (execution.Handle, error) {
	return execution.Handle{}, h.recoverErr
}

func (h *fakeHost) Retry(ctx context.Context, request execution.RetryRequest) (execution.Handle, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.retries = append(h.retries, request)
	return execution.Handle{RunID: request.RunID}, h.retryErr
}

func (h *fakeHost) appliedCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.applies)
}

func (h *fakeHost) appliedActions() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	ids := make([]string, 0, len(h.applies))
	for _, request := range h.applies {
		ids = append(ids, request.ActionID)
	}
	return ids
}

// scriptedProvider serves hosts in order and fails once the script runs dry,
// simulating endpoint loss after the last good connection dies.
type scriptedProvider struct {
	mu    sync.Mutex
	hosts []execution.RepositoryHost
	calls int
}

// newScriptedProvider serves hosts in order and fails once the script runs
// dry, simulating endpoint loss after the last good connection dies. The
// returned closure satisfies HostProvider directly.
func newScriptedProvider(hosts ...execution.RepositoryHost) HostProvider {
	p := &scriptedProvider{hosts: hosts}
	return p.Host
}

func (p *scriptedProvider) Host() (execution.RepositoryHost, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	index := p.calls
	p.calls++
	if index < len(p.hosts) {
		return p.hosts[index], nil
	}
	return nil, fmt.Errorf("provider script exhausted after %d hosts", len(p.hosts))
}

func (p *scriptedProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// Shared test harness for the model transition suites: builders and drivers
// used by both model_test.go and update_test.go.

// newTestModel builds a model over a collector whose cursor sits at
// afterCursor, with an optional initial observed view.
func newTestModel(t *testing.T, provider HostProvider, afterCursor uint64, initial attach.RunView) Model {
	t.Helper()
	return New("run-tui-test", "tester", provider, attach.NewReplayCollector(afterCursor), initial, context.Background())
}

// update feeds one message through Update and hands back the typed model.
func update(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	typed, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want tui.Model", next)
	}
	return typed, cmd
}

// runCmd executes a command synchronously and returns its message; valid
// only for commands that complete without wall-clock waits (the observe and
// action commands qualify against fakes; tick commands never qualify).
func runCmd(t *testing.T, name string, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatalf("%s returned no command", name)
	}
	return cmd()
}

// runningView is a stable in-flight projection for gating tests.
func runningView() attach.RunView {
	return attach.RunView{
		RunID: "run-tui-test", State: agentrun.StateRunning,
		Sequence: 5, Revision: 5,
		Invocations: []attach.InvocationEvidence{}, Responses: []attach.ResponseRecord{},
	}
}

// failedView is a retryable terminal projection pinning the head revision.
func failedView(revision uint64) attach.RunView {
	return attach.RunView{
		RunID: "run-tui-test", State: agentrun.StateFailed,
		Terminal: agentrun.TerminalFailure, Sequence: revision, Revision: revision,
		Outcome: agentrun.OutcomeFailure, Error: "boom",
		Invocations: []attach.InvocationEvidence{}, Responses: []attach.ResponseRecord{},
	}
}

// runningProjection is the store-side projection backing a live run.
func runningProjection() store.RunProjection {
	return store.RunProjection{
		RunID: "run-tui-test", State: agentrun.StateRunning,
		Sequence: 5, Revision: 5,
	}
}

func keyMsg(key string) tea.KeyMsg {
	runes := []rune(key)
	switch key {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEscape}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: runes}
	}
}
