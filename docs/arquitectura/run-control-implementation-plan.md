# Sentinel Runs: Durable Execution Implementation Plan

## Purpose

Replace opaque reviewer execution with a provider-neutral durable run control
plane that can start, observe, answer, abort, recover, and audit every logical
job and physical agent invocation without moving semantic review authority out
of Sentinel.

This is the canonical roadmap. The local tracker under
`.scratch/durable-runs/issues/` contains the currently published implementation
tickets; it is not a substitute for the complete plan.

## Current State

| Unit | Outcome | Status |
| --- | --- | --- |
| R0 | Gate output exposes admitted blocker evidence and infrastructure reasons. | Complete |
| R1 | Provider-neutral run, job, invocation, lifecycle, and event contracts. | Complete |
| R2 | Durable identities, event stream, projections, recovery, and locking. | Complete |
| R3 | Per-run execution controller with `Start`, `Inspect`, and `Apply`. | Active |
| R4-R11 | Core control-path migration and completion. | Planned |
| A1-A2 | ACP/acpx experiment and optional production adapter. | Planned |
| D1-D5 | Repository daemon, live attachment, TUI, and remote-host expansion. | Planned |

## Non-Negotiable Outcomes

1. A failed, unavailable, timed-out, cancelled, or crashed agent call remains
   visible after the caller and adapter process exit.
2. One root run groups logical jobs, retries, fallbacks, answers, and child work,
   while every physical invocation keeps its own immutable identity and lineage.
3. Operational lifecycle and semantic review outcome remain separate domains.
4. Adapter output is untrusted until Sentinel binds it to the admitted request,
   snapshot, prompt, policy, capability set, and evidence hashes.
5. Existing `review` and `gate` commands become compatibility facades over one
   lifecycle source of truth rather than parallel execution engines.
6. Cancellation owns the complete process tree and records whether termination
   and reaping actually completed.
7. Every state-changing control command is authenticated locally, revision-aware,
   idempotent, and durably receipted.

## Command Contract

The stable user-facing namespace is `sentinel runs`.

```text
sentinel runs start [review|gate] [existing command arguments]
sentinel runs status <run-id> [--json]
sentinel runs logs <run-id> [--follow] [--after <sequence>] [--json]
sentinel runs respond <run-id> <decision-id> --answer <value>
sentinel runs abort <run-id> [--reason <text>]
sentinel runs retry <run-id> [--job <job-id>]
sentinel runs recover <run-id>
sentinel runs verify <run-id>
sentinel runs attach <run-id>
```

Initial delivery does not require every command. The command contract reserves
the durable resource vocabulary so later daemon and TUI work does not rename the
domain or persistence layout.

### Command Semantics

| Command | Contract |
| --- | --- |
| `start` | Admit and persist the immutable request before execution, then return or stream the run identity. |
| `status` | Read a derived projection; never infer semantic verdicts from raw logs. |
| `logs` | Return ordered normalized events with resumable sequence cursors. |
| `respond` | Apply one explicit answer to one pending decision using expected revision and idempotency identity. |
| `abort` | Request cooperative cancellation, terminate owned processes if needed, reap them, and persist the terminal evidence. |
| `retry` | Create a new physical invocation while preserving the original attempt and logical job identity. |
| `recover` | Repair only a provably incomplete terminal write or resume an explicitly recoverable run. |
| `verify` | Recompute hashes and projections and report corruption or provenance drift without mutating semantic outcome. |
| `attach` | Observe and control through the repository host once the daemon/TUI expansion exists. |

## Domain Model

### Run

The root durable identity for one requested operation. It owns the admitted
request, repository identity, candidate snapshot, policy identity, root prompt,
capability policy, lifecycle projection, and all jobs and invocations.

### Job

A logical unit of work inside a run, such as one review dimension, validation
step, correction task, or decision request. A job may have several physical
attempts without changing its identity.

### Invocation

One physical adapter attempt. Every retry, fallback provider, resumed response,
or child execution receives a new invocation identity linked to its logical job
and parent lineage.

### Event

