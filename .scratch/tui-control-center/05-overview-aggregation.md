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
  Missing=true with no probes. Otherwise the probe order is common-dir
  FIRST (git.ObtenerGitCommonDir), then presence.Probe(commonDir) ALWAYS,
  then inventory.Inspect independently: a failed common-dir records Error
  with Stopped daemon and skips Inspect entirely; a failed Inspect after a
  successful probe keeps Daemon and records Error only. (Amended after
  review: the original prose read inspect-first; probe-first is binding.)
- Registry open failure IS a hard error for corrupt JSON or a directory at
  the registry path; an ABSENT file inherits registry.Open's empty-registry
  semantics (empty snapshot, nil error).
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

## Review amendments (Slice 5)

- Probe-first degradation ladder is normative (Scope amended above).
- Commit 1f49fed touched consumed package internal/presence comment-only to
  document pid-reuse residual risk symmetrically on both platforms;
  accepted as a docs exception to the "no changes to consumed packages"
  rule.
- Test helpers mirror the inventory test files per established per-package
  Go practice; extract a shared internal testutil if a third copy appears.
