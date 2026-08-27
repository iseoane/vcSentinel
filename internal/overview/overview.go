// Package overview fans out the Control Center's read-only sources into one
// deterministic snapshot: registry entries x git inventory x daemon presence.
// One unhealthy repository must never fail the whole view, so every per-
// repository problem is recorded in Repo.Error instead of aborting Collect.
package overview

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/inventory"
	"github.com/ISeoane-Quental/vas.sentinel/internal/presence"
	"github.com/ISeoane-Quental/vas.sentinel/internal/registry"
)

// isInternalWorktree applies host-normalized paths to the snapshot area rule.
func isInternalWorktree(worktreePath, commonDir string) bool {
	base := filepath.ToSlash(filepath.Clean(filepath.Join(commonDir, "vas-sentinel", "snapshots")))
	path := filepath.ToSlash(filepath.Clean(worktreePath))
	isWithin := func(candidate, root string) bool {
		if candidate == root {
			return true
		}
		return strings.HasPrefix(candidate, root) && len(candidate) > len(root) && candidate[len(root)] == '/'
	}
	if runtime.GOOS == "windows" {
		return isWithin(strings.ToLower(path), strings.ToLower(base))
	}
	return isWithin(path, base)
}

// Repo is the dashboard fact sheet of one registered repository.
type Repo struct {
	Path      string
	Name      string
	Enabled   bool
	Missing   bool
	Error     string
	Worktrees []inventory.Worktree
	Origin    string
	Daemon    presence.Presence
	Runs      []presence.RunSummary
}

// recentRunLimit is the operator-visible history depth for each activity scope.
const recentRunLimit = 10

// MaxSelectableWorktrees is the TUI's selectable worktree cap.
const MaxSelectableWorktrees = 20

// recentRuns is the store-backed read seam used by Collect tests.
var recentRuns = presence.RecentRunsForWorktrees

// Collect builds one deterministic Repo per registry entry and records probe
// failures on that Repo instead of failing the whole overview.
func Collect(registryPath string) ([]Repo, error) {
	reg, err := registry.Open(registryPath)
	if err != nil {
		return nil, fmt.Errorf("overview: open registry: %w", err)
	}
	entries := reg.List()
	repos := make([]Repo, 0, len(entries))
	for _, entry := range entries {
		repo := Repo{Path: entry.Path, Name: entry.Name, Enabled: entry.Enabled}
		switch {
		case entry.Missing():
			repo.Missing = true
		default:
			collectProbes(&repo, entry.Path)
		}
		repos = append(repos, repo)
	}
	return repos, nil
}

// collectProbes fills one Repo with independent daemon, inventory, and run reads.
func collectProbes(repo *Repo, repoPath string) {
	commonDir, err := git.ObtenerGitCommonDir(repoPath)
	if err != nil {
		repo.Error = fmt.Sprintf("overview: resolve git common dir for %q: %v", repoPath, err)
		return
	}
	repo.Daemon = presence.Probe(commonDir)
	snapshot, err := inventory.Inspect(repoPath)
	if err != nil {
		repo.Error = fmt.Sprintf("overview: inspect repository %q: %v", repoPath, err)
	} else {
		repo.Worktrees = visibleWorktrees(snapshot.Worktrees, commonDir)
		repo.Origin = snapshot.Origin
	}
	worktrees := repo.Worktrees
	if len(worktrees) > MaxSelectableWorktrees {
		worktrees = worktrees[:MaxSelectableWorktrees]
	}
	visiblePaths := make([]string, 0, len(worktrees))
	for _, worktree := range worktrees {
		visiblePaths = append(visiblePaths, worktree.Path)
	}
	runs, err := recentRuns(commonDir, recentRunLimit, visiblePaths)
	if err != nil {
		if repo.Error == "" { // first observed cause wins
			repo.Error = fmt.Sprintf("overview: read recent runs for %q: %v", repoPath, err)
		}
		return // Runs stays nil: never invent rows from a failed read.
	}
	if len(runs) > 0 {
		repo.Runs = runs // empty reads stay nil so runless repos compare and render as before
	}
}

// visibleWorktrees removes internal snapshot worktrees from the view.
func visibleWorktrees(worktrees []inventory.Worktree, commonDir string) []inventory.Worktree {
	var visible []inventory.Worktree
	for _, wt := range worktrees {
		if isInternalWorktree(wt.Path, commonDir) {
			continue
		}
		visible = append(visible, wt)
	}
	return visible
}
