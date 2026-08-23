# `sentinel runs` — Durable Run Commands

Operator entry points over the durable execution controller
(`internal/execution`). Every subcommand is thin: it parses flags, delegates
to the controller or store, maps errors through one exit-code function, and
formats output.

## Subcommands and flags

| Command | Flags | Behavior |
| --- | --- | --- |
| `runs start` | `--prompt <text>` (required), `--policy-id <id>` (default `operator`), `--json` | Admits a run through the configured agent chain (same profile resolution as the review, gate, and pr commands) and waits until the run reaches a settled state (`awaiting_decision` or terminal). The admission candidate is salted per invocation, so two identical prompts never collide on run identity. |
| `runs status` | `[--run <id>]`, `--json` | Without `--run`: list every run under the executions directory with lifecycle state, outcome class, and revision from derived projections. With `--run`: full inspection summary. Read-only; works without any agent configured. |
| `runs logs` | `--run <id>` (required), `[--after <cursor>]`, `[--limit N]` (default 100), `--json` | Pages the validated event stream. `--after` is an exclusive revision cursor; resume automation with the returned `next_cursor`. |
| `runs respond` | `--run <id>`, `--text <answer>`, `--json` | Applies a response to an awaiting decision and waits for the resulting attempt to settle. |
| `runs abort` | `--run <id>`, `--json` | Applies abort. Cancellation is cooperative, so this command does **not** wait for the worker to settle. |
| `runs retry` | `--run <id>`, `[--expected-revision N]`, `--json` | Relaunches a retryable terminal (`failed`, `canceled`, `timed_out`) as the next attempt inside the same run identity; waits for the new attempt to settle. |
| `runs recover` | `--run <id>`, `[--expected-revision N]`, `--json` | Explicit operator resume of reconstructable durable evidence: awaiting heads are reconstructed without adapter calls; retryable terminals delegate to retry. Prints the resulting state after settling. |
| `runs verify` | `--run <id>`, `--json` | Deterministic integrity check: event-log hashes, chain continuity, lineage boundaries, and derived-projection-versus-replay equality. |

Unknown subcommands and unknown/incomplete flags exit `1`; bare `sentinel runs`
prints usage and exits `1`.

## Exit codes and terminal-state mapping

Documented next to the dispatch in `cmd/sentinel/comandos_runs_decls.go`
(`runExitCode`) and mirrored here:

| Code | Meaning | Mapped errors |
| --- | --- | --- |
| `0` | Success: operation completed, including an idempotent repeat of an already-applied action at the current revision, and a valid `verify` verdict | — |
| `1` | Usage error: unknown subcommand/flag, missing required flag | — |
| `2` | Run not found: the `--run` identity has no durable execution record | `store.ErrExecutionNotFound` |
| `3` | Stale revision: stream head moved past `--expected-revision` | `execution.ErrStaleRevision` |
| `4` | Invalid state: the action cannot apply in the current lifecycle state | `ErrRunNotActive`, `ErrRunNotRetryable`, `ErrRunNotRecoverable`, `ErrDecisionNotPending`, `ErrUnsupportedAction`, `ErrRunAlreadyExists` |
| `5` | Infrastructure failure: corruption, unreadable evidence, unavailable adapter chain, any other controller/store failure | everything else, including `store.ErrEventCorrupt` and incomplete-tail errors |

`verify` folds every read failure except not-found into its integrity verdict:
an invalid verdict prints a concrete reason and exits `5`.

## Stable JSON shapes

Machine output never changes shape without a major note. Field names:

- `start`, `retry`, `recover`

  ```json
  {"run_id": "...", "job_id": "...", "invocation_id": "...", "state": "succeeded"}
  ```

- `respond`

  ```json
  {"run_id": "...", "invocation_id": "...", "accepted": true, "state": "succeeded"}
  ```

- `abort` (no waiting, so no `state` field)

  ```json
  {"run_id": "...", "invocation_id": "...", "accepted": true}
  ```

