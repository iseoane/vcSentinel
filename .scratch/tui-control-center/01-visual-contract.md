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

## Acceptance

- Operator approves splash, compact mascot, and dashboards (visual gate).
- `go build ./...`, `go vet ./...`, focused tests green; goldens stable.
- Staged commits within the 400-line review budget (split by work unit).

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
