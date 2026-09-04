# F9 — Observabilidad y calibración

**Objetivo**: saber qué aporta valor **antes** de afinarlo.

**Por qué va al final y no antes**: el scheduling dinámico, los umbrales y los
perfiles solo se pueden ajustar con datos reales. Afinarlos por intuición es
exactamente lo que produjo la asignación actual de perfiles por dimensión, que la
propia guía §10 admite que es una decisión de coste, no de calidad.

**Criterio de salida de la fase**

1. `sentinel metrics` responde, con datos del store: qué dimensión aporta
   hallazgos confirmados, qué modelo genera ruido, y cuánto cuesta cada hallazgo
   confirmado.
2. Ninguna métrica requiere telemetría externa: todo sale del store local.
3. Al menos un ajuste de configuración por defecto se justifica con datos
   medidos, no con criterio.
4. La retención borra el detalle en vuelo una vez publicado el trabajo, sin
   alterar ningún agregado que `sentinel metrics` reporte (añadido en la
   revalidación T9.0; ver T9.5).

> Revalidar la ficha al abrir la fase. El detalle de esta fase depende de qué
> haya producido F5 y F6 realmente.

## Opening revalidation and execution plan

**Phase status**: Open. T9.0 defines the execution contract; it does not close
F9 or accept any implementation candidate.

### Authority and execution policy

- Durable Runs is the sole execution authority. New metrics live with durable
  execution evidence; legacy `store.Run` must not become a parallel ledger.
- Historical ledgers and streams remain readable. Missing model, usage, cost,
  timing, or scope evidence means unknown, never zero.
- Effective model and usage come from observed adapter evidence. Never infer
  them from requested configuration.
- Harness subagents implement F9. Sentinel never implements it and is not used
  to review this plan. For the agreed final candidate, Sentinel uses the
  repository YAML's configured OpenCode reviewer and profiles for check, slice,
  review, gate, and evidence verification; the plan does not override them.
- Use one writer per task in a dedicated worktree. Use `scout` at High effort
  for read-only mapping. The user explicitly selected OpenCode
  `openai/gpt-5.6-luna` with `reasoning_effort: max` for every implementation
  and review-fix writer. Record the effective identity from execution evidence
  and stop if the requested model or effort was not honored.
- The fixed `sonnet / high` rows and line budgets below are retained as
  historical context. This plan supersedes them; quality and compatibility
  take precedence over advisory size.

### Execution matrix

| Slice | Depends on | Writer / effort | Primary targets | Deliverable and acceptance |
|---|---|---|---|---|
| T9.0 — revalidation | Observed F5/F6 and Durable Runs state | `scout` High, then `task` Medium | This document | Authority, dispatch, compatibility, task, and closure contracts are explicit; F9 stays open. |
| T9.1a — durable schema | T9.0 | OpenCode `openai/gpt-5.6-luna` / Max | `internal/store/execution_metrics.go`, `internal/store/execution_metrics_test.go`, existing `execution_*` storage | Versioned immutable metrics snapshot; historical absence stays absent; unsupported versions fail explicitly; `store.Run` is unchanged. |
| T9.1b — producers | Accepted T9.1a | OpenCode `openai/gpt-5.6-luna` / Max | `internal/execution`, `internal/acpadapter`, `internal/reviewexec`, `internal/store/execution_events.go`, `internal/store/execution_outcomes.go` | Real timing, identity, usage, cost, scope, reuse, and failure facts reach one final snapshot without inference or retry double-counting. |
| T9.2 — structured ops events | T9.1b | OpenCode `openai/gpt-5.6-luna` / Max | `internal/ops/events.go` and every `detail` producer/reader | New object payloads and old string payloads coexist in one append-only JSONL stream without rewriting history. |
| T9.3a — aggregator | T9.1b, T9.2 | OpenCode `openai/gpt-5.6-luna` / Max | Existing metrics responsibility or new `internal/metrics` | Pure deterministic aggregates with coverage, numerators, denominators, stable ordering, and no CLI coupling. |
| T9.3b — CLI | T9.3a | OpenCode `openai/gpt-5.6-luna` / Max | `cmd/sentinel` and `internal/metrics` | `sentinel metrics`, `--json`, and help expose local aggregates with stable units and null unknowns. |
| T9.4a — observation window | T9.3b | OpenCode `openai/gpt-5.6-luna` / Max; user/orchestrator operates it | Metrics output and this phase record | Sufficiency thresholds are frozen before collection; insufficient data blocks calibration without moving the thresholds. |
| T9.4b — calibration | Sufficient T9.4a result | OpenCode `openai/gpt-5.6-luna` / Max | Only defaults justified by evidence | At least one default change cites reproducible metric, period, sample, old/new value, expected effect, risk, and rollback. |
| T9.5 — event-driven retention | Accepted T9.1b; reconciled with T9.2 | Implemented directly by the coordinator: both subagent routes hit an exhausted provider | `internal/store/execution_prune.go`, `internal/metrics`, `internal/git`, `cmd/sentinel/retencion.go` triggers (`internal/review/ledger.go` and `internal/ops/events.go` deliberately untouched) | Retention collects the execution streams of published commits; fichas, events and every metrics snapshot survive; nothing is collected before its snapshot exists or while that snapshot disagrees with its stream; aggregates are unchanged from the agreement guard onwards. |
| Phase closure | All exit criteria | `task` Medium for documentation only | This file, `README.md`, architecture deviations, optional debt pool | F9 closes only after local metrics, historical compatibility, one evidence-backed calibration, and final verification. |

### T9.1a — durable metrics schema

Represent run/invocation identity, observed agent/model/effort and source,
total/per-capability/per-agent timing, optional token usage, exact integral
cost plus currency and provenance, full/affected scope and measured savings,
reuse/recomputation, and classified failures.

Required invariants:

- Optional numeric pointers distinguish observed zero from unavailable data.
- IDs, negative durations/tokens/costs, empty required sources, and mismatched
  run identities are rejected.
- Unknown extension fields and future non-empty categorical values remain
  readable when safe; unsupported schema versions return a classified error.
- The snapshot is immutable and written at most once per durable execution. It
  is retained outside `executions/v1/<runID>/` so pruning cannot remove it; see
  the retention decision below. Tests must cover the rejected second write.

Candidate decision: `f9-t9-1a-luna` is the accepted correction base. Both
candidates are preserved on `origin`; neither is merged into `main`.

- Selected: `f9-t9-1a-luna`, rebased onto `origin/main`. Its write-once path
  holds the existence check and atomic rename under the execution lock, so two
  independent `Store` values cannot replace the first snapshot. It also covers
  full-scope savings validation, invocation-only observed identity, historical
  absence, unsupported versions, corrupt records, and strict second writes.
