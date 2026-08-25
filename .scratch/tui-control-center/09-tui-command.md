# Slice 9 — `sentinel tui`: Owned Daemon Lifecycle & Program Wiring

## Goal
Ship the entry point: a top-level `sentinel tui` command that opens the
control center full-screen, starts this repository's daemon when none is
live (owning it for the session), leaves foreign daemons untouched,
stops only its own daemon on exit, and keeps the snapshot fresh.

## Scope
- internal/tui/control (tiny): the q/ctrl+c branch now also returns
  tea.Quit as its command (flag stays for tests). Update the affected
  table assertions.
- New cmd/sentinel/comandos_tui.go plus two tiny platform helpers:
  - `executeTui(out io.Writer, worktree string) int`.
  - Collect the initial snapshot via overview.Collect over
    resolveRepositoryRegistryPath(); registry-open failure prints an
    error line and exits infrastructure (5). Per-repo degradation stays
    inside the snapshot contract.
  - Daemon ownership decision for the CURRENT repository's common dir:
    endpoint record absent OR unreadable/undialable (stale residue) =>
    spawn `<selfexe> runs daemon start` detached (stdio discarded;
    unix Setsid via existing process-package conventions, Windows
    DETACHED_PROCESS + new process group), then wait bounded (<=5s)
    for the endpoint record to appear; mark owned=true. A dialable
    endpoint => foreign daemon: owned=false, never stopped here.
    Spawn or readiness failure => error line, exit infrastructure,
    TUI never opens half-owned.
  - Model: control.NewLive(initial, collect closure,
    control.DefaultRefreshInterval); program runs with alt-screen.
  - On program exit: when owned, graceful wire shutdown exactly like
    `runs daemon stop` (resolveRunsPrincipal, DialRemoteHost,
    host.Shutdown) under a bounded context (grace budget + margin);
    failures print one error line without crashing (exit success
    still, the session itself succeeded). Foreign daemons are never
    touched.
- Dispatch: register `tui` in main.go command routing with the same
  no-extra-arguments contract as every other command (exit 1 on any
  flag), plus its help entry in the English help texts.

## Non-goals
- No worktree/run-level actions yet (abort/respond/retry arrive later);
- no changes to overview/inventory/presence/registry/daemon packages;
- no AGENTS.md edits (orchestrator handles product docs).

## Acceptance
- Control quit-command tests updated and green.
- Unit-testable pieces covered with fakes/injection: ownership decision
  matrix (absent/stale/foreign/live-ours), bounded readiness wait
  (success, timeout), shutdown-on-exit invoked only when owned.
- `sentinel help` lists tui; `sentinel tui --anything` exits 1.
- build/vet/focused/race/full suite green; within the 400-line budget
  (request recorded bypass through the orchestrator if indivisible).
