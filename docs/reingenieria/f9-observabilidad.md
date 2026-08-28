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
| T9.5 — event-driven retention | Accepted T9.1b; reconciled with T9.2 | OpenCode `openai/gpt-5.6-luna` / Max | `internal/review/ledger.go`, `internal/store/execution_prune.go`, `internal/ops/events.go`, `internal/git`, `cmd/sentinel` triggers | One cascade deletes in-flight detail for published or vanished commits; every metrics snapshot survives; nothing is deleted before its snapshot exists; aggregates are unchanged. |
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
- The snapshot is immutable and written at most once beside the existing
  durable execution. Tests must cover the rejected second write.

Candidate note: TWO independent unaccepted candidates exist, neither pushed to
the remote. Verify both before choosing; do not reimplement either.

- `f9-t9-1a-luna`, commit `c99caf2`, based on `1e148a5` (this plan, merged).
  336 lines plus 674 of tests, across three commits that explicitly address the
  write-once invariant. Newer and far more thoroughly covered.
- `f9-t9-1a-schema`, commit `5a59067`, based on `ba7bb3f` (predates this plan).
  315 lines plus 280 of tests. This is the candidate the original note named,
  and it is the older of the two.

They are not related: neither branch contains the other's commit, so they are
two separate implementations of the same slice. Pick one, record why, and update
this note.

Retention consequence (added with T9.5): both candidates write `metrics.json`
INSIDE `executions/v1/<runID>/`, so `store.removeExecutionDirectory` would
delete the snapshot together with the execution it measures — destroying exactly
the record T9.5 requires to survive. Resolve this in T9.1a, before T9.5 is
built: either place the snapshot outside the execution directory, or make the
prune path preserve it explicitly. Whichever is chosen, a test must prove the
snapshot is still readable after its execution has been pruned.

Checks:

```text
go test -count=1 ./internal/store
go test -race -count=1 ./internal/store
go build ./...
go vet ./...
```

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

### T9.4a — observation sufficiency

Before collecting data, freeze minimum duration, logical-run volume,
per-dimension coverage, effective-identity coverage, usage/cost coverage,
included candidate classes, and retry/failure handling. The values are chosen
from expected operating volume, not invented in this plan. Evaluate the same
store deterministically. Insufficient evidence blocks T9.4b; never lower the
threshold after seeing results.

### T9.4b — evidence-backed calibration

An allowed change adjusts bundles, severity thresholds, model profiles,
full/affected scope, or remediation policy. Each change records metric, period,
sample and coverage, old/new value, expected effect, risk, and rollback.
Correlation is not presented as causality. Missing effective-model evidence or
insufficient coverage blocks model calibration. Never modify tests, fixtures,
goldens, or verification assets merely to make a calibration pass.

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
commit published in the remote base branch     ─┴─► ficha ─► its events ─► its runs
                                                    (the metrics snapshot survives)
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
- The events half must be reconciled with T9.2 before wiring: that slice forbids
  rewriting historical `events.jsonl`, and `PurgeEventosDePRsResueltas` — dead
  code today, with zero callers — rewrites the file. Either T9.2's append-only
  contract admits compaction explicitly, or the events stay and only the ficha
  and runs are collected.

**Acceptance.** Tests prove that an unpublished commit keeps everything; that a
published commit loses ficha, events and runs while its snapshot remains; that
an execution without a snapshot is never deleted; and that the aggregate
`sentinel metrics` reports over the surviving window is byte-identical before
and after retention runs. That last property is the real guarantee: retention
must be invisible to measurement.

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
moving a single aggregate. Then update this file, `README.md`, and
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
