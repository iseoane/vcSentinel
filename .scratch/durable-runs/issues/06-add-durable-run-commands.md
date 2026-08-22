# 06: Add Durable Run Commands

**What to build:** Operators can start, inspect, follow, respond to, abort,
retry, and recover runs without entering an interactive REPL. State-changing
commands are revision-aware and idempotent, logs are resumable through event
sequence cursors, output is stable JSON for automation, and exit codes map to
documented terminal states.

**Blocked by:** 05: Route Review Execution Through The Controller (complete;
merged to `main` at 52b3e86).

**Status:** complete

**Design contract (agreed analysis):**

- Binding follow-up from ticket 05 lands FIRST: one logical job owns N
  attempts. Extend the R3 contracts with retry-decision support so a retried
  invocation stays inside its original run instead of becoming a new run.
  The review transport's bundle/dimension/salt identity keeps producing
  distinct logical jobs per audit key; only retries and fallbacks of the
  same job group under it. No historical records are rewritten.
- Command handlers stay thin over the execution controller. Missing
  controller primitives are added there, not in the CLI layer:
  - `Retry`: relaunch a failed, canceled, or timed-out run as the next
    attempt inside the same logical job, recorded with a retry decision.
  - `Recover`: explicit operator entry point that resumes runs whose state
    is reconstructable from durable evidence (awaiting-decision
    reconstruction already exists); deep interruption classification stays
    in R8 and must not leak here.
  - `Verify`: deterministic integrity check that a run's event log parses
    as normalized events and its derived projection matches an event replay.
- `status` lists runs with lifecycle state and outcome class from the
  derived projections; `logs` pages `ReadEvents` with the existing
  cursor/limit protocol so automation can resume by sequence number.
- Every command supports `--json` with stable field names. Human output may
  exist but machine output never changes shape without a major note.
- State-changing commands (`respond`, `abort`, `retry`, `recover`) carry an
  expected revision; applying against a stale revision fails explicitly
  instead of guessing. Repeating an already-applied action on an unchanged
  revision reports idempotent success rather than an error.
- Exit codes document terminal-state mapping: success for reached-terminal
  outcomes, distinct nonzero codes for not-found, conflict/stale-revision,
  invalid-state, and infrastructure failure.
- Rollback boundary: removing the `runs` command dispatch returns callers
  to pre-R5 while compatibility paths keep using the controller directly.

**Acceptance criteria:**

- [ ] Focused tests prove a retried invocation extends its original run's
      attempt lineage instead of creating a sibling run.
- [ ] Focused tests prove recover resumes a reconstructable awaiting run and
      refuses fabricated completion for unreconstructable evidence.
- [ ] Focused tests prove verify detects a corrupted event log and confirms
      an intact one, including projection-versus-replay equality.
- [ ] Tests prove every command's `--json` shape is stable across repeated
      invocations on identical durable state.
- [ ] Tests prove stale-revision application fails explicitly and repeated
      application at the current revision is idempotent-success.
- [ ] Tests prove log pagination resumes exactly at the returned cursor
      without gaps or duplicates.
- [ ] Exit codes and terminal-state mapping are documented next to the
      dispatch code they constrain.
- [ ] The implementation records build, vet, tests, guardian, independent
      `code-review`, rollback boundary, and follow-ups here.

**Out of scope:** Evidence admission cutover (R6), cancellation process-tree
ownership (R7), startup scanning and interrupted-run classification (R8),
interactive REPL integration, remote/daemon surfaces (D units).

**Suggested slices:**

1. Attempt-grouping contract extension plus controller `Retry`.
2. Controller `Recover` and `Verify` primitives with focused tests.
3. `sentinel runs` dispatch, JSON output, cursors, exit-code documentation.

## Evidence — slice 1 (attempt grouping + controller Retry)

- Commits: e4a8ed3 contracts (+121), 3610a0a controller retry (+179/-21),
  b737005 integration tests (+302). Each staged candidate under the 400-line
  budget; pre-commit hook passed on every commit.
- Contract surface added: DecisionRetry transition rules (failed/canceled/
  timed_out -> Running XOR DecisionRetry via InvalidDecisionError),
  LifecycleState.Retryable(), NewRetryInvocation(parent), extended
  NewRecoveredInvocation(..., parentID, attempt).
- Controller surface: Retry(ctx, runID, expectedRevision uint64) with
  ErrStaleRevision/ErrRunNotRetryable sentinels, cross-process reconstruction,
  idempotent-safe double application.
- Independent code review (dual axis, explore subagents over frozen bundle):
  Standards found oversized controller.go (>500), speculative-generality
  options machinery, Inspect/durableEvidence duplication, string-keyed test
  adapters, redundant cast. Spec found awaiting-reconstruction attempt
  hardcoded to 1 (attempt duplication after fail->retry->awaiting->die->
  recover) and missing map-insert existence recheck.
- All findings fixed: retry machinery split into controller_retry.go
  (controller.go now 474 lines), plain expectedRevision parameter replaced
  the options API, Inspect reuses durableEvidence, both reconstruction paths
  derive attempts through shared countInvocationAttempts, Retry rechecks the
  live-run map under the second lock and cancels the worker context when it
  loses the race, test table carries adapter constructors directly.
