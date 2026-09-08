# Actionable — work that can start now

Ordered by readiness first, then by trust impact. Rationale: unblocked,
scoped work ships before work waiting on a missing measurement, and work
that undermines verification trust outranks work that only costs tokens.
One line per item states why it sits where it does.

## 1. Move provider isolation out of the evidence snapshot

Sits first: unblocked, and it is the reason the shared snapshot does not yet
deliver the reuse it was built for. Highest trust impact on this list.

- Wrong: `reviewEnvironment` (`internal/agentadapter/cli.go:523`) sets
  `isolationRoot := snapshot` and exports `HOME`, `OPENCODE_TEST_HOME` and
  every `XDG_*` path underneath the published snapshot. The audited evidence
  tree and the provider's writable home are the same directory. That is why
  its directories are 0700, and it is why sealing them read-only broke every
  review with `EACCES: permission denied, mkdir '<snapshot>/.local'`.
- Evidence, three independent consequences observed on 2026-09-08: the
  provider writes `.cache` and `.config` into the published tree, which
  `publishedSnapshotUsable` (store.go:227-239) then rejects as unexpected
  entries, so the next lease for that SHA discards and rematerializes instead
  of reusing — defeating the dedup item 1 landed for; concurrent dimensions
  auditing one SHA now share one home, and a review failed with the provider's
  own `CREATE TABLE workspace` SQLite error, consistent with concurrent
  migrations racing in that shared state; and Sentinel raised the coupling
  itself as a WARNING at confidence 0.9 on both the design and logic
  dimensions.
- Closing: a writable per-invocation isolation root distinct from the
  read-only evidence tree, so the snapshot carries only audited content and a
  lease can be reused as designed. Spans `internal/agentadapter` and
  `internal/reviewsnapshot`.
- Related: it also closes the tampering finding that motivated the reverted
  sealing attempt — concurrent reviewers can currently add, rename or delete
  evidence another reviewer is reading.

## 2. Bound the total size of the review snapshot store

Sits second: unblocked and small, but it only bites under unusual load.

- Wrong: the store under `os.TempDir()/vas-sentinel-snapshots-<uid>` is
  reaped by staleness alone. Nothing bounds its total size, and one committed
  tree of this repository is 625 files and 5.8 MB.
- Evidence: `sentinel review HEAD --all` audits every commit lacking a record
  — 871 here — which needs about 5.0 GB against a 3.8 GB tmpfs. It filled
  `/tmp` to 100% on 2026-09-08 and the remaining dimensions failed with
  `no space left on device`. The immediate cause was the wrong flag, but a
  store with no ceiling on a tmpfs is one bad invocation away from filling
  the disk.
- The threshold is also wrong, decided 2026-09-08: `staleSnapshotAge` is 24
  hours, but the window in which retention buys anything is minutes — one
  review's five dimensions — and at most an hour for format retries and
  chained reviews of the same commit. Twenty-four hours buys almost no extra
  reuse while multiplying the worst-case residue by every commit touched in a
  day. Measured that afternoon: 50 published trees and 446 MB, none older
  than an hour, so the reaper correctly refused to remove any of it while
  /tmp sat at 94%.
- Closing: both sides of the hole. A total-size ceiling that evicts the least
  recently leased unleased tree, AND `staleSnapshotAge` lowered to one hour.
  Either alone leaves the other axis unbounded.
- Note: today the store pays the retention cost without earning the reuse,
  because provider state contaminates each tree and forces rematerialization.
  Item 2 is what makes retention worth anything, so land it first and expect
  the tree count to fall to one per audited commit.

## 3. Decide whether the gate's review leaves a record

Sits third: unblocked, found by exercising the full pre-push flow, and the
cost it creates is already measured.

- Wrong, or possibly deliberate: `gate --stage pre-push` runs a semantic
  review of `HEAD` and prints its verdict, but writes no review record. On
  2026-09-08 it reported `Review of 19a5b2f6: warn` across three dimensions
  and no ficha exists for that SHA in the shared ledger (618 records at the
  time), nor in the worktree's per-checkout ledger, which holds only an
  `events.jsonl`. `status` does not list it, and it was not findable by SHA in
  the durable store either.
