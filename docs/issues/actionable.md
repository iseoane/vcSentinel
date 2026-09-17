# Actionable — work that can start now

Ordered by criticality: work that has stopped a run outright comes first, then
work that publishes something untrue, then work that costs tokens or leaves a
record incomplete, then work waiting on a measurement or on a platform
decision. One line per item states why it sits where it does.

Numbering is historical and deliberately not renumbered, so references from
`future.md` and from `decisions.md` keep pointing at what they name. Items 2,
3, 4, 5, 10, 11, 12, 13, 16 and 18 closed and moved to
[`decisions.md`](decisions.md); with them the five pieces of
[`docs/design/review-flow-ownership.md`](../design/review-flow-ownership.md)
are all done, so that design is now history rather than a work order.

## 1. Bound what the review flow costs the machine that runs it

**Measured 2026-09-10, and the number was missing until now: the ceiling is
concurrent reviewers, not the model or the timeout.** On a 7.4 GiB WSL2 host
already carrying two agent sessions and the CodeGraph indexer, `review.parallel:
5` killed four consecutive review runs — every dimension spawns its own
`opencode` process, and five at once does not fit. Lowering it to 2 still died.
Only `parallel: 1` completed. Each kill also left the durable runs non-terminal,
so they had to be retired by hand with `runs abort --orphaned`. Whatever this
item eventually bounds, it should be expressed in concurrent reviewers, and a
kill should retire its own runs instead of leaving them for an operator.

The durable-run half of that sentence was answered on 2026-09-15 and NOT the
way it is phrased: automatic settlement was attempted, returned ten CRITICAL
findings, and was rejected in favour of surfacing the residue in `runs status`
so the operator learns it exists. See `decisions.md`. The phrasing is left as
written because it records what was wanted before the attempt.

Corrected 2026-09-15: this item previously claimed the project configuration
ships `parallel: 1`. It does not — `.vas_sentinel/vassentinel.yml` sets
`parallel: 2`, reverted by `9ace763` after `87ea52d`, and the default in
`internal/config/parser.go` is also `2`. The value the item recorded as the
mitigation is not the value in force, and `2` is one of the settings this item
records as having died.

Sits first: it is the only item that has stopped work outright rather than
degrading it. Six consecutive failures on 2026-09-08.

- Wrong: nothing relates the flow's resource use to the machine's capacity, in
  three independent places. `review.parallel` (5 here) spawns that many
  concurrent OpenCode reviewers, each a full Bun process. The validation
  profile's `standard` stage ends in `go test ./...`, which in this repository
  spawns real child processes from `internal/durableruns_e2e`,
  `internal/daemon` and `internal/process`, with `mode: worktree`
  materializing another checkout. `/tmp` is tmpfs on this
  machine, so every byte of snapshot and provider residue is RAM, not disk. And
  a process killed mid-validation leaves its materialized worktree registered
  in git, which no reaper collects.
- The `pr create` half of this item is gone, 2026-09-15: it no longer runs a
  net semantic audit. `internal/app/pr/create.go` calls nothing in
  `internal/review`; it publishes the persisted `pr review` judgement and runs
  only the validation profile (`cmd/sentinel/pr_command.go:413`). Closing item
  13 removed that cost as a side effect, so what remains of this item is
  reviewer concurrency and the validation profile's own child processes.
- Evidence, 2026-09-15, the failure reproduced with its real cause measured:
  three processes were killed during one session — twice the review itself,
  once a wrapper — while `free` reported 5.2 GB available and the summed RSS
  of every process was under 1.1 GB. It was not a shortage of anonymous
  memory. `/tmp` is tmpfs, so its contents ARE memory: it stood at 735 MB, of
  which 486 MB were three `vas-sentinel-review-provider-*` directories of
  162 MB each, left by the killed reviews themselves, plus 107 MB of Bun `.so`
  residue (item 5, closed 2026-09-17). Removing them took `/tmp` to 144 MB and
  the reviews then
  completed. The loop is the point: each killed review leaves 162 MB of memory
  occupied, which makes the next one likelier to die.
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
- One half is closed, 2026-09-15: collection of validation worktrees left by a
  killed run landed in `f35f294`..`495892f` (see `decisions.md`). The killed
  run's own durable runs and the provider residue (item 16, closed 2026-09-17)
  are separate.
- Closing, and PARKED as of 2026-09-15 pending a decision by the repository
  owner: a ceiling derived from what the machine actually has, the way
  `storeCapacityLimit` derives from `statfs` rather than a magic number,
  applied to reviewer concurrency. Or a recorded determination that Sentinel
  targets machines where this does not arise, which would be a real decision
  given it runs on the same machine as the agents that drive it.
