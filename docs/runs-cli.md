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
| `runs recover` | `--run <id>`, `[--expected-revision N]`, `--json`; or `--repair <id>`; or neither (scan) | Three modes. With `--run`: explicit operator resume of reconstructable durable evidence — awaiting heads are reconstructed without adapter calls; retryable terminals delegate to retry; an orphaned-canceled stream (owner died mid-cancellation) materializes its reconciled canceled settlement and relaunches under a fresh invocation identity; every other shape is refused with its reason. Prints the resulting state after settling. With `--repair`: deterministic snapshot rebuild of one classified terminal-unprojected run from its verified stream; events bytes are never touched and every other class is refused with its reason. Without `--run` or `--repair`: read-only recovery scan listing every non-terminal run with its evidence-based class and reason; writes nothing and exits `4` when any entry requires an operator decision. `--expected-revision` is rejected as a usage error on the scan and with `--repair`. |
| `runs verify` | `--run <id>`, `--json` | Deterministic integrity check: event-log hashes, chain continuity, lineage boundaries, and derived-projection-versus-replay equality. |
| `runs prune` | `--older-than <duration>`, `--json` | Explicit operator maintenance (never automatic): removes ONLY terminal execution records whose last event predates the cutoff and that no review provenance references. Every other record is kept with an explicit machine-readable reason. See the retention policy below. |

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

### Recovery scan classes and exit codes

The read-only `runs recover` scan classifies every non-terminal run from
verified evidence only:

| Class | Meaning |
| --- | --- |
| `recoverable` | Awaiting-decision head with intact decision evidence; resume reconstructs the pending decision without an adapter call. |
| `terminal_unprojected` | A verified terminal frame exists but the persisted snapshot lags it; `--repair` rebuilds the snapshot from the stream. |
| `corrupt` | Hash-chain break, invalid transition, or unreadable tail; the reason carries the exact underlying error text. Never silently repaired. |
| `orphaned_canceled` | Owner death during cancellation (R7 reconciled view); final unless the operator retries explicitly. |
| `operator_required` | Evidence cannot decide between outcomes; the reason names the exact missing evidence. |

Exit semantics are deliberate: a corrupt-only scan still exits `0` because
the scan is informational — repairing bytes is a separate operator decision,
and `runs verify` remains the integrity verdict that owns exit `5`. Only an
`operator_required` row escalates the scan to exit `4`, so automation notices
when a human decision is actually required.

For resume specifically, `orphaned_canceled` splits by tail integrity: an
intact escalation tail materializes its reconciled settlement and resumes,
while a torn escalation tail (crashed before its final newline) fails closed
as corruption on resume and surfaces the exit `5` verdict `runs verify`
owns. This scan-versus-resume asymmetry for `orphaned_canceled` rows follows
from the exit semantics above: the scan only reports what it sees, but a
resume attempt must never rewrite unreadable evidence.

## Resume-creates-new-invocation guarantee

Recovering or retrying after owner loss never reuses an interrupted attempt's
identity: every relaunch derives its attempt through `NewRetryInvocation`, so
the resumed work carries a fresh invocation identity inside the original run,
job, and lineage. The interrupted attempt keeps its durable record — for an
orphaned-canceled run, its reconciled cancellation outcome stays inspectable
while the new attempt runs — and nothing ever claims the old attempt
completed.

## Stable JSON shapes

Machine output never changes shape without a major note. The table below is
the ONE consolidated reference for every stable JSON shape across `runs`
commands. It is derived manually from the Go structs in
`cmd/sentinel/comandos_runs_decls.go` (`runActionResult`,
`applyResultOutput`, `runsVerificationOutput`, `runsListEntry`,
`runsStatusSummary`, `runsLogsOutput`, `runsRecoveryRow`, `runsRecoveryOutput`,
`runsRepairOutput`, `runsPruneOutput`) and the store contracts they embed
(`store.AttemptOutcome`, `store.InvocationResponse`, `store.EventFrame`,
`store.PruneDecision`). Keep this table in sync with those structs by hand;
additive `omitempty` fields are the only allowed evolution.

Types: `string`, `bool`, `number` (JSON number; Go `uint64`/`int`), `time`
(RFC 3339 string), arrays, and `null` where noted. "always" means the field
is serialized on every emission of that shape; "omitempty" means Go's
`json:"...,omitempty"` semantics — the key is absent when the value is the
zero value.

