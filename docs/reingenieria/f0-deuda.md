# F0 — Saldar la deuda abierta

**Objetivo**: dejar la base limpia, el guardián a salvo y los hallazgos
confirmados de la auditoría cerrados. No introduce arquitectura nueva.

**Por qué va primero**: `sentinel check` está en **CRÍTICO (702 líneas)** y el
hook apunta al binario que `build.bat` sobrescribe. Sin resolver ambas cosas, la
primera tarea de F1 que rompa el build deja el repositorio sin poder commitear.

**Criterio de salida de la fase**

- `sentinel check` en verde partiendo de un worktree limpio.
- El hook apunta a un binario que ninguna tarea de desarrollo sobrescribe.
- H4 y H6 cerrados con evidencia en `docs/auditoria/README.md`.
- `go test ./...` en verde.
- `CLAUDE.md` describe la aplicación que existe.

---

## T0.0 — Blindar el guardián *(manual, no delegable)*


|             |                       |
| ----------- | --------------------- |
| Ejecuta     | Usuario / orquestador |
| Presupuesto | 0 líneas de código    |
| Depende de  | —                     |


**Objetivo**: que el hook `pre-commit` deje de apuntar al binario en desarrollo.

**Hacer**

1. Copiar el binario actual, que está verde, a `~/.vas_sentinel/bin/sentinel.exe`.
2. Ejecutar `init` **con ese binario**, para que `generarScriptHook`
 (`cmd/sentinel/main.go:384`, usa `os.Executable()`) reescriba el hook con la
 ruta estable.
3. Verificar que `.git/hooks/pre-commit` ya no menciona `bin/0.2.0/`.

**Criterio de aceptación**

```
cat .git/hooks/pre-commit    → apunta a ~/.vas_sentinel/bin/sentinel.exe
build.bat                    → no altera el comportamiento del hook
```

---

## T0.1 — Fragmentar y commitear el trabajo pendiente *(manual)*


|            |                       |
| ---------- | --------------------- |
| Ejecuta    | Usuario / orquestador |
| Depende de | T0.0                  |


702 líneas pendientes con las correcciones B1/B2/B3/B9 y su documentación.

**Hacer**

1. `go test ./...` para confirmar que lo pendiente está sano.
2. `sentinel slice`, revisar el plan propuesto y aprobarlo (A/R/E/C).
3. `sentinel review --chain` sobre los commits creados.

**Criterio de aceptación**: `sentinel check` en verde y `git status` limpio.

---

## T0.2 — H4: registrar el agente y el modelo que realmente respondieron


|             |                                                                      |
| ----------- | -------------------------------------------------------------------- |
| Agente      | sonnet / xhigh                                                       |
| Presupuesto | ≤ 200 líneas                                                         |
| Depende de  | T0.1                                                                 |
| Commit      | `fix(review): registrar el agente efectivo en la ficha de auditoria` |


**Contexto**

- `cmd/sentinel/comandos_review.go` (líneas 92-121)
- `internal/agentadapter/cadena.go`
- `internal/review/ledger.go`
- `docs/auditoria/README.md` → hallazgo **H4**

**Problema**: con `active_agent: auto`, la ficha guarda el nombre del perfil
(`flags.profile`, o `"default"`), no el agente que respondió. Si la cadena cae de
`claude` a `opencode`, el veredicto queda sin autor. Comprobado: la ficha de
`6c079a8` guarda `"model": "default"` tras responder `claude`.

**Hacer**

1. Que `CadenaAdaptador` exponga cuál de sus hijos atendió la última petición.
2. Propagar binario + modelo + esfuerzo efectivos hasta `Revision`.
3. Añadir esos campos a la ficha sin romper la lectura de las fichas existentes
 (campos nuevos opcionales).

**No tocar**: el formato de `revisions[]` como array append-only; la escritura
atómica de `guardarFicha`.

**Aceptación**

- Test con una cadena cuyo primer adaptador falla: la ficha registra el segundo.
- Una ficha v1 sin los campos nuevos se sigue leyendo sin error.

