# F6 — Agregador

**Objetivo**: un defecto, un hallazgo. Y estados de salida que digan qué falló.

**Problema que resuelve** (informe §3, I3): `veredictoGlobal`
(`internal/review/engine.go:156`) es una precedencia de cinco casos. No
deduplica, no correlaciona, no fusiona evidencia. Con dimensiones de mandatos
solapados —`design` y `logic` comparten complejidad; `security` y `logic`
comparten entrada no validada— el mismo defecto se reporta N veces.

**Criterio de salida de la fase**

1. Un defecto que dispara tres dimensiones se reporta como **un** hallazgo con
   tres evidencias.
2. Un hallazgo semántico que repite lo que ya dijo el linter **no aparece**.
3. Los cinco estados se distinguen en la salida y en el exit code.

> Revalidar la ficha al abrir la fase.

---

## T6.1 — Deduplicación

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 280 líneas |
| Depende de | F5 cerrada |
| Commit | `feat(aggregation): deduplicar hallazgos por huella y por proximidad` |

**Contexto**: `internal/review/finding.go` (fingerprint de F2), informe §15

**Hacer**

1. Dedup exacto por `fingerprint`.
2. Dedup por proximidad: mismo símbolo, rangos solapados y similitud de
   descripción por encima de umbral configurable.
3. La fusión conserva la severidad máxima y **acumula las evidencias**: dos
   agentes independientes señalando lo mismo **sube** la confianza, no la
   duplica.

**Aceptación**: test con tres hallazgos del mismo defecto en tres dimensiones →
uno con tres evidencias y confianza mayor que cualquiera individual.

---

## T6.2 — Supersede de determinista sobre semántico

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 220 líneas |
| Depende de | T6.1 |
| Commit | `feat(aggregation): el hallazgo determinista sustituye al semantico equivalente` |

**Hacer**: un hallazgo con `source: validation` invalida los `source: review` que
describen el mismo problema en la misma ubicación. Si el linter ya lo dijo, el
LLM no lo repite.

Es la reducción de ruido más directa del sistema y la contrapartida natural de
haber convertido `style` en capability determinista (F5, T5.2).

**Aceptación**: test con un finding de `gofmt` y uno semántico de estilo en la
misma línea → sobrevive el determinista.

---

## T6.3 — Correlación por causa

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 240 líneas |
| Depende de | T6.1 |
| Commit | `feat(aggregation): agrupar hallazgos por causa comun` |

**Hacer**: agrupar por síntoma compartido —mismo test fallando, misma frontera de
confianza— para reportar una causa en lugar de diez efectos.

**Aceptación**: test con cinco hallazgos derivados de un mismo test roto → un
grupo con cinco efectos.

---

## T6.4 — Estados y exit codes

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 240 líneas |
| Depende de | T6.2, T6.3 |
| Commit | `feat(aggregation): estados de salida diferenciados por fuente` |

**Contexto**: `cmd/sentinel/comandos_review.go:267`, informe §15

**Hacer**

| Estado | Condición | Exit |
|---|---|---|
| `PASS` | sin CRITICAL; WARN dentro del umbral de política | 0 |
| `VALIDATION_FAILED` | cualquier CRITICAL de `source: validation` | 1 |
| `CODE_REVIEW_FAILED` | CRITICAL semántico **confirmado** por el refutador | 1 |
| `NEEDS_USER_REVIEW` | CRITICAL refutado, baja confianza, ambigüedad o excepción | 2 |
| `REVIEW_INFRASTRUCTURE_ERROR` | proveedor caído, timeout, salida no parseable | 4 |

`VALIDATION_FAILED` y `CODE_REVIEW_FAILED` comparten exit code pero **se reportan
por separado**: la causa es distinta y la acción del usuario también. Nunca se
mezclan en un genérico «falló la revisión».

**Aceptación**: test por tabla de los cinco estados.

---

## T6.5 — Renderer adaptado

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 280 líneas |
| Depende de | T6.4 |
| Commit | `feat(review): plantilla con hallazgos agrupados y fuente diferenciada` |

**Contexto**: `internal/review/renderer.go`, `internal/review/renderer_test.go`

**Hacer**

1. Secciones separadas para validación determinista y revisión semántica.
2. Hallazgos fusionados con sus evidencias acumuladas y su confianza.
3. Conservar el truncamiento marcado y el recorte por runas (`recortarRunas`,
   `renderer.go:293`) — están probados y son correctos.

**Aceptación**: los tests de renderer existentes que sigan aplicando pasan;
los que cambien de forma se adaptan **explicando por qué** en el informe.
