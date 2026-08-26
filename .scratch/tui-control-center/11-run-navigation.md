# Slice 11 — Dual-Pane Focus, Run Navigation & Help

## Goal
Make ACTIVITY navigable: TAB switches focus between the repository tree
and the run list, j/k move the focused cursor, enter expands one run's
detail inline, and ? renders the help overlay. Pure model+render: no
exec, no daemon calls (actions arrive next slice).

## Scope
- internal/tui/art: replace the growing positional signature with one
  render-state value (kills the flagged data clump):
  `RenderOverview(width int, s ViewState) string` (+ Plain variant)
  where `ViewState{ Repos []overview.Repo; RepoCursor int; Focus
  FocusPane; RunCursor RunPos; OpenRunID string; Help bool }`,
  `FocusPane` enum {FocusTree, FocusRuns}, `RunPos{Repo, Run int}`.
  - Focused-pane affordance: the focused pane's heading renders with a
    marker ("▸ REPOSITORIES" purple vs dim "REPOSITORIES"); unfocused
    stays exactly as today.
  - The focused run row prefixes "▸" (purple) over its icon column;
    the focused repo row prefixes "▸" replacing its "▾/▸" marker glyph
    position semantics stay: expanded repos keep "▾".
  - enter semantics live in the MODEL; art only renders OpenRunID: the
    matching run row appends two indented detail lines (full RunID,
    state word, revision, UpdatedAt UTC, age) inside the ACTIVITY pane,
    dim, prefixed "└─". Unknown/mismatched OpenRunID renders nothing
    extra.
  - Help=true replaces the RIGHT pane content with a KEYS block
    (approved double rule separates it): each footer key plus a one-
    line English description including the new keys. Frame/footer
    unchanged; parity must hold.
- internal/tui/control Model gains: focus FocusPane, runCursor RunPos,
  openRunID string, help bool (+ accessors).
  - tab/shift+tab switch focus (clamped no-op on empty registries);
  - in FocusTree: up/down/j/k move repo cursor (existing clamps);
    enter toggles nothing visible yet EXCEPT moving focus to runs;
  - in FocusRuns: up/down/j/k walk every visible run row in render
    order (repo by repo); enter sets/clears openRunID (toggle on the
    same run, move clears it); empty run list is a clamped no-op;
  - "?" toggles help; any other key while help=true closes help first
    (except q/ctrl+c which quit as always).
- cmd/sentinel/comandos_tui.go: initial ViewState zero values; nothing
  else changes.

## Non-goals
- No abort/retry wiring, no daemon dials from the model, no mouse, no
  filter ("/" stays decorative until a later slice), worktree-level
  selection unchanged.

## Acceptance
- Table tests: focus switching incl. empty registry; run cursor walks
  render-order across repos and clamps; enter toggle sets/clears/
  moves-clear openRunID; help toggle and close-on-any-key; q still
  quits from any focus/help state.
- Art tests: focus markers, ▸ prefix only on focused item, detail
  block content for exact OpenRunID, help block lists all footer keys,
  plain==colored parity across fixtures x widths, stacking below 84.
- Existing call sites updated; build/vet/focused/race/full green;
  budget bypass expected (record via orchestrator if indivisible).

## Review decisions
- Budget bypass granted by standing operator authorization: render
  contract change plus model navigation plus their parity/table tests
  are one unit (848 lines).
- Spec amendment: the open-run detail renders ONE indented dim line
  carrying every enumerated field instead of two lines; tests pin the
  single-line shape.
- helpLines now reuses rule() and a shared keyHint type backs footer
  and KEYS overlay; "/" filter row added to help with "(soon)" marker.
- runAge helper extracted (was duplicated in runRow/runDetail).
- Zero-value ViewState intentionally differs from pre-slice bytes by
  the tree-focus affordance (purple heading marker + cursor glyph):
  forced by the spec's own initial state, not drift.
- VisibleRuns ordering and overviewActivity layout remain two
  traversals over one documented order, pinned by TestVisibleRuns-
  RenderOrder; snapshot refresh re-anchors an out-of-range run cursor
  from the top by contract.