- Rejected: `f9-t9-1a-schema` at `5a59067`. It exposes the same schema API but
  does not make the existence check and rename one locked write-once operation,
  so a concurrent second writer can replace the first snapshot.

The branches are independent implementations and cannot be combined by merge.
The Luna candidate is validated and corrected in place; it is not reimplemented.

Retention decision: metrics snapshots live outside
`executions/v1/<runID>/`. Execution pruning may delete the heavy run directory
without moving or rewriting its immutable snapshot, and `ReadExecutionMetrics`
remains usable after that deletion. `SaveExecutionMetrics` still requires an
admitted execution. A focused test must prove the snapshot remains readable
after `PruneExecutions` removes the execution.

Checks:

```text
go test -count=1 ./internal/store
go test -race -count=1 ./internal/store
go build ./...
go vet ./...
```

#### T9.1a implementation acceptance — 2026-08-29

The selected Luna candidate was delivered through `c50d6d5`:

- `a603377` — introduced the immutable execution-metrics schema and store.
- `28f3370` — enforced strict write-once behaviour and the corrected schema
  invariants.
- `c50d6d5` — moved retained snapshots outside the prunable execution directory
  and proved byte-identical reads after pruning.

The focused store suite, store race suite, build, and vet commands above passed
on the final candidate. The user explicitly accepted the clean cutover at
`metrics/v1/<runID>.json`; the alternative candidate layout had never reached
`main` or persisted user data, so compatibility code would have preserved an
unshipped implementation rather than a real external contract.

This is an implementation acceptance, not a formal Sentinel closure. The
authoritative review of `c50d6d5` retained a `spec` block requesting migration
from that unmerged candidate layout. Security and design passed; logic and
tests were advisory. The finding premise was explicitly rejected by the user,
but the historical ledger was not given a machine-readable disposition that
converts the block into a pass. The implementation and its checks are accepted;
the original review record remains blocked by design.

#### T9.1a disposition — 2026-08-31

The Sentinel review ledger is per worktree, not shared: each linked worktree
keeps its own under `.git/worktrees/<name>/vas-sentinel/`, so a review run in a
candidate worktree is invisible from the main checkout even though both share
one Git common directory. The original ficha for `c50d6d5` therefore still
exists, at
`.git/worktrees/f9-t9-1a-luna/vas-sentinel/c50d6d538f72eacae8f62752f5db8aec0d518bfc.json`.
It carries two revisions ending in `block`, with one CRITICAL `spec` finding
marked `confirmed` — the relocation premise addressed below — plus three
warnings. This corrects an earlier claim in this record that the ficha was
gone; only its location was wrong, and the disposition below is unaffected.

A fresh authoritative review was run on the merged commit from the main
worktree, which is why a second ficha for the same commit now exists there:

```text
bin/0.2.0/sentinel review c50d6d5 --timeout 1200
```

Result `block`: `spec`, `logic`, and `design` returned CRITICAL, `security` and
`tests` returned advisory warnings. All three CRITICAL findings share a single
premise — that relocating the snapshot to `metrics/v1/<runID>.json` orphans
snapshots previously persisted at `executions/v1/<runID>/metrics.json`.

That premise is verified inert, and the reason is structural rather than
circumstantial. At `c50d6d5` the only non-test references to
`SaveExecutionMetrics` are its own declaration and doc comment: no production
caller existed. The producers arrived later, in T9.1b, through
`internal/execution/metrics_finalize.go`, which calls
`SaveExecutionMetricsForRevision` — a function `c50d6d5` did not have. During
the entire window in which the legacy layout lived on `main`, no production
path ever wrote a snapshot, so the relocation could not orphan data that was
never produced. Observed store state agrees: zero files match
`executions/v1/*/metrics.json`, and 272 snapshots exist under `metrics/v1/`.

The findings remain literally true about the code — there is no legacy read
fallback — and the change is not reverted to silence them, because adding a
fallback would preserve a path that never held data. The earlier user rejection
of this premise is reaffirmed on this evidence.

Two advisory findings are carried as follow-ups rather than corrected here:

- `security`, `internal/store/execution_metrics.go:150` — a retained snapshot
  now outlives the pruning of its execution, so `ExecutionFailure.Detail` and
  usage/cost data persist beyond the execution-data lifecycle. This is the
  deliberate T9.1a retention decision, but its data-lifecycle consequence must
  be reconciled with T9.5 rather than assumed benign.
- `tests`, `internal/store/execution_metrics_test.go:626` — the retention
  regression test discards the `seedPruneRun` setup error, so a setup failure
  would surface as a later assertion failure.

T9.1a is therefore an implementation acceptance with a recorded, evidence-backed
disposition. It is not a Sentinel pass, and this record does not manufacture
one: the fresh ledger entry for `c50d6d5` stands as `block`.

### T9.1b — producer instrumentation

Map `acpadapter.Result` (`ObservedModel`, `UsageJSON`, `StopReason`,
`Enforcement`), execution attempts/outcomes/retry/recovery, and
`reviewexec.DurableTransport` before editing. Capture monotonic start/end time,
normalize only known usage formats, retain observed identity, classify existing
failure outcomes, and record scope/reuse at their real decision points. Persist
one final snapshot. Cover success, partial/no usage, requested-versus-observed
model, timeout, cancellation, invalid output, provider/process failure, retry,
recovery, fallback, and metrics-write failure.

Run focused tests and race tests for every touched package, then build and vet.

Producer coverage (recorded at implementation). Timing, observed identity,
usage, and classified failures reach the snapshot from real evidence. `Cost`,
`Scope`, and `Reuse` stay nil, and that absence is a determination, not an
omission:

- Cost: no configured adapter reports a price on the wire and the repository
  holds no pricing table, so any value would be invented. `ExecutionCost`
  requires an explicit `Provenance.Source`, and there is none to record.
- Scope: the only real full-versus-affected decision is
  `internal/validation.resolverComando`, which belongs to the deterministic
  validation gate, not to an agent execution. `ExecutionMetrics` is keyed by
  durable run ID, so recording that decision here would attribute it to a run
  that never took it.
- Reuse: no capability-level reuse path exists. `Controller.Start` rejects a
  duplicate run with `ErrRunAlreadyExists` instead of serving it from a prior
  result, so there is no reuse or recomputation to record.

Consequence for T9.3a: report the three as zero coverage, never as a measured
zero. The producers they would require are recorded as FU-3 in `f0-deuda.md`.