- Why it was parked: the number that killed the runs is not derivable from
  total memory. It depended on two agent sessions and a CodeGraph indexer
  sharing the host, and on 486 MB of tmpfs residue that items 5 and 16 remove.
  A ceiling derived then would still have been a chosen number with more code
  behind it — the same mistake item 6 records for the turn budget, where a
  value was raised without measurement and the premise turned out to be false.
- **The precondition is now met, 2026-09-17: items 5 and 16 are closed** (see
  `decisions.md`). The residue that made the host look smaller than it is no
  longer accumulates: orphaned provider context goes on the next `Create`
  instead of an hour later, that context now counts against the store ceiling,
  and the Bun residue is collected at all. What this item still needs is the
  re-measurement those closures were supposed to enable — run the flow that
  died and record what it costs on a host that is no longer carrying the
  residue. Do NOT derive a ceiling before that number exists; the whole reason
  this was parked was to avoid choosing one and calling it derived.
- Note recorded 2026-09-17 while closing items 5 and 16, because it bears on
  what the re-measurement will find: with no killed reviews in the session the
  store held zero orphaned provider directories, which matches item 16's
  observation that a review which COMPLETES leaves none. The residue is a
  consequence of the kill. A re-measurement therefore has to reproduce the
  kill, not just run a healthy review.
- There is also no resource sensing anywhere in the codebase: `NumCPU`,
  `MemTotal`, `Sysinfo` and `GOMAXPROCS` return nothing. `storeCapacityLimit`
  is a DISK ceiling from `statfs`; the reference above is a structural
  analogy, not reusable code. A memory-derived ceiling means new
  `*_linux.go`/`*_windows.go` files, since AGENTS.md requires identical
  behaviour on both platforms.

## 17. Record why a review dimension was unavailable

Opened 2026-09-15. Sits here because it does not break a run, it makes every
future break expensive to diagnose.

- Wrong: when a dimension returns `unavailable`, nothing consultable says why.
  `sentinel pr review` printed `Net audit verdict: unavailable` with exit `0`
  and an EMPTY stderr; the persisted `pr review` entry stored the verdict and
  no reason; the net audit leaves no ficha at all, so there is no
  per-dimension `reason` to read either.
- Evidence: diagnosing one such verdict on 2026-09-15 took several rounds of
  spelunking through `.git/vas-sentinel/executions/v1/` before the cause
  surfaced in a durable run's outcome text — a denied `todowrite` tool call
  killing one dimension of four. The information existed; nothing pointed at
  it. An operator who does not know the execution store exists cannot find it.
- It also misleads: `unavailable` reads as "the provider was flaky" when the
  actual cause was a deterministic, reproducible permission rejection that
  recurred on every run.
- Closing: the verdict names its cause where the verdict is read — the console
  line, the persisted entry, or both — or a recorded determination that the
  durable store is the only intended diagnostic surface, with the exact
  command to reach it printed alongside the `unavailable` verdict.

## 0. Capture a real fix's provenance beyond `fix(`

Sits fourth: it leaves the record incomplete rather than wrong, and item 13
(now in `decisions.md`) already removed the rebase cause that made it look
worse than it is.

Found on 2026-09-09 by exercising the flow on `feat/review-coverage-contract`.

- Current limitation: `recordFixes` (`cmd/sentinel/review_command.go`) returns
  before recording `FixedIn` unless the reviewed commit's message starts with
  `fix(`. A correcting `refactor(` or `docs(` commit can therefore lack the
  structural link that records which later commit was credited with the fix.
- Evidence: on that branch, `60e420c` and `6c3a2ac` had findings corrected by
  `e2f4b23` (`refactor(review):`) and `d8462e6` (`docs(issues):`), respectively.
  Neither commit received a `FixedIn` provenance link.
- Item 14 changed the boundary: `FixedIn` is provenance only. `RecordPending`
  decides whether a record blocks from its current findings and standing human
  answers, so recording or widening a provenance link must never retire a
  block. Re-auditing the blocked SHA or a human `refute` remains the only way
  to clear it.
- Goal: decide whether a non-`fix(` correction can make an explicit provenance
  claim, then preserve the clean review, file-overlap, and ancestry checks when
  recording it. This is a metrics and documentation improvement, not a change
  to branch blocking.
- Do not widen every Conventional Commit type by default: a `chore(` that
  merely touches the same file is not evidence of a correction. Prefer an
  explicit claim or a narrowly justified type policy.

## 6. Recalibrate or retire the OpenCode reviewer turn budget

