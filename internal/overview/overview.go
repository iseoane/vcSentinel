// Package overview fans out the Control Center's read-only sources into one
// deterministic snapshot: registry entries x git inventory x daemon presence.
// One unhealthy repository must never fail the whole view, so every per-
// repository problem is recorded in Repo.Error instead of aborting Collect.
package overview

import (
	"fmt"

	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/inventory"
	"github.com/ISeoane-Quental/vas.sentinel/internal/presence"
	"github.com/ISeoane-Quental/vas.sentinel/internal/registry"
)

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
		repo.Worktrees = snapshot.Worktrees
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
