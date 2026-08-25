# T1 — Visual contract: guardian pixel art + dashboard mockups

Status: in progress
Branch: feat/tui-control-center
Worktree: /home/iseoane/0-workspace/vas.sentinel-worktrees/tui-control-center
Base: 37fc94b (main)

## Goal

Produce the complete visual contract for the Sentinel Control Center TUI and
obtain explicit operator approval before any infrastructure work starts.

## Scope

- `internal/tui/art`: pure rendering package — palette, guardian pixel grids
  (splash + compact), half-block ANSI renderer, mono degradation, HTML
  preview writer, state recoloring (eyes + palm core react independently).
- `tools/tuipreview`: preview command printing every state and the mock
  dashboards (80/100/140 columns), with `-html` output for real-color review.
- Deterministic goldens for plain renders; tests pin grid invariants, state
  scoping, and mono/ANSI contracts.

## Out of scope

- No CLI wiring, no registry, no catalog, no daemon supervision.
- No changes outside the worktree; main and reengineering artifacts untouched.

## Acceptance (amended after operator review)

- Operator approves the dashboard contract (the pixel-art splash and
  compact mascot were REJECTED and removed; text-first dashboard only).
- `go build ./...`, `go vet ./...`, focused tests green; goldens stable.
- Staged commits within the 400-line review budget (split by work unit).
- Review mechanism: sentinel review ran for this slice; subsequent slices
  use the code-review skill until the operator fixes the sentinel review
  defects recorded above.

## Rollback

Delete `internal/tui/art` and `tools/tuipreview`; nothing else references them.

## Evidence (implementation)

- Commits: f0f5b7d (palette/states), c14a59b (renderer), b755a07 (artwork),
  7a31535 (dashboards + tuipreview). All within the staged review budget.
- Verification: gofmt clean; go build ./... ok; go vet ./... ok;
  go test ./internal/tui/art/ ./tools/... -count=1 ok;
  go test ./internal/tui/art/ -count=1 -race ok; full go test ./... 0 FAIL.
- Guardian: staged checks passed per commit (144/286/226/313 authored lines).
- HTML proof for the operator visual gate: generated via
  `go run ./tools/tuipreview -html <path>` (all states + dashboards).
- Pending: operator visual approval; independent code-review + Judgment Day
  run after the visual gate to avoid re-reviewing rejected artwork.

## Evidence (redesign round, operator feedback 2026-08-24)

- Operator rejected the pixel-art guardian and the flat dashboard; new
  contract: header panel, tree pane, LOCATION + ACTIVITY right pane, double
  separators, colored states.
- Commits: 7f9bc8d (drop pixel art), cd9f55f (ANSI styling), 3df8b1a
  (dashboard redesign + preview). All within budget; suite green incl. race.
- HTML proof regenerated via tuipreview -html.
- Pending: operator visual approval of the redesign; then code-review + JD.

## Review round 2 (sentinel review, then paused)

- sentinel review of 3df8b1a found: spec CRITICAL (doc comment promised an
  outer box frame the renderer never draws), spec WARNING (state columns not
  aligned for long names), design WARNING (renderDashboard mutated
  package-global color state), security WARNING (mock embedded operator
  identity). All four fixed in 655802d; design re-review verdict ok.
- Re-review logic WARNING (SetColors removal breaking callers) verified
  INERT: zero callers module-wide, build and tests green.
- OPERATOR DIRECTIVE: sentinel reviews are PAUSED until the operator fixes
  the defects discovered while running them. Until then slice verification
  uses the code-review skill. Defects discovered (for the operator):
  1. A review killed mid-run leaves durable runs stuck in class
     operator_required; `runs recover --repair` refuses the class and
     `runs abort` is accepted but inert (state stays running) — no CLI
     surface settles them.
  2. Review ledger records are written to the LINKED WORKTREE's git dir
     (.git/worktrees/<name>/vas-sentinel/) instead of the git COMMON dir,
     so records split across worktrees and main-side listing/prune cannot
     see them.
  3. `sentinel review --json` still emitted the human-readable summary; no
     machine-readable document was produced.
  4. Three orphaned operator_required runs remain in the shared store as
     known residue: 16eee5c5…, c7bd62ca…, e98dbc89….