Reviewer note. Review, gate, and `pr review` resolve their agent through
`agentadapter.NuevoAdaptadorConPerfil`, which reads `perfil.Binario` and then
`cfg.ActiveAgent`. `MY_SUB_AGENT` reaches only `NewAgentAdapter` and
`NewAgentAdapterParaMensaje`, so it cannot redirect a review: this candidate
was audited by the OpenCode reviewer the plan fixes, with no deviation.

Accepted findings (T9.1b, recorded at closure). No CRITICAL remains and every
blocked ficha is marked corrected. These stay open with their reason:

- The adapter-site assertions are source substring checks, so they cannot prove
  behaviour. That is the existing repository pattern for pinning a call site
  that has no reachable seam; the behaviour itself is covered by the store and
  controller tests.
- The monotonic-duration test asserts a lower bound over a real interval. Fully
  controlled timing would require injecting a monotonic source into the
  controller, which is a design change rather than a test fix.
- The stale-revision test moves the head before the write instead of exercising
  a concurrent interleaving. The guarantee comes from running the check inside
  `withExecutionLock`, and concurrent write-once is already covered by
  `TestSaveExecutionMetricsConcurrentIndependentStoresWriteOnce`; an assertion
  over the interleaving itself would be non-deterministic.
- The CLI runs test pins the absence of observed identity. That absence is the
  contract for a delegate that reports none; a delegate that does report one is
  covered by the adapter and controller observation tests.

Identity coverage for T9.3a: a run served by a plain CLI adapter contributes no
observed identity, because the configured model and effort are a declaration
rather than evidence. Per-model aggregates therefore cover ACP-served runs
only, and must report the rest as unknown rather than attributing them to the
configured model.

#### T9.1b formal closure — 2026-08-29

Producer instrumentation was delivered in eight reviewable commits ending at
`d970060`:

```text
e4ccfdf  chore(slice): bypass IA for semantic unit 6bfa6b1bf20aaeec
304b418  test(adaptersites): also check refutarHallazgosCriticosConEvidencia for transport routing
91800c2  docs(observability): document T9.1b nil producer coverage for cost, scope, and reuse
ae84ee2  docs(observability): clarify reviewer agent resolution
8a5baa0  fix(observability): report observed effort and repair producer test coverage
42ee955  fix(observability): stop the producer seam from bypassing policy and misfiling failures
9762fcf  fix(observability): complete the policy descent and the fail-closed refutation
d970060  docs(backend): document T9.1b technical debt
```

The correction rounds resolved five CRITICAL and five WARNING findings,
including retry/finalization ordering, revision-locked metric writes,
fail-closed refutation evidence, observed effort propagation, policy descent,
and provider/process failure classification. Every blocked ficha was marked
corrected. The recorded focused and race tests for the touched packages, build,
vet, and the repository build script passed. The final
`sentinel gate --stage pre-push` returned `PASS`, and the durable review runs
used as acceptance evidence were terminal and verified.

The historical closure summary records the verified runs but does not preserve
their individual IDs. That missing report detail must not be reconstructed or
invented. The two accepted design warnings are recorded as FU-4 in
`f0-deuda.md`; they concern dependency direction and producer-seam shape, not
incorrect metric values.

### T9.2 — structured append-only events

Write new `detail` values as typed JSON objects. Read legacy strings, new
objects, and mixed files in original order. Preserve non-JSON legacy text and
unknown object fields. Never rewrite historical `events.jsonl`.

Checks:

```text
go test -count=1 ./internal/ops
go test -race -count=1 ./internal/ops
go build ./...
go vet ./...
```

Reviewer tool failures must surface as the cause (added while accepting
T9.1b; T9.2 verifies it). A reviewer whose search tools fail does not fail
fast: it falls back to reading whole files, exhausts `review.timeout`, and the
dimension lands as `unavailable class=infrastructure` with the real cause
buried inside the raw provider stream.

The observed instance: `ripgrep` was absent, so the OpenCode reviewer's `Grep`
and `Glob` calls returned `ripgrep execution failed`. The `logic` dimension
then read about twenty files whole and died at the 600s budget, twice, and
completed only under `--timeout 1200`. The gate has no timeout flag at all, so
it returned `REVIEW_INFRASTRUCTURE_ERROR` with no operator-visible cause.

This is not a Sentinel dependency: Sentinel never invokes `rg`. It belongs to
the agent CLI, and the two configured families differ — Claude Code embeds
ripgrep, OpenCode shells out to an external one. T9.2's structured detail must
therefore carry the tool failure that actually happened rather than the
provider's raw text, so the operator reads the cause instead of a timeout.

The same requirement applies to the review context provider. `graph.Proveedor-
CodeGraph.Contexto` has six distinct `return nil, nil` exits (mismatched HEAD,
dirty worktree, uninitialized index, mismatched project path, pending changes,
worktree mismatch) and every one of them is silent. Today nobody can tell
whether a review received graph context or not, which is the same absence-versus-
zero conflation this phase forbids everywhere else. T9.2 makes the skip reason
observable; widening what the provider returns is FU-5, not this phase.

#### T9.2 formal closure — 2026-08-30

Structured event details were delivered through three bounded review rounds:

```text
7f907d8  chore(slice): bypass IA for semantic unit 8bfda61e30aa3740
6acaeff  fix(ops): preserve safe structured event details
4d26d9f  fix(gate): isolate persisted operational metadata
2e0cae5  refactor(gate): remove obsolete event wrapper
4da56f9  fix(ops): require structured event details
289aaef  fix(ops): reject nil event details
e3aac29  fix(ops): omit nil event details
```

The final contract is object-only for present new details, while readers retain
legacy strings, mixed ordering, arbitrary historical text, unknown object
fields, and unterminated final records without rewriting the JSONL history. A
nil detail preserves the pre-existing omitted-field representation and never
writes `detail:null`; invalid nested values fail before filesystem mutation.

The full suite, race tests for every affected package, build, vet, and the
repository build script passed after the correction rounds. Sentinel reviews
for `2e0cae5`, `289aaef`, and `e3aac29` each produced four durable runs; all
twelve reached a terminal successful state and verified with intact events.
Every final pre-push gate returned `PASS`.

The final `e3aac29` review retained one non-blocking specification warning:
marshalling occurs before directory creation. The ordering is intentional
because it guarantees invalid details cannot create or mutate the event log.
No blocked finding remains for T9.2.

### T9.3a — deterministic aggregation