- `status` (list mode)

  ```json
  {"runs": [{"run_id": "...", "state": "running", "outcome_class": "", "revision": 3}]}
  ```

  `outcome_class` is empty until the projection reaches a terminal state
  (`success`, `failure`, `cancellation`, `timeout`, `unavailable`).
  `orphaned_cancellation` is an optional additive field: present and true
  only when restart reconciliation classified the run as canceled-orphaned
  (owner died mid-cancellation); it stays omitted for every honestly settled
  or still-recoverable run.

- `status --run <id>` (detail)

  ```json
  {
    "run_id": "...",
    "job_id": "...",
    "state": "awaiting_decision",
    "sequence": 4,
    "revision": 4,
    "event_count": 4,
    "outcomes": [{"run_id": "...", "job_id": "...", "invocation_id": "...", "lineage_id": "...", "class": "success", "error": "", "output_hash": "...", "at": "..."}],
    "responses": [{"run_id": "...", "parent_invocation_id": "...", "invocation_id": "...", "lineage_id": "...", "response_hash": "...", "at": "..."}]
  }
  ```

  `outcomes` entries reuse the store's `AttemptOutcome` JSON contract;
  `responses` entries reuse `InvocationResponse`. Optional fields are omitted
  when empty. Like the list mode, `orphaned_cancellation` appears (true)
  only when restart reconciliation classified the run as canceled-orphaned;
  when it does, `state`, `sequence`, and `revision` all come from the
  reconciled view.

- `logs`

  ```json
  {
    "run_id": "...",
    "after_cursor": 2,
    "limit": 100,
    "events": [{"sequence": 3, "revision": 3, "from": "admitted", "to": "running", "decision": "start", "content_hash": "..."}],
    "has_more": true,
    "next_cursor": 3
  }
  ```

  `events` frames use the store's stable `EventFrame` tags. `next_cursor` is
  `null` when the log is exhausted; otherwise it names the exclusive cursor
  for the next `--after` value, so resumed reads see no gaps and no duplicates.

- `verify`

  ```json
  {"valid": true, "events": 4, "reason": ""}
  ```

## Notes

- Idempotency is per action and honest about durable state: repeating
  `abort` on a run that already reached any terminal state exits `0` with
  the settled state ("already satisfied"); `retry` and `recover` exit `0`
  when the run already has a live attempt (`state=running`). Every other
  refusal stays explicit — notably a repeated `respond` always fails with
  exit `4`, because each response extends the decision lineage and can
  never be a no-op.
- State-changing commands pin nothing by default; pass
  `--expected-revision N` to fail explicitly (exit `3`) when another writer
  advanced the stream past your observation.
- The controller's `Apply` actions (`respond`, `abort`) do not take a revision
  pin in this slice; only `retry` and `recover` accept `--expected-revision`.
- `start`, `respond`, `retry`, and `recover` block until the run reaches
  `awaiting_decision` or a terminal state because the CLI process must outlive
  its detached worker; exiting earlier would strand the attempt mid-flight.
- Evidence admission (ticket 07) is default-on: when `review.durable_runs`
  routes dimension reviews through the controller, their output only reaches
  verdicts after snapshot binding and output-hash verification pass, and a
  rejected completion surfaces as first-class evidence with the literal
  `admission:` reason prefix — distinct from infrastructure failures in gate,
  review, and pr reporting. Setting `review.evidence_admission: false` in
  `vassentinel.yml` restores lenient acceptance while every run stays fully
  inspectable through these `runs` commands.
- Cancellation escalation (ticket 08) is default-on: when a routed review
  owns its provider process tree, an abort cooperates for the grace budget,
  then escalates to whole-tree termination leaving termination-attempted and
  reaped evidence before the canceled settlement. Setting
  `review.cancellation_escalation: false` keeps cooperative cancellation and
  orphan detection while never signaling beyond the direct child. A run whose
  owner died mid-cancellation is classified canceled-orphaned on next
  observation through restart reconciliation — no fabricated completion, no
  silent resume, and the append-only stream is never rewritten.
