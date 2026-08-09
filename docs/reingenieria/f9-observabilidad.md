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