Sits fifth: unblocked but low value. Its original premise was disproven, and
the truncation it was created to explain was removed by the whole-tree
snapshot; what is left is choosing a value or recording a determination.

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
- CLOSED for `todowrite`, 2026-09-15 (`305814d`, corrected by `ef68642`): the
  reviewer's permission map now grants `todowrite`, so the rejection that
  killed a dimension at turn 1 no longer happens. Measured before and after on
  the same branch: `pr review`'s net audit went from `unavailable` (one
  dimension of four dead, zero coverage) to a verdict it actually computed.
  The deep fix — a rejection that does not end the turn — was ruled out with
  evidence: OpenCode ends the session itself and reports `stop reason:
  tool-calls`; `cli_review_context.go:130` only observes that and discards the
  incomplete answer. We can avoid the question, never make the rejection
  harmless.
  The first attempt shipped the grant as a pattern map, copying the shape
  `grep` and `glob` use, and OpenCode rejected the WHOLE configuration
  (`Expected PermissionActionConfig | undefined`), killing all four dimensions
  instead of one. `todowrite` takes a bare action because it matches no path or
  expression. The unit test that passed through that regression asserted the
  in-memory map; only a test parsing the serialized `OPENCODE_CONFIG_CONTENT`
  observes what the provider validates.
- STILL OPEN in the same boundary: a `glob` call was denied once
  (2026-09-08) despite its explicit `{"*": "allow"}`. That is not the relative-
  path case the determination in `cli.go` discusses, and it is not fixed by
  listing more tools, because `glob` was already listed. The determination
  there rests on an audit of 31 invocations with "zero denials"; this is a
  counterexample worth reconciling before trusting it.
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

Sits sixth: unblocked and cost-only. The token measurement now exists on all
three adapter paths (see the 2026-09-06 entry in `decisions.md`), so the
design can be selected with real numbers instead of guesses.

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

## 15. Derive the intent from a conversation, as its own change

Sits seventh: not a defect and not blocked — a parked feature that needs a
redesign before it returns.

Withdrawn from piece 1 on 2026-09-10 and parked here. It is NOT abandoned: the
reasoning recorded for item 12 in [`decisions.md`](decisions.md) — why `slice`
is the right moment — still holds, and provenance is still worth
distinguishing.

- Why it was withdrawn, with the count: `--intent-transcript` produced seven
  defects over three rounds of fixes in one night — shell injection through
  path metacharacters; `%VAR%` expansion by cmd.exe inside double quotes;
  input redirection through the `<path>` placeholder that replaced the
  interpolated path; a transcript symlinked to a tracked file, whose target
  was then captured and sent in the micro-diff; a symlinked directory that
  bypassed the exclusion; exclusion keys relative to the repository root
  compared against `git status` paths relative to the working directory; and
  the widening of external-diff consent to cover transcript data. Every fix
  was correct and every one exposed the next defect. The declared path,
  `--intent "<text>"`, produced none.
- What they have in common, which is the design lesson: none of them are about
  the intent. They come from accepting an operator-supplied PATH, reading it,
  excluding it from the draft, and sending its contents to an agent. That is
  four separate trust boundaries for an optional convenience.
- What was removed: both flags (refused by name, not reported as unknown),
  `internal/intent/summarize.go`, the exclusion mechanism in
  `internal/git/draft.go` — `CaptureDraftChangesExcluding`,
  `normalizedExcludedPaths`, `SemanticSliceOptions.ExcludedPaths` — and the
  inventory entry for the summarizer. The exclusion mechanism had no other
  caller and carried three of the seven defects.
- What survives and is worth reusing when this is picked up: everything in
  `internal/intent` except the summarizer — the trailer contract, `Normalize`,
  `Parse`, the provenance values, the asymmetry between rejecting an
  over-length declared intent and truncating a summarized one. The
  `conversation` source is still declared and still meaningful; nothing
  produces it today.
- Where to start when it returns, and this is the part the failed attempt
  earns: do not take a path. Read the transcript from stdin, or have the
  caller pass the text. That removes the path resolution, the symlink cases,
  the exclusion mechanism and the draft interaction in one move — four of the
  seven defects cannot exist. The two that remain are the consent boundary and
  the prompt injection framing, and both were already solved.
- Consent: `SummarizeTranscript` should take a value that cannot be
  constructed without checking consent, so the guarantee is enforced by the
  compiler rather than asserted in a comment. That was the standing
  improvement when the feature was withdrawn, and it is the right shape for
  whatever replaces it.

## 8. Give cost, scope and reuse a producer (FU-3)

Sits eighth: blocked. Tokens now have producers on every adapter path, but
cost, scope and reuse still have no observable source.

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

Sits last: conditional work — no action while Debian is the deployment
platform.

- Question: the `npx -> node __queue-owner -> npm exec -> node <agent>-acp`
  chain (TTL queue owner) is UNCONFIRMED on native Windows.
- Evidence: former `follow-ups.md` P3 item (git history); origin is the A1
  live probes.
- Closing: a confirmation run on native Windows, or no action at all.
- Starts only if: production runs on Windows.
