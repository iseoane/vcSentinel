# Slice 8 — Live Refresh Loop (snapshot collector)

## Goal
Make the control-center model live: on an interval it re-collects the
overview snapshot so daemon liveness, worktree states, and attention
flags track reality without restarting the TUI.

## Scope
- Extend `internal/tui/control` only:
  - `New` keeps its signature and stays static (no refresh): existing
    callers and tests must not change behavior.
  - New `NewLive(repos []overview.Repo, refresh func() ([]overview.Repo,
    error), interval time.Duration) Model`: enables the loop. `refresh`
    must be non-nil whenever the loop is enabled; document that calling
    `NewLive` with nil refresh panics by contract.
  - `Init` returns the first tick command when the loop is enabled,
    nil otherwise.
  - A private `tickMsg` drives the cycle: on receipt the model issues
    exactly one refresh command (`refreshSnapshot`) and reschedules
    the next tick. The refresh runs in the returned command (off the
    UI thread); its result arrives as a private `snapshotMsg` carrying
    `[]overview.Repo` plus an optional error.
  - `snapshotMsg` with repos replaces the model's repositories and
    clamps the selection back into range; with an error the model keeps
    the last good snapshot and records the error (accessor `Err()
    string`, empty when healthy). Rendering never invents data on
    error: the view keeps showing the last good snapshot unchanged.
  - Scheduling goes through a model field (default `tea.Tick`) so tests
    can drive ticks deterministically without sleeping.
- Non-goals: no daemon lifecycle, no exec besides the injected
  refresh closure, no rendering changes, no new dependencies, no
  changes outside internal/tui/control.

## Acceptance
- Table/direct tests: New stays static (Init nil, no tick reaction);
  NewLive schedules on Init; one tick produces exactly one refresh
  call and one reschedule; successful snapshot replaces repos and
  clamps selection; failing refresh keeps prior repos and surfaces
  Err(); unknown messages still ignored.
- No wall-clock sleeps in tests; build/vet/focused/race/full suite
  green; within the 400-line budget.