Aggregate effective findings, confirmations, refutations, user overrides,
reopens, remediation outcomes, execution metrics, and reuse. Every ratio
returns numerator, denominator, and coverage. Zero denominators produce
unavailable values, never NaN or infinity. Logical retries count once;
superseded findings are not effective findings; currencies are not combined
without explicit normalization. Define deterministic percentile and rounding
rules and stable dimension/model/stage ordering.

Use a hand-verifiable synthetic store containing two dimensions, two models, a
confirmed finding, a refutation, an override, a reopen, successful and failed
remediation, a retry, missing cost, and mixed historical/current data.

#### T9.3a implementation closure — 2026-08-31

The deterministic aggregator is implemented through `ba25dce`. The delivered
history ends with these corrective commits:

- `f5cca78` — skips blank event aliases so a populated fallback run identity is
  not hidden.
- `3247c29` — replaces delimiter-based execution identities with structural,
  deterministic identities and separates event-derived remediation targets.
- `ba25dce` — includes the resolved remediation outcome in the fallback
  identity and directly verifies agent-timing identity preservation in both
  input orders.

Independent verification of the final candidate passed:

```text
go build ./...
go vet ./...
go test -count=1 ./...
go test -count=1 -race ./internal/metrics
go run ./cmd/sentinel check
bin/0.2.0/sentinel gate --stage pre-push --timeout 1200
```

The final gate returned `PASS`; the worktree was clean and the guardian
reported zero authored lines. The two reviews for `3247c29` and `ba25dce`
created ten durable runs. Every run reached `succeeded`, and every
`sentinel runs verify` reported four intact events. The review of `3247c29`
blocked because the first remediation identity omitted the resolved outcome;
`ba25dce` corrected that block and Sentinel marked it as the correction.

One advisory finding remains accepted: the `ba25dce` subject names the
remediation-outcome correction but the commit also strengthens the agent-timing
regression required by the preceding review. Rewriting history solely to split
that review-mandated test would add no behavioural correction.

This is the formal T9.3a closure. The worktree-specific Sentinel ledger retains
historical blocks for `29556b9` and `3196e45` without `fixedIn` values because
the legacy ficha model has no whole-review `obsolete` disposition. Their known
behavioural premises are covered by later code and tests, including `f5cca78`.
The final passing gate is the superseding authority for this task, so those
fichas remain immutable historical evidence but no longer represent active
findings, deferred work, or T9.5 scope.

#### T9.3a correction — disposition coverage — 2026-08-31

Reopened after closure because the aggregates reported 100% coverage by
construction. Several `ratio` call sites passed the denominator as their own
coverage basis, so `coverage()` divided a value by itself. The same store then
contradicted itself: the global row read
`refutation rate: 0/831 (0.00%; coverage 831/831 (100.00%))` while its own
`dimension design` row read `refutation=0/212 (unknown; coverage 0/212
(0.00%))`, and a group literally named `unknown` claimed full coverage. This is
the absence-versus-zero conflation the phase forbids, and T9.4a would have
frozen its thresholds on it.

Landed as `f46e0ad`, merging five commits with `--no-ff` so the reviewed SHAs
keep the receipts recorded against them:

```text
545dcd3  fix(metrics): derive finding-disposition coverage from observed evidence
272a80c  test(metrics): discriminate the three disposition-coverage groupings
f837dc0  fix(metrics): emit unobservable reopen counts as null with their coverage
037c2db  fix(metrics): derive reopen coverage from observed reopen evidence
7b375fd  fix(metrics): model reopen observability independently of the outcome
```

No `ratio(x, d, d, d)` call remains. Confirmation and refutation now derive
their basis from the known-status population; per-model and per-agent counters
gained the `Known` basis they never had. Override keeps full coverage, and that
is now derived per observation with its evidence named: `LeerDecisiones`
(`internal/store/decision.go:71-91`) returns `nil, nil` only for an absent
file, errors on any read failure, and errors on a malformed line rather than
skipping it, so the decisions ledger is complete or the whole report fails.

The JSON `reopened` field became nullable because the T9.3b contract recorded
above requires "null unknowns"; emitting `0` for an unobservable value violated
the contract this phase already wrote.

**How the reopen basis was reached.** Three positions were taken, and the first
two were falsified by evidence rather than by preference. Two of the three were
the coordinator's, which is why the route is recorded and not just the
destination.

| Position | Conflation | Falsified by |
|---|---|---|
| Basis always zero | absence with impossibility | an observed reopen rendered `null` |
| Basis = `Reopened` | observability with outcome | cannot express complete evidence with zero reopens |
| Independent `ReopenResolved` | neither | — |

**Standing block, recorded rather than resolved.** The review of `7b375fd`
returned `block` with two CONFIRMED CRITICAL findings, `design` and `logic`,
both at `internal/metrics/metrics_findings.go:75`. Their shared premise is
**true and is not refuted**: `ReopenResolved` is incremented only inside the
`StatusReopened` branch, `FindingObservation` carries no independent resolution
signal, and therefore `Aggregate` cannot produce a fully resolved population
with zero reopens. The representable state exists on the DTO and is proven by
test, but no production input can reach it.

The remedy is out of scope. An input-level resolution signal has no producer,
so adding one would create a second declared-but-never-written field — exactly
the debt FU-6 records. FU-3 already fixed this precedent for the same class of
problem: "Until then T9.3a must report zero coverage for the three, never a
measured zero." The exported documentation does not overclaim: `ReopenCoverage`
states that "every store production can build today therefore resolves nothing
and reports an unknown count", verified through `go doc`.

Two blocks stand in the per-worktree ledger at
`.git/worktrees/f9-t9-3a-coverage/vas-sentinel/`, not one. `545dcd3` blocked
and carries `fixed_in: f837dc0`, so it is resolved. `037c2db` and `7b375fd`
both stand with no `fixed_in`: the first for the outcome-coupled basis that
`7b375fd` then replaced, the second for the premise above. `7b375fd`
superseded `037c2db`'s finding in substance, but Sentinel did not mark it, so
the record says so rather than claiming a resolution the ledger does not hold.

No pass was manufactured and no correct change was reverted to silence a
finding. FU-7 is what makes the disposition reachable; until then this
correction makes the report truthful, which is a smaller claim than making
exit criterion 1 answerable.

**Verification**, run by the coordinator on the candidate rather than taken
from the implementer:

```text
go build ./...
go vet ./...
go test -count=1 ./...     # 38 packages ok, no FAIL
go run ./cmd/sentinel metrics
bin/0.2.0/sentinel check    # 0 authored lines
```

`internal/execution` failed once under full-suite parallel load, then passed in
isolation and on two later full runs. It spawns real child processes; recorded
as an observed flake, not as a clean single-pass green.