---

## T0.3 — H6: leer el token de GitHub sin eco de terminal


|             |                                                              |
| ----------- | ------------------------------------------------------------ |
| Agente      | sonnet / xhigh                                               |
| Presupuesto | ≤ 120 líneas                                                 |
| Depende de  | T0.1                                                         |
| Commit      | `fix(setup): leer el token de GitHub sin eco en el terminal` |


**Contexto**

- `internal/setup/github.go` (línea 71 y `PrepararTokenGitHub` en :37)
- `internal/setup/github_test.go`

**Problema**: `bufio.NewReader(os.Stdin).ReadString('\n')` deja el secreto
visible en pantalla y en el scrollback.

**Hacer**

1. Leer con lectura sin eco, funcionando en Windows y en Linux.
2. Conservar una costura de inyección para poder testear sin terminal real.
3. Degradar con aviso explícito si no hay terminal (tubería, CI): nunca leer con
 eco en silencio.

**Aceptación**: test que inyecta el lector y verifica que el token no se emite
por la salida estándar.

---

## T0.4 — B4/O2: unificar umbrales y semántica de «líneas»


|             |                                                                   |
| ----------- | ----------------------------------------------------------------- |
| Agente      | sonnet / xhigh                                                    |
| Presupuesto | ≤ 180 líneas                                                      |
| Depende de  | T0.1                                                              |
| Commit      | `refactor(git): umbrales compartidos y semantica unica de lineas` |


**Contexto**

- `internal/git/diff.go`
- `internal/git/slice.go` (líneas 15-25, 160-215)
- `internal/review/rama.go` (línea 587)
- `docs/auditoria/b-guardian.md` → **B4**, **B5**

**Problema doble**

1. El `400` vive en tres sitios sin relación: `clasificarEstado`,
 `limiteLineasLote`/`limiteConfigGigante` y `LimiteDecisionChain`.
2. «Líneas» significa tres cosas distintas: añadidas (numstat), físicas
 (no rastreados) y añadidas+borradas (rango de rama).

**Hacer**

1. Constantes compartidas con nombre que diga qué miden.
2. Documentar en el propio código que `LimiteDecisionChain` coincide con el
 umbral del guardián **por decisión, no por casualidad**.
3. Corregir el texto de salida de `check`: dice «modificadas» y cuenta añadidas
 (B5).

**No tocar**: los valores numéricos. Esta tarea unifica, no recalibra.

**Aceptación**: los tests existentes de `internal/git` pasan sin modificarse.

---

## T0.5 — H1/B6: rechazar argumentos desconocidos


|             |                                                                           |
| ----------- | ------------------------------------------------------------------------- |
| Agente      | sonnet / xhigh                                                            |
| Presupuesto | ≤ 150 líneas                                                              |
| Depende de  | T0.1                                                                      |
| Commit      | `fix(cli): rechazar argumentos desconocidos en los subcomandos sin flags` |


**Contexto**

- `cmd/sentinel/main.go` (líneas 49-93)
- `cmd/sentinel/ayuda_test.go`

**Problema**: `sentinel check --loquesea` sale 0 sin protestar. El dispatcher no
valida `os.Args[2:]`. Afecta a `check`, `slice`, `lint`, `rebase`, `init`,
`uninit`, `install`, `upgrade`, `uninstall`.

**Hacer**: los subcomandos sin flags rechazan cualquier argumento extra con
mensaje claro y salida 1.

**Riesgo declarado**: rompe scripts que hoy pasan basura sin enterarse. Es el
comportamiento deseado.

**Aceptación**: test por tabla que recorre los nueve subcomandos.

---

## T0.6 — Actualizar la documentación del proyecto


|             |                                                                 |
| ----------- | --------------------------------------------------------------- |
| Agente      | sonnet / xhigh                                                  |
| Presupuesto | ≤ 200 líneas                                                    |
| Depende de  | T0.2 … T0.5                                                     |
| Commit      | `docs: actualizar CLAUDE.md y AGENTS.md a la aplicacion actual` |


