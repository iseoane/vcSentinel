# Slice 5 — Control Center Overview (read-only aggregation)

## Goal
Combine the three existing read-only sources into one deterministic snapshot
the future dashboard renders directly: registry entries x git inventory x
daemon presence. One unhealthy repository must never fail the whole view.

## Scope
- New package `internal/overview`, depending only on internal/registry,
  internal/inventory, internal/presence, internal/git (common-dir helper),
  stdlib.
- `type Repo struct { Path, Name string; Enabled, Missing bool; Error string;
  Worktrees []inventory.Worktree; Origin string; Daemon presence.Presence }`
- `func Collect(registryPath string) ([]Repo, error)`: open the registry;
  for each entry IN REGISTRY SORT ORDER produce one Repo. Disabled entries
  are included but flagged (caller filters). Entry.Missing() true =>
  Missing=true with no probes. Otherwise run inventory.Inspect; on failure
  record Error and still fill Daemon via presence.Probe over the resolved
  common dir (probe degrades to Stopped by design). Daemon probe uses
  git.ObtenerGitCommonDir(repoPath); if even that fails, Error notes it and
  Daemon stays Stopped zero-value.
- Registry open failure IS a hard error (no registry, no overview).
- No writes, no dialing, nothing started; pure fan-out of existing readers.

## Non-goals
- No TUI wiring, no managed/manual labeling, no refresh loop/caching.
- No changes to the four consumed packages.

## Acceptance
- Tests with a temp registry + real temp repositories covering: healthy
  repo fully populated; missing directory flagged without probes; repo with
  valid registration but broken git payload yields Error plus Stopped
  daemon without failing Collect; disabled entry included and flagged;
  ordering matches registry sort; corrupt registry path errors explicitly.
- build/vet/focused/race/full suite green; within the 400-line budget.
