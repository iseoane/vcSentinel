# Actionable — work that can start now

Ordered by readiness first, then by trust impact. Rationale: unblocked,
scoped work ships before work waiting on a missing measurement, and work
that undermines verification trust outranks work that only costs tokens.
One line per item states why it sits where it does.

## 1. Share one review snapshot per audited commit

Sits first: unblocked, and it is the direct cost consequence of widening the
snapshot to the whole committed tree. Nothing else must land before it.

- Wrong: `reviewsnapshot.Create` is called once per PROVIDER INVOCATION, in
  `reviewWithContextResultPolicy`, with a caller-owned `defer cleanup()`.
  Every dimension, every format retry and every chained call materializes its
  own copy. The copies are bit-identical: the content is derived from the
  audited SHA, so one commit has exactly one correct snapshot.
- Evidence: one review of commit `7e41611` on 2026-09-07 issued 11 provider
  invocations, so 11 snapshots. While the snapshot held only the changed
  files this was free; now that it carries the whole tree it is ~5.6 MB and
  609 files per copy in this repository, so that review would materialize
  ~62 MB instead of 5.6 MB. `reapAbandonedSnapshots` already documents that
  472 MB of residue was measured once on a 3.8 GB tmpfs.
- Closing: a snapshot keyed by the audited SHA, created once and reused by
  every invocation auditing that commit.
- Design constraints, none of which the current per-call ownership solves:
  a lifetime owner (with `review.parallel: 5` there are up to five concurrent
  reviewers on the same SHA, so either reference counting or ownership by the
  whole review run); atomic publication (build in a temporary directory and
  rename into place under a lock, so concurrent creators neither duplicate the
  work nor observe a half-written tree); and a reaper that no longer assumes
  exclusive ownership — today it deletes by age alone and could remove a
  shared snapshot still in use.
- Related: item 3 attacks the same duplication one layer up (the evidence sent
  to the provider); this item is the on-disk half.

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
  provider default instead.
- No longer blocked on persistence, as of 2026-09-08: the consumed turn count
  now reaches the durable store as `AttemptObservation.Turns`, a `*int` that
  stays nil for every provider that reports no comparable count, so an
  unobserved count never renders as a real zero. Records written before that
  date carry no count and cannot be backfilled, because only the concatenated
  answer text was kept, not the event stream.
- Waiting on sample only: filter on completing reviews, whose mapped stop
  reason is `end_turn` (raw `stop`). Truncated runs are right-censored at
  their denial point and would bias the value down. Pre-2026-09-07 records are
  not comparable either way, because the truncations were caused by denied
  tool calls and the whole-tree snapshot removed that cause. Measured rate:
  144 completing invocations in the two days after that change, so tens of
  samples accumulate within a day of ordinary use.
- Remaining limitation, which does not block the closing condition: the
  denial evidence is still unpersisted. `TruncatedTurnError.ToolCallErrors`
  carries the observed `DeniedToolCalls`, but nothing writes it to the store,
  so a truncated run's event stream holds no record of a rejected permission
  and the store cannot attribute any truncation to a denial. Selecting the
  turn value does not need that attribution; re-testing the disproven premise
  above would.

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