**Contexto**

- `CLAUDE.md`, `AGENTS.md`, `README.md`
- `docs/guia-implantacion-revision.md` (secciones 2 y 15)

**Problema**: `CLAUDE.md` describe una aplicación anterior y más pequeña. No
menciona `review`, `status`, `pr review`, `pr create`, `lint`, `rebase`, ni el
ledger, ni los eventos, ni la verificación dual.

**Hacer**

1. Actualizar la sección de arquitectura con los paquetes que existen.
2. Documentar los 15 subcomandos reales.
3. Enlazar `docs/arquitectura/replanteamiento-objetivo.md` y este directorio.

**No tocar**: el bloque `## REGLA CRÍTICA DE VOLUMEN (EL GUARDIÁN)`. `uninit` lo
busca **literalmente** (`main.go:24`, `main.go:339`); cualquier cambio de un byte
lo deja sin poder retirarse.

---

## T0.7 — B10: `init` duplica el bloque de reglas con finales de línea mixtos

| | |
|---|---|
| Agente | sonnet / xhigh |
| Presupuesto | ≤ 180 líneas |
| Depende de | T0.1 |
| Commit | `fix(init): detectar el bloque de reglas con finales de linea mixtos` |

**Contexto**

- `cmd/sentinel/main.go` (`reglasVolumen` :24, `inyectarReglasDeArchivo` :312,
  `quitarReglasDeArchivo` :339)
- `cmd/sentinel/init_test.go`
- `.gitattributes`

**Hallazgo nuevo, confirmado por ejecución en esta sesión.** Al ejecutar
`sentinel init` sobre este repositorio con `CLAUDE.md`, `AGENTS.md` y
`.claudecode.md` ya inicializados, el bloque se **duplicó** en los tres:

```
$ sentinel init
📝 Reglas de volumen inyectadas en: AGENTS.md
📝 Reglas de volumen inyectadas en: CLAUDE.md
📝 Reglas de volumen inyectadas en: .claudecode.md

$ git diff --stat
 .claudecode.md | 6 +++++-
 AGENTS.md      | 5 +++++
 CLAUDE.md      | 5 +++++
```

**Causa**: `inyectarReglasDeArchivo` compara con `strings.Contains` contra una
constante que usa solo `\n`, mientras el archivo en disco tiene finales de línea
**mixtos** (`file CLAUDE.md` → «CRLF, LF line terminators»), producto de
`* text=auto` en `.gitattributes` sobre Windows. Un `\r\n` interno rompe la
coincidencia.

**Gravedad**: afecta a las dos direcciones. `init` deja de ser idempotente y
`uninit` **no puede retirar** un bloque que no reconoce, así que `uninit` no
revierte del todo lo que `init` hizo — que es exactamente su contrato.

**Hacer**

1. Normalizar los finales de línea **en la comparación**, no en el archivo: no
   reescribir el archivo del usuario para poder compararlo.
2. Que `quitarReglasDeArchivo` retire todas las apariciones con cualquier mezcla
   de finales de línea, incluidos los duplicados que dejaron ejecuciones previas.
3. Escribir el bloque con el final de línea que ya domina en el archivo destino.

**Aceptación**

- Test: archivo con el bloque en CRLF → `init` **no** duplica.
- Test: archivo con el bloque en CRLF → `uninit` lo retira.
- Test: archivo con dos bloques duplicados → `uninit` retira ambos.

---

## T0.8 — Clases de archivo y volumen que no bloquea por documentación

| | |
|---|---|
| Ejecuta | **Ya implementado en la sesión de planificación** |
| Estado | 🔄 hecho en el worktree, pendiente de commit en T0.1 |
| Commit | `feat(git): el volumen no bloquea por documentacion ni archivos generados` |

Se adelantó porque **bloqueaba el propio trabajo de planificación**: escribir el
informe de arquitectura puso `check` en 4701 líneas y frenó el desarrollo sin
aportar ninguna seguridad.

**Qué se hizo**

