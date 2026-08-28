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

> Revalidar la ficha al abrir la fase. El detalle de esta fase depende de qué
> haya producido F5 y F6 realmente.

## Opening revalidation and execution plan

**Phase status**: Open. T9.0 revalidates the phase before implementation; it
does not close F9.

### Revalidated architecture boundaries

- Durable Runs is the execution authority. Metrics belong to its durable
  execution evidence, not to a second execution path.
- Legacy `store.Run` must not become a parallel metrics ledger. Readers may
  project compatible historical data, but new execution facts have one
  authoritative durable-run source.
- Effective model and usage may originate in adapter evidence. Persist observed
  evidence when available; do not infer it from a requested model or fabricate
  it when absent.
- Historical data remains readable, including pre-existing ledgers and run
  streams, through compatible readers.

### Dispatch, Sentinel, and model boundary

- F9 implementation is delegated through harness subagents only. Sentinel does
  not implement F9; it is reserved for check, slice, review, gate, and
  execution evidence or validation.
- Sentinel's configured `active_agent: auto` and its profiles are separate from
  harness dispatch. They do not select or promise a harness subagent model.
- Use `codex-rescue` at High effort for substantial implementation, `scout` at
  High effort for read-only mapping, and `task` at Medium or High effort for
  mechanical or local writing according to scope. Each task has one designated
  writer in its own dedicated worktree.
- The harness Task API exposes type and effort, but no model selector. Do not
  invent or hardcode a model identity; record the effective model only when
  execution or adapter evidence exposes it.
- Sentinel determines the applicable review scope from the candidate it
  inspects.
- The legacy fixed `Agent | sonnet / high` rows below are superseded as
  execution metadata and retained only as historical context. Their former
  line budgets are advisory: quality, compatibility, and the acceptance
  boundaries below take precedence.

| Slice | Dependencies | Harness delegation (type / effort) | Sentinel profile boundary | Model-selection rule | Output | Acceptance boundary |
|---|---|---|---|---|---|---|
| T9.0 — revalidation | Observed F5/F6 and Durable Runs state | `scout` / High for mapping, then `task` / Medium as the single writer | `active_agent: auto` is Sentinel-only; no implementation | No Task API selector; effective identity from evidence only | Revalidated F9 plan and task matrix | Records authority, compatibility, dispatch, and review boundaries; F9 remains open. |
| T9.1a — durable schema | T9.0 | `codex-rescue` / High, single writer | Sentinel validates evidence only | No Task API selector; effective identity from evidence only | Durable-run metrics schema and compatible readers | New execution facts have one durable-run source; `store.Run` is not a parallel ledger; historical data stays readable. |
| T9.1b — instrumentation | T9.1a; adapter evidence surfaces | `codex-rescue` / High, single writer | Sentinel validates evidence only | No Task API selector; effective identity from evidence only | Durable-run instrumentation for timing, cost, usage, failures, and effective identity | Persists observed execution facts, including failures, without inventing effective model or usage. |
| T9.2 — structured events | T9.1b | `codex-rescue` / High, single writer | Sentinel validates evidence only | No Task API selector; effective identity from evidence only | Structured append-only event payloads and compatible readers | Old and new event lines are readable together; append-only behavior remains intact. |
| T9.3a — aggregator | T9.1b and T9.2 | `codex-rescue` / High, single writer | Sentinel validates evidence only | No Task API selector; effective identity from evidence only | Data aggregation for the F9 metric questions | Synthetic durable and historical data yields hand-verifiable aggregates; no CLI contract is claimed here. |
| T9.3b — CLI | T9.3a | `task` / Medium for local CLI wiring; High if scope expands, single writer | Sentinel validates evidence only | No Task API selector; effective identity from evidence only | `sentinel metrics`, including structured output | The command exposes the verified aggregates from local stored data. |
| T9.4a — observation window | T9.3b | `task` / Medium, single writer; user/orchestrator operates the window | Sentinel validates evidence only | No Task API selector; effective identity from evidence only | Sample-sufficiency rule and observation-window record | Defines minimum coverage, volume, and duration before observation; insufficient data is explicit and no calibration occurs. |
| T9.4b — data-backed calibration | T9.4a and a sufficient observation result | `task` / Medium for local configuration changes, single writer | Sentinel validates evidence only | No Task API selector; effective identity from evidence only | Measured default-configuration changes with rationale | Cannot proceed on insufficient data; every calibration links to the measured evidence that justifies it. |
| Phase closure | T9.4b and all F9 exit criteria | `task` / Medium, single documentation writer | Sentinel validates evidence only | No Task API selector; effective identity from evidence only | Closure evidence and planned progress/deviation updates | F9 is marked closed only after its exit criteria and closure artifacts are evidenced. |

The phase-closure row is future work. T9.0 does not update the README phase
status or close F9.

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
