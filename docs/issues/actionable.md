# Actionable — work that can start now

Ordered by readiness first, then by trust impact. Rationale: unblocked,
scoped work ships before work waiting on a missing measurement, and work
that undermines verification trust outranks work that only costs tokens.
One line per item states why it sits where it does.

Decided work order, superseded 2026-09-09 by
[`docs/design/review-flow-ownership.md`](../design/review-flow-ownership.md),
which allocates one question to each command and orders the work as five
pieces. Two are done: piece 2 (`review` is the only per-commit authority,
commit 89cab36) and piece 3 (`gate` stops auditing, commit 144c9c8). The three
that remain map onto this file as **item 12 (piece 1), then item 10 (piece 4),
then item 11 (piece 5)**; item 4 is closed by dissolution. All three now have a
written implementation plan under `docs/design/`, linked from each item. The paragraph below
is the ordering that preceded that design and is kept because the items still
carry its numbering.

Original note: overriding the readiness ordering for three
items only: **item 4, then item 10, then item 11**. They are one problem seen
from three ends — nothing records what was reviewed, so nothing downstream can
build on it — and solving them out of order builds each on a record the
previous one has not defined yet. Numbering is left alone so existing
references from `future.md` and from within this file keep pointing at what
they name.

**Superseded later the same day. Read
[`docs/design/review-flow-ownership.md`](../design/review-flow-ownership.md)
first: it is the governing design, and where it disagrees with anything below,
it wins.** What follows is kept because it records how the problem was
understood when these items were written, and the evidence in each item is
still valid.

The contract those three items originally served, stated by the repository
owner on 2026-09-09, because each item was drifting away from it independently:

- `gate` — "this commit is well made". It looks at one piece.
- `pr review` — "the pieces together tell a coherent and complete story". It
  does NOT look at each piece again. It looks at what is only visible with all
  of them together: whether something is missing, whether one piece undoes
  another, whether the final result does what it promised.
- `pr create` — reviews NOTHING. It takes `pr review`'s report and publishes.
  If no report exists, it asks for one first.

Read that as an allocation of ownership, not a description of today's code.
One consequence it settles and that still holds: the judgement published by
`pr create` is authored by `pr review`, never by `pr create` itself.

**What the superseding design changed, and why the two texts disagree.** The
first line above gave `gate` the per-commit audit. Working through the whole
flow showed why that is wrong: "compiles and passes its checks" is a property
of the tree at one moment, not of a commit in isolation — split one piece of
work across seven commits and the third usually does not build alone. So the
two questions cannot share an owner. `review` owns "is this piece well made"
and is the only writer of per-commit verdicts; `gate` owns "does this work
right now" and stops auditing entirely. Everywhere below that assigns a
per-commit audit to `gate`, read `review`.

## 0. Let a real fix retire a block even when it is not called `fix(`

Found on 2026-09-09 by exercising the flow on `feat/review-coverage-contract`,
where it produced three live blocks describing code that no longer existed.

- Wrong: `recordFixes` (`cmd/sentinel/review_command.go`) returns immediately
  unless the reviewed commit's message starts with `fix(`. Everything after that
  guard — the active-block check, the file overlap, the ancestry check, and
  `MarkFixed` — is unreachable for a correcting commit named anything else.
- Evidence: on that branch, `60e420c` and `6c3a2ac` each recorded CRITICAL
  findings that `e2f4b23` (`refactor(review):`) and `d8462e6` (`docs(issues):`)
  genuinely corrected. Neither retirement fired. Three blocks stayed live over
  code that had been rewritten, and the only lever left was `refute`.
- Why the workaround is not the answer: `refute` records "this finding does not
  describe the code", which is true here, but the reason field then has to carry
  "and it was fixed in <sha>" as prose. The link between the block and the
  commit that resolved it — which `MarkFixed` stores structurally — is lost, and
  metrics counting fixes cannot see it. The three refutations recorded on that
  branch all say so explicitly.