| Shape (command) | Field | Type | Presence | Semantics |
| --- | --- | --- | --- | --- |
| result (`start`, `retry`, `recover --run`) | `run_id` | string | always | Durable run identity. |
| result | `job_id` | string | always | Logical job identity shared by every attempt in the run. |
| result | `invocation_id` | string | always | Invocation identity of the settled attempt. |
| result | `state` | string | always | Lifecycle state after settling (`awaiting_decision` or terminal). |
| apply result (`respond`, `abort`) | `run_id` | string | always | Durable run identity. |
| apply result | `invocation_id` | string | always | Identity bound to the applied control action. |
| apply result | `accepted` | bool | always | Whether the controller applied the action. |
| apply result | `state` | string | omitempty | Present only when the command waited for a transition (`respond`); `abort` returns without waiting, so it has no `state`. |
| verification (`verify`) | `valid` | bool | always | Integrity verdict over hashes, chain continuity, lineage boundaries, and projection equality. |
| verification | `events` | number | always | Number of validated events. |
| verification | `reason` | string | always | Empty when valid; the concrete failure otherwise. |
| list wrapper (`status`) | `runs` | array | always | Empty array, never null, for an empty store. |
| list entry (`runs[]`) | `run_id` | string | always | Durable run identity. |
| list entry | `state` | string | always | Lifecycle state from the reconciled projection. |
| list entry | `outcome_class` | string | always | Empty until terminal (`success`, `failure`, `cancellation`, `timeout`, `unavailable`). |
| list entry | `revision` | number | always | Stream head revision. |
| list entry | `orphaned_cancellation` | bool | omitempty | Additive (ticket 08): true only when restart reconciliation classified the run as canceled-orphaned. |
| summary (`status --run`) | `run_id` | string | always | Durable run identity. |
| summary | `job_id` | string | omitempty | Absent only for a stream with zero events. |
| summary | `state` | string | always | Lifecycle state (reconciled view when applicable). |
| summary | `sequence` | number | always | Head event sequence (reconciled view when applicable). |
| summary | `revision` | number | always | Head revision (reconciled view when applicable). |
| summary | `event_count` | number | always | Total events read by the inspection. |
| summary | `outcomes` | array of outcome | always | Admitted invocation outcomes in completion order. |
| summary | `responses` | array of response | always | Control responses bound to child invocations. |
| summary | `orphaned_cancellation` | bool | omitempty | Same additive semantics as the list entry; when present, `state`, `sequence`, and `revision` all come from the reconciled view. |
| outcome (`outcomes[]`) | `run_id`, `job_id`, `invocation_id`, `lineage_id` | string | always | Attempt identities. |
| outcome | `class` | string | always | Terminal outcome class of the attempt. |
| outcome | `error` | string | omitempty | Adapter error text; absent on success. |
| outcome | `output_hash` | string | omitempty | Hash binding raw output to this invocation; absent when none was produced. |
| outcome | `at` | time | always | Completion instant. |
| response (`responses[]`) | `run_id`, `parent_invocation_id`, `invocation_id`, `lineage_id`, `response_hash`, `at` | string/time | always | Response binding record; no optional fields. |
| logs (`logs`) | `run_id` | string | always | Echoed run identity. |
| logs | `after_cursor` | number | always | Exclusive cursor the page started at. |
| logs | `limit` | number | always | Applied page limit (default 100). |
| logs | `events` | array of frame | always | Validated frames; empty array, never null, past the head. |
| logs | `has_more` | bool | always | Whether another page exists. |
| logs | `next_cursor` | number \| null | always | Exclusive cursor for the next `--after`; null once the log is exhausted. |
| frame (`events[]`) | `sequence`, `revision` | number | always | Contiguous positions; they advance together. |
| frame | `at` | time | always | Event instant. |
| frame | `run_id`, `job_id`, `invocation_id`, `lineage_id` | string | always | Event identities. |
| frame | `parent_invocation_id` | string | omitempty | Present only on continuation events. |
| frame | `response_hash` | string | omitempty | Present only on response continuations (implies `parent_invocation_id`). |
| frame | `outcome_class`, `outcome_error`, `output_hash` | string | omitempty | Terminal evidence, present only on terminal events (legacy streams may omit them; the class then derives from the lifecycle state). |
| frame | `from`, `to`, `decision`, `terminal` | string | always | Transition endpoints, driving decision, and terminal class. |
| frame | `predecessor_hash` | string | omitempty | Empty only on the first frame. |
| frame | `content_hash` | string | always | Self-validating hash of the frame content. |
| recovery scan (`recover`) | `recoveries` | array of row | always | Empty array, never null, when nothing requires recovery. |
| recovery row (`recoveries[]`) | `run_id` | string | always | Non-terminal run identity. |
| recovery row | `class` | string | always | One of the scan classes below. |
| recovery row | `reason` | string | always | Exact evidence or missing evidence behind the verdict; the underlying error text for the corrupt class. |
| recovery row | `head_sequence` | number | always | Verified stream head sequence. |
| recovery row | `reconciled` | bool | omitempty | True only when the verdict derives from R7 restart reconciliation. |
| repair (`recover --repair`) | `run_id` | string | always | Repaired run identity. |
| repair | `class_before`, `class_after` | string | always | Classifier verdict before and after the rebuild. |
| repair | `rewritten` | bool | always | False when replay already matched the snapshot byte for byte. |
| prune report (`prune`) | `cutoff` | time | always | The applied cutoff instant (`now - --older-than`). |
| prune report | `examined`, `pruned`, `kept` | number | always | Counts over every execution record found. |
| prune report | `decisions` | array of decision | always | One entry per examined record; empty array, never null. |
| decision (`decisions[]`) | `run_id` | string | always | Examined run identity. |
| decision | `action` | string | always | `pruned` or `kept`. |
| decision | `reason` | string | always | `pruned`, or a stable retention reason: `terminal-recent`, `non-terminal`, `corrupt: …`, `unreadable: …`, `incomplete or corrupt admission record`, `awaiting metrics snapshot`, `multiple attempts: retry evidence lives only in the event stream`, `orphaned-canceled recovery evidence`, `review provenance references invocation <id>`, `parent of surviving run <id>`, `removal failed: …`. A successful removal of a crash-interrupted leftover reports `prunable-remnant` instead of plain `pruned`. |