An immutable normalized lifecycle fact. Events are ordered per run, hash-linked,
revisioned, and sufficient to rebuild projections. Provider logs may be attached
as evidence, but they are not lifecycle authority.

### Decision

A durable request for explicit human input. It has an immutable prompt, allowed
answers, creation revision, status, answer receipt, and the invocation that
requested it. Agents never answer a human decision on the user's behalf.

### Controller

The provider-neutral component that admits requests, owns lifecycle transitions,
persists events, invokes adapters, handles control commands, and returns derived
state. It does not decide whether a review finding is semantically valid.

### Adapter

A provider-specific execution port. It receives an invocation envelope and emits
untrusted observations, output, and errors. Sentinel normalizes and admits those
facts into the durable run.

## Identity And Lineage

Every durable record must bind the following identities where applicable:

- repository common-directory identity;
- root run ID;
- logical job ID;
- physical invocation ID;
- parent invocation ID and root lineage ID;
- admitted request hash;
- candidate snapshot and commit/blob identities;
- prompt hash and prompt-schema version;
- policy and capability-policy hashes;
- adapter, binary, model, and reasoning-effort identity;
- event sequence, expected revision, previous hash, and event hash;
- decision and command idempotency identities;
- evidence blob hashes and resulting semantic record identities.

Retries never mutate an old invocation. Fallback never disguises one provider as
another. A response creates an explicit continuation or child invocation linked
to the original decision and attempt.

## Lifecycle Model

Operational lifecycle is distinct from review outcome.

```text
admitted
  -> queued
  -> starting
  -> running
  -> waiting_for_input
  -> cancelling
  -> succeeded | failed | unavailable | timed_out | cancelled | orphaned
```

Not every path visits every state. The transition table in code is authoritative
and rejects invalid or stale transitions.

Semantic outcomes such as `approved`, `blocked`, `refuted`, `confirmed`, or
`infrastructure_failure` are stored separately and may only be produced by the
existing Sentinel admission and review authority.

### Terminal Classes

| Class | Meaning |
| --- | --- |
| `succeeded` | Adapter execution completed and produced output; this does not imply semantic approval. |
| `failed` | Execution completed with a deterministic provider, protocol, or processing failure. |
| `unavailable` | Required execution capability could not be obtained after policy-approved attempts. |
| `timed_out` | The owned execution exceeded its admitted deadline. |
| `cancelled` | Explicit cancellation completed and the process tree was reaped. |
| `orphaned` | Ownership was lost or termination could not be proven; recovery or operator action is required. |

## Capability Policy

The controller admits an explicit `CapabilityPolicy`; adapters do not silently
choose their own privileges.

```go
type CapabilityPolicy struct {
    FilesystemRead   bool
    FilesystemWrite  bool
    Network          bool
    Shell            bool
    ChildAgents      bool
    InteractiveInput bool
    MaxRuntime       time.Duration
    MaxOutputBytes   int64
}
```

The exact Go shape may evolve during R1/R3, but the semantic constraints remain:

- deny by default;
- hash and persist the admitted policy;
- reject adapters that cannot satisfy required restrictions;
- record effective capability and provider identity for every invocation;
- never infer permission from agent profile names.

## Persistence Layout

Persist under `<git-common-dir>/vas-sentinel/runs/<run-id>/` so linked worktrees
share lifecycle truth.

```text
runs/<run-id>/
  request.json
  identity.json
  events.log
  projection.json
  receipts/
  decisions/
  evidence/
  invocations/<invocation-id>/
```

### Storage Rules

1. Immutable records use canonical encoding and content hashes.
2. `events.log` is append-only, sequence-checked, revision-checked, and hash-linked.
3. Projection writes are atomic and can be rebuilt from immutable records and
   accepted events.
4. One operating-system-backed exclusive writer lock protects each run.
5. A live lock is never evicted because of elapsed wall-clock time.
6. Readers do not require the owner process and can page events by sequence.
7. Corruption fails closed. Only a provably incomplete final frame may be
   recovered automatically or through an explicit recovery command.
8. Control-command receipts make repeated requests deterministic and auditable.
9. Evidence blobs are immutable and referenced by hash rather than duplicated.

## Execution Topology

### Initial Topology

