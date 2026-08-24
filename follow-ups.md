# Follow-ups

Living list of accepted follow-ups from the durable-runs units (A1/A2) and
live-probe findings, kept visible at the repository root. Each entry names its
origin ticket. Tick items as they land.

## Adapter & enforcement

- [ ] **Durable raw-transcript threading** — persist raw NDJSON transcripts /
      observed model / stop reason into durable run records. Gated on the
      capability-policy runtime work (requires store schema extension).
      Origin: 16 (JD-E remainder, documented boundary).
- [ ] **Admission upgrade via native sandboxes** — codex reviews admitted with
      `mode=read-only` stamped at session admission (verified write
      containment); claude via project sandbox settings incl. `denyRead`;
      opencode stays grant-by-design declared. Origin: 16 + live probes.
- [ ] **Extract containment watchdog** (`containAfterCancellation`) into
      `internal/process` next time that file is touched — currently mirrored
      verbatim in agentadapter and acpadapter. Origin: 16 slice 2 review.
- [ ] **Surface stderr in failure details** for acpx outcomes (currently only
      classification detail is carried). Origin: 16 slice 2 review.
- [ ] **Consolidate adapter-family dispatch** when a third adapter kind
      appears — kind switch currently lives in three factory sites.
      Origin: 16 slice 3 review.
- [x] **codex-acp `-32000 Authentication required`** — resolved by refreshing
      the ChatGPT login (`codex login`); adapter works end-to-end including
      gating probes. Origin: 16 environment note.
- [ ] **Windows validation of the acpx spawn chain** (`npx → node
      __queue-owner → npm exec → node <agent>-acp`, TTL queue owner) —
      UNCONFIRMED on native Windows; cross-platform mandate requires a
      validation pass before relying on it there. Origin: A1 probes.

## Testing & infrastructure

- [ ] **Stabilize daemon wire-parity tests under load**
      (`TestServerErrorVocabularyParityOverWire`,
      `TestRemoteClientRetryAndRecoverCarrySentinelIdentity`,
      `TestServerRetryAndRecoverParityOverWire`) — intermittent failures in
      full-suite runs; reproduced at base HEAD without local diffs; pass in
      isolation and `-count=3`. Origin: 16 slice 3 verification.
- [ ] **Simplify vestigial `spawnHelper` signature** (hardcoded nil error
      return kept for old call sites). Origin: 16 slice 2 review.
