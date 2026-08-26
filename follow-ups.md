# Follow-ups

Living list of accepted follow-ups from the durable-runs units (A1/A2) and
live-probe findings, ordered by criticality and dependency. Tick items as they
land.

## P1 — Now (protect verification trust and core business)

- [ ] **Admission upgrade via native sandboxes** — codex reviews admitted with
      `mode=read-only` stamped at session admission (verified write
      containment, new exit code 5 on denied ops); claude via project sandbox
      settings incl. `denyRead`; opencode stays grant-by-design declared.
      Converts today's biggest containment gap into real enforcement for the
      dominant use case (reviews). No blocking dependencies. Origin: 16 +
      live probes.
      Live-probed instance (2026-08-26): OpenCode `grep`/`glob` permission
      rules match search expressions, not searched paths, so an injected
      prompt inside committed content can point them at arbitrary host paths;
      `read` is exactly confined (ask-fallback contract), and Claude confines
      `Grep`/`Glob` by path, so the channel is adapter-specific.

## P2 — Next (clear value, small effort)

- [x] **Make `runs attach` discoverable by help** — `attach` is missing from
      the `sentinel runs --help` subcommand list, and `runs attach --help`
      exits with a rejection although the command accepts real flags
      (`--run`, `--follow`). The ticket-15 central `-h/--help` interception
      does not reach the `runs` subcommand dispatcher. Origin: D3 / ticket 17
      follow-up found while auditing help surfaces post-A2.

All three P2 items landed in `8d7c2ed` (merged as `b4405f3`): shared
`process.ContainAfterCancellation` replaced both watchdog copies; acpx
failure/timeout details now carry a bounded stderr excerpt across all three
failure routes; `spawnHelper` simplified. See Resolved below.

## P3 — Conditional / gated

- [ ] **Windows validation of the acpx spawn chain** (`npx → node
      __queue-owner → npm exec → node <agent>-acp`, TTL queue owner) —
      UNCONFIRMED on native Windows. Blocking ONLY if production runs on
      Windows; no action needed while Debian is the deployment platform.
      Origin: A1 probes.
- [x] **Consolidate adapter-family dispatch** when a third adapter kind
      appears — kind switch currently lives in three factory sites. Trigger:
      third kind existing. Origin: 16 slice 3 review.
- [x] **Durable raw-transcript threading** — persist raw NDJSON transcripts /
      observed model / stop reason into durable run records. GATED on the
      capability-policy runtime work (requires store schema extension), which
      is roadmap-scale and not yet ticketed; revisit when that work starts.
      The enforcement declaration itself is ALREADY durably recorded via the
      `agent.enforcement` admission capability. Origin: 16 (JD-E remainder).

## P-Future — parked (maybe someday)

No current trigger or use case; revisited only if circumstances change.

- [ ] **D4: remote host contract** — mutual auth, repository/run
      authorization, disconnect ownership transfer, trust boundaries for
      code/prompts/credentials/evidence. GATED on a concrete operator use
      case for executing durable runs on another machine; no such scenario
      exists today and the local daemon + attach TUI fully cover current
      needs. Architecture is already prepared (transport-neutral six-op
      RepositoryHost, JSON envelopes, cursor replay, idempotency identities),
      so parking this costs nothing future. Origin: roadmap D4.
- [ ] **D5: complete remote control** — remote adapter/host implementation,
      partition reconnect, hostile-network tests. GATED on D4 landing AND an
      approved threat model; R10 dependency already satisfied. Origin:
      roadmap D5.

## Resolved

- [x] **P2 hygiene batch** — shared `process.ContainAfterCancellation`
      (R7 semantics, nil-ctx guard included) replaced both watchdog copies;
      acpx failure/timeout details carry a bounded stderr excerpt (last 500
      chars, all three failure routes); `spawnHelper` signature simplified.
      Reviewed for behavior preservation (watchdog verified line-for-line
      against the R7 original). Merged as `b4405f3`. Origin: 16 reviews.
- [x] **Stabilize daemon wire-parity tests under load** — fixed upstream by
      `f2aa7a4 fix(execution): retry and recover cross-check durable truth
      over stale bookkeeping`: control-op classification no longer trusts
      in-memory bookkeeping over the durable stream, so the succeeded-run /
      stale-revision races are gone. Independently verified with a flake loop
      that went 3/5 red before the fix and 0/5 after. Origin: 16 slice 3
      verification.
- [x] **codex-acp `-32000 Authentication required`** — resolved by refreshing
      the ChatGPT login (`codex login`); adapter works end-to-end including
      gating probes and native-sandbox discovery. Origin: 16 environment note.