**Deviation.** The phase plan pins OpenCode `openai/gpt-5.6-luna` at
`reasoning_effort: max` for every implementation writer. This correction was
written by a Claude Code subagent because OpenCode was not available in the
coordinating session. The writer's effective identity is therefore not the one
the plan requires, and that is recorded here rather than presented as
compliance.

### T9.3b — `sentinel metrics`

Support:

```text
sentinel metrics
sentinel metrics --json
sentinel metrics --help
help metrics
```

The handler only renders the aggregator. Human output shows coverage and
insufficient-sample warnings. JSON uses stable units, null unknowns, explicit
numerators/denominators, and deterministic ordering. Test empty, historical,
mixed, and unreadable stores; help exits 0 with empty stderr; undeclared flags
exit 1. Smoke-test the three executable command forms with `go run`.

T9.3b closed on the `f9-t9-3b` worktree. `f8beca9` introduced the command,
typed and human renderers, strict arguments, help, store scenarios, and command
smoke coverage. Its review blocked on inconsistent partial-evidence handling.
`e0409e1` aligned human and JSON availability, introduced the typed JSON view,
centralized evidence sufficiency in `metrics.Report`, propagated output errors,
and strengthened command-level tests. Its review found that total cost evidence
was still coupled to the independent per-confirmed ratio. `2051311` corrected
that invariant with `CostAggregate.TotalCoverage`; Sentinel marked both prior
blocked fichas as fixed. `6ace7d8` and `267b2c3` closed the remaining
determinism and cost-warning test gaps, and the latter review returned `ok`.

The implementation used `openai/gpt-5.6-luna`; the execution environment did
not expose a separate agent identity or verified effective reasoning effort.
All 26 durable review runs reached `succeeded` and each `runs verify` reported
four intact events. Independent closure checks passed: `go test -count=1 ./...`,
`go test -race -count=1 ./cmd/sentinel ./internal/metrics`, `go build ./...`,
`go vet ./...`, `./build.sh`, the human/JSON/help/invalid-flag executable smoke
checks, and `bin/0.2.0/sentinel check`. The final
`bin/0.2.0/sentinel gate --stage pre-push --timeout 1200` returned `PASS`.
The non-blocking scope warning on `2051311` is accepted because those additional
tests were required by the preceding Sentinel review; rewriting reviewed
history solely to separate them would add no behavioural correction.

#### FU-7 landed — dispositions are reachable — 2026-09-01

`c1655a2` merges six reviewed commits from `f9-fu7-dispositions`. Exit
criterion 1 is now answerable from the store instead of returning unknown:

| | before | after |
|---|---|---|
| observed | 837 | 838 |
| confirmed | 0 | 78 |
| refuted | 0 | 1 |
| confirmation coverage | `0/837 (0.00%)` | `78/837 (9.32%)` |

Per dimension: `spec` 24, `logic` 26 plus the single refutation, `security` 10,
`design` 10, `tests` 8, `style` 0. The rate values still print `unknown`
because coverage is partial; that is the T9.3a contract working, not a
residual failure.

The `observed` delta of one is the single refuted raw finding that
`aggregation.go:24` drops and that now enters as its own observation. Nothing
was duplicated.

**How it works.** A new observation-only projection,
`Revision.FindingsWithDispositions`, joins the two persisted shapes at the read
boundary on dimension, file, start line and description — the same key
`refutarHallazgoV2` already uses. `Evidence` and `Title` cannot join them
because the v1 shape carries neither, which is also why aggregation's
recomputed fingerprints cannot serve. It follows the precedent
`cmd/sentinel/comandos_runs_prune.go:80-87` set for the same gap.

**`HallazgosEfectivos` is untouched**, verified byte-identical against
`2a273f6` after all six commits, with `git diff` reporting zero removed content
lines in `internal/review/ledger.go`. It gates `riesgos()` and
`BloqueantesDeRama`, so changing it would change branch blocking and
`pr create --force`. The projection is for observation only.

**Refusals, chosen over guesses.** A key matching more than one aggregate
attributes to none; contradictory raw statuses record nothing; absorbed
siblings stay unknown. Under ambiguity a refuted raw finding is suppressed
rather than appended, because "no double counting" was a hard invariant while
"never lose a disposition" was not, and inflating the denominator of every
ratio is worse damage than under-reporting one disposition. Both refusal paths
measure zero incidence on the live ledger.

**Losses, recorded rather than hidden.** Of 85 recorded dispositions, 79 are
counted: two collapse onto one coarse projection fingerprint, and five lose the
pre-existing global dedupe to a later status-less observation of the same
fingerprint under the existing `At` then `Revision` precedence. Both are prior
identity contracts, not regressions introduced here.

**One behaviour change.** The `revision.Fixed` remediation branch now skips
refuted findings; before, a refuted raw finding in a fixed fallback-path
revision emitted a `fixed:` remediation. A refuted finding was never a defect.
Zero live revisions are affected.

**Still broken upstream, deliberately.** `internal/review/aggregation.go:24`
still drops refuted findings and `internal/review/engine.go:501-511` still
writes `StatusConfirmed` onto the v1 collection only. FU-7 scoped the fix to
the read boundary because changing the engine would change what the blocking
gate sees.

**Review outcome.** Six commits, three rounds. `211ac5c` blocked on a
half-applied normalization — the canonical status was computed for the presence
check and discarded, so an aggregate's own status came back as persisted while
raw-path findings came back canonical. `5f9c6df` corrected it and Sentinel
marked it `fixed_in`. No block stands for FU-7. The commit message of `5f9c6df`
additionally claims it fixed a whitespace-only status surviving as present;
that claim is wrong, since `NormalizeStatus("   ")` is already empty and
`211ac5c` had fixed that. The message is left as written because amending it
would change the SHA and invalidate its receipt.

**Deviation.** Same as T9.3a: written by a Claude Code subagent, not the
OpenCode `openai/gpt-5.6-luna` writer the phase plan pins.

### T9.4a — observation sufficiency

Before collecting data, freeze minimum duration, logical-run volume,
per-dimension coverage, effective-identity coverage, usage/cost coverage,
included candidate classes, and retry/failure handling. The values are chosen
from expected operating volume, not invented in this plan. Evaluate the same
store deterministically. Insufficient evidence blocks T9.4b; never lower the
threshold after seeing results.

### T9.4a — observation sufficiency verdict — 2026-09-01

**Route.** No prospective observation window was opened. The task requires
evaluating "the same store deterministically", and three measured facts made a
waiting period pointless: `sentinel metrics` exposes no time filter, so coverage
is cumulative and historical unknowns never leave the denominator; the store
already spans 2026-08-24..2026-09-01 with 850 logical runs at roughly 94 per
day; and every axis that blocks is blocked by a missing or unreadable producer,
which no amount of waiting creates.

