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
3. The five states are distinguishable in output and map deterministically to
   exit codes, but they do not have five unique numeric codes
   (`VALIDATION_FAILED` and `CODE_REVIEW_FAILED` both use `1`).

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
reescribe historia sin decisión explícita del usuario). The historical gate
PASS statement is not retained as evidence here because no persisted event
proves that invocation; current integrated verification is recorded in the
phase closure below.

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

## T6.2 — Deterministic supersede over semantic findings ✅ (technical close `b8496e2`; documentation close `97588b6`)

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 220 líneas |
| Depende de | T6.1 |
| Commits | `d795584` (implementation: bridge + supersede), `1c900e5` (review correction: require the same `Dimension`, apply the deterministic result only to `HEAD`, normalize paths, and stamp `Source: SourceReview` with engine authority), `cf347a5` (explicitly validate the SHA instead of inferring it from position), `be44742` (warn instead of silently discarding when `HEAD` cannot be resolved), `b53cdf4`/`b8496e2` (test reinforcement; `b8496e2` is the final technical code/test commit), followed by documentation close `97588b6` |

**Decisión de alcance, confirmada con el usuario antes de implementar**: el
`gate` automático (pre-commit/pre-push) NO se toca — corta en corto a
propósito ("cero tokens gastados" si la validación ya falló, decisión de
costo ya documentada en `comandos_pr.go`). El supersede se cablea solo en
`pr create --force`, el único flujo real donde validación determinista y
revisión semántica ya coexisten en el mismo reporte sin coste adicional de
tokens (con `--force`, `AnalizarRama` ya se invoca pese a la validación en
rojo).

**Puente construido**: `cmd/sentinel/proyeccion_validacion.go`
(`proyectarHallazgosValidacion`) convierte cada `validation.Hallazgo` en uno o
más `review.Hallazgo{Source: SourceValidation}`, parseando `Evidencia` línea a
línea: el prefijo `archivo:línea[:columna]:` que ya usan `go vet`/`go
build`/el compilador produce una ubicación exacta; una línea que es solo una
ruta (`gofmt -l`) cubre el archivo entero; si nada es reconocible, se conserva
un hallazgo sin ubicación en vez de perder la evidencia. `Dimension` se asigna
por un mapa conservador `capability → dimensión` (`format`/`lint` → `style`
únicamente); cualquier capability ausente del mapa (`unit_test`, `build`,
cualquiera desconocida) deja `Dimension` vacía a propósito.

**Núcleo de supersede**: `internal/review/supersede.go`
(`SupersedeDeterministicFindings`) exige **ambas** condiciones para descartar
un hallazgo semántico: misma `Dimension` (nunca solo ubicación — la primera
implementación lo hacía y la revisión semántica real lo bloqueó como
`CRITICAL` de seguridad: un `gofmt` de archivo completo podía borrar
hallazgos de seguridad no relacionados en el mismo archivo) y ubicación
solapada (o archivo completo si el determinista no trae línea). Un
determinista con `Dimension` vacía nunca suplanta nada. Rutas se comparan
normalizadas (`path.Clean`) para que `./a.go` y `a.go` coincidan. `Source:
SourceReview` ahora lo estampa el motor con autoridad
(`stamparSourceReview` en `engine.go`, junto a `stamparProductorEfectivo` de
T6.1) en vez de confiar en que el LLM lo declare en su JSON — el prompt nunca
se lo pide, así que dependía de un campo que en la práctica siempre llegaba
vacío.

**Alcance de rama, no solo de commit**: los hallazgos deterministas de
`pr create --force` provienen de validar el worktree actual (el tip), no cada
commit histórico de la rama. `OpcionesRama.HallazgosDeterministasSHA`
(resuelto explícitamente vía `git.SHAHead()`, nunca inferido por posición en
la lista de SHAs) le dice a `AnalizarRama` a qué commit exacto aplicarlos;
cualquier otro commit de la rama recibe `nil`. Si `HEAD` no se puede
resolver, se avisa explícitamente en la terminal y el supersede
simplemente no se aplica en esa ejecución (nunca en silencio, y nunca
aborta la publicación por esto).

**Hacer**: un hallazgo con `source: validation` invalida los `source: review`
que describen el mismo problema (misma dimensión) en la misma ubicación. Si
el linter ya lo dijo, el LLM no lo repite.

**Acceptance**: fulfilled — `TestAuditarCommitSupersedesSemanticFindingWithDeterministicOne`
(`engine_test.go`) tests the original ficha case exactly: a
`review.Hallazgo{Source: SourceValidation}` and a `{Source: SourceReview}`
with the same `Location` and `Dimension` leave the deterministic finding in
place. The historical gate PASS statement is not retained as evidence here
because no persisted event proves that invocation; current integrated
verification is recorded in the phase closure below.

