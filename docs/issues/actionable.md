# Actionable — work that can start now

Ordered by readiness first, then by trust impact. Rationale: unblocked,
scoped work ships before work waiting on a missing measurement, and work
that undermines verification trust outranks work that only costs tokens.
One line per item states why it sits where it does.

## 1. Calibrate the OpenCode reviewer step budget with measured data

Sits first: a measured 21% of semantic reviews are being truncated before
they return a verdict, so review coverage is silently incomplete today.
The provisional budget was raised without the measurement that would size
it; this item closes that gap.

- Wrong: `defaultReviewToolCalls` (`internal/agentadapter/cli.go`) is the
  OpenCode `Steps` value — the number of model turns the restricted
  reviewer may spend. It was `8`, chosen without measurement. When the
  reviewer exhausts it, OpenCode ends the turn with `step_finish
  reason: "tool-calls"` and exit 0, having emitted only its opening
  narration. The budget is provider-conditional: the Claude branch
  intentionally ignores it (no confirmed flag caps tool calls there).
- Evidence: 2026-09-07 audit of `<git-common-dir>/vas-sentinel/executions/v1`.
  Of the 90 outcomes that record a stop reason (capture landed 2026-09-06),
  71 ended `end_turn` and 19 ended `tool-calls` — 21% truncated. Truncation
  concentrates by model: `muse-spark-1.3-contributor` 12/68 truncated,
  `glm-5.3-flash` 0/15. Commit `eaf82b2` dimension `logic` truncated twice
  (runs `703826855ea4`, `863b4c9b0b63`), the second being the wasted format
  retry, and surfaced as `missing_semantic_payload`.
- Blocked on: the distribution of steps actually consumed by reviews that
  complete. The durable store records the stop reason but not the step
  count, and it cannot be reconstructed — only the concatenated answer text
  is persisted, not the event stream. Recording it is the prerequisite.
- Closing: a step budget selected from the observed distribution of a
  completing review (headroom over the p90), or a recorded determination
  that the budget is the wrong control and truncation must be handled by
  scope reduction instead. Revisit the provisional value then.
- Note: raising the budget trades latency for coverage. Completing reviews
  observed a 54.5s median and 209.6s p90; truncated ones 38.8s median.

## 2. Cache shared audit evidence across review dimensions

Sits second: the token measurement now exists on all three adapter paths
(see the 2026-09-06 entry in `decisions.md`) — the design can be selected
with real numbers instead of guesses.

- Wrong: a five-dimension audit sends the same commit message, diff,
  allowed paths and CodeGraph context to isolated `opencode run --pure`
  invocations, each building its own snapshot and tool permissions, while
  producing short outputs.
- Evidence: former `follow-ups.md` P2 item (git history); origin is the
  FU-6 review-token investigation, 2026-09-04. The measurement prerequisite
  landed 2026-09-06 (token producers on ACP/acpx plus direct OpenCode and
  Claude).
- Closing: one immutable snapshot per audit; a stable evidence envelope
  (anti-injection rules, commit message, diff, permitted paths, CodeGraph
  context) rendered first with the dimension contract and output schema as
  suffix; provider cache reuse only within a group sharing model,
  reasoning effort and tool definitions (never across `cheap`/`normal`/
  `deep`); a stable cache key from the audited SHA if OpenCode exposes it.
- Measure input tokens, cached-token reads, latency and
  review-equivalence before selecting the design. Do not cache model
  outputs or reduce dimension coverage.

## 3. Give cost, scope and reuse a producer (FU-3)

Sits third: tokens now have producers on every adapter path, but cost,
scope and reuse still have no observable source.

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
- Blocked on: an observable source for price, scope or reuse; the token half
  of the shared note is resolved (see the 2026-09-06 entry in `decisions.md`).

## 4. Validate the acpx spawn chain on native Windows

Sits fourth: conditional work — no action while Debian is the deployment
platform.

- Question: the `npx -> node __queue-owner -> npm exec -> node <agent>-acp`
  chain (TTL queue owner) is UNCONFIRMED on native Windows.
- Evidence: former `follow-ups.md` P3 item (git history); origin is the A1
  live probes.
- Closing: a confirmation run on native Windows, or no action at all.
- Starts only if: production runs on Windows.