**Freeze.** The criteria are frozen in
[`evidence/t9-4a-contract.md`](evidence/t9-4a-contract.md), committed as
`a1a5803` **before** any axis was evaluated against them, so the freeze is
auditable in history rather than asserted in prose. The record discloses that
the baseline was visible when the criteria were set; each criterion is
therefore justified on grounds independent of the observed values — the tool's
own evidence policy, a calendar week, or the aggregator's own definitions.

**The decisive threshold is the tool's, not this plan's.** A rate is
presentable only when `metrics.Ratio.Known()` holds, which requires
`Coverage.Complete()` (`internal/metrics/metrics_types.go:257`, enforced at
`cmd/sentinel/metrics_json.go:244`). Below full coverage the command emits
`null`. Any percentage threshold under 1 would therefore unlock a value the
tool refuses to render, so no such threshold is admissible.

**Evidence.** [`evidence/t9-4a-metrics.json`](evidence/t9-4a-metrics.json) is
the verbatim `sentinel metrics --json` output captured at
2026-09-01T19:49:35Z against a store holding 850 executions, 325 metrics
snapshots, and 207 fichas. It is an immutable record of that moment, not a
re-runnable assertion: the store grows, so a later run returns different
figures. Every number below is derived from that artifact.

#### Verdict

| Axis | Class | Numerator / denominator | Coverage | Verdict |
|---|---|---|---|---|
| Execution duration | C | 325 / 325 | 1 | **sufficient**, censored |
| Success rate | C | 768 / 850 | 1 | sufficient; calibrates no current default |
| Override rate | C | 0 / 837 | 1 | sufficient; the value is 0 and calibrates nothing |
| Per-dimension dispositions | B | 78 / 837 | 0.0932 | insufficient |
| Per-model finding attribution | B | 122 / 838 attributed | below 1 | insufficient |
| Execution identity | B | 0 / 325 | 0 | insufficient; see FU-9 |
| Cost and usage | A | 0 / 325 | 0 | no producer (FU-3) |
| Scope | A | 0 / 325 unknown | 0 | no producer (FU-3) |
| Reuse | A | 0 / 325 | 0 | no producer (FU-3) |
| Reopen | A | 0 / 838 | 0 | no writer (FU-6) |
| Stage latency | A | 0 stages | not applicable | no producer: `timing.by_capability` is empty in all 325 snapshots |
| Remediation | A | 0 attempts | not applicable | no producer exercised |
| Failure classes | A | inadmissible | not applicable | double-counted; see FU-8 |

Class A is not "insufficient sample". Recording it that way would imply that
more observation fixes it, and it does not. Class B means a producer exists but
its evidence is incomplete, so the tool renders `null`.

The duration and volume thresholds are met and are not the binding constraint:
the store spans 9 days against a frozen minimum of 7, and holds 850 logical
runs against a frozen minimum of 500. Retries are not double-counted — 1 run of
850 carries more than one unique outcome.

#### One axis is sufficient, so T9.4b is not blocked

Execution duration has complete coverage over the 325 measured runs, and one
premise was verified in the store rather than assumed: 849 of 850 runs carry
exactly one `invocation_id`, so `ExecutionTiming.TotalDurationNanos` measures a
single reviewer invocation and maps onto `review.timeout`, which was the
600-second value in force at the time of the T9.4a evaluation; T9.4b later raised
it to 900 seconds.

The observed distribution is p50 62.2s, p90 334.3s, p95 413.9s, p99 607.3s,
max 718.7s, mean 116.8s over a 37953.7-second total.

**The censoring caveat T9.4b must carry.** Seven of the 850 logical runs exceeded 600 seconds:
606.4s and 606.7s classified `timeout`, 687.9s classified `unavailable`, and
607.3s, 649.8s, 659.9s and 718.7s classified **`success`**. A run cannot
succeed past a budget that applied to it, so those four ran under a different
effective budget — the phase protocol itself uses `--timeout 1200`. The store
does not record the effective budget per run, so the sample mixes censoring
levels. T9.4b may use this distribution, but must treat it as right-censored
and must not read a quantile as if it were uncensored.

Success rate and override rate also reach complete coverage, but neither
calibrates a current default: 0.9035 corresponds to no configured threshold,
and the override rate is 0 over a store that holds no `decisions.jsonl` at all.

#### RED and GREEN for a task that ships no code

T9.4a produces documentation and an evidence artifact, so no production
behaviour exists for a focused test to exercise. The recorded substitute, agreed
before the record was written, is that every figure the verdict cites must be
derivable from the committed artifact.

[`evidence/t9-4a-check.py`](evidence/t9-4a-check.py) is that check. It parses
this section, extracts the eleven load-bearing figures from
[`evidence/t9-4a-metrics.json`](evidence/t9-4a-metrics.json), and fails if any
is absent or if the artifact describes an empty store. Both its inputs are
committed, so it is reproducible by anyone: `python3
docs/reingenieria/evidence/t9-4a-check.py`. It failed before this section
existed and passes after it.

#### This verdict itself received no semantic review

Sentinel selected zero dimensions for all four of this task's commits, so the
analysis below — the frozen thresholds, the axis classification, and the
`review.timeout` pointer T9.4b will build on — carries no semantic review.

Zero dimensions was the correct scope for a documentation candidate, and
forcing dimensions with `--dims` is prohibited. But the reason the scope was
empty is narrower than "Sentinel judged this content low risk": FU-10 records
that the review planner classifies from paths and symbols alone and never
reads content, so a documentation candidate cannot reach any risk level above
`none` whatever it says. A reader of T9.4b should weigh this analysis knowing
that.

**Update 2026-09-02, after FU-10 was resolved.** The caveat above no longer
applies to work done after that date, and the paragraph is kept as written
because it was true when T9.4a ran. The planner now receives the same evidence
as `explain`, so a documentation candidate reaching `none` today does mean
Sentinel judged its content low risk. The outcome for these four commits is
unchanged and was measured rather than assumed: every one of them still derives
`none` under the full evidence. The narrower reading applies only to the review
scope recorded at the time, not to the conclusion.

### T9.4b — evidence-backed calibration

An allowed change adjusts bundles, severity thresholds, model profiles,
full/affected scope, or remediation policy. Each change records metric, period,
sample and coverage, old/new value, expected effect, risk, and rollback.
Correlation is not presented as causality. Missing effective-model evidence or
insufficient coverage blocks model calibration. Never modify tests, fixtures,
goldens, or verification assets merely to make a calibration pass.