- `internal/git/clases.go`: `ClaseArchivo` con precedencia
  `generado > test > docs > config > código`, sin clasificar por subcadena
  suelta — `latest/version.go` y `contest.go` vuelven a ser código.
- `CuentaParaVolumen`: `docs` y `generated` no frenan al guardián.
- `internal/git/diff.go`: `MedirVolumen` devuelve `Bloqueante` e `Informativo`
  por separado; `CheckDiffLimits` se conserva devolviendo el bloqueante.
- `esCodigoGigante` deja de dispararse con documentos y generados: un `.md` de
  1792 líneas ya no recibe una oferta de refactorización SRP.
- `esDocumentacionExtensa` aísla el documento largo en su propio lote, como ya
  hacía la configuración.
- `check` informa aparte de las líneas que no bloquean.

**Verificado**

```
📊 Líneas añadidas de código en este Worktree: 998 [CRITICO]
📄 Además, 4055 líneas de documentación y archivos generados (no cuentan para el límite).
```

**Lo que NO se hizo y queda para F3-T3.1**: hacer las reglas de clase
configurables por repositorio y retirar `ClasificarCapa`. Aquí solo se añadió el
eje de clase, ortogonal a la capa, sin tocar el criterio de agrupación de
`slice`.

---

## T0.9 — `slice plan`: proponer sin bloquear

| | |
|---|---|
| Agente | sonnet / xhigh |
| Presupuesto | ≤ 300 líneas |
| Depende de | T0.1 |
| Commit | `feat(slice): subcomando plan que propone sin commitear ni bloquear` |

**Contexto**

- `internal/git/plan.go` (`ConstruirPlanFragmentacion` :46, `PlanFragmentacion` :28)
- `cmd/sentinel/main.go` (`ejecutarSlice` :421, `construirDecisionGigante` :457)
- `docs/auditoria/b-guardian.md` → **B9** y la sección «Descartado: un flag `--yes`»

**Por qué**: hoy `slice` es un REPL sobre `stdin`, así que un agente no puede
conducirlo y toda tarea de la reingeniería se detiene en el commit. La petición
**no es autoaprobación** —eso sigue descartado— sino cambiar el transporte de la
pregunta: que el agente la reciba y se la traslade al usuario.

**Hacer**

1. `sentinel slice plan [--json]`: construye el plan y lo emite. **No commitea
   nada.** Es idempotente y seguro de ejecutar tantas veces como haga falta.
2. Segunda implementación del callback de decisión: en vez de preguntar por
   `stdin`, **registra la pregunta** en el plan como decisión pendiente. El
   modo interactivo actual se conserva intacto.
3. El plan serializado incluye:
   - `plan_id`: hash de los lotes y sus rutas;
   - `estado_worktree`: hash del árbol pendiente en el momento de planificar;
   - `decisiones_pendientes[]`: id, archivo, pregunta y opciones admitidas.
4. Exit: `0` sin decisiones pendientes, `3` si las hay.

**No tocar**: `ejecutarSlice` interactivo. Esta tarea **añade** una vía, no
sustituye la existente.

**Aceptación**

- Test: con un archivo de código gigante, `plan` devuelve la decisión pendiente
  y exit 3, **sin crear ningún commit**.
- Test: dos ejecuciones seguidas sobre el mismo árbol dan el mismo `plan_id`.

---

## T0.10 — `slice apply`: ejecutar un plan aprobado

| | |
|---|---|
| Agente | sonnet / xhigh |
| Presupuesto | ≤ 320 líneas |
| Depende de | T0.9 |
| Commit | `feat(slice): subcomando apply con aprobacion ligada al plan` |

**Contexto**: `internal/git/plan.go` (`EjecutarPlanFragmentacion` :174),
`cmd/sentinel/main.go`

**Hacer**

```
sentinel slice apply --plan plan.json --answers respuestas.json
```

Tres reglas de seguridad, cada una con su test:

1. **Ligadura al árbol**: si `estado_worktree` no coincide con el árbol actual,
   `apply` se niega. Es la congelación del candidato de F1 aplicada aquí: no se
   ejecuta un plan calculado sobre otro estado.
