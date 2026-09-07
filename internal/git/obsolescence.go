package git

import (
	"fmt"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
)

// FrozenCandidate pins, at the instant of Freeze(), the exact identity a
// later validation assumes is still current: the HEAD commit and the
// worktree's tree (tree OID) at that moment. Both fields take part in the
// StillValid comparison: if either changed, the candidate no longer
// represents the state the user believes they are validating.
type FrozenCandidate struct {
	SHA  string
	Tree string
}

// Freeze captures the current state of the repository: the SHA of HEAD and
// the tree OID of the worktree.
//
// Design decision: if the worktree is clean, the tree is directly HEAD's
// (TreeOf("HEAD")), which is cheap and touches neither the index nor the
// worktree. If it is dirty, "git stash create" is used: it generates a
// dangling commit representing the current content of the tracked files,
// without modifying the index, the worktree, or the real stash list (no
// "stash store" is run). Known and accepted limitation: "stash create" does
// not include untracked files; if that matters for a concrete use case,
// that call must first rely on
// WorktreeClean()/RequireCleanWorktreeForInplace, as the inplace mode
// further down in this file already requires.
func Freeze() (FrozenCandidate, error) {
	sha, err := SHAHead()
	if err != nil {
		return FrozenCandidate{}, fmt.Errorf("could not freeze the candidate: %w", err)
	}

	clean, err := WorktreeClean()
	if err != nil {
		return FrozenCandidate{}, fmt.Errorf("could not freeze the candidate: %w", err)
	}

	var tree string
	if clean {
		tree, err = TreeOf("HEAD")
	} else {
		tree, err = treeOfDirtyWorktree()
	}
	if err != nil {
		return FrozenCandidate{}, fmt.Errorf("could not freeze the candidate: %w", err)
	}

	return FrozenCandidate{SHA: sha, Tree: tree}, nil
}

// treeOfDirtyWorktree obtains the tree OID of the worktree's current
// content (tracked files) without mutating the index or the worktree, via
// "git stash create". See the design note in Freeze.
func treeOfDirtyWorktree() (string, error) {
	out, err := runGitOutput("stash", "create")
	if err != nil {
		return "", fmt.Errorf("could not capture the dirty worktree tree: %w", err)
	}
	anchorCommit := strings.TrimSpace(out)
	if anchorCommit == "" {
		// Should not happen if WorktreeClean() already detected dirt, but
		// for robustness against a race (another process committed in
		// between) it falls back to HEAD's tree instead of failing.
		return TreeOf("HEAD")
	}
	return TreeOf(anchorCommit)
}

// StillValid rechecks the repository's current state and compares it with
// the one captured in Freeze(). It is still valid only if both the SHA of
// HEAD and the tree OID match the candidate's: a new commit invalidates the
// candidate even when the resulting tree is identical, because it
// references a different HEAD than the one the user froze.
func StillValid(candidate FrozenCandidate) (bool, error) {
	actual, err := Freeze()
	if err != nil {
		return false, fmt.Errorf("could not check whether the candidate is still valid: %w", err)
	}
	return actual.SHA == candidate.SHA && actual.Tree == candidate.Tree, nil
}

// RequireCleanWorktreeForInplace aborts before any validation in inplace
// mode if the worktree has uncommitted changes: without this guard, the
// validation would run anyway and report the result of a different tree
// than the one the user believes they are validating. Outside inplace mode
// it does not apply (no abort): worktree mode already operates on an
// isolated snapshot.
func RequireCleanWorktreeForInplace(mode string) error {
	if mode != config.ModeInplace {
		return nil
	}
	clean, err := WorktreeClean()
	if err != nil {
		return fmt.Errorf("could not check the worktree state: %w", err)
	}
	if !clean {
		return fmt.Errorf("inplace mode requires a clean worktree before validating: there are uncommitted changes")
	}
	return nil
}
