package main

import (
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

// sharedReviewLedger builds the v1 review ledger that review, status and pr
// read and write. It is the single place that decides where review evidence
// lives, so the decision can be read once instead of inferred from five call
// sites.
//
// It anchors on the Git common directory and NOT on the checkout's own gitDir.
// review.NewLedger files records under whatever directory it is handed, and
// these commands handed it the checkout's gitDir; the main checkout survived
// that only because its two paths coincide. A linked worktree's gitDir is
// <common-dir>/worktrees/<name>, so a review run there filed its record inside
// the administrative directory `git worktree remove` deletes, and delegating
// work to a writer in a dedicated worktree is the mandated workflow here, so
// that was the normal path. FU-12 in docs/issues/decisions.md records the
// measurement, including a review whose record landed in
// .git/worktrees/fu10-ticket-04/vas-sentinel/.
//
// Records already written under a per-checkout ledger are not migrated and are
// not read from here. They stay where they are, `runs prune`, `status --prune`
// and `review --prune` still enumerate every per-checkout ledger so no
// execution stream loses its provenance, and the commands above simply no
// longer see them.
//
// GetGitCommonDir also isolates the Git environment, which GetGitDirFrom
// does not. Sentinel runs inside its own pre-commit hook, where Git exports
// GIT_DIR, and GIT_DIR takes priority over -C.
func sharedReviewLedger(worktree string) (*review.Ledger, error) {
	commonDir, err := git.GetGitCommonDir(worktree)
	if err != nil {
		return nil, err
	}
	return review.NewLedger(commonDir), nil
}