#### T9.4b record — 2026-09-01

**Metric.** Total duration of one reviewer invocation,
`ExecutionTiming.TotalDurationNanos`.

**Period.** `2026-08-29..2026-09-01`, the window in which metrics snapshots
exist.

**Sample and coverage.** 325 measured runs, with complete coverage at `325 / 325`.
The population is homogeneous: every run is a `review <dimension>` operation;
no validation run carries a snapshot.

**Calibration.** The old shipped default is `300s` in
`internal/config/parser.go`, overridden to `600s` by
`.vas_sentinel/vassentinel.yml`. Both values are changed to `900s`.

**Expected effect.** Among the 325 measured runs, exceedance falls from
`12.3% (40/325)` at `300s` and `1.8% (6/325)` at `600s` to `0% (0/325)` at
`900s`. The T9.4a record counts seven runs over 600s because it counts all 850
logical runs; the seventh carries no metrics snapshot and is therefore outside
this denominator.

**Risk.** A genuinely hung provider is detected up to `600s` later than at
`300s`. Sentinel exists to review work, so killing real review work is the worse
failure.

**Rollback.** Restore both values. No schema, storage, or contract change
accompanies this, so a revert is complete.

**Claim boundary.** The claim is that `300s` and `600s` are too low, **not**
that `900s` is provably sufficient. The store records no per-run timeout
budget, so the tail is censored at an unknown mix of levels. Any per-dimension
breakdown is an observation only, not a causal claim about dimensions.

**Retention does not disturb this record (checked 2026-09-04).** T9.5's first
collecting pass moved the success and failure counters, so this calibration was
re-examined against it. It is unaffected, for two reasons that hold by
construction rather than by luck. Its axis is
`ExecutionTiming.TotalDurationNanos`, which lives in the metrics snapshot, and
retention never collects a snapshot. Its denominator is measured runs, which
grew from `325` to `919` with `duration_nanos` coverage at `919 / 919`: the
measured population expanded, so nothing was removed from underneath it. The
counters that did move are success and failure, which this record never uses.
Multi-attempt runs keep their streams under `PruneReasonMultiAttempt`, so
`retried_runs` is not silently deflated either. Re-running the calibration over
the same period reproduces it.

### T9.5 — event-driven retention

**Why this belongs to F9 and not before it.** This phase already forbids
conflating absence with zero: historical ledgers stay readable and missing
evidence is unknown, never zero. Deleting execution evidence before T9.1b
writes its snapshot would therefore degrade the first observation window
permanently, and every aggregate computed afterwards would be measuring a store
someone emptied. The reverse is equally true: once the snapshot exists, keeping
the heavy per-commit detail forever serves nothing. Sentinel exists to control
work IN FLIGHT — review dimensions, correct them, publish with guarantees — so
the detail has no operational reader once the work is published.

**Publication boundary.** A commit is published when it is an ancestor of the
remote base branch (`git merge-base --is-ancestor <sha> origin/main`).

Two rejected alternatives, both verified against this repository:

- NOT "the pull request was merged": `pr-create` has never run here (152
  `review`, 56 `gate`, 1 `pr-review`, 0 `pr-create` recorded events), so a
  PR-only trigger would never fire and nothing would ever be collected.
- NOT "ancestor of local `main`": with a direct-to-main flow the record would
  die immediately after the commit, before `gate` or `pr review` could read it.

The remote boundary is the only one that holds for both flows, and it coincides
exactly with the end of the in-flight window.

**The cascade.** One primitive, two entry conditions, several triggers:

```
commit no longer exists (rebase/amend/squash)  ─┐
commit published in the remote base branch     ─┴─► its execution streams
                                                    (ficha, events and the
                                                     metrics snapshot survive)
```

The deletion primitives already exist and are covered by tests:
`review.Ledger.PurgarHuerfanas`, `ops.PurgeEventosDe`, and
`store.PruneExecutions` together with `collectProvenanceReferences`, which
already computes which invocations remain referenced. Today the cascade is cut:
`status --prune` deletes the ficha and its events but never the runs, which is
why executions accumulate unbounded.

**Triggers.** Commands that already consult the remote — `rebase` and
`gate --stage pre-push`. Retention is best-effort and never blocks or fails the
real operation.

**Invariants.**

- Nothing is deleted whose metrics snapshot has not been written. A missing
  snapshot keeps the execution, exactly as absence is never read as zero.
- The snapshot outlives its execution and stays readable by the aggregator.
- Deletion is the idempotence guarantee: an artifact reaches the metrics exactly
  once, because afterwards it no longer exists. No running counter is needed and
  none may be introduced — that would be the parallel ledger this phase forbids.
- Local, unpublished work is never touched.
- The events half was reconciled with T9.2 as required, and resolved the way
  that slice's append-only contract demands: the events stay. Retention never
  rewrites historical `events.jsonl`, and `PurgeEventosDePRsResueltas` keeps its
  zero callers.
- Agreement, not mere presence, authorizes collection. A run whose snapshot
  contradicts its terminal outcome keeps its stream, because collecting it would
  hand the aggregator a snapshot that reads differently from the stream it
  replaced. See FU-20 for the producer-side question this guard defers.

**Acceptance.** Tests prove that an unpublished commit keeps everything; that a
published commit loses its execution streams while its ficha, its events and
its metrics snapshot remain; that an execution without a snapshot is never
deleted; that an execution whose snapshot contradicts its stream is never
deleted; and that the aggregate `sentinel metrics` reports over the surviving
window is byte-identical before and after retention runs. That last property is
the real guarantee: retention must be invisible to measurement.

**Ratified deviation (2026-09-04): the ficha and the event log stay.** The
clause above originally required a published commit to lose its ficha and
events too. That is unsatisfiable together with byte-identical measurement, and
this phase resolves the contradiction in favour of measurement, which the
paragraph itself names the real guarantee:

- `sentinel metrics` reads findings, dispositions and remediation evidence
  directly from the fichas (`internal/metrics/metrics_reader.go`, `readLedger`).
  Deleting a published ficha would move `Findings.Observed` and
  `Findings.Confirmed` on the spot. Fichas are kilobytes; the growth this slice
  exists to stop is execution streams.
- The event log stays under the reconciliation this section already demanded:
  T9.2 forbids rewriting historical `events.jsonl`, and stage/remediation
  aggregates read it. Retention is the admitted compaction for execution
  directories only.