- Why the guard exists, and what NOT to break: the prefix is one of four
  independent narrowings, alongside a clean exit, file overlap, and an ancestry
  check added by FU-17 after a `fix(` on one branch cleared a block recorded on
  an unrelated one. The ancestry check is the one doing the real work. The
  prefix is a proxy for intent, and a weak one: it accepts a `fix(` that
  corrects something else entirely, and refuses a `refactor(` that corrects
  exactly the finding.
- Not obvious, and worth deciding rather than assuming: whether ANY Conventional
  Commit type may retire a block, or only a named set. Widening it to every type
  makes a `chore(` that happens to touch the file retire a block it never
  addressed — the ancestry and overlap checks do not test intent. The narrower
  reading is that the retirement should not be inferred from the message at all,
  and should be an explicit claim.

## 1. Bound what the review flow costs the machine that runs it

Sits first: unblocked, and it is the only item that has stopped work outright
rather than degrading it. Six consecutive failures on 2026-09-08.

- Wrong: nothing relates the flow's resource use to the machine's capacity, in
  four independent places. `review.parallel` (5 here) spawns that many
  concurrent OpenCode reviewers, each a full Bun process. `pr create` runs the
  validation profile AND the net semantic audit; the `standard` profile ends in
  `go test ./...`, which in this repository spawns real child processes from
  `internal/durableruns_e2e`, `internal/daemon` and `internal/process`, with
  `mode: worktree` materializing another checkout. `/tmp` is tmpfs on this
  machine, so every byte of snapshot and provider residue is RAM, not disk. And
  a process killed mid-validation leaves its materialized worktree registered
  in git, which no reaper collects.
- Evidence, 2026-09-08: `pr create` was killed by the OOM reaper six times
  across four configurations — untouched, `GOFLAGS=-p=2`, that plus
  `review.parallel: 2`, and a prebuilt binary with `GOFLAGS=-p=1`. A review of
  one commit was killed the same way earlier. Freeing 914 MB of residue (525 MB
  of snapshots plus 389 MB of Bun temporaries) moved available memory from
  4.1 GB to 4.9 GB, which is what proves tmpfs residue is memory. Closing one
  agent pane freed a further 780 MB and let a review reach completion. Three
  orphaned validation worktrees had accumulated under
  `.git/vas-sentinel/snapshots/`, taking `git worktree list` to seven entries.
- Why the symptom misleads: the process dies with no diagnostic of its own, so
  it reads as a hang or a provider fault. It cost several wrong diagnoses in one
  afternoon, including blaming the snapshot store for a disk fill that was
  mostly the provider's Bun residue.
- Closing: a ceiling derived from what the machine actually has, the way
  `storeCapacityLimit` derives from `statfs` rather than a magic number —
  applied to reviewer concurrency and to running validation alongside a
  semantic audit — plus collection of validation worktrees left by a killed
  run. Or a recorded determination that Sentinel targets machines where this
  does not arise, which would be a real decision given it runs on the same
  machine as the agents that drive it.

## 2. Make the PR verification notice read the configuration this project uses

Sits second: unblocked, small, and it makes a published claim untrue.

- Wrong: `verifyInternal` (`internal/ops/verify.go:107-109`) builds its command
  list from `Cfg.LintCommands`, `Cfg.TestCommands` and `Cfg.BuildCommands`
  only. This project configures verification under `validation.capabilities`
  instead — `gofmt -l .`, `go vet ./...`, `go build ./...`, `go test ./...` —
  and `translateLegacyCommandsToCapabilities` (`internal/config/parser.go:595`)
  converts legacy keys INTO capabilities, never the reverse, and returns early
  when capabilities are already present. So the list is empty and
  `verificationNoticeText` announces "No verification commands are configured
  (lint_commands/test_commands/build_commands in vassentinel.yml)" followed by
  "No CI was detected: this PR will not have any automatic verification".
- Evidence: on 2026-09-08 `pr create` printed exactly that, minutes after
  `gate --stage pre-push` had run all four of those commands and reported PASS.
  The template it wrote contradicts its own console notice: the template's
  Validation section lists the four commands at exit 0. The two read different
  places, and the template is the one telling the truth.
- It also blocks scripted use: the notice ends by asking the operator to reply
  `configure`, `skip` or `delegate` on stdin, so a non-interactive run answers
  by EOF and proceeds on a default nobody chose.
