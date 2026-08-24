# Follow-ups

Living list of accepted follow-ups from the durable-runs units (A1/A2) and
live-probe findings, ordered by criticality and dependency. Tick items as they
land.

## P1 — Now (protect verification trust and core business)

- [ ] **Stabilize daemon wire-parity tests under load**
      (`TestServerErrorVocabularyParityOverWire`,
      `TestRemoteClientRetryAndRecoverCarrySentinelIdentity`,
      `TestServerRetryAndRecoverParityOverWire`) — intermittent failures in
      full-suite runs; reproduced at base HEAD without local diffs; pass in
      isolation and `-count=3`. An unreliable suite erodes every downstream
      verification. No dependencies. Origin: 16 slice 3 verification.
- [ ] **Admission upgrade via native sandboxes** — codex reviews admitted with
      `mode=read-only` stamped at session admission (verified write
      containment, new exit code 5 on denied ops); claude via project sandbox
      settings incl. `denyRead`; opencode stays grant-by-design declared.
      Converts today's biggest containment gap into real enforcement for the
      dominant use case (reviews). No blocking dependencies. Origin: 16 +
      live probes.

## P2 — Next (clear value, small effort)

- [ ] **Surface stderr in failure details** for acpx outcomes (currently only
      the classification detail is carried) — immediate operability gain for
      the brand-new adapter. Origin: 16 slice 2 review.
- [ ] **Extract containment watchdog** (`containAfterCancellation`) into
      `internal/process` next time that file is touched — currently mirrored
      verbatim in agentadapter and acpadapter; deduplicate before the two
      copies diverge. Origin: 16 slice 2 review.
- [ ] **Simplify vestigial `spawnHelper` signature** (hardcoded nil error
      return kept for old call sites) — bundle with either P2 item above in a
      single hygiene pass. Origin: 16 slice 2 review.

## P3 — Conditional / gated

- [ ] **Windows validation of the acpx spawn chain** (`npx → node
      __queue-owner → npm exec → node <agent>-acp`, TTL queue owner) —
      UNCONFIRMED on native Windows. Blocking ONLY if production runs on
      Windows; no action needed while Debian is the deployment platform.
      Origin: A1 probes.
- [ ] **Consolidate adapter-family dispatch** when a third adapter kind
      appears — kind switch currently lives in three factory sites. Trigger:
      third kind existing. Origin: 16 slice 3 review.
- [ ] **Durable raw-transcript threading** — persist raw NDJSON transcripts /
      observed model / stop reason into durable run records. GATED on the
      capability-policy runtime work (requires store schema extension), which
      is roadmap-scale and not yet ticketed; revisit when that work starts.
      The enforcement declaration itself is ALREADY durably recorded via the
      `agent.enforcement` admission capability. Origin: 16 (JD-E remainder).

## Resolved

- [x] **codex-acp `-32000 Authentication required`** — resolved by refreshing
      the ChatGPT login (`codex login`); adapter works end-to-end including
      gating probes and native-sandbox discovery. Origin: 16 environment note.
