# Actionable — work that can start now

Ordered by readiness first, then by trust impact. Rationale: unblocked,
scoped work ships before work waiting on a missing measurement, and work
that undermines verification trust outranks work that only costs tokens.
One line per item states why it sits where it does.

## 1. Restore permission-aware removal of published snapshots

Sits first: a regression introduced on `fix/reviewsnapshot-platform-modes`,
not yet merged, and it blocks that branch. Smallest fix on this list.

- Wrong: published regular files are still tightened to their published
  permission (`os.Chmod` at `internal/reviewsnapshot/store.go:146`, 0400 or
  0500), but `removeReadOnlyStoreEntry` — which chmodded a tree back to 0700
  before deleting it — was removed as dead code when directory sealing was
  reverted. Cleanup is now raw `os.RemoveAll`/`os.Remove` at eight sites
  (store.go:438-440 and 486-513). Unlinking a read-only file inside a
  writable directory succeeds on Linux, so the suite passes; Windows refuses
  to delete a read-only file, so the reaper and the failed-publication
  cleanup cannot remove anything, residue accumulates without bound, and the
  rename error path then reads an existing target as a competing winner and
  can make `Create` retry indefinitely.
- Evidence: Sentinel logic dimension, CRITICAL at confidence 0.9, on commit
  `ea5d579`. The helper was deleted on my instruction: I judged it dead
  because directories were no longer sealed, which was wrong — it existed for
  read-only FILES, and those never stopped being read-only.
- Closing: removal that restores owner write access before deleting,
  reinstated for every cleanup path, with the Windows expectation pinned the
  way `publishedPermMatches` now pins the validation rule.

## 2. Move provider isolation out of the evidence snapshot

Sits second: unblocked, and it is the reason the shared snapshot does not yet
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

## 3. Bound the total size of the review snapshot store

Sits third: unblocked and small, but it only bites under unusual load.

- Wrong: the store under `os.TempDir()/vas-sentinel-snapshots-<uid>` is
  reaped by staleness alone. Nothing bounds its total size, and one committed
  tree of this repository is 625 files and 5.8 MB.
- Evidence: `sentinel review HEAD --all` audits every commit lacking a record
  — 871 here — which needs about 5.0 GB against a 3.8 GB tmpfs. It filled
  `/tmp` to 100% on 2026-09-08 and the remaining dimensions failed with
  `no space left on device`. The immediate cause was the wrong flag, but a
  store with no ceiling on a tmpfs is one bad invocation away from filling
  the disk.
- Closing: a total-size ceiling that evicts the least recently leased
  unleased tree, or a recorded determination that staleness alone is the
  intended policy and the flag is the thing to guard.

## 4. Recalibrate or retire the OpenCode reviewer turn budget

Sits fourth: unblocked but low value, and its original premise was disproven.

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

## 5. Cache shared audit evidence across review dimensions

Sits fifth: the token measurement now exists on all three adapter paths
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

## 6. Give cost, scope and reuse a producer (FU-3)

Sits sixth: tokens now have producers on every adapter path, but cost,
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

## 7. Validate the acpx spawn chain on native Windows

Sits seventh: conditional work — no action while Debian is the deployment
platform.

- Question: the `npx -> node __queue-owner -> npm exec -> node <agent>-acp`
  chain (TTL queue owner) is UNCONFIRMED on native Windows.
- Evidence: former `follow-ups.md` P3 item (git history); origin is the A1
  live probes.
- Closing: a confirmation run on native Windows, or no action at all.
- Starts only if: production runs on Windows.
