# 06: Add Durable Run Commands

**What to build:** Operators can start, inspect, follow, respond to, abort,
retry, and recover runs without entering an interactive REPL. State-changing
commands are revision-aware and idempotent, logs are resumable through event
sequence cursors, output is stable JSON for automation, and exit codes map to
documented terminal states.

**Blocked by:** 05: Route Review Execution Through The Controller (complete;
merged to `main` at 52b3e86).

**Status:** ready-for-agent

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