- Closing: read the capability profile the project actually declares, so the
  notice and the template agree, and decide the interactive question's
  non-interactive answer explicitly instead of by EOF.

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

## 4. Give the pre-publication audits a record `pr create` can consult

**CLOSED 2026-09-09 by dissolution, not by implementation.** This item existed
because `gate` audited `HEAD` and threw the verdict away, so the fix looked like
"make `gate` write a ficha". Working the flow through end to end showed the
premise was the mistake: "compiles and passes its checks" is a property of the
tree at one moment and "is this piece well made" is a property of one commit,
so the two cannot share an owner. `gate` stopped auditing (piece 3, commit
144c9c8) and `sentinel review` became the only writer of per-commit verdicts
(piece 2, commit 89cab36). There is no discarded verdict left to persist.

What survives this item and where it went:

- The keying problem for a RANGE verdict, and the staleness discipline that a
  range record cannot inherit a per-commit record's rebase survival, are the
  live constraints on piece 5 in
  [`docs/design/review-flow-ownership.md`](../design/review-flow-ownership.md).
- The gentle-ai `target_identity`/`revision`/`lineage_id` reference design and
  the no-mistakes git-native custody reference below are still the two designs
  to compare when that record is built. They were not consumed by pieces 2
  or 3.
- The observation that a team using `gate` plus `pr review` could publish with
  no semantic signal at all is now false by construction for a different
  reason: `pr create` verifying that per-commit verdicts exist is piece 5's
  job, and `review` is mandatory.

Everything below is kept as the evidence trail for those constraints, and
describes code as it was BEFORE pieces 2 and 3. Do not read it as current.

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
- Confirmed in code 2026-09-09, which removes the "possibly deliberate" doubt
  from the first bullet as a question of fact while leaving it open as a
  question of intent: `internal/gate` contains no call to
  `ledger.SaveRevision` anywhere. The gate writes durable RUN records through
  agentrun/store, but no review ficha. So the gap is not a missed write path;
  there is no write path at all, and adding one is a design decision rather
  than a repair.
- Shape proposed by the repository owner, 2026-09-09, and worth stating before
  the cheap half is built because it constrains it: ONE record per change that
  the gate opens, `pr review` complements, and `pr create` reads to compose the
  published report — rather than three independent artefacts that later have to
  be reconciled. This is the `lineage_id` idea from the gentle-ai reference
  above, made concrete: the gate's per-commit verdict, the net-diff verdict,
  and whatever intent is established (item 10) become entries threaded onto one
  identity, so `pr create` composes a report instead of recomputing one
  (item 11).
- What that shape must NOT do, restating the staleness discipline for the
  combined case: a single record spanning a per-commit verdict and a range
  verdict inherits the range half's staleness. The per-commit entries survive a
  rebase through their content blobs; the range entry does not. Any consumer
  must be able to tell WHICH entries still describe the code in front of it,
  which means each entry carries its own identity and is refused independently,
  not the record as a whole. A record that goes stale atomically is worse than
  two records, because it discards valid per-commit evidence along with the
  invalid range evidence.
- What the measurement above does NOT decide, corrected 2026-09-09 after the
  repository owner rejected an earlier reading of this file: it does not decide
  whether the range entry is built. That was a cost question posing as a design
  question. Even at total overlap between per-commit and net findings, a PR
  report assembled from per-commit fichas answers "how well is each piece made"
  when the question a pull request asks is "what does this change do as a whole,
  and is it well resolved". The range entry is the deliverable of item 11, not
  an optimisation of it. What the measurement still decides is narrower and
  purely about cost: whether the per-commit audit is worth running at all when
  the net audit already covers the same ground.

## 5. Bound the provider's own temporary residue

Sits fifth: unblocked and mechanical, but the residue is the provider's, not
this repository's, so the fix can only be to clean it, not to prevent it.