2. **Ligadura al plan**: las respuestas referencian el `plan_id`. Una aprobación
   de un plan anterior **no sirve** para uno nuevo.
3. **Sin valores por defecto**: toda decisión pendiente exige respuesta
   explícita. Si falta una, error. Ni siquiera «aprobar todo» es implícito.

Los commits siguen usando `--no-verify`, con la misma justificación de siempre
(`plan.go:167-173`).

**Garantía que se preserva y garantía que NO se promete**

- Se elimina el **accidente** de B9: no hay lectura de `stdin` que pueda
  confundir «nadie al teclado» con «el humano aprobó». La aprobación es un
  artefacto explícito ligado a un plan concreto.
- **No** impide a un agente deliberado saltarse el guardián: siempre puede
  llamar a `git commit` directamente. Eso está fuera del modelo de amenaza y
  conviene no vender la propiedad de más.

**Aceptación**

- Test: `apply` con el árbol modificado tras el `plan` → se niega, sin commitear.
- Test: `apply` con respuestas de otro `plan_id` → se niega.
- Test: `apply` con una decisión sin responder → error, sin commitear.
- Test: camino feliz → los commits esperados, con los mensajes aprobados.

---

## T0.11 — Documentar el flujo para agentes

| | |
|---|---|
| Agente | sonnet / xhigh |
| Presupuesto | ≤ 150 líneas |
| Depende de | T0.10 |
| Commit | `docs: flujo de slice conducido por agente con decision humana` |

**Contexto**: `CLAUDE.md`, `AGENTS.md`, `.claudecode.md`, `README.md`

**Hacer**: actualizar la regla de volumen para describir el flujo de dos pasos —
`slice plan` → trasladar las preguntas al usuario → `slice apply`— dejando claro
que la decisión sigue siendo del humano.

**No tocar el bloque literal de reglas sin coordinarlo con T0.7**: `uninit` lo
busca byte a byte (`main.go:24`, `main.go:339`). Si T0.7 ya normalizó la
comparación, esta tarea puede reescribir el bloque; si no, no.

---

## T0.12 — `slice` deja de mezclar documentación con código

| | |
|---|---|
| Agente | sonnet / xhigh |
| Presupuesto | ≤ 180 líneas |
| Depende de | T0.1 |
| Commit | `fix(slice): no mezclar documentacion y codigo en el mismo lote` |

**Contexto**

- `internal/git/clases.go` (`ClaseArchivo`, ya implementado en T0.8)
- `internal/git/plan.go` (`agruparPorCapas` :211, `ConstruirPlanFragmentacion` :46)
- `internal/git/slice.go` (`construirSecuenciaLotes`, `ordenCapas`)

**Evidencia del problema, de esta misma sesión.** El commit `3160133` salió así:

```
feat(git): add file class taxonomy with observability design doc
  docs/reingenieria/f9-observabilidad.md
  internal/git/clases.go
```

Un commit que es dos cosas, porque `ClasificarCapa` manda los `.md` a `backend`
(cae en el caso por defecto, `slice.go:214-227`). No es anecdótico: las tareas
restantes de la reingeniería producen código y documentación juntos, así que se
repetiría en cada una.

**Hacer**

1. Agrupar primero por **clase** (`ClaseArchivo`) y dentro de cada clase
   mantener el orden de capa actual. Ningún lote mezcla clases.
2. Orden de salida de los lotes: `config → source → test → docs → generated`.
3. El límite de 400 líneas por lote no cambia.

**Alcance estricto**: esto **no** es el clustering por cohesión. Solo impide
mezclar clases. La agrupación por componentes conexas sigue siendo **F3-T3.7**,
y la configurabilidad de las reglas de clase sigue siendo **F3-T3.1**.

**No tocar**: el diálogo A/R/E/C, el trato de archivos gigantes, ni el
`--no-verify` y su justificación (`plan.go:167-173`).

**Aceptación**

- Test: un cambio con `.go` y `.md` produce lotes donde ningún lote contiene
  ambas clases.
