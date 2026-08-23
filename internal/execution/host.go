package execution

import (
	"context"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// RepositoryHost is the narrow control seam over durable runs. It carries
// exactly four operations; transport concerns arrive in a later slice.
type RepositoryHost interface {
	Start(ctx context.Context, request StartRequest) (Handle, error)
	Inspect(ctx context.Context, runID agentrun.Identity) (Inspection, error)
	Subscribe(ctx context.Context, request SubscribeRequest) (store.EventPage, error)
	Apply(ctx context.Context, request ApplyRequest) (ApplyResult, error)
}

// StartRequest names the run to admit and the durable policy governing it.
type StartRequest struct {
	Request agentrun.RunRequest
	Policy  store.RunPolicy
}

// ApplyRequest names the run receiving a control action.
type ApplyRequest struct {
	RunID  agentrun.Identity
	Action ControlAction
}

// SubscribeRequest pages durably recorded events after an exclusive cursor.
type SubscribeRequest struct {
	RunID       agentrun.Identity
	AfterCursor uint64
	Limit       int
}

// InProcessHost serves RepositoryHost from an in-process controller.
type InProcessHost struct {
	controller *Controller
}

var _ RepositoryHost = (*InProcessHost)(nil)

// NewInProcessHost binds the repository host seam to controller.
func NewInProcessHost(controller *Controller) *InProcessHost {
	return &InProcessHost{controller: controller}
}

// Start admits a run and returns its live handle.
func (h *InProcessHost) Start(ctx context.Context, request StartRequest) (Handle, error) {
	return h.controller.Start(ctx, request.Request, request.Policy)
}

// Inspect returns the durable projection, events, outcomes, and responses of a run.
func (h *InProcessHost) Inspect(ctx context.Context, runID agentrun.Identity) (Inspection, error) {
	return h.controller.Inspect(ctx, runID)
}

// Subscribe returns the events recorded strictly after AfterCursor, honoring Limit.
func (h *InProcessHost) Subscribe(ctx context.Context, request SubscribeRequest) (store.EventPage, error) {
	return h.controller.ReadEventPage(ctx, request.RunID, request.AfterCursor, request.Limit)
}

// Apply delivers a control action to a run.
func (h *InProcessHost) Apply(ctx context.Context, request ApplyRequest) (ApplyResult, error) {
	return h.controller.Apply(ctx, request.RunID, request.Action)
}
