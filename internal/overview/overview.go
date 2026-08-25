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
}

// Collect opens the registry at registryPath and builds exactly one Repo per
// entry, preserving the registry's sort order (by Path).
//
// Degradation contract — the returned error is reserved for registry-open
// failure (no registry, no overview); it is never combined with a partial
// slice. Every per-repository problem lives in that Repo's Error field:
//
//   - Entry.Missing(): Missing is flagged and both probes are skipped
//     (Worktrees nil, Origin empty, Daemon the Stopped zero value).
//   - Otherwise the git common dir is resolved first; on failure Error
//     records it while Daemon stays the zero value and Worktrees stays nil.
//   - Once the common dir resolves, the presence probe always runs (it
//     degrades to Stopped by its own contract); a subsequent inventory
//     failure records Error while keeping the probe result.
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

// collectProbes fills repo with the daemon and inventory facts of repoPath,
// recording any failure in repo.Error without ever failing the collection.
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
		return
	}
	repo.Worktrees = snapshot.Worktrees
	repo.Origin = snapshot.Origin
}