- Test: los lotes siguen siendo ≤400 líneas.
- Los tests existentes de `plan_test.go` que sigan aplicando pasan; los que
  cambien se adaptan **explicando por qué** en el informe.

---

## T0.13 — El prompt de mensajes de commit no fija el idioma

| | |
|---|---|
| Agente | sonnet / xhigh |
| Presupuesto | ≤ 120 líneas |
| Depende de | T0.1 |
| Commit | `fix(agentadapter): fijar el idioma del mensaje de commit en el prompt` |

**Contexto**

- `internal/agentadapter/cli.go` (`construirPromptAgente` :1074)
- `internal/agentadapter/cli_test.go`
- `CLAUDE.md` → sección «Convenciones»

**Evidencia de esta sesión.** La fragmentación de T0.1 produjo 13 commits con el
idioma **mezclado**: 11 en inglés y 2 en castellano, decididos al azar por el
modelo dentro de la misma ejecución.

```
docs(reingenieria): add F1 gate documentation
docs(reingenieria): añadir contrato del store para la fase 2
docs(reingenieria): add F3 risk change analysis document
docs(reingenieria): añadir diseño de la fase 4 de grafo incremental
```

**Causa**: `construirPromptAgente` pide «un mensaje de commit semántico bajo el
estándar Conventional Commits» y no dice en qué idioma. El historial del
repositorio está en castellano sin tildes.

**Hacer**

1. Fijar el idioma en el prompt, explícitamente y con un ejemplo.
2. Hacerlo configurable en el yml (`commit_language`, por defecto el del
   historial) en lugar de codificarlo: no todos los repositorios escriben en
   castellano.

**Aceptación**: test de que el prompt generado contiene la instrucción de idioma
y el ejemplo.

**Nota**: es un defecto de *prompt*, no de arquitectura. Se arregla aquí porque
ensucia el historial de todas las fases siguientes.
---

## Follow-up pool — durable-runs core closure (post R11, ticket 13)

Registered 2026-08-24 after completing the core R-roadmap. Deferred by user
decision; not part of any active unit.

### FU-1: Spanish strings sweep in production surfaces (policy debt)

Post-legacy production code still ships user-facing Spanish strings, which
violates the AGENTS.md language policy for new artifacts:

- `internal/gate/gate_durable.go`: `"gate: la ejecución duradera no pudo
  asentarse"`.
- `internal/setup/uninstall.go`: hook disclaimer constant (`avisoHooks-
  Repositorio`, "no se elimina"...).
- Validation orchestration messages (`No se pudo ejecutar la validación...`)
  and remaining ❌-prefixed CLI texts in gate/review paths.

Scope: sweep + update every test that pins those literals. Coordinate with
FU-2 because both touch the same files.

### FU-2: oversized files split (>500-line guardian threshold)

Pre-existing debt recorded with guardian bypasses across multiple units:

- `cmd/sentinel/main.go` (~1093 lines)
- `internal/review/engine.go` (~682)
- `internal/config/parser.go` (~672)
- `internal/agentadapter/cli.go` (~575)
- `cmd/sentinel/comandos_runs_actions.go` (~398 and growing)

Split by cohesion (the engine's orchestration/adapters/tests precedent from
R9 applies). Do the FU-1 string sweep in the same pass to avoid touching these
files twice.

---

## Follow-up pool — F9 observability (T9.1b)

Registered 2026-08-29 while accepting T9.1b. Deferred because no evidence
source exists today, not because the work was skipped.

### FU-3: cost, scope, and reuse have no producer

T9.1a's schema declares `ExecutionCost`, `ExecutionScope`, and
`ExecutionReuse`. T9.1b leaves all three nil because none has an observable
source in the agent execution path:

- Cost: no configured adapter reports a price and the repository holds no
  pricing table. A producer needs either provider-reported cost on the wire or
  an explicit, versioned pricing source; `Provenance.Source` exists precisely
  so an estimate can never masquerade as a report.