- Evidence of the cost, corrected after actually running the flow: `pr review`
  does NOT re-audit those commits. It audits the net diff and REPORTS the
  commits lacking a record, which is the behaviour commit 6a43b4a deliberately
  introduced. So the cost is not a duplicated audit; it is that the gate's
  warnings survive only in terminal output, and that `pr review` then labels a
  commit the gate did review as having no record and recommends auditing it by
  name. An operator who follows that recommendation pays for the same audit
  twice, and one who does not is left with no record of what the gate warned.
- Note it may be intended: `AGENTS.md` names `review`, `status` and `pr` as
  the commands that anchor the shared ledger, and pointedly not `gate`. The
  gate is a lifecycle gate rather than an audit of record.
- Closing: either the gate's review persists a record the rest of the system
  can see — so `pr review` reuses it instead of paying twice, and its warnings
  outlive the terminal — or a recorded determination that the gate
  deliberately keeps none, with the double-audit cost accepted in writing.

## 4. Bound the provider's own temporary residue

Sits fourth: unblocked and mechanical, but the residue is the provider's, not
this repository's, so the fix can only be to clean it, not to prevent it.

- Wrong: each restricted reviewer invocation leaves a roughly 14 MB
  `/tmp/.<hex>-00000000.so` file behind, written by the Bun runtime OpenCode
  ships, and nothing ever removes them. Measured on 2026-09-08: 540 files
  totalling 2,945 MB, accumulated since 2026-09-06, none held by any process.
  Deleting the unheld ones took `/tmp` from 92% to 16% used.
- Why it matters more than its size suggests: item 2 bounds THIS package's
  store, which was 191 MB at that moment — fifteen times less than the
  provider residue sitting beside it. No ceiling of ours touches it. On the
  3.8 GB tmpfs this machine uses, a day of heavy reviewing fills the disk from
  this alone, and the symptom is `no space left on device` inside a review,
  which invites blaming the snapshot store. That misdiagnosis already happened
  once during this work.
- Closing: the reaper that already runs at every snapshot creation also
  collects unheld, stale provider temporary files, or a recorded determination
  that cleaning another tool's residue is out of scope and the operator owns
  it. Do not simply widen the existing prefix match without checking that a
  live invocation's file is never removed.

## 5. Recalibrate or retire the OpenCode reviewer turn budget

Sits fifth: unblocked but low value, and its original premise was disproven.

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
- First measurement, 2026-09-08, now that the count persists: completing
  reviews consumed 3, 4, 5 and 6 turns against a budget of 16, and the two
  truncated dimensions of that same review both died at 1 turn. The budget is
  roughly three times what a completing review needs and was never
  approached, in either direction.
- The truncation cause is now named on the wire: `denied tool call —
  todowrite: The user rejected permission to use this specific tool call`.
  The reviewer reaches for `todowrite`, which is absent from the read/search
  set, and the agent-level `"*": "ask"` rule auto-rejects it in a
  non-interactive run, ending the turn at once. That `ask` fallback is
  deliberate — an agent-level `deny` would shadow every admitted read through
  deny dominance — but its side effect is that any tool outside the allowed
  set kills the whole dimension on its first turn. Fixing that boundary would
  remove the truncations this item was created to explain; choosing a
  different budget value would remove none of them.
- Closing: given the measurement above, a recorded determination that the
  turn budget is not a useful control and the constant should hold a
  documented provider default. Selecting a value from the distribution
  remains admissible but is now the weaker option, and neither closes the
  denial boundary, which deserves its own unit.
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

## 6. Cache shared audit evidence across review dimensions

Sits sixth: the token measurement now exists on all three adapter paths
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

## 7. Give cost, scope and reuse a producer (FU-3)

Sits seventh: tokens now have producers on every adapter path, but cost,
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

## 8. Validate the acpx spawn chain on native Windows

Sits eighth: conditional work — no action while Debian is the deployment
platform.

- Question: the `npx -> node __queue-owner -> npm exec -> node <agent>-acp`
  chain (TTL queue owner) is UNCONFIRMED on native Windows.
- Evidence: former `follow-ups.md` P3 item (git history); origin is the A1
  live probes.
- Closing: a confirmation run on native Windows, or no action at all.
- Starts only if: production runs on Windows.
