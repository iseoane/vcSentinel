# Slice 7 — Control-Center Model & Navigation (pure Bubble Tea model)

## Goal
Introduce the interactive control-center model in a new
`internal/tui/control` package: it renders real snapshots through the
approved layout engine and navigates repositories with the keyboard,
replacing the hardcoded "first repository" right pane with the
operator-selected one.

## Scope
- Extend `internal/tui/art`: `RenderOverview` and `RenderOverviewPlain`
  gain a selected-index parameter (clamped; negative means none). The
  right pane LOCATION/ACTIVITY render the selected repository instead of
  always `repos[0]`. Mock functions, goldens, and tuipreview stay
  untouched. Plain==colored rune parity must still hold.
- New `internal/tui/control` package (Bubble Tea model, same testing
  style as `internal/tui`: drive `Update` directly, no teatest):
  - `New(repos []overview.Repo) Model` plus accessors for tests.
  - `Update` handles: `tea.WindowSizeMsg` (width, floor 40),
    `up`/`k`, `down`/`j` moving the repository cursor with clamping,
    `q`/`ctrl+c` quitting, everything else ignored.
  - `View` renders `art.RenderOverview(width, repos, selected)`;
    before any window-size message it renders at a documented default
    width (80).
  - No refresh loop, no daemon lifecycle, no exec, no time ticks
    (later slices).
- Non-goals: no changes to overview/inventory/presence/registry
  packages; no new dependencies; mock renderer untouched.

## Acceptance
- Table tests over `Update`: cursor clamps at both ends, unknown keys
  are no-ops, quit flags observed via accessor, window size updates
  width.
- View tests: selected repo drives LOCATION Repository/Path lines;
  empty registry renders the approved empty state; width default 80
  pre-message.
- Existing art tests updated for the new signature and still green;
  build/vet/focused/race/full suite green; within the 400-line budget.

## Review decisions
- Budget bypass granted by standing operator authorization: the control
  package is one indivisible unit (model 95 + tests 313 = 408 lines);
  splitting it would break slice isolation.
- Test helpers `locationBlock`/`stripANSI` stay duplicated between the
  art and control test packages: extracting a shared test-support
  package for ~20 lines of fixture code trades slice isolation for DRY
  and was rejected as speculative generality at this size.
- `apply` renamed to `pressKeys`; default-width View coverage added
  (max line width <= DefaultWidth) per spec review.
