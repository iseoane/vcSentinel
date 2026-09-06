# Future — deliberately postponed

Each item names the condition that brings it forward. Nothing here is
forgotten; everything here waits on something stated.

## Shared note: token observability (blocks two items)

Tokens are the missing measurement behind both FU-3 (cost attribution, in
`actionable.md`) and the shared-evidence cache (also in `actionable.md`):
no adapter reports token usage or price on the wire, identity coverage
measured 0 of 325 in the FU-9 store scan, and any value written without a
producer would be invented. Neither item can start until an adapter
reports token evidence. This is said once here and referenced from both.

## D4: remote host contract

- Postponed: mutual auth, repository/run authorization, disconnect
  ownership transfer, and trust boundaries for code, prompts, credentials
  and evidence.
- Comes forward when: a concrete operator use case exists for running
  durable runs on another machine. None exists today; the local daemon
  plus attach TUI cover current needs.
- Cheap to keep parked: the architecture is already prepared
  (transport-neutral six-op RepositoryHost, JSON envelopes, cursor replay,
  idempotency identities). Origin: roadmap D4.

## D5: complete remote control

- Postponed: remote adapter/host implementation, partition reconnect,
  hostile-network tests.
- Comes forward when: D4 has landed AND an approved threat model exists.
  The R10 dependency is already satisfied. Origin: roadmap D5.

## Admission upgrade via native sandboxes

- Postponed 2026-09-05 (reclassified from P1 to parked): codex reviews
  admitted with `mode=read-only`, claude via project sandbox settings
  including `denyRead`, opencode grant-by-design declared.
- Comes forward when: an adapter exposes a path-scoped search permission,
  or reviews start running on untrusted third-party content.
- The gap stays documented and unmitigated: the enforcement is
  adapter-native, so it can only be requested from each adapter and
  verified, not built here. Live probe 2026-08-26 showed OpenCode
  `grep`/`glob` permission rules matching search expressions rather than
  searched paths, so injected content can point them at arbitrary host
  paths; `read` is exactly confined and Claude confines `Grep`/`Glob` by
  path.

## FU-1: Spanish strings sweep in production surfaces

- Postponed by decision 2026-09-05: post-legacy production code still ships
  user-facing Spanish strings (instances recorded in
  `internal/gate/gate_durable.go`, `internal/setup/uninstall.go` and the
  validation orchestration messages), against the language policy for new
  artifacts.
- Comes forward when: a unit already opens one of the listed files, or a
  decision makes the policy externally visible.
- Coordinate with FU-2 when it moves: both touch the same files.

## FU-4: producer-seam structural debt

- Postponed by decision 2026-09-05: the T9.1b producer seam reverses the
  dependency direction (the domain engine imports `internal/acpadapter`
  and must change per provider result shape), metrics finalization crosses
  into review as a callback, `observedAgent` owns two capability descents
  that can diverge, and `internal/review/diagnostico.go` reads an agent
  CLI's unstructured terminal presentation inside the domain layer.
- Comes forward when: the unit adding a third adapter kind performs this
  restructuring as part of its own work. Restructuring now would rewrite
  the most delicate piece of the system for no observable benefit, and
  paying for it twice is the outcome the trigger exists to avoid.
