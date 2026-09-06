# Actionable — work that can start now

Ordered by readiness first, then by trust impact. Rationale: unblocked,
scoped work ships before work waiting on a missing measurement, and work
that undermines verification trust outranks work that only costs tokens.
One line per item states why it sits where it does.

## 1. Cache shared audit evidence across review dimensions

Sits first: designed but gated on the token measurement below — starting
it now means designing blind on cache value.

- Wrong: a five-dimension audit sends the same commit message, diff,
  allowed paths and CodeGraph context to isolated `opencode run --pure`
  invocations, each building its own snapshot and tool permissions, while
  producing short outputs.
- Evidence: former `follow-ups.md` P2 item (git history); origin is the
  FU-6 review-token investigation, 2026-09-04.
- Closing: one immutable snapshot per audit; a stable evidence envelope
  (anti-injection rules, commit message, diff, permitted paths, CodeGraph
  context) rendered first with the dimension contract and output schema as
  suffix; provider cache reuse only within a group sharing model,
  reasoning effort and tool definitions (never across `cheap`/`normal`/
  `deep`); a stable cache key from the audited SHA if OpenCode exposes it.
- Measure input tokens, cached-token reads, latency and
  review-equivalence before selecting the design. Do not cache model
  outputs or reduce dimension coverage.
- Blocked on: the shared token-observability note in `future.md`.

## 2. Give cost, scope and reuse a producer (FU-3)

Sits second: blocked on the same missing measurement as item 1, with no
observable source in the agent path today.

- Wrong: the metrics schema declares `ExecutionCost`, `ExecutionScope`
  and `ExecutionReuse`, but all three stay nil: no adapter reports a price
  and no pricing table exists (`Provenance.Source` guards estimates); the
  only full-vs-affected decision lives in `internal/validation`
  `resolverComando`, which belongs to the deterministic gate, not to an
  agent run; `Controller.Start` rejects duplicates with
  `ErrRunAlreadyExists`, so no reuse path exists.
- Evidence: FU-3 entry in the former `docs/reingenieria/f0-deuda.md`
  (git history).
- Closing: an observable source for at least one of the three, or a
  recorded determination that none can exist (the F9 precedent for cost:
  a deliberately nil value with provenance is a determination, not a gap).
- Blocked on: the shared token-observability note in `future.md`.

## 3. Validate the acpx spawn chain on native Windows

Sits third: conditional work — no action while Debian is the deployment
platform.

- Question: the `npx -> node __queue-owner -> npm exec -> node <agent>-acp`
  chain (TTL queue owner) is UNCONFIRMED on native Windows.
- Evidence: former `follow-ups.md` P3 item (git history); origin is the A1
  live probes.
- Closing: a confirmation run on native Windows, or no action at all.
- Starts only if: production runs on Windows.
