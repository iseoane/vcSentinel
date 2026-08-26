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

// snapshotsBaseSuffix is the sentinel plumbing area relative to the git
// common dir: worktrees created there are internal bookkeeping (validation
// snapshots), never operator-visible state.
const snapshotsBaseSuffix = "/vas-sentinel/snapshots"

// isInternalWorktree reports whether worktreePath lives inside the sentinel
// snapshot area under commonDir, i.e. equals or nests below
// <commonDir>/vas-sentinel/snapshots.
//
// Both inputs are slash-normalized with filepath.ToSlash before comparing:
// inventory paths arrive native-cleaned per host OS (internal/inventory
// converts git's slash output back to native), while commonDir comes from
// the git plumbing in whatever form the platform produces. Trailing slashes
// on either side are accepted. On Windows the final comparison folds case,
// because registry entries, git-resolved paths, and mapped drives disagree
// about casing there; on case-sensitive platforms it stays exact so two
// genuinely distinct directories can never collide.
func isInternalWorktree(worktreePath, commonDir string) bool {
	base := strings.TrimRight(filepath.ToSlash(commonDir), "/") + snapshotsBaseSuffix
	path := strings.TrimRight(filepath.ToSlash(worktreePath), "/")
	if runtime.GOOS == "windows" {
		return strings.EqualFold(path, base) || strings.HasPrefix(strings.ToLower(path), strings.ToLower(base)+"/")
	}
	return path == base || strings.HasPrefix(path, base+"/")
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

// recentRunLimit bounds how many durable-run summaries each repository
// snapshot carries; the activity pane renders one row per summary.
const recentRunLimit = 3

// recentRuns is the RecentRuns seam: a package-level function variable so
// Collect tests can stub the store-backed read without building real
// execution stores. Production code never reassigns it.
var recentRuns = presence.RecentRuns

// Collect opens the registry at registryPath and builds exactly one Repo per
// entry, preserving the registry's sort order (by Path).
//
// Degradation contract — the returned error is reserved for registry-open
// failure (no registry, no overview); it is never combined with a partial
// slice. Every per-repository problem lives in that Repo's Error field:
//
//   - Entry.Missing(): Missing is flagged and every probe is skipped
//     (Worktrees nil, Origin empty, Daemon the Stopped zero value, Runs nil).
//   - Otherwise the git common dir is resolved first; on failure Error
//     records it while Daemon stays the zero value and Worktrees and Runs
//     stay nil.
//   - Once the common dir resolves, the presence probe always runs (it
//     degrades to Stopped by its own contract); an inventory failure records
//     Error while keeping the probe result.
//   - A successful inventory keeps only operator-visible worktrees: internal
//     sentinel plumbing under <commonDir>/vas-sentinel/snapshots (validation
//     snapshot worktrees) is filtered out before it reaches Repo.Worktrees,
//     so every derived count (tree children, LOCATION status tallies) sees
//     the same visible set.
//   - The recent-runs read still executes afterwards: a successful read
//     stores up to recentRunLimit summaries in Runs (an empty store keeps
//     Runs nil); a runs-read failure records its cause in Error only when
//     the field is still empty (the first observed cause wins) and also
//     leaves Runs nil.
//
// Disabled entries are included but flagged; callers filter. There is no
// caching and nothing is written, dialed, or started.
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

// collectProbes fills repo with the daemon, inventory, and durable-run facts
// of repoPath, recording any failure in repo.Error without ever failing the
// collection. The probes are independent reads over the resolved common dir:
// an early failure keeps its recorded cause because every later failure only
// writes when Error is still empty.
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
	runs, err := recentRuns(commonDir, recentRunLimit)
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

// visibleWorktrees filters the inventory's worktrees down to the
// operator-visible set: internal sentinel plumbing (snapshot worktrees under
// the common dir's vas-sentinel/snapshots area) never reaches the snapshot
// view, keeping tree children and every derived count honest. A result with
// no survivors stays nil so empty views keep comparing and rendering as nil.
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