One durable supervisor process owns one run. Existing `review` and `gate` callers
may wait synchronously, but caller cancellation detaches observation rather than
silently deleting the durable run.

### Repository Host Topology

After the per-run contract is stable, one repository-local daemon owns admission,
process supervision, attachment, and control for all runs in the repository.

### Remote Topology

Remote hosting is deferred until local daemon authority, transport authentication,
reconnection, replay, and process ownership are proven. Remote execution must use
the same run/job/invocation/event contracts.

## Core Delivery Plan

Each unit is a reviewable vertical slice. Implementation must not begin until its
blocking units are complete and its local ticket exists.

### R0: Expose Gate Review Evidence

**Outcome:** Ordinary gate output explains every admitted blocker and every
infrastructure failure without reading internal ledgers.

**Scope:**

- print effective confirmed CRITICAL findings with identity, dimension, location,
  description, evidence, confidence, and producer;
- preserve per-dimension unavailable reasons;
- retain existing exit codes and semantic authority;
- test blocked, unavailable, refuted, and mixed outcomes.

**Blocked by:** None.

**Rollback:** Remove only the enriched presentation; no stored data changes.

### R1: Define Durable Run Contracts

**Outcome:** Provider-neutral immutable contracts exist for run, job, invocation,
events, transitions, terminal classes, decisions, commands, and lineage.

**Scope:**

- canonical typed contracts and deterministic identities;
- explicit transition validation;
- separation of lifecycle and semantic outcome;
- canonical hashing and schema-version tests.

**Blocked by:** R0.

**Rollback:** Remove the unused domain package while existing review/gate paths
remain unchanged.

### R2: Persist Durable Run Events

**Outcome:** Durable requests and ordered event streams survive writer exit and
are readable across processes.

**Scope:**

- immutable request and identity records;
- append-only hash-linked events;
- expected-revision writes and atomic projections;
- paged reads, receipts, evidence blobs, and explicit final-tail recovery;
- operating-system-backed exclusive writer ownership;
- subprocess tests for live-lock exclusion and release on process exit.

**Blocked by:** R1.

**Rollback:** Leave the isolated store unread by production paths; remove its
directory only when no durable run has been admitted in production.

### R3: Add The Per-Run Execution Controller

**Outcome:** One provider-neutral controller can `Start`, `Inspect`, and `Apply`
commands for a durable run using the R2 store and one adapter attempt.

**Scope:**

- admit request, snapshot, policy, prompt, and capability identity before launch;
- create logical jobs and physical invocation lineage;
- normalize success, failure, unavailable, timeout, cancellation, and adapter
  process errors into durable events;
- inspect from persisted state after the controller process exits;
- support initial response and abort commands through deterministic fake adapters;
- prove caller-context cancellation detaches observation rather than aborting the
  durable run.

**Blocked by:** R2.

**Out of scope:** Daemon, TUI, remote execution, production ACP/acpx, and migration
of every existing review/gate path.

**Rollback:** Remove the controller and tests; preserve R0-R2.

### R4: Route Review Execution Through The Controller

**Outcome:** The review scheduler uses the durable controller for every dimension
while preserving existing review results and ledger compatibility.

**Scope:**

- create one logical job per review dimension;
- record every retry and fallback as a physical invocation;
- preserve scheduler parallelism and configured timeouts;
- translate controller terminal classes into existing dimension results without
  losing concrete provider failures;
- retain append-only review revisions and content-stable finding fingerprints;
- add migration tests comparing legacy and controller-backed outcomes.

**Blocked by:** R3.

**Audit:** Critical milestone; requires independent code review and Judgment Day.

**Rollback:** Feature flag or narrow adapter seam returns review execution to the
legacy scheduler while preserving already-written durable runs.

### R5: Add Durable Run Commands

**Outcome:** Operators can start, inspect, follow, respond to, abort, retry, and
recover runs without entering an interactive REPL.

**Scope:**

- add `sentinel runs start/status/logs/respond/abort/retry/recover/verify`;
- support stable JSON output for automation;
- make logs resumable with event sequence cursors;
- make all state-changing commands revision-aware and idempotent;
- keep command handlers thin over the controller;
- document exit codes and terminal-state mapping.

