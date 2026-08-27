# Slice 4 — Runtime Facts (read-only)

## Goal
Turn one repository's git-common-dir into the runtime facts the dashboard
renders beyond git state: whether its daemon is live right now, and the
recent durable-run rows that feed the ACTIVITY pane. Read-only consumption
of existing stable APIs; no roadmap work, no writes, no connections dialed,
nothing started or stopped.

## Scope
- New package `internal/presence`.
- `Probe(gitCommonDir) Presence`: classify daemon as Live or Stopped using
  `daemon.LoadEndpoint` over `daemon.Dir(gitCommonDir)`. A missing record or
  a dead PID means Stopped — crash residue must not report Live. When live,
  surface PID, transport network/address, and StartedAt verbatim from the
  record. Probe never fails: Stopped is a valid answer; unreadable/corrupt
  records also degrade to Stopped (documented).
- PID liveness through a build-tagged platform seam (unix signal 0 /
  windows OpenProcess), mirroring the registry package's platform-file
  idiom. No new dependencies.
- `RecentRuns(gitCommonDir, limit) ([]RunSummary, error)`: open
  `store.NuevoStore(gitCommonDir)`, list via `ListExecutionIDs`, project
  each via `ReadProjection`; return at most `limit` summaries (RunID, State,
  Revision) with the ordering choice documented against real ID format.
  Store errors stay explicit.
- Managed-vs-manual labeling is NOT in this slice: that distinction belongs
  to whoever starts/stops daemons (later slice). This slice reports only
  Live/Stopped plus the raw facts.

## Non-goals
- No dialing, no handshake attempts, no daemon start/stop, no TUI wiring.
- No changes to daemon, store, execution, or any roadmap behavior.
- No $HOME scanning; callers pass explicit paths.

## Acceptance
- Tests with real temporary repositories: no-daemon => Stopped; crafted
  endpoint.json with dead pid => Stopped; live pid (self or child) => Live
  with parsed fields; corrupt endpoint.json => Stopped.
- RecentRuns: empty store => empty slice nil error; seeded runs via
  store.CreateRun+projection => listed with correct State/Revision; limit
  respected.
- go build/vet/focused tests/race/full suite green; within 400-line budget.
