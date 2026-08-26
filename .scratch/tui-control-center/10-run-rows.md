# Slice 10 — Real Run Rows in ACTIVITY

## Goal
Feed durable-run facts into the snapshot and render one ACTIVITY row
per recent run, replacing the repo-only summary rows with the operator-
approved mock semantics (RUNNING / DECISION / PASSED families) and
making the header "active" count real runs instead of dirty worktrees.

## Scope
- internal/presence: `RunSummary` gains `UpdatedAt time.Time` copied
  from the stored projection (additive; existing callers unaffected).
- internal/overview: `Repo` gains `Runs []presence.RunSummary`.
  `collectProbes` fetches up to `recentRunLimit` (package const, 3)
  via presence.RecentRuns once the common dir resolved. Degradation:
  a runs-read failure records its error in Repo.Error only when the
  field is still empty (the first observed cause wins) and leaves
  Runs nil; a repo that already degraded keeps its original error and
  no runs. Document on Collect.
- internal/tui/art: ACTIVITY renders, per repository, its summary row
  only when it carries attention/live/stopped AND additionally one row
  per run:
  - mapping: running/admitted/queued/created/terminating =>
    "⠹" blue RUNNING family word RUNNING; awaiting_decision =>
    "⏸" yellow DECISION; succeeded => "✓" green PASSED;
    failed/timed_out/unavailable => "✗" red FAILED;
    canceled/terminated => "○" dim CANCELED.
  - flow = fitRunes of the first 12 runes of RunID; stage =
    "rev <revision>"; age = compact hh:mm since UpdatedAt ("-" when
    zero). Icons inherit their state color (existing rule).
  - Repos with neither children nor runs nor degradation keep exactly
    one summary row (no invented data).
- Header semantics change (documented on overviewSummary): active now
  counts NON-terminal runs across all repositories; daemons and
  attention stay as they are. The dirty-worktrees proxy disappears.
- Plain==colored rune parity must hold; width stacking unchanged.

## Non-goals
- No operation names or worktree attribution per run (projection does
  not carry them); no actions (abort/retry arrive next slice); no
  changes to store schema; no Bubble Tea wiring changes.

## Acceptance
- Table tests: state-to-row mapping covers every LifecycleState;
  mixed repos (runs + worktrees + degraded) render both child kinds;
  header counts non-terminal runs only; runs-read failure degrades
  per contract (first-error-wins pinned).
- Existing goldens/mocks untouched; build/vet/focused/race/full suite
  green; within the 400-line budget (record bypass via orchestrator
  if indivisible).

## Review decisions
- Budget bypass granted by standing operator authorization: the three-
  package unit (projection field, snapshot plumbing, render mapping)
  is one semantic slice (556 lines); the guardian plan decision
  02dca5bafdca119a resolves as bypass.
- runState keeps its explicit state switch instead of deriving from
  TerminalClass: class granularity cannot distinguish RUNNING from
  DECISION; alignment duty documented on both functions.
- terminated counts as header-active while rendering dim CANCELED —
  domain contract honored, tension documented at isTerminalRun.
- Runs-read failures beside an earlier repo error stay swallowed by
  the first-error-wins contract; ops-level diagnostics remain out of
  scope for this slice.
- Ticket wording drift resolved in favor of the implemented replace
  rule; STOPPED-with-runs suppression now test-pinned.
