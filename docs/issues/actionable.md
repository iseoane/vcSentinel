# Actionable — work that can start now

Ordered by readiness first, then by trust impact. Rationale: unblocked,
scoped work ships before work waiting on a missing measurement, and work
that undermines verification trust outranks work that only costs tokens.
One line per item states why it sits where it does.

## 1. Decide whether a PR still needs its per-commit audits

Sits first: unblocked, and it decides how much every future review costs.

- Wrong: `pr review` audits every commit without a record AND then runs the
  net audit. The same code reaches the reviewer twice.
- Not a bug, and NOT the gap it first looks like: `runNetReview`
  (`internal/review/net_pr.go:174`) already calls `PlanForProfile` on the NET
  diff, so the whole picture is planned from its own aggregate risk, not from
  the union of the per-commit plans. The two audits ask genuinely different
  questions of the same code — "is this commit sound alone?" and "is the whole
  coherent?" — and the prompt is relabelled accordingly.
- Evidence: reviewing this branch on 2026-09-08 audited 5 commits plus the net
  diff. The per-commit half only became expensive because the commits were
  created without reviewing as they went, so `pr review` paid the whole
  accumulated bill at once instead of finding records already there.
- The decision: keep auditing unaudited commits inside `pr review`, or let the
  net verdict stand alone and leave per-commit records as optional work for
  whoever reviews incrementally.
  - For keeping them: they attribute a finding to one commit, they are what
    `refute` and `accept` operate on, and they survive rebases through the blob
    index. They are also the reviewable-unit discipline the guardian exists to
    enforce.
  - For dropping them: the net verdict is what gates, per-commit records do not
    change it, and paying for both is the largest single cost in a review.
- Closing: a recorded determination either way, and — if they stay — a way for
  the per-commit half not to be silently deferred until PR time, since that is
  what makes the cost feel like a defect.

## 2. Recalibrate or retire the OpenCode reviewer turn budget

Sits second: unblocked but low value, and its original premise was disproven.

- Wrong: `defaultReviewToolCalls` (`internal/agentadapter/cli.go`) is the
  OpenCode `Steps` value — the number of model turns the restricted reviewer
  may spend. It was `8`, chosen without measurement, and was raised to a
  PROVISIONAL `16` that is equally unmeasured. The budget is
  provider-conditional: the Claude branch intentionally ignores it (no
  confirmed flag caps turns there).
- Disproven premise: the raise was made believing budget exhaustion caused the
  truncated reviews. It did not. A denied tool call kills the turn. Captured
  raw NDJSON from one review on 2026-09-07 showed 11 invocations: the 3 that
  recorded a permission rejection all ended `tool-calls` (truncated at 4 and 5
  turns out of 16), and the 8 with no rejection all ended `stop`. The
  correlation was exact, and the budget was never approached.
- Evidence for the original 21% figure, now explained by denials rather than
  by the budget: of the 90 durable outcomes recording a stop reason (capture
  landed 2026-09-06), 19 ended `tool-calls`.
- Closing: either a value selected from the observed distribution of turns
  consumed by completing reviews, or a recorded determination that the turn
  budget is not a useful control and the constant should hold a documented
  provider default instead. Both need the per-review turn count, which is now
  counted but not yet persisted.
- Blocked on: persisting the consumed turn count in the durable store. It
  cannot be reconstructed from existing records — only the concatenated
  answer text is kept, not the event stream.

## 3. Cache shared audit evidence across review dimensions

Sits third: the token measurement now exists on all three adapter paths
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

## 4. Give cost, scope and reuse a producer (FU-3)

Sits fourth: tokens now have producers on every adapter path, but cost,
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

## 5. Validate the acpx spawn chain on native Windows

Sits fifth: conditional work — no action while Debian is the deployment
platform.

- Question: the `npx -> node __queue-owner -> npm exec -> node <agent>-acp`
  chain (TTL queue owner) is UNCONFIRMED on native Windows.
- Evidence: former `follow-ups.md` P3 item (git history); origin is the A1
  live probes.
- Closing: a confirmation run on native Windows, or no action at all.
- Starts only if: production runs on Windows.