- Deferred follow-ups: recovered-invocation six-primitive identity clump
  (pre-existing shape; a recovered-identity type is contract churn better
  landed with slice 2 Recover work); NewNormalizedEvent awaiting->running
  remains Respond-only by design — slice 2 Recover must route resumption
  through respond/abort semantics.
- Verification after fixes: gofmt clean, build/vet OK, full suite green,
  go test -race on execution/agentrun/store/reviewexec green, guardian
  advisory CRITICO only at whole-worktree level before commits.

## Evidence — slice 2 (Recover + Verify)

- Commits: c0d0558 shared ancestry validation (+100/-15), 54baf4e recover and
  verify primitives, b3df8b5 recover resumption tests (+368), c82845b verify
  integrity tests (+104). Every staged candidate passed the pre-commit
  budget; one combined test commit was correctly rejected by the hook and
  split by concern.
- Contract surface: brokenAncestryRule now centralizes the three physical-
  ancestor checks shared by NewInvocationEnvelope and NewRecoveredInvocation;
  each constructor keeps its own error texts. Recovered envelopes carry the
  complete faithful ancestor chain (slice 1 follow-up closed here).
- Controller surface: Recover(ctx, runID, expectedRevision) maps durable
  shapes to exactly one action — awaiting heads reconstruct without adapter
  calls (respond AND abort both proven on fresh controllers), retryable
  terminals (failed/canceled/timed_out all tested) delegate to Retry with
  the revision pin travelling through, succeeded/unavailable/running-head/
  empty evidence refuse with ErrRunNotRecoverable or explicit store errors,
  corrupt logs propagate untouched. Verify(ctx, runID) returns
  {Valid, Events, Reason}: chain continuity plus derived-projection-equals-
  replay equality; corruption yields concrete reasons without panics.
- Independent code review (dual axis): spec confirmed awaiting-resume,
  refusal matrix, corruption detection, double-recover rejection; flagged
  missing revision pin on Recover (fixed), untested abort-after-recovery
  (fixed), untested canceled/timed_out classes (fixed), Verify doc hash
  claim (verified TRUE against store scanEventLog/validateFrame which check
  content and predecessor hashes). Standards flagged first-appearance loop
  duplication (fixed via physicalInvocationChain; attempt = len(chain)),
  duplicated ancestry checks in contracts (fixed via brokenAncestryRule),
  deliberate TOCTOU double-read (documented at the switch), recovered-
  identity seven-parameter clump (deferred with slice 1's note as one
  contracts-hygiene item before R6 opens).
- Deferred follow-ups: bundle recovered-invocation identity parameters into
  a dedicated type once the CLI surface freezes at slice 3.
- Verification after fixes: gofmt clean, build/vet OK, full suite green,
  race clean on execution/agentrun/store/reviewexec.

## Evidence — slice 3 (CLI dispatch) and closure

- Commits: e02f6d4 store listing + not-found sentinel (+27), 9c891c5 shared
  declarations and read-only inspection (+354), ea2c308 dispatch with
  documented exit codes (+398 incl. docs/runs-cli.md), ae08995 mapping/
  pagination/json-stability tests (+316), 94a64e6 respond/retry/verify
  end-to-end tests (+178), then fix round: idempotent repeats + help +
  stable arrays (+139/-80) and English locals refactor (+132/-28).
- Surface: eight subcommands dispatched from main.go; exit codes 0-5 mapped
  next to runExitCode and mirrored in docs/runs-cli.md; --json shapes stable;
  logs cursor resume gap-free; start resolves the same profile factory as
  review/gate/pr.
- Independent code review (dual axis) round: spec flagged the promised
  idempotent-success as unimplemented (real contract contradiction),
  shape-stability proven only for status, help/usage omitting runs, false
  MY_SUB_AGENT doc wording, events:null on exhausted pages. Standards flagged
  Spanish identifiers in brand-new files, a one-line middle man, duplicated
  encode-error blocks, oversized pre-existing main.go (1093 lines, +6 here).
- All spec findings fixed: narrow honest idempotency implemented at CLI
  layer (abort on settled run, retry/recover on live attempt emit the SAME
  JSON shape as first application; repeated respond stays exit 4 by design
  because responses extend lineage — documented in docs and below), logs and
  verify stability tests added, runs added to usage/help (width test kept
  green), doc wording corrected, exhausted pages emit [].
  Standards fixes: salida/comoJSON renamed to out/asJSON, start indirection
  merged. Accepted judgement calls recorded as follow-ups: consolidated
  encode-error helper and per-action handler skeleton refactor (mechanical,
  lands with R6 cutover which rewrites these paths), buildReadonlyController
  dual return (deliberate: read-only commands must not require configured
  agents), observeUntilSettled unbounded poll (R7 owns cancellation and
  process ownership), N+1 projections in status listing (operator scale),
  AdaptadorPrompt fat-interface stub noise in one test double,
  parseRunOptions accepting irrelevant known flags per subcommand.
- Decision recorded for veto: idempotency semantics defined as
  goal-satisfaction (abort=run settled; retry/recover=a live attempt
  exists); respond excluded because lineage growth is never a no-op.
- Final verification snapshot: gofmt clean, build/vet OK, full suite green
  across 22 packages, race clean on touched packages, guardian clean tree.

**Closed:** R5 complete — operators manage durable runs without the REPL;
attempt-grouping follow-up from ticket 05 landed as slice 1.
