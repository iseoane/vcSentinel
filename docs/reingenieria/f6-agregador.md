# F6 — Agregador

**Objetivo**: un defecto, un hallazgo. Y estados de salida que digan qué falló.

**Problema que resuelve** (informe §3, I3): `veredictoGlobal`
(`internal/review/engine.go:484`) sigue siendo una precedencia de casos: no
deduplica, no correlaciona, no fusiona evidencia. F5 (T5.3) ya corrigió la
degradación excesiva que ocultaba un `block` de `logic` cuando `security`
estaba caído — el `switch` actual solo deja que `VerdictUnavailable` rebaje el
peor veredicto si este era `ok` o `warn`, nunca si ya era `block` o
`question` — así que ese defecto concreto ya no aplica. Lo que sigue sin
resolver es el problema original: con dimensiones de mandatos solapados
—`design` y `logic` comparten complejidad; `security` y `logic` comparten
entrada no validada— el mismo defecto se reporta N veces, una por dimensión
que lo detectó.

**Criterio de salida de la fase**

1. Un defecto que dispara tres dimensiones se reporta como **un** hallazgo con
   tres evidencias.
2. Un hallazgo semántico que repite lo que ya dijo el linter **no aparece**.
3. Los cinco estados se distinguen en la salida y en el exit code.

> Revalidada al abrir la fase (ver nota de cierre al final). El alcance y los
> criterios de salida siguen siendo correctos; T6.4 resultó estar ya resuelta
> por F1, y T6.1/T6.2 tienen prerrequisitos de diseño que la ficha original no
> mencionaba.

---

## T6.1 — Deduplicación ✅ (hash de cierre `a9a661e`)

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 280 líneas |
| Depende de | F5 cerrada |
| Commits | `bdb9727` (implementación), `a483f24` (refuerzo tras revisión: excluye hallazgos refutados, recalcula fingerprint siempre, corrige el caso de un solo productor), `a9a661e` (normaliza también el `Producer` de cada evidencia, no solo del hallazgo agregado) |

**Contexto**: `internal/review/finding.go` (`Fingerprint`, F2), informe §15

**Punto de partida real**: `Fingerprint(h Hallazgo)` ya existe (F2) y se usa
hoy en `internal/store` para identidad entre ejecuciones (blob index tras
rebase), nunca para deduplicar dentro de una misma auditoría —
`AuditarCommit` no lo invoca. Su cálculo incluye `Dimension` como componente
del hash, así que dos hallazgos del mismo defecto real reportados por
dimensiones distintas (el caso que motiva esta fase) **producen fingerprints
distintos**: el dedup exacto por fingerprint tal cual existe hoy solo
colapsaría repeticiones literales dentro de la misma dimensión (p. ej. un
reintento), no el caso cruzado que es el problema real. El dedup por
proximidad (punto 2) es quien tiene que cargar con el caso cruzado.
`Hallazgo.Evidence` es un `string` único: no hay campo para acumular varias
evidencias hoy, hace falta añadirlo (o una lista de `Productor`/evidencia por
fuente) como parte de esta tarea.

**Hacer**

1. Dedup exacto por `fingerprint` (colapsa repeticiones literales dentro de
   la misma dimensión; no resuelve por sí solo el caso cruzado de dimensiones
   solapadas).
2. Dedup por proximidad: mismo símbolo, rangos solapados y similitud de
   descripción por encima de umbral configurable, **sin** exigir la misma
   `Dimension` — es el mecanismo que cubre el caso real de la ficha.
3. La fusión conserva la severidad máxima y **acumula las evidencias** (nuevo
   campo, a diseñar en esta tarea): dos agentes independientes señalando lo
   mismo **sube** la confianza, no la duplica.

**Aceptación**: test con tres hallazgos del mismo defecto en tres dimensiones →
uno con tres evidencias y confianza mayor que cualquiera individual. Verificado
con `TestAuditarCommitAggregatesProximateFindingsFromIndependentDimensions`
(`engine_test.go`): logic/design/security sobre el mismo símbolo → 1 hallazgo,
severidad `CRITICAL` (la máxima de las tres), 3 evidencias, confianza > 0.6
(cada una individual). Verificado con `sentinel review a9a661e` (dimensiones
completas): `logic`/`tests` = `ok`, `spec` = `warn` no bloqueante (el mensaje
`chore(slice): auto-fragmented cohesion batch #1` no menciona que ese commit
también normaliza el `Producer` de cada evidencia — aceptado como está, no se
reescribe historia sin decisión explícita del usuario). `sentinel gate
--stage pre-push` → `PASS`.

Implementación real: `internal/review/aggregation.go` (`aggregateFindings`,
`mergeFindings`, `areProximateFindings`, `corroboratedConfidence` con una
fórmula de combinación tipo *noisy-or* — `1 - Π(1 - confianza_i)` por
productor distinto, así que dos fuentes corroborando **suben** la confianza en
vez de promediarla o duplicarla) y `stamparProductorEfectivo` en `engine.go`
(protección de seguridad no anticipada por la ficha original: sustituye el
`Producer` que el propio LLM reporta en el JSON — que podría falsificar su
identidad para simular corroboración independiente — por el
`agentadapter.AgenteEfectivo` verificado de T5.4, tanto en el hallazgo como en
cada evidencia individual).