- Wrong: each restricted reviewer invocation leaves a roughly 14 MB
  `/tmp/.<hex>-00000000.so` file behind, written by the Bun runtime OpenCode
  ships, and nothing ever removes them. Measured on 2026-09-08: 540 files
  totalling 2,945 MB, accumulated since 2026-09-06, none held by any process.
  Deleting the unheld ones took `/tmp` from 92% to 16% used.
- Why it matters more than its size suggests: item 3 bounds THIS package's
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

## 6. Recalibrate or retire the OpenCode reviewer turn budget

Sits sixth: unblocked but low value, and its original premise was disproven.

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

## 7. Cache shared audit evidence across review dimensions

Sits seventh: the token measurement now exists on all three adapter paths
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

## 8. Give cost, scope and reuse a producer (FU-3)

Sits eighth: tokens now have producers on every adapter path, but cost,
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

## 9. Validate the acpx spawn chain on native Windows

Sits ninth: conditional work — no action while Debian is the deployment
platform.

- Question: the `npx -> node __queue-owner -> npm exec -> node <agent>-acp`
  chain (TTL queue owner) is UNCONFIRMED on native Windows.
- Evidence: former `follow-ups.md` P3 item (git history); origin is the A1
  live probes.
- Closing: a confirmation run on native Windows, or no action at all.
- Starts only if: production runs on Windows.

## 10. Give `pr review` something to judge the change against

**This item is now PIECE 4 of
[`docs/design/review-flow-ownership.md`](../design/review-flow-ownership.md),
which is the governing text.** It depends on piece 1 (item 12 below), which
decides where the intent comes from; this item is only about consuming it.
**The implementation plan is
[`docs/design/piece-4-pr-review-authors.md`](../design/piece-4-pr-review-authors.md),
written 2026-09-09. It is the specification: where it disagrees with the text
below, the plan wins.** What follows is the evidence that the problem is real,
and it still holds.

Found 2026-09-09 by tracing the flows conceptually rather than by a failure.

- Wrong: no flow in this repository ever receives a statement of what the change
  was supposed to do. `DimensionSpec.Instructions`
  (`internal/reviewcontract/contract.go:117-123`) asks whether "the diff does
  exactly what the commit message claims", and `internal/review/prompts.go:23-85`
  fills that slot with whatever `message` the caller passed to `AuditCommit`.
  For `sentinel review` that is the real commit message, which is at least
  evidence (it was also true of `gate` until piece 3 stopped it auditing). For the net audit in `pr review` and `pr create` it is the
  literal constant `HonestNetIntention` (`internal/app/pr/review.go:20`): "No PR
  title/description exists before publication: claims cover branch commits and
  the net diff only."
- Why that is worse than an empty field: the `netAxes` block
  (`internal/review/net_pr.go:316-323`) asks the reviewer, among other things,
  whether the PR delivers what its title and description promise — of a prompt
  that has just told it no title or description exists. The reviewer is asked to
  verify conformance to something it was explicitly told it does not have. The
  `--answer` clarification channel is empty for both flows too
  (`BranchOptions.Answers` is never set in `review.go:118-147` nor in
  `create.go`), so `net_pr.go:210` forwards `""`.
- What is NOT wrong, corrected 2026-09-09 against an earlier reading of this
  file: `pr review` is not a per-commit audit repeated at branch scale. Its net
  half is a genuinely distinct unit — one `AuditCommit` over the whole range
  diff, planned from that diff's own aggregate risk, asking about integration
  between commits, one commit undoing another, net regression, undeclared
  contract breaks and net coverage. It also carries a DETERMINISTIC classifier
  (`classify`, `net_pr.go:67-132`) that checks against git blobs whether a later
  commit removed the code an earlier finding pointed at, rather than asking a
  model to notice. The branch-level judgement is built. It is the input that is
  missing.
- Loose end the ownership contract exposes: `--audit-pending` makes `pr review`
  audit each pending commit, which is `gate`'s question asked by the wrong
  command. It is off by default, so nothing is broken today, but under the
  contract it has no owner — either it is a convenience that should be named as
  "run gate's audit for these commits" and routed accordingly, or it should go.
  Decide it while item 4 is being built, since that is when the per-commit
  record gains a writer.