- Scope: `internal/validation.resolverComando` decides full versus affected,
  but it belongs to the deterministic gate, not to a durable run.
  `ExecutionMetrics` is keyed by run ID, so a producer needs a validation-side
  record, not an attribution onto an agent run that never made the decision.
- Reuse: `Controller.Start` rejects a duplicate run with
  `ErrRunAlreadyExists`; nothing serves a capability from a prior result. A
  producer needs an actual reuse path to exist first.

Target: revisit when a provider exposes cost, or when validation-scope and
capability-reuse evidence gains a durable home of its own. Until then T9.3a
must report zero coverage for the three, never a measured zero.

### FU-4: the T9.1b producer seam carries structural debt

Recorded 2026-08-29 when T9.1b was accepted. Every item below was raised as a
WARNING by the phase reviewer, verified, and deferred as structural work on the
seam T9.1b had just built rather than as a defect in it.

- `internal/review/engine.go` now imports `internal/acpadapter` and exposes its
  result type through the reviewer capability. The domain engine therefore has
  to change for each provider's result shape, which reverses the intended
  dependency direction.
- Metrics finalization crosses into the review domain as a four-string
  callback, and each command wiring converts it into storage failures
  separately. The primitive signature leaves ownership and the valid failure
  values implicit.
- `observedAgent` now owns two capability descents over the same wrapper: the
  rich policy-aware one and the older `ReviewWithContextAndPolicy` one. They
  are maintained independently and can diverge on policy enforcement, context
  handling, or authorship recording.

Target: a provider-neutral rich result contract that lets one descent serve
both paths. Doing it inside T9.1b would have rewritten the seam under test
while its own correctness was still being established.

A fourth item joined the same family when the unavailable reason started
leading with the cause the provider reported. `internal/review/diagnostico.go`
reads an agent CLI's unstructured terminal presentation — a line marker plus
ANSI colouring — from inside the domain layer, with no provider-specific
extension seam. A provider that formats its failures differently would need a
change in `internal/review` rather than in its adapter.

It was accepted deliberately. The alternative, a table of which agent prints
what, ages with every provider and was the coupling this change existed to
avoid; and leaving the operator with a bare timeout while the real cause sat
hundreds of lines down was the worse of the two. The durable fix belongs with
the contract above: once results carry provider-neutral structure, the failure
cause travels as data instead of being recovered from rendered text.

### FU-5: the review context provider is too narrow, and F4 is closed

Recorded 2026-08-29 while accepting T9.1b. The reviewer spends its budget
searching for call sites: the observed queries were shapes like
`FinalizeMetrics|ReviewTransportWithEvidence|...` and
`AppendAttemptObservation|AttemptObservation|...`, which all ask the same
question — where else is this symbol used. `graph.ProveedorCodeGraph` could
answer it and does not: it returns only `affectedTests`, capped at 32
references and 32 KB.

This belongs here rather than in F4. F4 closed with evidence at `fc9cb82`, and
T4.6 is precisely this provider. Two of its recorded constraints are deliberate
fixes, not oversights, and must not be undone by whoever picks this up:

- The `HEAD == reviewed sha` gate is the correction for the CRITICAL "contexto
  no ligado al commit" that blocked `627430d` and was fixed in `fc80f96`. The
  index describes the working tree, so answering about a different commit would
  be inference. Widening coverage means computing the context over the same
  immutable review snapshot Sentinel already materializes, never relaxing the
  check.
- The 3s independent per-subprocess timeout was an explicit user decision in
  `5187fb0`. More calls per review need a total budget, not a bigger per-call
  one.

Prerequisite: T9.2 must first make the provider's six silent skips observable.
Widening what it returns before knowing how often it is active at all would be
tuning by intuition, which is what F9 exists to stop.

Scope when it is picked up: new `review.Relation` values for callers and
callees of the symbols in the diff, sourced from `codegraph callers|callees|
impact`, with a per-relation share of the existing reference budget so callers
cannot crowd out affected tests, and every new path validated through
`rutasSeguras` plus symlink resolution and the containment check.
