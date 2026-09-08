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

## 3. Give the pre-publication audits a record `pr create` can consult

Sits third: unblocked, found by exercising the full pre-push flow, and its
cheap half is separable from its expensive half.

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
- The same hole seen from the publishing end: `pr create` does not audit at
  all. `internal/app/pr/create.go` reads the shared ledger through
  `review.AnalyzeBranch` and derives its notice from those records via
  `SemanticNoticeWithDispositions` -> `review.BranchBlockers`; the only gate
  that blocks is a red deterministic validation, overridable with `--force
  --reason`. So the semantic signal that reaches a published PR comes ONLY
  from `sentinel review` fichas. A team using gate plus `pr review` — the two
  commands that sound like "before publishing" — would publish with no
  semantic signal at all, not because the code is clean but because nothing
  was written down. On this branch `pr review` reported 11 findings on the net
  diff and none of them can reach the PR.
- Why the two halves differ, and this decides the design: the ledger is keyed
  by commit SHA. The gate audits `HEAD`, a single commit, so it HAS a natural
  key and needs no new record shape — it would inherit fingerprints,
  `refute`/`accept`/`reopen`, the metrics aggregates, blob-index reuse across
  rebases, and the existing `--prune`. `pr review` and `pr create` audit a
  net diff over a RANGE, which has no such key.
- The trap in the expensive half: a range record would have to be keyed by
  `base..head` or by a hash of the net diff, and it goes stale the moment the
  branch moves — one more commit and it describes different code. A per-commit
  ficha survives a rebase through its content blobs; a range record cannot. A
  STALE range record is worse than none: `pr create` would publish a PR
  asserting a verdict that does not describe the code being published, whereas
  today's "these commits have no record" is at least true. Any such record
  must be bound to an exact identity and refused when it does not match,
  exactly as `reviewsnapshot` revalidates its manifest on every lease.
- Reference design, gentle-ai, inspected on disk 2026-09-08. Its
  `~/.gentle-ai/review-contexts/v1/` records carry `schema`
  (`gentle-ai.review-repository-context/v1`), a content-addressed `handle`
  (`rctx1_<sha256>`), a `lineage_id`, and — the part that matters here —
  `target_identity` and `revision`, both `sha256:` digests rather than commit
  SHAs, alongside `repository_identity` and the resolved root, common dir and
  git dir. Hashing the candidate dissolves the keying problem stated above: a
  range, a net diff, or any arbitrary candidate becomes addressable, so the
  obstacle was never that a range has no key, it was indexing by commit.
  Storing `revision` next to the record turns staleness into something a
  consumer DETECTS by comparing identities instead of assuming, which is the
  discipline this item already demands; gentle-ai's own contract states that
  any byte, path or mode change invalidates the receipt and requires a new
  review. Two further choices worth copying: the `lineage_id`, which threads
  the operations performed on one candidate — precisely what would relate "the
  gate reviewed this" to "pr review analysed this" to "pr create published
  this" — and keeping the record informational so it never becomes delivery
  authority, which Sentinel already does by blocking only on deterministic
  validation but does not state.
- Limit of that reference: only the on-disk shape of two 630-byte records was
  inspected, plus gentle-ai's documented contract. Its source was not read, so
  how it renders findings, and whether it supports per-finding human
  disposition comparable to `refute`/`accept`/`reopen`, is UNVERIFIED. Confirm
  before copying that part.
- Second reference design, no-mistakes (`kunchenguid/no-mistakes`, Go), read
  from source 2026-09-08. It keeps evidence in git itself rather than beside
  it. `internal/custody/refs.go` anchors a terminal run at
  `refs/no-mistakes/recover/<runID>` through
  `update-ref --no-deref <ref> <head> <zeros>` — a create-only
  compare-and-swap against the null OID, idempotent when the commit matches
  and failing closed on a conflicting or symbolic ref, so evidence is never
  silently replaced. `internal/evidence/publish.go` publishes a run's evidence
  directory to an ORPHAN branch pushed to the same remote as the code branch,
  fork-aware so a PR's evidence lands in the fork holding the head, under a
  directory prefix plus slugged branch segments, and bounded at 500 files,
  256 MB total and 64 MB per file on the stated grounds that evidence is
  agent-produced and a runaway recording must fail the publish closed rather
  than push gigabytes.
- What each reference answers, since they are complementary rather than
  competing: gentle-ai answers the KEYING question above, and no-mistakes
  answers durability and shareability — its evidence travels with the pull
  request, so the human reviewing it sees what the tooling found, and it is
  maintainable in the sense this item needs, prunable as refs and branches and
  reclaimable by `git gc`. The trade-off to state before copying it: that
  evidence is PUSHED, hence visible to anyone who can read the repository,
  whereas this project's ledger is deliberately machine-local under the git
  common directory. Choosing one is choosing who the audit trail is for.
- Closing, cheap half first: make the gate's per-commit review persist a
  record, or record a determination that it deliberately keeps none. Then
  decide the range half by MEASUREMENT rather than preference — compare the
  net-diff findings against the union of the per-commit findings for the same
  commits. If they substantially overlap, no range record is justified and
  per-commit records are enough; if the net audit sees interactions between
  commits that no single commit shows, that is the evidence for building it,
  with the staleness discipline above. The first data point is available: 11
  net findings on `99138bb..19a5b2f` versus the per-commit fichas for
  `e408d42` and `19a5b2f`.

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