- Reference design, no-mistakes (`kunchenguid/no-mistakes`, Go), read from
  source 2026-09-09. A dedicated pipeline step (`IntentStep`,
  `internal/pipeline/steps/intent.go`) runs BEFORE review, with two paths. The
  explicit path: the agent driving the run passes `--intent "..."`, persisted
  with `Source == agent`. The inferred path, when none was passed:
  `intent.Extract` (`internal/intent/intent.go`) discovers LOCAL transcripts of
  the developer's own sessions with coding agents (Claude Code, Codex,
  OpenCode, Rovo Dev, Pi, Copilot CLI — one reader per agent), filtered by
  originating repository and by the time window between base and head; scores
  each candidate session by what fraction of the diff's files it mentions
  (`matcher.go`, `score()`), taking a decisive match outright and otherwise
  routing to an LLM disambiguator; then summarises the winner with a dedicated
  prompt (`summarizer.go`) constrained to 2-6 plain sentences under a forced
  JSON schema, instructed to report what the DEVELOPER was trying to achieve,
  not what the assistant did.
- The part of that design that matters most here is not the extraction but the
  PROVENANCE. `internal/pipeline/steps/intent_prompt.go` wraps the text in
  `BEGIN/END USER INTENT` markers and changes the reviewer's obligation by
  source: an explicit intent is framed as AUTHORITATIVE acceptance criteria and
  activates an extra clause requiring a finding when the diff contradicts or
  omits something marked required — even when the change is otherwise clean —
  whereas an inferred intent is framed as "may be partial or wrong; treat as a
  hint, not ground truth" and carries no such clause. Both are declared
  untrusted DATA, never instructions. Absence never blocks: the section is
  simply omitted and review proceeds on the diff alone.
- Notable absence in that reference, since it contradicts the obvious guess:
  no reader sources intent from an issue or a PR body. The source is always the
  conversation that produced the change, or an explicit declaration by the agent
  driving it.
- Closing: decide where intent comes from for this project, then make the net
  audit receive it with its provenance attached rather than a constant that
  denies its existence. The provenance half is not optional — an inferred intent
  treated as authoritative would manufacture findings against a goal nobody
  stated, which is a worse failure than the current silence. If the answer is
  that no source is available here, that is a determination worth recording,
  and it should replace `HonestNetIntention` with an omitted section rather than
  a sentence the reviewer is then asked to verify against.
- Relates to item 4: whatever intent is established belongs on the same record,
  so `pr create` (item 11) publishes the goal alongside the verdict instead of
  restating a diff.

## 11. Let `pr create` compose the report instead of recomputing it

**This item is now PIECE 5 of
[`docs/design/review-flow-ownership.md`](../design/review-flow-ownership.md),
which is the governing text.** It is last because it needs both the coverage
contract (piece 2, done) and the intent (pieces 1 and 4). Found 2026-09-09.
**The implementation plan is
[`docs/design/piece-5-pr-create-composes.md`](../design/piece-5-pr-create-composes.md),
written 2026-09-09. It is the specification: where it disagrees with the text
below, the plan wins.**

- The subject of the report, stated first because it constrains everything
  below and because getting it wrong is the failure this item exists to
  prevent: a published PR report is about the QUALITY OF THE IMPLEMENTED WHOLE,
  not about the quality of the commits that carried it. Per-commit fichas are
  the record of what `sentinel review` did, and it asks a different question —
  is this piece well made (this bullet said `gate` until piece 3 moved that
  question to its only owner). They are not the raw material of a PR report, and a report
  assembled by concatenating them is the wrong artefact however cheap it is to
  produce. The net verdict is the report's substance; the per-commit records are
  at most supporting detail, and possibly not even that.
- Wrong: `pr create` reuses nothing from `pr review`. `RunPrCreateWith`
  (`internal/app/pr/create.go:222`) calls the same `review.AnalyzeBranch` that
  `pr review` calls, from scratch, on every invocation — re-deriving the merge
  base, re-reading the range diff, and re-running `runNetReview`'s
  `AuditCommit` over the net diff from zero. The per-commit half does reuse
  work, through `ledger.ReadRecord` and the blob-content lookup that survives
  rebases; the net half, which is the part closest to an actual PR verdict, has
  no record to consult and no cache, so it is paid for twice by any operator who
  reviews before publishing.
