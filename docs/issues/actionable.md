# Actionable — work that can start now

Ordered by readiness first, then by trust impact. Rationale: unblocked,
scoped work ships before work waiting on a missing measurement, and work
that undermines verification trust outranks work that only costs tokens.
One line per item states why it sits where it does.

## 1. Verify whether FU-12 is already resolved

Sits first: small, and it settles a resolved-or-not question in one reading.

- Question: the FU-12 defect states `MigrarDesdeV1` has no production call
  site and the v1 writers use `gitDir`, but `AGENTS.md` describes
  `sharedReviewLedger` anchoring on the common directory.
- References, both verified 2026-09-06: `internal/store/migracion.go:58`
  defines `MigrarDesdeV1` with no production caller (callers: tests only);
  `cmd/sentinel/shared_ledger.go:33` anchors `review`, `status` and `pr`
  on the Git common directory. The writers half of the defect is fixed;
  the migration half is dead code.
- Closing: one reading that records whether the ledger anchoring closes
  the defect, and files or removes the dead migration accordingly. Do not
  resolve it by assumption here.
- Blocks: nothing.

## 2. Cache shared audit evidence across review dimensions

Sits second: designed but gated on the token measurement below — starting
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

## 3. Give cost, scope and reuse a producer (FU-3)

Sits third: blocked on the same missing measurement as item 2, with no
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

## 4. Validate the acpx spawn chain on native Windows

Sits fourth: conditional work — no action while Debian is the deployment
platform.

- Question: the `npx -> node __queue-owner -> npm exec -> node <agent>-acp`
  chain (TTL queue owner) is UNCONFIRMED on native Windows.
- Evidence: former `follow-ups.md` P3 item (git history); origin is the A1
  live probes.
- Closing: a confirmation run on native Windows, or no action at all.
- Starts only if: production runs on Windows.

## 5. Teach the gate to collect merge-commit diffs

Sits last: new, small, and every future merge replays it — the FU-5 merge
landed NEEDS_USER_REVIEW for no content reason.

- Wrong: `gate --stage pre-push` on merge `ae804a5` (parents `f0e25c4` plus
  `a6f8c9d`) collected an empty diff: no review ficha was recorded and the
  reviewer asked `Is the diff empty due to collection failure`, while
  `git diff HEAD^1 HEAD` shows the full branch content (+454/-5 across 3
  files). Exit `2` (`NEEDS_USER_REVIEW`).
- Evidence: observed 2026-09-06 landing FU-5; the first-parent range
  reviews (`91a3c3d`, `fe97ca1`, `a6f8c9d`) plus gate PASS at branch tip
  `a6f8c9d` carry the actual coverage.
- Closing: the gate collects the first-parent diff for merge commits (or
  reports the merge range explicitly), with a test pinning a two-parent
  HEAD.
- Blocks: nothing.
