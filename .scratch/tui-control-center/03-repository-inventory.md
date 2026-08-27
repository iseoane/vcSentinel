# Slice 3 — Repository Inventory (read-only)

## Goal
Turn one registered repository path into the plain facts the Control Center
dashboard renders on its left tree and LOCATION pane: worktrees, branches,
origin, and per-worktree cleanliness. Read-only over git; no daemon, no runs,
no writes anywhere.

## Scope
- New package `internal/inventory`, stdlib only.
- `Inspect(repoPath string)` returns a deterministic snapshot: repository
  name, origin URL (empty when absent), and one entry per linked worktree
  (path, branch name, detached flag, clean/dirty).
- Git access through `git worktree list --porcelain` parsed strictly;
  unknown tokens are errors, never silently ignored.
- Clean/dirty per worktree via `git status --porcelain` scoped to that
  worktree directory (non-empty output => dirty).
- Paths normalized with filepath (Abs/Clean); never string-concatenated.
- Errors explicit and wrapped with the failing path/command; partial
  snapshots are never returned silently.

## Non-goals
- No daemon discovery, no run/activity feeds (Slice 4 loads the
  durable-runs skill first).
- No TUI wiring, no config changes, no writes to any file.
- No $HOME scanning; callers pass explicit repository paths (registry owns
  discovery).

## Acceptance
- Unit tests with real temporary git repositories covering: single worktree,
  multiple worktrees, detached HEAD, dirty vs clean worktree, missing origin,
  non-repository path error, and porcelain parse strictness.
- `go build ./...`, `go vet ./...`, focused tests, full suite green.
- Reviewable within the 400-line budget.

## Rollback
Delete `internal/inventory`; nothing else depends on it yet.

## Review amendments (Slice 3)

Normative decisions adopted while closing the Slice 3 code-review findings:

- Bare main entries report `Clean=true`: with no working tree there is
  nothing to probe, so cleanliness is vacuously true for them.
- `bare` is accepted only on the first (main) porcelain block; any later
  block carrying `bare` is a parse error.
- HEAD hashes of exactly 40 or 64 lowercase hex characters are accepted,
  covering repositories created with either sha1 or sha256 object format
  (`git init --object-format=sha256`).

## Operator decision (2026-08-25)

The operator approved continuing with the cumulative slice diff (561 added
lines) under the same explicit-bypass precedent recorded for Slice 2. Every
individual commit passed the staged hook (398 PUNTO_OPTIMO, 113 PEQUENO).
