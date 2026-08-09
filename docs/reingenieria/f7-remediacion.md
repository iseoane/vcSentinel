# F7 — Remediación acotada

**Objetivo**: corregir hallazgos sin que «arregla esto» se convierta en un
refactor oportunista imposible de revisar.

**Criterio de salida de la fase**

1. Un fix que toca archivos fuera del alcance de los hallazgos **se descarta
   íntegro** y se reporta.
2. El agente de remediación **no tiene shell**. La validación la ejecuta Go.
3. Una sola ronda por unidad de cambio. Si tras re-validar quedan bloqueantes, el
   resultado es `NEEDS_USER_REVIEW`.

> Revalidar la ficha al abrir la fase.

---

## T7.1 — Remediation Planner

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 240 líneas |
| Depende de | F6 cerrada |
| Commit | `feat(remediation): planificador de correcciones por tipo de arreglo` |

**Contexto**: contrato de finding (F2), informe §16

**Hacer**: enrutar por el campo `Fixable`.

```
safe          → agente de remediación
needs_review  → propuesta al usuario, sin aplicar
manual        → informe, sin intento
```

La clasificación es determinista y proviene del contrato del finding, no de una
decisión del agente en el momento.

**Aceptación**: test por tabla de los tres destinos.

---

## T7.2 — Agente de remediación con permisos acotados

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 280 líneas |
| Depende de | T7.1 |
| Commit | `feat(remediation): agente con edicion restringida a los archivos con hallazgos` |

**Contexto**: `internal/agentadapter/*`, informe §16

**Hacer**

1. Toolset `Read` + `Edit`, **sin `Bash`**, sin red.
2. Escritura permitida **solo** en archivos que contienen hallazgos. Archivos
   nuevos permitidos únicamente si son tests.
3. Prompt: cambio mínimo, sin refactor oportunista, sin cambios no relacionados,
   y explicación de lo modificado.

**Por qué sin shell**: es la diferencia entre «corrige esta línea» y «haz que los
tests pasen». La segunda es exactamente cómo aparecen tests debilitados y
`//nolint`. La validación la ejecuta Go, no el agente.

**Aceptación**: test con doble que intenta escribir fuera del alcance → rechazado.

---

## T7.3 — Diff Guard

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 260 líneas |
| Depende de | T7.2 |
| Commit | `feat(remediation): descartar correcciones que salen del alcance` |

**Contexto**: `internal/git/*`, informe §16

**Hacer**: tras la edición, comprobar que el diff del fix toca **solo** las zonas
de los hallazgos ±N líneas (N configurable). Si no: **se descarta el fix entero**
y se reporta `remediation out of scope`.

Es la pieza que hace la remediación aceptable. Sin ella, el resto de la fase es
un riesgo neto.

**Aceptación**: test con un fix correcto y otro que además renombra una variable
en otra función → el segundo se descarta completo, no parcialmente.

---

## T7.4 — Re-validación y ronda única

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 240 líneas |
| Depende de | T7.3 |
| Commit | `feat(remediation): revalidacion acotada con una sola ronda` |

**Contexto**: `internal/validation/*` (F1), `internal/graph/*` (F4)

**Hacer**

1. Re-validar con el mismo perfil que falló, acotado a lo tocado por el fix más
   su cierre inverso.
2. Re-analizar, re-planificar y re-revisar **solo lo tocado**.
3. **Límite duro: una ronda.** Si quedan bloqueantes → `NEEDS_USER_REVIEW`.
4. Reportar hallazgos no resueltos **y** hallazgos nuevos introducidos por el fix.

**Por qué el límite**: los bucles fix→review→fix son donde el coste se descontrola
y donde el sistema deja de ser reproducible.

**Aceptación**: test de que tras una ronda fallida no se lanza una segunda.

---

## T7.5 — Persistencia de decisiones humanas

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 200 líneas |
| Depende de | T7.4 |
| Commit | `feat(store): persistir decisiones del usuario sobre hallazgos` |

**Contexto**: `internal/store/*` (F2), `internal/review/engine.go:141`
(`question` + `--answer`), informe §22

**Hacer**

1. `decisions.jsonl` con `{quien, cuando, finding_id, motivo, alcance}`.
2. Las respuestas a `question` dejan de perderse al terminar el proceso: no se
   vuelve a preguntar lo mismo sobre el mismo blob.
3. `--force` deja de ser una excepción sin traza (informe **M3**): registra quién
   y por qué.

**Aceptación**: test de que la misma pregunta sobre el mismo blob no se repite en
una segunda ejecución.
