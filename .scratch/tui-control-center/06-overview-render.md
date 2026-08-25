# Slice 6 — Real-Data Dashboard Render (pure functions)

## Goal
Render a real internal/overview snapshot through the operator-approved
Slice 1 layout engine, replacing mock-only rendering for production use
while keeping the approved mock goldens untouched as the visual contract.

## Scope
- Extend internal/tui/art (same painter/rules/keys infrastructure).
- New entry point `RenderOverview(width int, repos []overview.Repo) string`
  plus a plain variant, producing the SAME approved structure: header with
  daemon summary computed from the snapshot (live daemons, active runs,
  attention = repos with Error or Missing), left tree pane listing each
  repo and its worktrees, right pane LOCATION of the first/selected repo
  and ACTIVITY rows derived from presence data available per repo.
- State mapping follows the approved semantics: repo with Error or Missing
  => attention/yellow; Daemon.Live => managed-style rose; stopped => red;
  clean worktree => dim; dirty worktree => blue. Activity icons inherit
  their state color.
- Degraded repos render their one-line Error in place of worktree children.
- Empty registry renders the empty-state line inside the tree pane; frame,
  rules, and footer stay identical to the contract.
- Rune-column discipline: every promised column uses fitRunes; plain and
  colored variants rune-identical (tested).
- The Slice 1 mock functions, goldens, and tuipreview stay working
  unchanged.

## Non-goals
- No Bubble Tea model, no key handling, no refresh loop (next slice).
- No changes to overview/inventory/presence/registry packages.
- No visual redesign: layout, colors, separators, footer keys are frozen by
  the Slice 1 contract.

## Acceptance
- Table tests over constructed []overview.Repo covering: healthy single
  repo, multi-worktree repo, degraded repo with Error, missing dir repo,
  live daemon vs stopped, empty overview, width stacking below 84 columns.
- Plain==colored rune parity asserted; all sections present at 84+ widths.
- Existing art goldens unchanged and green; build/vet/focused/race/full
  suite green; within the 400-line budget.