---

## T6.2 — Supersede de determinista sobre semántico

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 220 líneas |
| Depende de | T6.1 |
| Commit | `feat(aggregation): el hallazgo determinista sustituye al semantico equivalente` |

**Prerrequisito que la ficha original no mencionaba**: `internal/validation.Hallazgo`
(F1, `internal/validation/validacion.go`) no tiene `Location` ni archivo/línea
— solo `Capability`, `Comando`, `Evidencia` — así que hoy no se puede comparar
posicionalmente contra la `Location` de un `review.Hallazgo` para decidir "misma
ubicación". `SourceValidation` ya está declarado en
`internal/review/finding.go` con el puente pensado desde F2 (comentario en esa
misma sección: "Un Hallazgo con `Source=SourceValidation` se rellenaría con
`Confidence: 1.0`... y `Producer` describiendo el comando ejecutado"), pero
**ese puente no está construido**: ningún código produce hoy un
`review.Hallazgo{Source: SourceValidation}` a partir de un
`validation.Hallazgo`. Esta tarea depende de construir ese puente primero
(darle `Location` real a `validation.Hallazgo`, o proyectar sus hallazgos como
`review.Hallazgo` en el punto de agregación) antes de poder implementar el
supersede en sí.

**También revisar el caso de ejemplo original**: en el `gate` actual
(`internal/gate/gate.go`, T1.7), si `validation.Hallazgos(runs, ...)` no está
vacío, `EjecutarGate` devuelve `VALIDATION_FAILED` de inmediato y **nunca
llega a invocar** `review.AuditarCommit` — la revisión semántica no se
ejecuta si la validación determinista ya falló. El ejemplo "un finding de
`gofmt` y uno semántico de estilo en la misma línea" no ocurre literalmente
en el flujo de `gate` tal como está cableado hoy (más aún: F5 T5.2 ya
convirtió `style` en capability determinista con la semántica residual
desactivada por defecto, evitando el choque de raíz en ese caso concreto). El
supersede sigue siendo relevante para otros puntos donde ambas fuentes sí
puedan coexistir en un mismo reporte (p. ej. `pr review` agregando varios
commits, o un futuro flujo que no corte en corto ante un fallo de
validación) — hay que decidir al abrir la tarea si ese escenario combinado ya
existe en algún flujo real o si esta tarea también debe crearlo para tener
algo que probar.

**Hacer**: un hallazgo con `source: validation` invalida los `source: review` que
describen el mismo problema en la misma ubicación. Si el linter ya lo dijo, el
LLM no lo repite.

**Aceptación**: test con un `review.Hallazgo{Source: SourceValidation}` y uno
`{Source: SourceReview}` sobre la misma `Location` → sobrevive el
determinista. (El ejemplo "`gofmt` + semántico de estilo" de la ficha
original ya no es reproducible tal cual dentro de `gate` por el corte en
corto descrito arriba; usar el puente construido en esta misma tarea para
generar el caso de prueba.)

---

## T6.3 — Correlación por causa

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 240 líneas |
| Depende de | T6.1 |
| Commit | `feat(aggregation): agrupar hallazgos por causa comun` |

Sin cambios: no existe hoy ningún mecanismo de agrupación por causa común en
el repositorio (`internal/review`, `internal/gate`) — confirmado por
búsqueda de "agregad"/"dedup"/"aggreg" en todo el módulo, sin resultados
salvo referencias a esta misma ficha.

**Hacer**: agrupar por síntoma compartido —mismo test fallando, misma frontera de
confianza— para reportar una causa en lugar de diez efectos.

**Aceptación**: test con cinco hallazgos derivados de un mismo test roto → un
grupo con cinco efectos.

---

## T6.4 — Estados y exit codes

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | — |
| Depende de | — |
| Commit | ya cerrada en F1 (T1.7) |

**Ya resuelta por F1, no por esta fase.** `internal/gate/gate.go` (paquete
`gate`, comentario propio: "el subcomando gate (T1.7)") ya define exactamente
los cinco estados y sus exit codes que esta tarea pedía:

| Estado (`gate.go`) | Condición real hoy | Exit (`CodigoSalida`) |
|---|---|---|
| `EstadoPass` (`PASS`) | validación sin hallazgos y revisión semántica sin `block` ni pregunta ni crítico refutado | 0 |
| `EstadoValidationFailed` (`VALIDATION_FAILED`) | `validation.Hallazgos(runs, ...)` no vacío | 1 |
| `EstadoCodeReviewFailed` (`CODE_REVIEW_FAILED`) | `review.VerdictBlock` (CRITICAL semántico confirmado) | 1 |
| `EstadoNeedsUserReview` (`NEEDS_USER_REVIEW`) | `review.VerdictQuestion`, o veredicto final con `RefutedCritical` (T5.7) | 2 |
| `EstadoReviewInfrastructureError` (`REVIEW_INFRASTRUCTURE_ERROR`) | fallo al orquestar validación, `review.VerdictUnavailable`, o cualquier estado no reconocido por `CodigoSalida` (fail-closed explícito) | 4 |

`traducirVeredicto` en el mismo archivo ya mantiene `VALIDATION_FAILED` y
`CODE_REVIEW_FAILED` reportados por separado con mensajes propios
(`mensajesValidacionFallida` vs. el `String()` de la revisión semántica),
igual que pedía esta tarea. La referencia original de contexto
(`cmd/sentinel/comandos_review.go:267`) estaba desactualizada: esa lógica no
vive en `cmd/`, vive en `internal/gate/gate.go` por decisión explícita de
arquitectura de T1.7 (el propio archivo dice: "La lógica de negocio vive
aquí... nunca en `cmd/`"); `cmd/sentinel/comandos_gate.go` solo consume las
constantes `gate.Estado*`.

**Qué queda pendiente aquí, si acaso**: nada de exit codes o vocabulario de
estados. Lo único que T6.1/T6.2 podrían necesitar tocar en `gate.go` es cómo
se cuentan los hallazgos ya deduplicados/fusionados para decidir el veredicto
(hoy `veredictoGlobal` opera sobre `ResultadoDimension` crudos, uno por
dimensión, no sobre una lista ya agregada) — evaluar al implementar T6.1 si
eso exige un ajuste menor en `veredictoGlobal` o en `traducirVeredicto`. Esta
tarea queda cerrada como fue definida; no depende de T6.2/T6.3 como decía la
ficha original, y nada aquí bloquea empezar T6.1 primero.

---

## T6.5 — Renderer adaptado

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 280 líneas |
| Depende de | T6.1 |
| Commit | `feat(review): plantilla con hallazgos agrupados y fuente diferenciada` |

**Contexto**: `internal/review/renderer.go`, `internal/review/renderer_test.go`

**Punto de partida real**: la separación de secciones (punto 1) ya existe
parcialmente. `RenderPlantillaPr` ya combina `seccionValidacion` (T1.8: exit
codes reales de las capabilities deterministas) y `seccionVerificacion`
(estado de tests, determinista o delegado) como secciones propias,
independientes de la matriz de hallazgos semánticos (`RenderMatriz`/
`RenderResumen`) — pero eso separa comandos de validación (pass/fail por
comando), no hallazgos individuales por fuente. Lo que falta de verdad es el
punto 2: no hay ningún hallazgo fusionado con evidencias acumuladas que
renderizar todavía, porque depende por completo de que T6.1 exista primero.
`recortarRunas` está hoy en `renderer.go:271`, no en la línea 293 que citaba
la ficha original — sigue probado y correcto, sin cambios necesarios.

**Hacer**

1. Extender (no rehacer) la separación de secciones ya presente en
   `RenderPlantillaPr` para que la matriz de hallazgos semánticos distinga
   fuente (`review` vs. `validation`, una vez exista el puente de T6.2).
2. Hallazgos fusionados con sus evidencias acumuladas y su confianza (bloqueado
   por T6.1: no hay estructura de datos que renderizar hasta entonces).
3. Conservar el truncamiento marcado y el recorte por runas (`recortarRunas`,
   `renderer.go:271`) — están probados y son correctos.

**Aceptación**: los tests de renderer existentes que sigan aplicando pasan;
los que cambien de forma se adaptan **explicando por qué** en el informe.

---

## Nota de revalidación (al abrir la fase)

Revalidada contra el código real tras el cierre de F5 (`91dc65f`/`fdfafbb`).
Confirmado sin cambios: el criterio de salida de la fase, T6.3 (sin ningún
mecanismo previo), y la mecánica central de T6.1/T6.5. Corregido: la línea de
`veredictoGlobal` y de `recortarRunas` (ambas se movieron al crecer sus
archivos durante F5); la afirmación de que `veredictoGlobal` "oculta un
block si security está caído" (F5 T5.3 ya lo arregló). Hallazgo nuevo, no
anticipado por la ficha original: **T6.4 ya está completamente resuelta por
F1** (T1.7, `internal/gate/gate.go`) — los cinco estados y sus exit codes
existen tal cual se pedían, y su referencia de contexto apuntaba a un archivo
equivocado. También nuevo: T6.1 tiene una trampa de diseño (`Fingerprint`
incluye `Dimension`, así que el dedup exacto no resuelve el caso cruzado que
motiva la fase) y T6.2 tiene un prerrequisito no construido (el puente
`validation.Hallazgo` → `review.Hallazgo{Source: SourceValidation}` que F2
dejó explícitamente pendiente para esta fase, y que hoy no existe en absoluto).