- One behavioural difference worth keeping in view while designing this:
  `Overview: true` is hardcoded in `pr create` (`create.go:230`) but flag-gated
  and off by default in `pr review`. With overview off, `AnalyzeBranch`
  (`branch.go:299-308`) routes every branch over `DecisionChainLimit` (400
  lines) to `chain` without ever asking whether the change is coherent. So the
  two commands do not currently reach the same decision on the same branch, and
  a naive "reuse the previous result" would silently change `pr create`'s
  routing. Whichever behaviour is correct should be chosen deliberately rather
  than inherited from whichever record happens to exist.
- Closing, under the ownership contract at the head of this file: `pr create`
  audits nothing. It reads the net entry from item 4's record and composes the
  published report from it — what the change does as a whole, measured against
  the intent from item 10. When no net entry exists, or when its identity no
  longer matches the code being published, `pr create` does not audit its way
  out: it requires a `pr review` and either invokes it or refuses to publish
  until one exists. The distinction is not cosmetic — it keeps a single author
  for the branch-level judgement, so a PR cannot be published carrying a verdict
  nobody could reproduce by running `pr review` themselves. Until that record
  exists, do not add a cache: a reused net verdict with no identity check is
  exactly the stale-record failure item 4 already refuses.
- What follows from the subject stated above, and is worth settling before any
  code: whether per-commit findings appear in the published report at all, and
  under what framing. Today's notice is derived from per-commit records
  (`SemanticNoticeWithDispositions` -> `BranchBlockers`), which is the inversion
  this item corrects.
- Note on what this does not change: publication blocks only on a red
  deterministic validation, overridable with `--force --reason`
  (`create.go:120-186`). The semantic verdict is advisory. Composing the report
  from a record must not quietly turn it into a gate.

## 12. Capture what the work was for, at the moment `slice` commits it

**This is PIECE 1 of
[`docs/design/review-flow-ownership.md`](../design/review-flow-ownership.md),
which is the governing text. The implementation plan is
[`docs/design/piece-1-slice-intent.md`](../design/piece-1-slice-intent.md),
written 2026-09-09; it answers the three questions this item left open and is
the specification.** Opened 2026-09-09; it had no item in this file
because it was proposed after items 4, 10 and 11 were written, and it is what
unblocks item 10.

- The problem it replaces: item 10 needs a statement of what a change was
  supposed to do, and the obvious source — mining the conversations that
  produced the commits — means reconstructing after the fact across several
  days, more than one agent, and commits that may have no conversation at all.
- Why `slice` is the right moment, and this is the whole idea: at slice time
  the intent is present rather than reconstructed. The work just finished, the
  conversation is current, the commit being created is small and concrete, and
  `slice` ALREADY invokes an agent right there to write the commit message. The
  expensive part — being at the right moment with the right context — is
  already paid for.
- Reference design, `no-mistakes` (`kunchenguid/no-mistakes`), read from source
  2026-09-08: a cheap model summarises the transcript into a few plain
  sentences about what the HUMAN wanted, not what the assistant did; the
  summary is injected into later prompts between explicit begin/end markers,
  framed as untrusted data, with an instruction never to obey anything written
  inside it; the origin is recorded alongside the summary. All three are worth
  copying verbatim.
- Non-negotiable: provenance travels with the intent. "Derived from the working
  conversation" and "declared by the human" are not worth the same, and the
  reviewer that consumes it must be able to say which it got. Without that, a
  reviewer asserts more than it can support.
- Open, and the reason this is not a two-hour task:
  - Commits made without `slice` carry no intent. Decide whether that is
    acceptable or whether one can be annotated afterwards.
  - Where the record lives, given it must survive a rebase and travel with the
    branch. This is the same keying problem item 4 stated, and the answer must
    be the same one.
  - Whether a branch-level intent is the union of its commits' intents or a
    separate declaration.
- Cost: the configured `cheap` profile is sufficient. The task is
  summarisation, not judgement.