**Blocked by:** R4.

**Rollback:** Remove command dispatch while compatibility callers continue through
the controller.

### R6: Cut Over Evidence Admission

**Outcome:** Agent output can affect review or gate results only when its durable
invocation provenance and evidence bindings pass Sentinel admission.

**Scope:**

- bind findings and reviewer output to request, snapshot, prompt, policy,
  capability, adapter, model, and evidence hashes;
- reject stale candidate snapshots and mismatched invocation lineage;
- preserve refutation, effective finding, and append-only revision rules;
- expose admission failures as evidence rather than generic unavailable results;
- verify rebases still reuse reviewed content only through existing blob identity
  rules.

**Blocked by:** R4 and R5.

**Audit:** Critical milestone; requires independent code review and Judgment Day.

**Rollback:** Disable strict durable admission only while controller-backed
execution remains observable; never rewrite accepted historical records.

### R7: Own Cancellation And Process Trees

**Outcome:** Abort, timeout, and shutdown have deterministic cross-platform process
ownership and cannot report cancellation while descendants remain unaccounted for.

**Scope:**

- cooperative cancellation first, then bounded escalation;
- process-group or job-object ownership appropriate to Linux and Windows;
- durable cancellation-requested, termination-attempted, reaped, and orphaned
  evidence;
- restart reconciliation for owner death during cancellation;
- subprocess harnesses for child/grandchild trees, ignored signals, races, and
  process exit.

**Blocked by:** R3 and R5.

**Audit:** Critical milestone; requires independent code review and Judgment Day.

**Rollback:** Disable hard termination escalation while retaining cooperative
cancellation and orphan detection.

### R8: Add Recovery And Reconciliation

**Outcome:** Interrupted runs are classified and recovered without fabricating
completion or discarding failed invocations.

**Scope:**

- scan non-terminal runs on startup or explicit recovery;
- distinguish recoverable, terminal-but-unprojected, corrupt, and orphaned runs;
- rebuild projections from verified event streams;
- resume only operations whose adapter and command contracts are idempotent;
- create new invocation identities for retries after owner loss;
- record operator-required states instead of guessing.

**Blocked by:** R5 and R7.

**Rollback:** Keep recovery explicit and read-only; operators can still inspect
and retry manually.

### R9: Route Gate Through Durable Runs

**Outcome:** Gate validation and semantic review are represented by one root run
whose jobs retain deterministic validation evidence and reviewer invocation
history.

**Scope:**

- model validation commands and review dimensions as separate logical jobs;
- preserve validation-before-review ordering;
- keep `--stage` lifecycle context and validation-profile semantics;
- surface validation, review, and infrastructure terminal classes separately;
- retain the current gate CLI and exit-code contract as a compatibility facade;
- prove gate output can be reconstructed from admitted durable state.

**Blocked by:** R6 and R8.

**Rollback:** Route gate back to the legacy orchestration seam while retaining R0
evidence presentation and durable history already written.

### R10: Complete Core Durable Control

**Outcome:** Durable runs are the single execution lifecycle authority for review
and gate, with documented operational recovery and no silent legacy bypass.

**Scope:**

- remove or close remaining parallel lifecycle paths;
- verify all adapter attempts use admitted invocation envelopes;
- complete JSON schemas, command help, operator documentation, retention, and
  purge behavior;
- add end-to-end crash, timeout, abort, retry, response, fallback, and recovery
  harnesses;
- prove linked worktrees observe the same run state;
- define compatibility and schema migration policy.

**Blocked by:** R9.

**Audit:** Critical milestone; requires independent code review and Judgment Day.

**Rollback:** Retain one release-bounded compatibility switch to the R9 facade;
do not restore multiple persistence authorities.

### R11: Remove Core Migration Scaffolding

**Outcome:** Temporary dual-path code, obsolete flags, and migration-only adapters
are removed after one stable release window and evidence confirms no fallback use.

**Scope:**

- remove the release-bounded compatibility switch;
- delete dead legacy scheduler and gate execution paths;
- preserve readers for historical review and durable-run records;
- document minimum supported schema and upgrade path;
- verify install, upgrade, and repository-local hook behavior.

