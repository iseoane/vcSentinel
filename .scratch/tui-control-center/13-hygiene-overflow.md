# Slice 13 — Snapshot Hygiene & Tree Overflow Control

## Goal
Make the control center usable against real repositories like
vas.sentinel (76 worktrees): internal snapshot worktrees disappear from
the view, oversized trees collapse behind an explicit "+N more" line,
and snapshot worktrees stop accumulating forever because creation now
sweeps stale ones.

## Scope
A) internal/overview — hide internal plumbing:
   - `collectProbes` filters `snapshot.Worktrees`: any worktree whose
     path lives under `<commonDir>/vas-sentinel/snapshots` is dropped
     before it reaches Repo.Worktrees. Exported helper
     `IsInternalWorktree(worktreePath, commonDir string) bool` owns the
     rule (slash-normalized prefix comparison; documented that paths
     come from inventory already slash-normalized). Counts everywhere
     (tree children, LOCATION Status counts, dirty/clean tallies)
     therefore reflect only operator-visible worktrees. Document on
     Collect.
B) internal/tui/art — overflow control:
   - Package const `maxTreeChildren = 12`: a repository renders at most
     that many worktree child lines; further children collapse into one
     final dim line `└─ … N more`. The degradation Error line is never
     counted nor collapsed. Plain==colored parity must hold; widths and
     stacking unchanged.
C) internal/git — wire the existing purge:
   - `CrearSnapshot` performs a best-effort sweep of snapshot entries
     older than new const `snapshotRetention = 24 * time.Hour` after a
     successful publish, inside the existing `snapshotMu`, reusing
     `purgarSnapshotsLocked` logic (refactor the body of
     PurgarSnapshots into an unexported locked helper both call);
     errors are ignored by design (documented: hygiene, not
     correctness). Public PurgarSnapshots behavior unchanged.

## Non-goals
- No scrolling/viewport machinery; no changes to inventory parsing; no
  deletion of non-snapshot worktrees; no UI for pending action feedback.

## Acceptance
- overview table tests: mixed fixture with real + snapshot-path
  worktrees keeps only real ones across every count surface; helper
  unit-tested incl. trailing-slash and nested subpaths.
- art tests: exactly maxTreeChildren children + one "... N more" line;
  N correct; repo with fewer children unchanged; degraded repo exempt;
  parity pinned.
- git tests: CrearSnapshot sweeps a seed entry whose ModTime is older
  than retention (os.Chtimes) and keeps fresh ones; concurrent create
  still safe (existing race test stays green); PurgarSnapshots public
  contract unchanged.
- build/vet/focused/race/full green; bypass recorded if indivisible.

## Review decisions
- IsInternalWorktree exported per spec, then unexported on review:
  nothing outside the package consumes it. Windows now folds path case
  via runtime.GOOS (registry/git/mapped-drive casing disagreements);
  case-sensitive platforms stay exact.
- Spec parenthetical corrected: inventory paths are native-cleaned,
  not slash-normalized; the helper normalizes both inputs itself.
- maxTreeChildren comment reworded: with plumbing hidden, the cap is
  defense in depth for operator repos with many legitimate worktrees.
- CrearSnapshot early returns (cache hit, race loser) skip the sweep
  by spec wording ("after a successful publish"); repeated requests
  for an existing tree never trigger hygiene — accepted.
- Sweep re-resolving directorioSnapshots inside the locked helper is
  accepted (same cwd contract).
