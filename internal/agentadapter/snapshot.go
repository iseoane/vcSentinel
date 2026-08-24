package agentadapter

import (
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewsnapshot"
)

// createReviewSnapshot delegates to the shared snapshot discipline in
// internal/reviewsnapshot. Ticket 16 slice 3 relocated the implementation
// there so the ACP/acpx adapter family can run the exact same discipline
// without importing this package (which now constructs ACP-backed adapters
// and therefore depends on acpadapter). Keep this a pure delegation: any
// semantic change belongs in reviewsnapshot so both adapter kinds stay
// byte-identical.
func createReviewSnapshot(worktree, sha string, paths []string) (string, []string, func(), error) {
	return reviewsnapshot.Create(worktree, sha, paths)
}

// rutasRevisionSeguras delegates to the shared reviewer path filter. See the
// createReviewSnapshot note for why the implementation moved.
func rutasRevisionSeguras(rutas []string) []string {
	return reviewsnapshot.SafePaths(rutas)
}