**Blocked by:** R10 and the agreed stability window.

**Rollback:** Revert only cleanup code within the same schema generation; never
downgrade or rewrite durable records.

## ACP/acpx Expansion

ACP/acpx is an optional adapter strategy, not the durable domain or lifecycle
authority.

### A1: Prototype ACP/acpx Capability Mapping

**Outcome:** A throwaway experiment determines whether ACP/acpx can satisfy the
required invocation, permission, cancellation, streaming, and identity contracts.

**Scope:**

- map `CapabilityPolicy` to ACP/acpx permissions;
- verify model, binary, session, and effective-capability identity;
- test output streaming, cancellation, child processes, timeout, and failure
  classification;
- identify protocol gaps without modifying production review paths;
- record whether ACP/acpx is suitable, suitable with constraints, or unsuitable.

**Blocked by:** R3, so the experiment targets a stable controller port.

**Rollback:** Delete the prototype; retain only the decision record and fixtures.

### A2: Add The Production ACP/acpx Adapter

**Outcome:** ACP/acpx is an optional production adapter behind the same admitted
invocation envelope and durable lifecycle semantics as CLI adapters.

**Scope:**

- implement the adapter without leaking ACP concepts into run contracts;
- reject unsupported required capabilities before launch;
- normalize protocol events and preserve raw evidence by hash;
- support cancellation and process ownership through R7 contracts;
- compare outcomes against an existing CLI adapter using contract tests;
- document fallback and operator diagnostics.

**Blocked by:** A1 suitability decision, R6, and R7.

**Audit:** Critical milestone; requires independent code review and Judgment Day.

**Rollback:** Disable ACP/acpx selection and fall back only through explicit
configured adapter policy; durable records remain readable.

## Repository Daemon And TUI Expansion

Do not implement the TUI by polling per-run files as a permanent architecture.
`runs logs --follow` is the initial live interface. The daemon precedes attachment
and TUI work so there is one owner for admission, commands, and event streaming.

### D1: Define The Repository Host Seam

**Outcome:** The controller can run in-process or behind a local repository-host
interface without changing command or persistence contracts.

**Scope:**

- extract the narrow host port for start, inspect, subscribe, and apply;
- define local transport envelopes, authentication context, revisions, and
  idempotency identities;
- keep the in-process host as the default implementation;
- contract-test both sides without starting a background daemon.

**Blocked by:** R5.

**Rollback:** Keep commands bound to the in-process host.

### D2: Add The Repository-Local Daemon

**Outcome:** One authenticated local daemon owns repository run admission,
supervision, attachment, and command serialization.

**Scope:**

- repository-common-dir endpoint discovery;
- single-owner startup and stale-endpoint recovery;
- local-user authentication and repository binding;
- daemon restart reconciliation through R8;
- bounded shutdown that preserves or explicitly orphans active runs;
- Windows and Linux lifecycle harnesses.

**Blocked by:** D1 and R8.

**Audit:** Critical milestone; requires independent code review and Judgment Day.

**Rollback:** Stop daemon autostart and use the in-process host; persisted runs and
transport-neutral commands remain valid.

### D3: Add Attach And The Run TUI

**Outcome:** Humans can attach to a live repository host, observe all jobs and
invocations, inspect evidence, answer decisions, abort, and retry without losing
terminal history.

**Scope:**

- `sentinel runs attach <run-id>` and optional repository run list;
- event subscription with replay from a sequence cursor;
- explicit views for lifecycle, logical jobs, physical invocations, decisions,
  evidence, and semantic outcome;
- keyboard actions routed through revision-aware daemon commands;
- reconnect and terminal-state behavior;
- Bubble Tea tests, golden views, and daemon integration harnesses.

**Blocked by:** D2.

**Audit:** Critical milestone; requires independent code review and Judgment Day.

**Rollback:** Remove the TUI while retaining `status`, `logs --follow`, and daemon
control commands.

### D4: Define The Remote Host Contract

**Outcome:** The repository-host port supports an authenticated remote transport
without changing durable domain objects or granting semantic authority remotely.

