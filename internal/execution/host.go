package execution

import (
	"context"
	"errors"
	"reflect"
	"sync"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
)

// Port-level validation sentinels, declared beside the controller error
// block in the same style. They never reclassify controller outcomes; they
// only report seam-level envelope validation.
var (
	// ErrMissingPrincipal reports a request envelope without an
	// authenticated principal. D1 validates presence only; identity
	// semantics arrive with the daemon.
	ErrMissingPrincipal = errors.New("execution: request carries no authenticated principal")
	// ErrDuplicateAction reports an Apply whose caller-generated
	// idempotency identity was already consumed for the same run.
	ErrDuplicateAction = errors.New("execution: control action identity was already consumed for this run")
)

// AuthContext names the local principal issuing a repository-host request.
type AuthContext struct {
	Principal string `json:"principal"`
}

// RepositoryHost is the narrow control seam over durable runs. It carries
// exactly six operations; transport concerns arrive in a later slice.
type RepositoryHost interface {
	Start(ctx context.Context, request StartRequest) (Handle, error)
	Inspect(ctx context.Context, request InspectRequest) (Inspection, error)
	Subscribe(ctx context.Context, request SubscribeRequest) (store.EventPage, error)
	Apply(ctx context.Context, request ApplyRequest) (ApplyResult, error)
	Recover(ctx context.Context, request RecoverRequest) (Handle, error)
	Retry(ctx context.Context, request RetryRequest) (Handle, error)
}

// StartRequest names the run to admit and the durable policy governing it.
// It carries exactly one of two mutually exclusive payload forms: either the
// canonical Request, or the explicit Candidate plus Prompt pair that a
// transport reconstructs into the canonical request on the receiving side.
// Raw prompts never travel as JSON inside Request because its fields are
// deliberately unexported identity-canonical data; the explicit pair is the
// transport-safe spelling. ValidateStartRequest enforces the invariant.
type StartRequest struct {
	Request     agentrun.RunRequest `json:"request"`
	Candidate   string              `json:"candidate,omitempty"`
	Prompt      string              `json:"prompt,omitempty"`
	Policy      store.RunPolicy     `json:"policy"`
	AuthContext AuthContext         `json:"auth_context"`
}

// startRequestIsZero reports whether an admission envelope's canonical
// Request payload carries its zero value, i.e. the envelope uses the explicit
// Candidate/Prompt transport form. It is the single source of that test so
// validation and resolution can never drift.
func startRequestIsZero(request StartRequest) bool {
	return reflect.ValueOf(request.Request).IsZero()
}

// ValidateStartRequest enforces the admission payload invariant of
// StartRequest: either Request is the zero value with a non-empty Candidate
// AND a non-empty Prompt (the explicit transport form), or Request is
// non-zero with both strings empty (the canonical form). Mixing the forms or
// carrying no payload at all is rejected with a deterministic inline error.
// Every admission path — InProcessHost.Start and the daemon dispatch alike —
// shares this single helper so the two can never drift.
func ValidateStartRequest(request StartRequest) error {
	candidateSet := request.Candidate != ""
	promptSet := request.Prompt != ""
	zeroRequest := startRequestIsZero(request)
	explicitForm := zeroRequest && candidateSet && promptSet
	canonicalForm := !zeroRequest && !candidateSet && !promptSet
	if !explicitForm && !canonicalForm {
		return errors.New("execution: start request carries conflicting payloads")
	}
	return nil
}

// ResolveAdmissionRequest returns the canonical agentrun.RunRequest an
// admission delegates to the controller. For the explicit Candidate/Prompt
// form it constructs the real request via agentrun.NewRunRequest; for the
// canonical form it returns Request untouched. Callers must validate the
// envelope with ValidateStartRequest first; the resolution itself is total
// and never fails.
func ResolveAdmissionRequest(request StartRequest) agentrun.RunRequest {
	if startRequestIsZero(request) {
		return agentrun.NewRunRequest(agentrun.Candidate(request.Candidate), agentrun.Prompt(request.Prompt), nil)
	}
	return request.Request
}

// InspectRequest names the run whose durable evidence is reconstructed.
type InspectRequest struct {
	RunID       agentrun.Identity `json:"run_id"`
	AuthContext AuthContext       `json:"auth_context"`
}

// ApplyRequest names the run receiving a control action. ActionID is a
// caller-generated idempotency identity consumed once per run.
type ApplyRequest struct {
	RunID       agentrun.Identity `json:"run_id"`
	Action      ControlAction     `json:"action"`
	ActionID    string            `json:"action_id"`
	AuthContext AuthContext       `json:"auth_context"`
}

// SubscribeRequest pages durably recorded events after an exclusive cursor.
type SubscribeRequest struct {
	RunID       agentrun.Identity `json:"run_id"`
	AfterCursor uint64            `json:"after_cursor"`
	Limit       int               `json:"limit"`
	AuthContext AuthContext       `json:"auth_context"`
}