Vanished commits keep the existing two-step path: `status --prune` deletes their
fichas and events, and the next retention pass collects their now-uncited runs.
An undecidable publication keeps the record's protection rather than reading as
vanished, which would reopen FU-15.

Checks:

```text
go test -count=1 ./internal/review ./internal/store ./internal/ops ./internal/git
go test -race -count=1 ./internal/store
go build ./...
go vet ./...
```

### Final verification and closure

Run all commands from the final candidate worktree:

```text
go build ./...
go vet ./...
go test -count=1 ./...
go test -race -count=1 <explicit touched package list>
bin/0.2.0/sentinel check
```

Expected result: every command exits 0, all packages pass, the race detector
reports no race, and Sentinel reports a successful measurement. Only after the
candidate and its intended paths are fixed may the agreed final slice, review,
gate, and durable evidence verification run. Never change tests or verification
assets to manufacture those results.

Close the phase only when `sentinel metrics` answers from local storage,
historical data remains readable, observed identity is truthful, success and
failure paths are covered, at least one default is calibrated from a
pre-declared sufficient sample, and retention collects published detail without
moving a single aggregate.

That last criterion holds for every pass from the agreement guard onwards, and
it did not hold for the first one. On 2026-09-04 the first collecting pass
moved the success and failure counters once (~150 runs out of success, ~180
into failure) on pre-existing records whose snapshot already disagreed with
their stream. The movement is irreversible and not re-derivable: the streams
that carried the other reading are gone. **Series from before and after
2026-09-04 are therefore not comparable**, and any calibration that spans that
date must state it. `PruneReasonContradiction` closes the hole for every later
pass; FU-20 carries the producer-side question underneath it.

Then retire the phase's branches and worktrees, and not before: every slice
produces its own candidate branch, and deleting one mid-phase loses the context
a later slice may need. Cleanup belongs to closure for the same reason the
guardian promotion does.

Order matters, because a branch whose worktree still exists cannot be deleted:
`git worktree remove <path>` first, then `git branch -d <name>`. Retire only
branches whose outcome is recorded in this file — merged, or explicitly
discarded with its reason. A branch that is neither is unfinished work, not
residue. The worktrees under `.git/vas-sentinel/snapshots/` are Sentinel's own
validation snapshots, purged automatically after 24h by
internal/validation/candidato.go; they are never part of this cleanup.

Open at the time of writing: `f9-t9-0-plan`, `f9-t9-1a-luna`, `f9-t9-1a-schema`,
`f9-t9-1b-luna`, each with a linked worktree under `../vas.sentinel-worktrees/`.
`f9-t9-1a-schema` already carries its rejection reason in the T9.1a candidate
decision, so it may be retired at closure. Then update this file, `README.md`, and
`docs/arquitectura/replanteamiento-objetivo.md`; use `f0-deuda.md` only for
genuine deferred work. Document deviations instead of rewriting history.

Key risks: parallel ledgers, absence/zero conflation, retry double-counting,
configured-versus-observed model attribution, incompatible cost aggregation,
JSONL history breakage, post-hoc sample thresholds, checks run from the
wrong worktree, and retention deleting evidence before its snapshot exists.

---

## T9.1 — Métricas por ejecución

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 280 líneas |
| Depende de | F6 cerrada |
| Commit | `feat(observability): registrar coste, latencia y modelo efectivo por ejecucion` |

**Contexto**: `internal/store/*` (F2), `internal/agents/*` (F5), informe §24

**Hacer**: registrar en cada `run`

```
duración total · duración por capability · duración por agente
tokens in/out · coste estimado · modelo efectivo · esfuerzo
alcance: completo vs afectado, y cuánto se ahorró
caché: reutilizados vs recalculados
fallos: timeout, salida inválida, proveedor caído
```

**Dependencia crítica**: el modelo efectivo debe venir de la sonda de T5.4. Sin
ella, la métrica por modelo miente y toda la fase pierde sentido.

**Aceptación**: test de que un run persiste todos los campos, incluidos los de
fallo.

---

## T9.2 — Evento con detalle estructurado

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 200 líneas |
| Depende de | T9.1 |
| Commit | `feat(ops): detalle estructurado en los eventos` |

**Contexto**: `internal/ops/events.go`, informe **M4**

**Problema**: `detail` es hoy un `string` con JSON serializado dentro
(`comandos_pr.go:138`), lo que obliga a re-parsear para consultar.

**Hacer**: `detail` como objeto, con migración de lectura para las líneas
antiguas. `events.jsonl` sigue siendo append-only.

**Aceptación**: test de que un `events.jsonl` con líneas del formato antiguo y
del nuevo se lee entero sin error.

---

## T9.3 — `sentinel metrics`

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 300 líneas |
| Depende de | T9.2 |
| Commit | `feat(cli): subcomando metrics con agregados del almacen` |

**Contexto**: `internal/store/*`, informe §24

**Hacer**: agregados que responden las preguntas de las §37–38 del enunciado
original.

| Métrica | Qué decide |
|---|---|
| Hallazgos y confirmados por dimensión | Si una dimensión merece existir |
| Tasa de refutación por agente y modelo | Si un modelo genera ruido |
| Tasa de override del usuario por dimensión | Si el umbral de severidad está mal |
| Hallazgos reabiertos | Si las correcciones son reales |
| Éxito de remediación | Si compensa automatizarla |
| **Coste por hallazgo confirmado** | La eficiencia del sistema |
| Latencia p50/p95 por etapa | Si `pre-commit` sigue siendo usable |

Con `--json` para orquestadores, como ya hace `status`.

**Aceptación**: test con store sintético poblado y agregados verificables a mano.

---

## T9.4 — Calibración con datos

| | |
|---|---|
| Ejecuta | Usuario / orquestador, con apoyo de agente |
| Presupuesto | ≤ 200 líneas |
| Depende de | T9.3 + uso real acumulado |
| Commit | `chore(config): calibrar bundles y umbrales con datos medidos` |

**Hacer**

1. Recoger métricas de uso real durante un periodo con volumen suficiente.
2. Ajustar bundles, umbrales de severidad y perfiles de modelo **según lo
   medido**.
3. Documentar cada ajuste con la métrica que lo justifica.

**Regla**: ningún ajuste sin dato. Un cambio de configuración por defecto
justificado con «parece mejor» no entra.

---

## Cierre del plan

Al cerrar F9, actualizar en `README.md` de este directorio la tabla de progreso y
registrar en `docs/arquitectura/replanteamiento-objetivo.md` las desviaciones
acumuladas entre el diseño propuesto y lo construido.

Las desviaciones se documentan. No se ocultan, y no se reescribe el diseño para
que parezca que se cumplió.
