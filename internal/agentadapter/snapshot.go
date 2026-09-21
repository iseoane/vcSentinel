package agentadapter

import (
	"context"

	"github.com/ISeoane-Quental/vcSentinel/internal/reviewsnapshot"
)

// createReviewSnapshot delegates to the shared snapshot discipline in
// internal/reviewsnapshot. Ticket 16 slice 3 relocated the implementation
// there so the ACP/acpx adapter family can run the exact same discipline
// without importing this package (which now constructs ACP-backed adapters
// and therefore depends on acpadapter). Keep this a pure delegation: any
// semantic change belongs in reviewsnapshot so both adapter kinds stay
// byte-identical. ctx is threaded straight through so a caller cancellation
// aborts materialization instead of waiting for it to finish.
func createReviewSnapshot(ctx context.Context, worktree, sha string, paths []string) (string, []string, func(), error) {
	return reviewsnapshot.Create(ctx, worktree, sha, paths)
}