## Retention and purge policy for executions

Nothing in sentinel ever purges durable execution records automatically.
Retention is append-only evidence by default, and shrinking it is always an
explicit operator action:

- **What purges:** `sentinel runs prune --older-than <duration>` removes a
  whole per-run directory under `executions/v1/<run_id>/` only when ALL of
  the following hold:
  - its immutable admission records exist and decode;
  - its event stream scans clean with no incomplete final tail;
  - the honest reconciled view is terminal and NOT canceled-orphaned;
  - its last event predates the cutoff (`now - duration`);
  - none of its invocation identities is cited as review provenance by any
    persisted finding blob or review ledger ficha (per-dimension results or
    aggregated findings);
  - its immutable metrics snapshot exists — a missing snapshot is absence
    of evidence, never a zero, so the stream stays until measurement
    survives it (T9.5);
  - it settled in a single attempt — retry multiplicity lives only in the
    event stream, which the snapshot does not record (T9.5);
  - it is not the parent linkage target (`parent_run_id`) of any surviving
    run — a gate root referenced by a live child survives even when it is
    itself old and terminal.
- **What is retained forever** (unless the operator edits the store directly,
  which is unsupported):
  - non-terminal runs of any age — they are recoverable state, not garbage;
  - orphaned-canceled streams — their canceled settlement exists only as
    derived evidence over those exact bytes until an explicit `recover`
    settles them;
  - corrupt records and incomplete tails — refused with explicit reasons;
    repair them through `runs verify` / `runs recover --repair` first, never
    by deleting bytes;
  - anything referenced by review provenance, so no persisted finding ever
    loses the evidence that produced it;
  - parents referenced by surviving runs.
- **How it removes:** whole directories only, under the run's cross-process
  event lock, with re-verification inside the lock. Prune never rewrites the
  bytes of a surviving record; the append-only guarantee applies to
  everything it keeps.
