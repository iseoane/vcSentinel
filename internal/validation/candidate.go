package validation

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/graph"
)

// ErrStaleCandidate signals that the repository tree (or HEAD) changed while
// validation was running in worktree mode: the result no longer represents
// the state the user froze and must not be used as evidence for any gate
// (pre-commit, pre-push, pr review...).
var ErrStaleCandidate = errors.New("the candidate changed during validation: stale result, not usable as evidence")

// RunProfileOnCandidate is the entry point of T1.6: it wraps RunProfile
// (T1.3) with the isolation/freshness guarantee demanded by
// config.ValidationConfig.Mode, without the caller having to know the
// difference between running on a snapshot or on the real worktree.
//
//   - inplace: demands a clean worktree (git.RequireCleanWorktreeForInplace)
//     BEFORE running anything; if it is dirty, aborts without running any
//     command. If it is clean, runs directly on opts.Worktree (the real
//     worktree): there is no snapshot to create nor freshness to check,
//     because the guard already demanded that nothing be mid-commit.
//   - worktree (default, also when Mode is empty): freezes the candidate
//     (git.Freeze) BEFORE running, runs the validation on a snapshot
//     isolated from the frozen candidate tree (git.CreateSnapshot) and, when
//     done, checks that the candidate is still current (git.StillValid).
//     If it no longer is —the HEAD or the real worktree tree changed during
//     execution—, the result is discarded and ErrStaleCandidate is returned:
//     runs that may correspond to a state other than the one the user
//     believes they are validating are never propagated.
//
// Known and accepted limitation of worktree mode: the snapshot is a clean
// checkout in a new directory (git.CreateSnapshot), so it lacks node_modules,
// .env, and untracked fixtures that the real worktree does have; a capability
// that depends on those will fail there even though it would pass on the real
// worktree. The inplace mode exists as a configurable alternative exactly for
// that case.
func RunProfileOnCandidate(profile string, changedPaths []string, opts RunOptions) ([]ValidationRun, error) {
	mode := opts.Cfg.Validation.Mode
	if mode == "" {
		mode = config.ModeWorktree
	}

	if mode == config.ModeInplace {
		if err := git.RequireCleanWorktreeForInplace(mode); err != nil {
			return nil, err
		}
		return RunProfile(profile, opts)
	}

	// Snapshot cleanup is best-effort, but it must cover every worktree-mode
	// attempt, including failures while freezing the candidate or creating its
	// snapshot.
	defer func() { _ = git.PurgeSnapshots(git.SnapshotRetention) }()

	candidate, err := git.Freeze()
	if err != nil {
		return nil, fmt.Errorf("could not freeze the candidate before validating: %w", err)
	}

	// The snapshot is anchored to the candidate.Tree tree, not to a separately
	// recomputed TreeOf("HEAD"): Freeze already decided the right tree
	// (HEAD's if the worktree was clean, or the stash-anchor if it was dirty).
	// Anchoring to HEAD separately would ignore uncommitted changes that
	// already existed BEFORE calling this function, and StillValid would
	// accept them if nothing else changed during execution: the result would
	// look current but a tree other than the one the user froze would have
	// been validated.
	snapshot, releaseSnapshot, err := git.AcquireSnapshot(candidate.Tree)
	if err != nil {
		return nil, fmt.Errorf("could not create the validation snapshot: %w", err)
	}
	defer releaseSnapshot()

	opts.Worktree = snapshot
	opts.authorization = graph.ScopeAuthorization{}
	if opts.ProviderGraph != nil && len(changedPaths) > 0 {
		result, graphErr := opts.ProviderGraph(snapshot, candidate.Tree).Analyze(changedPaths)
		if graphErr == nil && result.SnapshotIdentity() == candidate.Tree && samePaths(result.AnalyzedPaths(), changedPaths) {
			opts.authorization, _ = graph.AuthorizePartialScope(result)
		}
	}
	runs, err := RunProfile(profile, opts)
	if err != nil {
		return runs, err
	}

	current, err := git.StillValid(candidate)
	if err != nil {
		return nil, fmt.Errorf("could not check whether the candidate was still current after validating: %w", err)
	}
	if !current {
		// The tree changed during execution: the runs already obtained might
		// correspond to a state other than the one the user froze, so they are
		// discarded instead of letting a gate use them by mistake.
		return nil, ErrStaleCandidate
	}
	return runs, nil
}

func samePaths(analyzed, changed []string) bool {
	if len(analyzed) != len(changed) {
		return false
	}
	pending := make(map[string]int, len(changed))
	for _, path := range changed {
		pending[filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))]++
	}
	for _, path := range analyzed {
		pending[path]--
	}
	for _, count := range pending {
		if count != 0 {
			return false
		}
	}
	return true
}