**Scope:**

- mutual authentication and repository/run authorization;
- transport-level replay, cursors, deadlines, and idempotency;
- capability negotiation without weakening admitted policy;
- disconnect classification and ownership transfer rules;
- explicit trust boundaries for code, prompts, credentials, and evidence.

**Blocked by:** D2 and a concrete remote-host use case.

**Rollback:** Keep remote transport disabled; local daemon behavior is unchanged.

### D5: Complete Remote Control

**Outcome:** Approved remote hosts can execute and stream durable invocations with
the same evidence, cancellation, recovery, and admission guarantees as local
hosts.

**Scope:**

- remote adapter/host implementation;
- reconnect and replay after network partition;
- remote process-tree cancellation evidence;
- snapshot and evidence transfer by verified content identity;
- end-to-end hostile-network and stale-command tests;
- operator documentation, revocation, and incident recovery.

**Blocked by:** D4, R10, and an approved threat model.

**Audit:** Critical milestone; requires independent code review and Judgment Day.

**Rollback:** Revoke and disable remote endpoints; local durable runs continue
through the same controller and store.

## Dependency Graph

```text
R0 -> R1 -> R2 -> R3 -> R4 -> R5 -> R6 -> R9 -> R10 -> R11
                  |          |      |      ^
                  |          |      -> R7 -> R8 --|
                  |          |
                  |          -> D1 -> D2 -> D3
                  |
                  -> A1 -> A2

D2 + concrete remote use case -> D4
D4 + R10 + approved threat model -> D5
```

Additional edges:

- R6 requires R4 and R5.
- R7 requires R3 and R5.
- R8 requires R5 and R7.
- R9 requires R6 and R8.
- A2 requires A1, R6, and R7.
- D2 requires D1 and R8.

## Verification Contract

Every implementation unit must record truthful evidence for:

1. blocker and frontier status before writing;
2. `sentinel check` before and after the bounded change;
3. focused tests for the unit's behavior and failure modes;
4. `go build ./...`;
5. `go vet ./...`;
6. `go test ./...`;
7. subprocess or runtime harnesses where process boundaries matter;
8. independent review of the real diff rather than the implementer's report;
9. `sentinel slice plan --json` when the review budget requires it;
10. commit identities, rollback boundary, findings, and accepted follow-ups.

Until R10 is accepted, `sentinel review` and `sentinel gate` must not certify the
roadmap that is replacing their execution path. Deterministic build, vet, tests,
runtime harnesses, ordinary independent review, and milestone Judgment Day remain
mandatory.

## Critical Milestones

The following units require the stronger adversarial audit in addition to normal
independent review:

- R4: first production review-path migration;
- R6: evidence and admission authority cutover;
- R7: process ownership and cancellation;
- R10: completion of the core lifecycle authority;
- A2: production ACP/acpx adapter;
- D2: repository daemon ownership;
- D3: live control TUI;
- D5: remote execution and control.

## Ticket Publication Rule

The roadmap is the complete architectural plan. Before implementing a unit, create
or approve a local ticket under `.scratch/durable-runs/issues/` that contains:

- the exact outcome and scope selected from this roadmap;
- blocking ticket identities and confirmed completion;
- behavior-focused acceptance criteria;
- tests and runtime harnesses required for that slice;
- rollback boundary;
- implementation commit, verification evidence, independent findings, and
  follow-ups.

Only frontier tickets should be marked `ready-for-agent`. Publishing a subset of
tickets never truncates or replaces this roadmap.

## Completion Definition

Core durable runs are complete at R10 when:

- every production review and gate invocation has durable run/job/invocation
  lineage;
- failed calls and unavailable providers remain inspectable after process exit;
- commands can observe, answer, abort, retry, recover, and verify runs;
- cancellation owns and accounts for process trees;
- semantic findings are admitted only with bound provenance and evidence;
- one lifecycle authority drives compatibility facades;
- linked worktrees observe the same repository state;
- crash and recovery harnesses pass on Windows and Linux.

R11 removes migration scaffolding after the stability window. A and D families
are separately optional expansions and do not redefine core completion.
