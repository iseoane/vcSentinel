# Actionable — work that can start now

Ordered by criticality: work that has stopped a run outright comes first, then
work that publishes something untrue, then work that costs tokens or leaves a
record incomplete, then work waiting on a measurement or on a platform
decision. One line per item states why it sits where it does.

Numbering is historical and deliberately not renumbered, so references from
`future.md` and from `decisions.md` keep pointing at what they name. Items 2,
3, 4, 5, 6, 10, 11, 12, 13, 16, 17 and 18 closed and moved to
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
- **Healthy-run baseline measured 2026-09-17, which this item never had.** A
  five-dimension audit of `35927d8` at `parallel: 2` on this 7.4 GiB host,
  while a Claude Code session was also running: baseline 3524 MB used / 4074 MB
  available / `/tmp` at 339 MB; peak 6125 MB used / **1473 MB available**; end
  4047 MB used / 3550 MB available / `/tmp` at 326 MB. 11m34s, no kill, zero
  unavailable dimensions. Two things follow. `/tmp` stayed FLAT for the whole
  run, which confirms the note below: a review that COMPLETES leaves no
  provider residue. And the margin is thin — `parallel: 2` came within 1473 MB
  of exhaustion on a host carrying one agent session, not the two that were
  present when the kills were recorded. This is the comparison baseline, NOT
  the kill reproduction the item still needs.
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

## 0. Capture a real fix's provenance beyond `fix(`

Sits second: it leaves the record incomplete rather than wrong, and item 13
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

## 19. Reconcile the `glob` denial against the zero-denial audit

Sits third: it costs a whole review dimension when it happens, but it has been
observed once and the evidence needed to reproduce it is not persisted.

Opened 2026-09-17, carved out of item 6 when that item closed by determination
(see [`decisions.md`](decisions.md)). It belongs to the permission boundary,
not to the turn budget, and nothing about the budget's closure resolves it.

- Wrong: a `glob` call was denied once on 2026-09-08 despite its explicit
  `{"*": "allow"}` in the reviewer permission map. That is not the
  relative-path case the determination in `internal/agentadapter/cli.go`
  discusses, and it is not fixed by listing more tools, because `glob` was
  already listed.
- Why it matters: that determination rests on an audit of 31 invocations
  reporting "zero denials". This is a counterexample to the evidence the
  determination is built on, so the determination cannot be trusted until it
  is reconciled.
- It costs a whole dimension when it happens. A denied tool call ends the turn
  at once: OpenCode closes the session itself and reports `stop reason:
  tool-calls`, and `cli_review_context.go:130` only observes that and discards
  the incomplete answer. A rejection cannot be made harmless; it can only be
  avoided.
- Blocked on evidence that is not persisted: `TruncatedTurnError.ToolCallErrors`
  carries the observed `DeniedToolCalls`, but nothing writes it to the store,
  so a truncated run's event stream holds no record of a rejected permission
  and the store cannot attribute any truncation to a denial. Reproducing this
  one needs that attribution.
- Closing: a reproduction that names why an explicitly allowed `glob` was
  denied, or a corrected determination in `cli.go` that accounts for the
  counterexample instead of resting on the zero-denial audit alone.

## 7. Cache shared audit evidence across review dimensions

Sits fourth: partly landed and cost-only. The token measurement now exists on all
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
- **The envelope half landed 2026-09-17 as `4c6fa11`** (see `decisions.md`).
  The ordering was measured first: on `a6da249` a six-dimension audit sent
  98104 bytes sharing a 68-byte prefix, 0.42%, because every prompt diverged
  at the dimension name before any shared evidence. The shared envelope now
  renders first with the dimension contract and output schema as suffix, taking
  the shared prefix to 85% of the shortest prompt on the test fixture. It was
  verified to be a pure reordering: the rendered prompt's line set is
  unchanged.
- Still open, and this is the whole remainder: `provider cache reuse only
  within a group sharing model, reasoning effort and tool definitions (never
  across `cheap`/`normal`/`deep`); a stable cache key from the audited SHA if
  OpenCode exposes it.` Nothing about caching was implemented — only the
  ordering that makes it possible.
- **BLOCKED on a missing measurement, established 2026-09-17 by running it.**
  A live audit of `35927d8` (five dimensions, `parallel: 2`, `active_agent:
  claude`) plus a one-dimension probe on a cold store produced 3065189 cached
  input tokens against 322 fresh ones over six invocations. That number cannot
  answer this item. The COLD probe alone — zero prior invocations — already
  reported 126705 cached reads, so cached reads are dominated by
  intra-invocation multi-turn re-reads and Claude Code's own system-prompt
  cache, not by one dimension reusing another's.
- The blocker is named and verified in code: `claudeUsageProbe`
  (`internal/agentadapter/claude_review.go`) maps `input_tokens`,
  `output_tokens`, `cache_read_input_tokens` and `thinking_tokens`, and its own
  comment records that `cache_creation_input_tokens` "is observed on the wire
  but intentionally unmapped: acpadapter.Usage has no destination for it". The
  persisted observation confirms it, and the raw usage member is never written
  to the store. Without cache CREATION there is no way to tell a dimension that
  READ a previous dimension's cache from one that WROTE its own — which is
  exactly the distinction this item turns on. Giving `acpadapter.Usage` a
  destination for cache creation, and persisting it, was the prerequisite unit.
  **It landed 2026-09-17 as `e647010`..`bb4ce77`** (see `decisions.md`):
  `acpadapter.Usage.CacheWriteInputTokens` now reaches the store and both
  `sentinel metrics` surfaces on the Claude and OpenCode paths, and stays nil
  on acpx, whose captured wire reports no such member. Plumbing only — no
  ratio and no cache-hit rate, because what to compute from the two numbers is
  this item's decision.
- **That audit was run on 2026-09-17 and the answer is NO for the Claude path**
  (see `decisions.md`). Two byte-identical prompts back to back both WRITE the
  ~52907-token body and read none of it; the 11717 they both read is Claude
  Code's own preamble. The six-dimension audit agrees: every dimension writes
  13944-67796 tokens and none writes ~0, while reads track OUTPUT tokens rather
  than prompt sharing. There is no cross-invocation cache to hit, so the
  envelope reorder does not pay here — it stays correct, harmless, and the
  precondition for a path that does share one.
- What is left is therefore a decision, not an implementation: re-measure the
  same way on `opencode run --pure`, which is the path this item was actually
  written about, or record a determination that provider cache reuse is not
  available to Sentinel and close the item on that. Do not implement a cache
  key against a cache that was measured not to exist.
- Premise mismatch to resolve when that lands: this item is written about
  `opencode run --pure`, its snapshot and its cache key, but the configured
  `active_agent` is `claude`, so the run above measured a different provider
  path. Either re-measure on OpenCode or restate the item for the path the
  repository actually runs.
- Latency measured on the way, as a planning reference rather than a closing
  condition: 6m22s for one dimension alone, 11m34s for five at `parallel: 2`.
- Review-equivalence remains unmeasured. Do not cache model outputs or reduce
  dimension coverage.
- Checked and not a blocker: `opencode run --pure` means "run without external
  plugins" and says nothing about sessions or caching.

## 15. Derive the intent from a conversation, as its own change

Sits fifth: not a defect and not blocked — a parked feature that needs a
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

Sits sixth: blocked. Tokens now have producers on every adapter path, but
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