// RecoverRequest names the run resumed through explicit operator recovery.
// ExpectedRevision optionally pins the durable stream head; zero skips the
// check, mirroring Controller.Recover.
type RecoverRequest struct {
	RunID            agentrun.Identity `json:"run_id"`
	ExpectedRevision uint64            `json:"expected_revision"`
	AuthContext      AuthContext       `json:"auth_context"`
}

// RetryRequest names a retryable run relaunched inside its original logical
// job and run identity. ExpectedRevision optionally pins the durable stream
// head; zero skips the check, mirroring Controller.Retry.
type RetryRequest struct {
	RunID            agentrun.Identity `json:"run_id"`
	ExpectedRevision uint64            `json:"expected_revision"`
	AuthContext      AuthContext       `json:"auth_context"`
}

// InProcessHost serves RepositoryHost from an in-process controller. Replay
// detection of Apply idempotency identities lives for this process lifetime;
// cross-restart dedup arrives with the daemon.
type InProcessHost struct {
	controller *Controller

	mu        sync.Mutex
	actionIDs map[appliedAction]struct{}
}

// appliedAction keys one consumed Apply idempotency identity to its run: the
// same ActionID under a different run is a distinct identity.
type appliedAction struct {
	runID    agentrun.Identity
	actionID string
}

var _ RepositoryHost = (*InProcessHost)(nil)

// NewInProcessHost binds the repository host seam to controller.
func NewInProcessHost(controller *Controller) *InProcessHost {
	return &InProcessHost{controller: controller, actionIDs: make(map[appliedAction]struct{})}
}

// Start admits a run and returns its live handle. The envelope is validated
// before any controller contact: principal presence first, then the
// admission payload invariant. The explicit Candidate/Prompt form resolves
// into the canonical request here, so the controller only ever sees
// identity-canonical data.
func (h *InProcessHost) Start(ctx context.Context, request StartRequest) (Handle, error) {
	if request.AuthContext.Principal == "" {
		return Handle{}, ErrMissingPrincipal
	}
	if err := ValidateStartRequest(request); err != nil {
		return Handle{}, err
	}
	return h.controller.Start(ctx, ResolveAdmissionRequest(request), request.Policy)
}

// Inspect returns the durable projection, events, outcomes, and responses of a run.
func (h *InProcessHost) Inspect(ctx context.Context, request InspectRequest) (Inspection, error) {
	if request.AuthContext.Principal == "" {
		return Inspection{}, ErrMissingPrincipal
	}
	return h.controller.Inspect(ctx, request.RunID)
}

// Subscribe returns the events recorded strictly after AfterCursor, honoring Limit.
func (h *InProcessHost) Subscribe(ctx context.Context, request SubscribeRequest) (store.EventPage, error) {
	if request.AuthContext.Principal == "" {
		return store.EventPage{}, ErrMissingPrincipal
	}
	return h.controller.ReadEventPage(ctx, request.RunID, request.AfterCursor, request.Limit)
}

// Apply delivers a control action to a run. ActionID is a caller-generated
// idempotency identity: an already-consumed identity on the same run is
// rejected with ErrDuplicateAction before reaching the controller. The
// identity is consumed when admission is attempted, so a rejected action
// still burns its identity; callers retrying a failed action must issue a
// fresh identity. Replay detection lives for this process lifetime;
// cross-restart dedup arrives with the daemon.
func (h *InProcessHost) Apply(ctx context.Context, request ApplyRequest) (ApplyResult, error) {
	if request.AuthContext.Principal == "" {
		return ApplyResult{}, ErrMissingPrincipal
	}
	if request.ActionID == "" {
		return ApplyResult{}, errors.New("execution: control action identity is empty")
	}
	key := appliedAction{runID: request.RunID, actionID: request.ActionID}
	h.mu.Lock()
	if _, seen := h.actionIDs[key]; seen {
		h.mu.Unlock()
		return ApplyResult{}, ErrDuplicateAction
	}
	h.actionIDs[key] = struct{}{}
	h.mu.Unlock()
	return h.controller.Apply(ctx, request.RunID, request.Action)
}

// Recover resumes a run from durable evidence through explicit operator
// recovery, exactly like Controller.Recover but behind the authenticated
// seam.
func (h *InProcessHost) Recover(ctx context.Context, request RecoverRequest) (Handle, error) {
	if request.AuthContext.Principal == "" {
		return Handle{}, ErrMissingPrincipal
	}
	return h.controller.Recover(ctx, request.RunID, request.ExpectedRevision)
}

// Retry relaunches a retryable run inside its original identity, exactly like
// Controller.Retry but behind the authenticated seam.
func (h *InProcessHost) Retry(ctx context.Context, request RetryRequest) (Handle, error) {
	if request.AuthContext.Principal == "" {
		return Handle{}, ErrMissingPrincipal
	}
	return h.controller.Retry(ctx, request.RunID, request.ExpectedRevision)
}
