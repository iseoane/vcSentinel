package process

import (
	"context"
	"time"
)

// ContainAfterCancellation is the shared adapter-side containment watchdog for
// owned trees spawned by the provider adapters. When the context fires, the
// tree gets the shared grace budget plus margin to die through the
// controller's escalation path first; if it is still running past that
// deadline, this watchdog hard-terminates whatever remains so no descendant
// of a deep spawn chain can outlive its budget silently. When the stamped
// policy restricts kills to the direct child, the watchdog never fires and no
// code path signals the tree.
//
// This function replaces the near-verbatim copies that used to live in
// internal/agentadapter/cli_review_context.go (the R7-proven original) and
// internal/acpadapter/review.go. The unified semantics are the original's,
// including its nil-context guard: a nil context disables the watchdog
// entirely instead of panicking on the select below.
func ContainAfterCancellation(ctx context.Context, tree *Tree) {
	if ctx == nil {
		return
	}
	select {
	case <-ctx.Done():
	case <-tree.Exited():
		return
	}
	if !WholeTreeTermination(ctx) {
		return
	}
	select {
	case <-tree.Exited():
	case <-time.After(ContainmentDeadline(ctx)):
		_ = Terminate(tree)
	}
}