- **Crash-interrupted removals complete on the next prune:** removal deletes
  the record's children first and its event-lock file plus directory last;
  a crash in between leaves a directory holding only that lock file. Such a
  remnant carries no admission record and no event bytes, so prune
  classifies it as `prunable-remnant` and finishes removing it on the next
  pass regardless of age. A directory missing `request.json` but still
  holding event bytes is never treated as a remnant and stays refused.
- **Relationship to other retention surfaces:** the operations events log
  (`internal/ops`, surfaced by `status --prune`) is a separate, selective
  purge keyed by commit SHA, never by age; execution pruning never touches
  it, and ops purging never touches executions.

## Compatibility and migration policy

Persisted shapes across R1–R10 changed additively only:

| Change | Kind | Compatibility contract |
| --- | --- | --- |
| `ExecutionRequest.parent_run_id` (gate root→child linkage) | additive field, `omitempty` | Old records never carry it; old readers ignore unknown keys; parentless runs keep byte-identical persisted shape. |
| Transient lifecycle states `terminating` / `terminated` plus their decisions | additive transition kinds | They appear only in streams written after ticket 08; old stores never contain them, and new binaries validate old stores unchanged. |
| `RunProjection.orphaned_cancellation` | read-time only | Never persisted: every writer derives projections with it false, and `omitempty` keeps `state.json` bytes identical to the legacy shape. |
| Review provenance `invocation_id` on findings/dimension results | additive field, `omitempty` | Legacy direct-path findings stay empty; fingerprints deliberately exclude it. |

Old-store guarantee: stores written by any earlier release keep scanning,
projecting, and operating unchanged. Readers ignore unknown JSON keys and
zero-value missing keys; nothing ever rewrites or migrates old bytes in
place; append-only event logs keep validating against their original hash
chain. New capabilities must arrive as additive fields or new files, never
as reshaped existing ones.

## Minimum supported schema

R11 removed the two release-bounded switches (`review.durable_runs` and
`gate.durable_runs`) together with their legacy execution paths, but the
removal was a cutover completion, not a format change. The oldest shapes
still fully readable after R11:

- **Durable run streams (pre-R1 through current):** the on-disk stream shape —
  `executions/v1/<run-id>/` with `request.json`, `policy.json`, a hash-chained
  `events.jsonl`, and `outcomes/<invocation>.json` records — is unchanged
  since the first release that wrote it. Streams written before the transient
  `terminating` / `terminated` states existed simply never contain those
  states; every reader validates them unchanged. Focused tests hand-seed a
  minimal settled pre-R9-shaped stream (admission records plus a complete
  created→…→terminal chain with no transient tail) and prove listing,
  request reads, event pages, attempt outcomes, and reconciled projections
  all resolve from store contents alone.
- **Review ledger revisions (pre-R1 through current):** ficha JSON keeps its
  append-only `revisions[]` shape; revisions written before agent/model/effort
  attribution (T0.2) or aggregated findings (T6.1) decode with those keys
  absent, and their per-dimension v1 findings still project into the uniform
  v2 finding shape for reporting and branch analysis.
- **Pre-R1 repositories:** there is no conversion step. A repository created
  before R1 has no `vas-sentinel` directory yet; it simply gains the ledger
  and execution files on demand the first time a review, gate, or `runs`
  command writes them. Nothing is rewritten or migrated.

Upgrade note: configuration yamls that still declare the removed keys
(`review.durable_runs`, or the whole `gate:` section) now FAIL to load with an
explicit unknown-key error naming file and line. Delete those keys when
migrating; durable history already written stays fully inspectable via these
`runs` commands.

## `runs attach` — observing one live run

`runs attach` is the human observation surface over a single durable run.
Its data pipeline is the same everywhere: an Inspect snapshot for the durable
projection plus event replay strictly after the resume cursor
(`--after`, default `0`), merged into one RunView by the pure read-model in
`internal/attach`. Evidence appears as presence and class only — output and
response bytes never surface, only their hashes.

### Modes