**Limitación de testabilidad, aceptada y documentada (no bloqueante)**:
`ResultadoAuditoria.Findings` (el resultado consolidado de T6.1+T6.2, con
dedup y supersede ya aplicados) hoy no tiene NINGÚN consumidor en producción
— ni el veredicto (`veredictoGlobal` opera sobre `resultado.Dims`, previo al
supersede), ni la ficha persistida en el ledger, ni ningún renderer. Por
eso no existe un test de integración de `AnalizarRama` con 2+ commits que
observe el efecto del supersede de punta a punta (la revisión semántica lo
señaló dos veces): el mecanismo está probado exhaustivamente a nivel unitario
(la función de selección de commit, el wiring de `auditarCommitRama`, el
supersede puro) pero su efecto real solo será observable cuando exista un
consumidor — tarea de T6.5 (renderer adaptado), no de esta.

---

## T6.3 — Correlación por causa ✅ (hash de cierre `17e8970`)

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 240 líneas |
| Depende de | T6.1 |
| Commits | `c47ecc1` (implementación: `CauseGroup`, `correlateFindingsByCause`), `439513f` (corrección tras revisión: agrupamiento por componentes conexas vía union-find en vez de solo-ancla, para que sea transitivo), `3e05565` (selección de `Cause` por medoide en vez de por Confidence bruta, para no etiquetar mal un grupo encadenado transitivamente), `9310aa8`/`6e05456`/`6a12df2`/`6a2d24f`/`17e8970` (correcciones sucesivas de la revisión semántica al desempate de `dominantCause`/`selectDominant`: ancla de máximo real estable frente a ruido de suma en punto flotante, tolerancia escalada al número real de sumandos en vez de un épsilon fijo, y separación en tests aislados y verificados uno a uno contra su propio mutante) |

Sin cambios en el diseño original: no existía ningún mecanismo de agrupación
por causa común en el repositorio antes de esta tarea.

**Hacer**: agrupar por síntoma compartido —mismo test fallando, misma frontera de
confianza— para reportar una causa en lugar de diez efectos.

**Aceptación**: cumplida — `TestCorrelateFindingsByCauseGroupsDistinctLocationsSharingRootCause`
(`aggregation_test.go`) prueba exactamente el caso de la ficha: cinco
hallazgos en cinco ubicaciones distintas describiendo el mismo síntoma → un
grupo con cinco efectos, sin pérdida ni duplicación de ninguno.

Implementación real: `internal/review/aggregation.go`
(`correlateFindingsByCause` agrupa por componentes conexas de similitud de
descripción, deliberadamente sin restricción de ubicación —a diferencia de
`areProximateFindings` de T6.1—, así que captura efectos correlacionados en
archivos y símbolos distintos que la deduplicación por proximidad nunca
fusiona) y `dominantCause`/`selectDominant` (selecciona la etiqueta `Cause`
por medoide —mayor similitud total al resto del grupo— con desempate por
Confidence, comparando puntuaciones con una tolerancia en ULPs escalada al
número de sumandos para no confundir ruido de suma en punto flotante con una
diferencia real de puntuación).

**Aceptado sin corregir, con motivo documentado** (todos de severidad
ADVISORY o WARNING no bloqueante, revisados y verificados uno a uno):
recálculo O(n²) de similitudes en `dominantCause` que `correlateFindingsByCause`
ya había computado (mantiene `dominantCause` como función pura,
independientemente testeable, igual que el propio `aggregateFindings` de T6.1
ya paga el mismo coste sin límite de tamaño de entrada); `Cause` es un símbolo
representativo por similitud léxica, no una identidad de causa raíz verificada
—coherente con el propio alcance de la ficha ("síntoma compartido"), no una
promesa de diagnóstico causal—; y el hallazgo original de seguridad sobre
coste cuadrático sin cota de tamaño en `correlateFindingsByCause`, ya
compartido sin cota por `aggregateFindings` desde T6.1 y no introducido por
esta tarea. Ninguno tiene consumidor que dependa hoy de un límite de tamaño o
de una identidad causal verificada.

---

## T6.4 — States and exit codes

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | — |
| Depende de | — |
| Commit | Delivered by F1/T1.7 |

**Delivered by F1/T1.7, not by this phase.** `internal/gate/gate.go` (paquete
`gate`, comentario propio: "el subcomando gate (T1.7)") ya define exactamente
los cinco estados y sus exit codes que esta tarea pedía:

| Estado (`gate.go`) | Condición real hoy | Exit (`CodigoSalida`) |
|---|---|---|
| `EstadoPass` (`PASS`) | validación sin hallazgos y revisión semántica sin `block` ni pregunta ni crítico refutado | 0 |
| `EstadoValidationFailed` (`VALIDATION_FAILED`) | `validation.Hallazgos(runs, ...)` no vacío | 1 |
| `EstadoCodeReviewFailed` (`CODE_REVIEW_FAILED`) | `review.VerdictBlock` (CRITICAL semántico confirmado) | 1 |
| `EstadoNeedsUserReview` (`NEEDS_USER_REVIEW`) | `review.VerdictQuestion`, o veredicto final con `RefutedCritical` (T5.7) | 2 |
| `EstadoReviewInfrastructureError` (`REVIEW_INFRASTRUCTURE_ERROR`) | fallo al orquestar validación, `review.VerdictUnavailable`, o cualquier estado no reconocido por `CodigoSalida` (fail-closed explícito) | 4 |

**The five states are distinguishable in output and map deterministically to
exit codes, but they do not have five unique numeric codes: `VALIDATION_FAILED`
and `CODE_REVIEW_FAILED` both use `1`.**

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

## T6.5 — Renderer adaptado ✅ (hash de cierre `a883cac`)

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 280 líneas |
| Depende de | T6.1 |
| Commits | `d78b3b6` (implementation: `Revision.AggregatedFindings` persists the aggregated result of T6.1+T6.2, `HallazgosEfectivos()` consumes it with a legacy `Dims` fallback, and `riesgos()`/`renderMergedFinding` render source and evidence), `5344e08` (review correction: aligned the effective-finding selection consumed by `riesgos()` and `BloqueantesDeRama` through `Revision.HallazgosEfectivos()`; `RevisionCorrigeBlockPrevio` only checks the prior revision result), `031b59e` (sanitize `Description` and `Location.Archivo` to prevent Markdown injection in the PR body), `048662f` (avoid propagating `Source` without real `Confidence` in the v1→v2 projector), `a883cac` (document the intentional asymmetry of the v1↔v2 `Source` mapping) |

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

**Aceptación**: cumplida. `HallazgosEfectivos()` (`ledger.go`) resuelve el
hueco de testabilidad que T6.2 había dejado documentado: cuando
`AggregatedFindings` está presente (el resultado dedup+supersede de T6.1/T6.2)
lo usa; si no, cae a la proyección legacy de `Dims`. `riesgos()`/
`renderMergedFinding()` (`renderer.go`) pintan cada hallazgo fusionado con su
fuente (`review` vs. `validation`) y confianza corroborada. `5344e08` aligned
the effective-finding selection consumed by `riesgos()` and
`BloqueantesDeRama` through `Revision.HallazgosEfectivos()`.
`RevisionCorrigeBlockPrevio` only checks the prior revision result; it does not
select effective findings. This prevents `pr create --force` from blocking on a
semantic finding that T6.2 already superseded. The persisted ledger proves
that `sentinel review a883cac` returned `ok` for `design`, `spec`, `tests`, and
`logic`. No persisted event proves the historical `sentinel gate --stage
pre-push` invocation previously claimed for `a883cac`; current integrated
verification is recorded below.

---

## Phase closure

**Outcome: F6 is implemented and closed at `a883cac`.** T6.1–T6.5 are
complete:

- T6.1 closes at `a9a661e`.
- T6.2 has a final technical code/test close at `b8496e2`; its subsequent
  documentation close is `97588b6`.
- T6.3 closes at `17e8970`.
- T6.4 was delivered by F1/T1.7.
- T6.5 closes at `a883cac`.

The three phase exit criteria are satisfied on the real code:

1. A defect reported by three dimensions becomes one finding with three
   evidence items — `TestAuditarCommitAggregatesProximateFindingsFromIndependentDimensions`
   (T6.1), rendered in the PR body through `renderMergedFinding` (T6.5).
2. A semantic finding that repeats what the linter already reported is
   superseded — `TestAuditarCommitSupersedesSemanticFindingWithDeterministicOne`
   (T6.2), wired only into `pr create --force`.
3. The five states are distinguishable in output and map deterministically to
   exit codes, but there are not five unique numeric codes:
   `VALIDATION_FAILED` and `CODE_REVIEW_FAILED` both use `1` (T6.4, delivered
   by F1/T1.7).

The persisted ledger proves that `sentinel review a883cac` returned `ok` in
`design`, `spec`, `tests`, and `logic`. No persisted event proves the
historical `sentinel gate --stage pre-push` invocation previously claimed for
`a883cac`.

**Current reproducible verification (2026-08-20, integrated tree `aaf21f4`)**:
`go build ./...`, `go vet ./...`, `go test ./...`, the guardian
(`sentinel check`), and `sentinel gate --stage pre-push` all passed. This
validates the current integrated code, not a fabricated historical gate event
on `a883cac`.

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
equivocado. Another discovery was T6.1's design trap: `Fingerprint` includes
`Dimension`, so exact deduplication does not solve the cross-dimension case
that motivates the phase. At revalidation time, T6.2 also had one missing
prerequisite: the `validation.Hallazgo` →
`review.Hallazgo{Source: SourceValidation}` bridge left by F2 and subsequently
implemented by T6.2.
