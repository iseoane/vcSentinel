package execution

import (
	"context"
	"errors"
	"fmt"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
)

// Verification reports the deterministic integrity verdict for one run's
// durable evidence. The zero value is a safe "not verified" result.
type Verification struct {
	Valid  bool
	Events int
	Reason string
}

// Verify recomputes the integrity of a run's event stream without mutating
// anything. Hash and link validation happen inside the validated store scan;
// Verify additionally replays the lifecycle chain, checks invocation lineage
// boundaries, and requires ReadDerivedProjection to equal that fresh replay
// in state, sequence, revision, and head hash.
//
// Integrity findings — corrupted or truncated logs, broken continuity,
// divergent projections — yield Valid=false with a concrete Reason and a nil
// Go error, so automation can branch on Valid alone. Only infrastructure
// failures (context cancellation, an unreachable store) return a Go error.
func (c *Controller) Verify(ctx context.Context, runID agentrun.Identity) (Verification, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Verification{}, err
	}
	if c.store == nil {
		return Verification{}, ErrControllerNotReady
	}

	events, err := c.readAllEvents(ctx, runID)
	if err != nil {
		return verificationFailure(len(events), err)
	}

	replayed := agentrun.StateCreated
	var previousInvocation, previousParent string
	for index, frame := range events {
		position := uint64(index + 1)
		if frame.Sequence != position || frame.Revision != position {
			return invalidVerificationf(len(events), "event %d breaks sequence or revision contiguity", position), nil
		}
		if frame.RunID != string(runID) {
			return invalidVerification(len(events), "event carries foreign run identity"), nil
		}
		if frame.From != replayed {
			return invalidVerificationf(len(events),
				"event %d starts from %q but the replayed state is %q", position, frame.From, replayed), nil
		}
		if fault := lineageBoundaryFault(index, frame.InvocationID, frame.ParentInvocationID, previousInvocation, previousParent); fault != "" {
			return invalidVerification(len(events), fault), nil
		}
		previousInvocation, previousParent = frame.InvocationID, frame.ParentInvocationID
		replayed = frame.To
	}

	projection, projectionErr := c.store.ReadDerivedProjection(string(runID))
	if projectionErr != nil {
		return verificationFailure(len(events), projectionErr)
	}
	if projection.State != replayed || projection.Revision != uint64(len(events)) ||
		projection.Sequence != uint64(len(events)) {
		return invalidVerificationf(len(events),
			"derived projection (state %q revision %d sequence %d) diverges from the fresh replay (state %q revision %d)",
			projection.State, projection.Revision, projection.Sequence, replayed, len(events)), nil
	}
	if len(events) > 0 && projection.LastEventHash != events[len(events)-1].ContentHash {
		return invalidVerification(len(events), "derived projection head hash diverges from the event stream"), nil
	}
	return Verification{Valid: true, Events: len(events)}, nil
}

// lineageBoundaryFault enforces invocation-lineage consistency: the first
// event must be a root continuation, a new physical invocation must descend
// directly from the previous stream head, and one invocation must keep a
// stable parent across its whole contiguous block. An empty result means the
// boundary is consistent.
func lineageBoundaryFault(index int, invocation, parent, previousInvocation, previousParent string) string {
	switch {
	case index == 0:
		if parent != "" {
			return "the first event must belong to a root invocation without a parent"
		}
		return ""
	case invocation != previousInvocation:
		if parent == "" || parent != previousInvocation {
			return fmt.Sprintf("invocation %q does not continue the previous physical invocation %q", invocation, previousInvocation)
		}
		return ""
	case parent != previousParent:
		return fmt.Sprintf("invocation %q changed its parent lineage mid-block", invocation)
	default:
		return ""
	}
}

func invalidVerification(events int, reason string) Verification {
	return Verification{Valid: false, Events: events, Reason: reason}
}

func invalidVerificationf(events int, format string, args ...any) Verification {
	return invalidVerification(events, fmt.Sprintf(format, args...))
}

func verificationFailure(events int, err error) (Verification, error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return Verification{}, err
	}
	return invalidVerification(events, err.Error()), nil
}