| Invocation | Behavior |
| --- | --- |
| `runs attach` (no `--run`) | Lists every non-terminal durable run as an attach candidate (state, revision, head sequence, orphaned-cancellation verdict). The listing walks the reconciled projection directly from the store — read-only, no daemon roundtrip required — and exits `0` on success whether the listing is empty or not; store read failures map through the shared exit-code table below. |
| `runs attach --run <id>` | Prints one plain-text observation snapshot rebuilt from Inspect plus replay after `--after`. This is a point-in-time view: terminal and non-terminal runs alike exit `0`; there is no wait-for-terminal code here. |
| `runs attach --run <id> --follow` | Launches the interactive Bubble Tea attach program over the same pipeline. It repolls from the last applied cursor, survives endpoint loss through bounded reconnect, freezes on a terminal projection, and detaches cleanly on `q`, `ctrl+c`, SIGINT, or SIGTERM. `--follow` without `--run` is a usage error. |

### Keys (`--follow` mode)

| Key | Action | Contract |
| --- | --- | --- |
| `q` / `ctrl+c` | Quit | Clean detach; exits `0` even mid-run. A SIGINT/SIGTERM behaves identically. |
| `r` | Refresh | One immediate observe cycle against a freshly resolved host. Inert while disconnected. |
| `a` | Abort | Same idempotency identity discipline as `runs abort`: a fresh action identity per keypress, cooperative cancellation left to settle, picked up by the next observe. Inert while disconnected or frozen. |
| `e` | Respond | Opens a text input sub-view (`enter` sends, `esc` cancels, `backspace`/`ctrl+u` edit); the typed answer travels verbatim like `runs respond --text`. While the input is open it consumes every other key, so an answer containing `q` or `a` is safe to type. |
| `y` | Retry | Only advertised when the observed state is retryable (`failed`, `canceled`, `timed_out`). Carries `ExpectedRevision` pinned from the last observed stream head, exactly like `runs retry --expected-revision`, so a competing writer makes the relaunch fail explicitly instead of silently double-launching. Retry is deliberately the one key that stays live through terminal freeze — a settled-but-retryable run is what it exists for. |

### Reconnect and terminal freeze

Endpoint loss flips the session into bounded reconnect: exponential backoff
starting at 500ms, doubling up to 8s, hard-capped at 8 attempts. Every attempt
redials through the host provider and resumes the replay strictly after the
last applied cursor, so nothing is duplicated or skipped across the gap. The
status band shows the attempt counter while reconnecting; action keys stay
inert there so a failed action can never schedule a second backoff chain.

Two terminal states end the loop honestly:

- **Run reached a terminal projection:** the view freezes into its final
  state with a `press q to exit` hint instead of polling forever. `y`
  remains available when the state says retryable.
- **Reconnect budget exhausted:** the view freezes into a lost-contact state
  preserving the last observation, and the process reports infrastructure
  failure (exit `5`) instead of looping forever.

### Daemon preference

Every observe and keyboard action resolves its repository host through the
daemon-preferred resolver with silent fallback: when a live repository daemon
is serving this common dir, each resolution dials it fresh (never reusing a
connection that may have died); otherwise the identical operations execute
against the in-process controller over the same store. Keyboard actions thus
produce the same durable effects as their CLI twins regardless of which
transport served them.

### Exit codes

| Code | When |
| --- | --- |
| `0` | List mode (empty or not); snapshot mode; clean TUI detach via `q`, `ctrl+c`, SIGINT, or SIGTERM. |
| `1` | Usage error: unknown flag, `--follow` without `--run`. |
| `2` | The `--run` identity has no durable execution record. |
| `5` | Observation failure of the snapshot path's mapped infrastructure errors; TUI session failure; reconnect budget exhausted while following. |

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
  pin in this slice; only `retry` and `recover --run` accept
  `--expected-revision`. Using it with the read-only recovery scan or with
  `--repair` is a usage error (exit `1`) instead of a silently ignored flag.
- `start`, `respond`, `retry`, and `recover` block until the run reaches
  `awaiting_decision` or a terminal state because the CLI process must outlive
  its detached worker; exiting earlier would strand the attempt mid-flight.
- Evidence admission (ticket 07) is default-on: dimension reviews route
  through the controller, so their output only reaches
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
- Gate durable runs (ticket 11, unconditional since R11): `sentinel gate`
  executes as ONE root durable run with deterministic validation jobs and the
  routed review invocations as children linked through their persisted parent
  linkage, so the gate summary reconstructs from store contents alone. The
  root and every review child share the repository common-dir store. The old
  `gate.durable_runs: false` rollback switch was removed in R11; a yaml still
  carrying it fails strict config load (see "Minimum supported schema" above).
