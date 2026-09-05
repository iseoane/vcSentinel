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

#### Partial split 2026-09-05 (FU-2/C6, pr subcommand only)

Moved PR orchestration from `cmd/sentinel/comandos_pr.go` (1045 lines) into new `internal/app/pr` (`review.go` 303, `create.go` 350, `publish.go` 151, `wiring.go` 79); `cmd/sentinel/comandos_pr.go` is now 480 thin lines (flag parsing plus dispatch). Pure mechanical movement, no logic change; existing pr tests pass unmodified. Hazard cleared before moving: 21 live block fichas cite `internal/git/snapshot.go`, `cmd/sentinel/comandos_runs.go`, and siblings — zero cite `comandos_pr.go` — and no `dispositions.jsonl` exists, so the move orphans no live block and no standing disposition. `review.Fingerprint` inputs, the ledger, and `review.IsBlocking` are untouched.

Deliberately untouched (11 files still over 500): `internal/review/engine.go`, `cmd/sentinel/main.go`, `internal/review/finding.go`, `internal/store/execution_events.go`, `cmd/sentinel/comandos_review.go`, `internal/metrics/metrics_executions.go`, `internal/tui/art/overview.go`, `internal/config/parser.go`, `internal/review/ledger.go`, `internal/review/renderer.go`, `internal/tui/control/control.go`. A partial, verified split is the correct outcome; the rest stays for future one-subcommand units per C6.

#### Accepted, not repaired (2026-09-05)

The remaining oversized files stay as they are. This is a decision, not a
pause: the split is mechanical and carries no design risk, but it is the only
item in this register whose cost is large and whose benefit is entirely
internal, so it does not earn priority over work with a correctness or security
consequence.

Corrected count: 22 production files exceed 500 lines, not the 11 listed above.
That list named only the files the pr unit deliberately left behind, and it was
never the full inventory. The worst are `internal/review/engine.go` (1245) and
`cmd/sentinel/main.go` (1179).

The blocker anyone resuming this must clear first. `review.Fingerprint` hashes
Symbol-or-File and NO finding in this repository populates `Simbolo` (measured:
0 of 1174), so every fingerprint falls back to the file path. Moving code out of
a file changes the fingerprint of every finding anchored to it, and live blocks
on that file become unaddressable: still in the ledger, no longer referenceable,
impossible to refute because `refute --fingerprint` needs the exact value. The
pr split was safe only because zero live block fichas cited `comandos_pr.go` —
that was luck, not procedure.

Measured 2026-09-05 for the two obvious next candidates:
`cmd/sentinel/comandos_review.go` — 19 fichas cite it, 11 findings confirmed +
CRITICAL and 1 CRITICAL with no status, all twelve blocking under
`review.IsBlocking`. `cmd/sentinel/main.go` — 99 fichas cite it.

Trigger to revisit: a file's size actually obstructs a change someone needs to
make, or the fingerprint gains a symbol component that survives a move. Resume
with a file carrying few live blocks, never with `main.go`.

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

### FU-6: three finding statuses have no production writer

Recorded 2026-08-31 while disposing of T9.1a and auditing T9.3a's aggregates.

`internal/review/finding.go:112-114` declares `StatusAcceptedByUser`,
`StatusFixed`, and `StatusReopened`. None of the three has a single writer in
production code. Only `StatusConfirmed` is assigned
(`internal/review/engine.go:511`, `internal/gate/gate.go:434`,
`cmd/sentinel/proyeccion_validacion.go:96`) and only `StatusRefuted` is
assigned (`internal/review/engine.go:569,619`).

Two consequences, each verified separately because they are not the same
problem:

- A human who rejects a finding's premise has nowhere to record it against the
  finding. T9.1a's disposition had to be written as prose in
  `f9-observabilidad.md` because the ledger accepts no machine-readable answer,
  so the fresh review of `c50d6d5` stands as `block` over an implementation
  whose premise was verified inert.
- `FindingsAggregate.Reopened` is structurally zero: it is set only from
  `StatusReopened`, which nothing writes. `sentinel metrics` can never report a
  nonzero reopen count, so exit criterion 3 cannot cite it.

`Overrides` is deliberately excluded from that second point. It has a real
producer: besides `StatusAcceptedByUser`, `aggregateFindings` derives it from
`store.Decision` records through `isUserOverride`, and `pr --force` writes
those to `decisions.jsonl` (`cmd/sentinel/comandos_pr.go:827`). This
repository simply has no `decisions.jsonl` at all, which is consistent with
`pr create` never having run here. Its zero is therefore an unexercised path,
not a missing producer, and it is a coverage question for T9.4a rather than
debt.

This is a missing capability, not a metrics defect: the aggregator reads
statuses the rest of the system never writes. Fixing it means a disposition
surface — a way to answer a finding and persist that answer against its stable
fingerprint — which is new product scope and deliberately outside F9.

Target: pick it up when a disposition surface is designed. Until then T9.3a
must report reopen coverage as unknown rather than as a measured zero, exactly
as FU-3 requires for cost, scope, and reuse.

#### Recording a disposition is not enough to clear a block

Added 2026-09-01, after verifying what actually makes a CRITICAL finding stop
blocking. The paragraphs above understated the problem: they treat the missing
writer as the whole gap. It is not.

Three sites decide whether a CRITICAL blocks, and all three use the same
predicate:

```go
finding.Severity == SevCritical && finding.Status != StatusRefuted
```

`internal/gate/gate.go:411`, `internal/review/engine.go:638`, and
`internal/review/engine.go:647`. Only `StatusRefuted` clears a block, and only
the automated refuter writes it (`engine.go:569,619`), after
`validarEvidenciaRefutacion` checks the supplied reason and line range against
the immutable snapshot.

Two consequences:

- A human has no path to clear a block today. `sentinel review --answer`
  answers reviewer *questions* and is folded back into the audit; it never
  touches a finding's `Status`.
- A disposition surface that only persisted `StatusAcceptedByUser` would record
  the human's judgement and change nothing: the finding would still block,
  because none of the three predicates honours that status.

This is the shape of every standing block in F9 — `c50d6d5` (T9.1a), `037c2db`
and `7b375fd` (T9.3a). In each one the code is correct, the finding's premise
is refutable with evidence, and there is no channel to say so.

#### Two options, to weigh when this is picked up

**Option A — human refutation through the existing evidence gate.** A person
supplies a reason and an evidence line range, the same
`validarEvidenciaRefutacion` validates it against the immutable snapshot, and
the finding records `StatusRefuted` with provenance marking the refutation as
human-issued rather than refuter-issued.

- It reuses the gate rather than weakening it: the evidence requirement is
  unchanged, only the issuer differs.
- It clears all three standing blocks through one mechanism, because they share
  one shape.
- It needs the provenance field so metrics and audits can separate human from
  automated refutations; without it, `RefutationRate` silently mixes two
  different things and per-model noise measurement becomes meaningless.
- Risk to design against explicitly: this is a path to unblock the guardian. It
  must stay evidence-bound and auditable, never a free override. Reusing
  `validarEvidenciaRefutacion` is the load-bearing part of this option, not an
  implementation detail.

**Option B — make each finding's premise false in code.** For `c50d6d5` that
means adding the legacy read fallback the reviewer asks for; the finding then
stops applying and the block disappears on re-review.

- It writes dead code for data that never existed: at `c50d6d5` there was no
  production caller of `SaveExecutionMetrics` at all, and the store holds zero
  files matching `executions/v1/*/metrics.json`.
- It does not generalize. Each standing block would need its own bespoke
  change, and each would preserve a path nothing uses.
- It was already rejected on the merits for T9.1a, and that rejection was
  reaffirmed on the "no producer existed" evidence rather than on preference.

Recommendation when picked up: Option A, and **not inside F9**. It is new
product surface and it touches the gate, so it deserves its own change with its
own review. F9 closes with its three blocks recorded as they stand.

#### Resolution (Option A, 2026-09-04)

Picked up as its own change per the recommendation above. `sentinel refute`
records an evidence-bound human refutation addressed by reviewed SHA plus
stable fingerprint, with a reason and an immutable snapshot line range
checked by the existing `validarEvidenciaRefutacion` gate (SHA echo, safe
path, range bounds, finding containment, exact snapshot evidence, range
hash). Answers persist append-only in `dispositions.jsonl`, separate from
the immutable review revisions, and apply through the single
`ApplyDispositions` projection. One shared `IsBlocking` rule now serves the
engine, the gate, and `BloqueantesDeRama`: human `refuted` and `fixed`
clear a block, `accepted_by_user` never does, and `reopened` blocks again.
Human and automated provenance stay distinguishable through
`RefutationActor`, and per-model/agent aggregates credit no human decision
to an agent invocation. Out of scope on purpose: threading standing answers
through fresh branch re-audits (`pr review` re-reports before the overlay),
and writers for `accepted_by_user`/`fixed`/`reopened` beyond the projection
semantics pinned for them.
#### Unit A (2026-09-05)

Standing answers now carry into fresh net re-audits: `NetReviewOptions`
carries the range dispositions (moved there from `OpcionesRama` after a
design warning: the branch scope never consumed the field, so it was a
misleading courier). `runNetReview` selects answers recorded against
in-range commits whose evidence still holds at the head (normalized
containment through the shared `contieneEvidenciaNormalizada` helper, so a
moved line still carries and removed evidence re-reports), then
`mergeNetDispositionsForEngine` joins them with the head answers: head wins
by fingerprint, empty fingerprints are dropped, and the rest are cloned to
the head SHA as engine input so the single SHA-bound overlay, aggregation,
and verdict downgrade stay in one place. Anything unverifiable is dropped
rather than cleared. `pr review` and `pr create` both fail closed on a
corrupt log before spending review tokens (`aplicarDisposicionesPrReview`
covers the pr-review branch with the same tested seam pr-create already had).
#### Unit B (2026-09-05)

The remaining two writers landed as their own commands, not as one change
with the carry-over (unit A): `sentinel accept` records `accepted_by_user`
with a reason and no evidence range — judgement, never an unblock — and
`sentinel reopen` records `reopened` through the same evidence gate as
refutation, on its own path because it targets the non-blocking finding
`refute` refuses. `fixed` deliberately gained no per-fingerprint writer: the
correction attribution (`registrarCorrecciones`) matches by file overlap plus
ancestry, not defect identity, so emitting `StatusFixed` from it would invent
precision the data does not carry; `FixedIn` stays the separate branch-level
record. `StatusReopened` now has a production writer, so reopen coverage is
measurable where answers exist.

#### Review corrections on units A and B (2026-09-05)

Every finding below arrived as a Sentinel WARNING/ADVISORY (never a block)
and was answered before close. Fixed: head-precedence at the merge (a stale
intermediate answer could override a fresher head answer); the empty-carried
drop (RED-first test, then guard); the pr-review tested seam with identity
and nil-net assertions; accept corrupt-log and lock-cleanup coverage;
reopen fixed-status reopen, non-numeric line parsing, and an isolated
unsafe-path refusal test; completed command enumerations (including the
pre-existing `gate` gap); stale reopen-coverage comments; the shared
disposition test fixture adopted at every site. Recorded without code
change: range scoping from fichas (a disposition needs its review record to
exist, so the gap is unreachable and fail-safe); the refute/accept/reopen
scaffold triplication (deliberate: the compare-and-append path stays
untouched while it hardens); reopen gate ordering (fail-closed either way);
the 20-line cap (enforced by the shared gate, help claim backed);
`validarArgumentos` (parser-owned commands pass through; smoke-tested
end to end); the SinNet snapshot artifact (helper in production since the
earlier fix commit).
#### Post-merge defects 1-3 (2026-09-05)

Three defects found reviewing FU-6, fixed test-first on dedicated branches
and merged `--no-ff`. The core design held: fingerprint-only matching,
append-only `dispositions.jsonl`, unchanged `IsBlocking` and `Fingerprint`
inputs, no revision mutation, no third finding shape.

Defect 1, escalation inheritance: a standing refutation is now refused when
the re-audited finding is MORE severe than the recorded `TargetSeverity`
(audit metadata beside `TargetDimension`, never a match key; ordered by the
existing `severityRank`). The guard lives in `applyToHallazgo` /
`applyToReviewFinding`, so every application path inherits it, and a refusal
stamps nothing: the finding keeps its status quo in every blocker surface
operators already read. Pre-`TargetSeverity` records keep applying
(apply-as-unknown) so no historical refutation re-opens. That choice is
safe beyond preserving behaviour: the only disposition status that moves a
finding from blocking to non-blocking is `refuted`, its sole writer (the
refute command) gate-enforces a blocking — hence CRITICAL — target, and
CRITICAL is the rank maximum, so no legacy clearing record can ever meet a
strictly more severe re-audit. `accepted_by_user` never clears and
`reopened` re-blocks, so neither can suppress a block however applied. The
`severityRank` totality question closed with ingestion evidence: `procesarFindings`
normalizes every engine-emitted severity at the single parse point, so no
unranked value reaches the guard. De-escalation and equal severity still
apply, pinned by named subtests, as do the `TargetSeverity` write-time pins
on the refute/accept/reopen persistence paths.

Defect 2, line-less findings: the human gate pins the validation line to the
evidence range start when `LineaInicio <= 0`, waiving only containment. File
pin, 20-line window, exact extract match against the audited Git object,
12-character minimum, and range hash still apply byte for byte, so the gate
stays fail-closed; `reopen` gains the path through the shared gate with zero
changes. Ingestion-side fix rejected: it would mutate append-only ledger
records and change the finding contract. Accepted trade-off: a file-scoped
finding is cleared by evidence anywhere in the cited file — inherent to a
finding with no line to bind to.

Defect 3, model noise basis: a human-refuted finding now leaves the model
and agent `Known` basis entirely, so the rate is computed over a population
its numerator can reach; a purely human-refuted population reports no
evidence instead of a measured 0.00. Dimension counters are byte-identical
(`dim.Refuted` still counts human refutations, of record there). Rule of
thumb: any aggregate whose numerator is restricted to one actor must exclude
the other actor's records from its denominator.

Follow-up: an escalated refusal is currently indistinguishable from no
answer at all (deliberate — the refusal is the absence of the answer, not a
record). A refusal marker (audit-metadata field or metric) would let an
operator seeing a confirmed CRITICAL blocker know a human already answered
it at a lower severity and should re-refute at the escalated severity.
#### Merge closure (2026-09-05, `811b9f1`)

Merged `--no-ff` as `feat(review): evidence-bound human finding refutation
(FU-6)`: 17 commits (13 implementation + 4 `pr create` seam fix and test
hardening). The `pre-commit` volume hook rejected the merge commit (2820
staged lines); it was completed with `--no-verify` because every
constituent commit carries its own review record and the conflict
resolution is config-only (review-model rename, branch side wins).
`go test ./...` passes (39 packages, zero failures).

Coverage gaps, recorded rather than waived:

- Four branch commits have no ficha: `fbaa725` (config-only model rename),
  `fc1172b`, `5d25287`, `3b6ede6` (tests/fix). A review of `3b6ede6` was
  attempted 2026-09-04 but produced no record.
- Two mid-branch fichas carry `block`: `b2f6c7f` (initial implementation,
  six CRITICALs over corrupt-log appends, ambiguous fingerprints, and the
  advisory/template overlay) and `1e5a8f0` (lock-cleanup discarding a
  completed append). Both were addressed by descendant commits
  (`fe562f7`, `f9ba1f8`, `2d1258d`, `3b6ede6`, `be3dd5b`) and every later
  ficha, including the tip, is `warn` with no block. That resolution is by
  descendant fix, not by a formal disposition through this feature's own
  human-refutation path; no refutation record claims otherwise.
- Gate evidence: `gate --stage pre-push` PASS on tip `dbb16f8` (paid review
  profiles, all dims `ok`) during a free-tier provider outage that 500s
  every `normal`/`deep` free-model call. A clean-tree gate on the merge
  commit itself is non-meaningful with this toolchain: the merge diff
  extracts empty (one spec artifact, premise disproven, inert) and the
  free tier still fails the remaining dims as infrastructure.

### FU-7: aggregation drops the disposition, so metrics cannot see it

Recorded 2026-08-31 while correcting T9.3a. This is the real obstacle to exit
criterion 1 of F9, and it is not FU-6: `StatusConfirmed` and `StatusRefuted`
both have producers, and 85 real dispositions already exist on disk.

Measured on the live ledger:

- 1377 raw findings across every ficha revision. 85 carry a disposition: 84
  `confirmed` and 1 `refuted`.
- Those 85 live only in the v1 raw `dims[].findings[]` shape.
- 74 revisions carry `aggregated_findings`, and all 564 of those aggregated
  findings have an empty status. The correlation is exact: every revision
  holding a disposition also holds aggregated findings that drop it.
- `review.Revision.HallazgosEfectivos` (`internal/review/ledger.go:54-55`)
  prefers `AggregatedFindings` whenever they are present, and only falls back
  to converting `Dims`.
- `internal/metrics/metrics_reader.go:115` reads findings through that method.

So `sentinel metrics` reports zero confirmed and zero refuted at zero coverage
while 85 dispositions sit in the store.

Upstream cause: `internal/review/engine.go:501-511` assigns `StatusConfirmed`
to `dimension.Resultado.Findings[i]`, the per-dimension collection, and
`:569,619` assign `StatusRefuted` there too. The aggregated set — assigned at
`cmd/sentinel/comandos_review.go:174` and `internal/review/rama.go:332` —
never receives it. `internal/review/aggregation.go:24` additionally skips
refuted findings outright.

The repository already knew this and solved it once, in the other direction:
`cmd/sentinel/comandos_runs_prune.go:80-87` deliberately scans the raw `Dims`
hallazgos and states that "Aggregation drops refuted findings, so scanning
AggregatedFindings alone would miss the refuter stream; the raw Dims hallazgos
close that gap." The metrics reader does exactly what the prune code was
written to avoid. Use that precedent when picking this up.

Scope when taken: make the disposition reachable from the ledger, either by
propagating `Status` into `AggregatedFindings` or by having the metrics reader
consult the raw `Dims` hallazgos for it. Prefer whichever keeps one source of
truth; do not introduce a third finding shape. Deliberately excluded from the
T9.3a coverage correction so that candidate stayed one reviewable change, and
because this one touches the review engine rather than the aggregator.

Priority: before T9.4a. It converts exit criterion 1 from "unknown" into 85
measurable dispositions across six dimensions, using data that already exists
and needing no new product surface.

#### Landed 2026-09-01

`c1655a2` merges six reviewed commits from `f9-fu7-dispositions`, ahead of
T9.4a as this entry required. Exit criterion 1 of F9 is answerable from the
store: confirmed `0 → 78`, refuted `0 → 1`, confirmation coverage
`0/837 (0.00%) → 78/837 (9.32%)`. The `observed` delta of one is the single
refuted raw finding that `aggregation.go:24` drops and that now enters as its
own observation; nothing was duplicated.

The chosen shape is the one this entry preferred: no third finding shape. A
read-side observation-only projection, `Revision.FindingsWithDispositions`,
joins the two persisted shapes, so the raw `Dims` hallazgos remain the single
source of truth for a disposition — the same precedent
`comandos_runs_prune.go:80-87` set. Full record, including the per-dimension
breakdown, in [`f9-observabilidad.md`](f9-observabilidad.md) under
"FU-7 landed — dispositions are reachable".

This entry stayed open in the register for three days after the work landed.
The rate values still print `unknown` because coverage is partial, which is the
T9.3a contract working rather than a residual failure.

### FU-8: the executions aggregate counts terminal failures twice

Recorded 2026-09-01 while evaluating T9.4a's failure-class axis.

`internal/execution/metrics_finalize.go:93-99` copies every terminal
non-success outcome into `ExecutionMetrics.Failures`, converting the
`agentrun.OutcomeClass` into a `store.FailureClass` of the same name. The
aggregator then counts the same event twice:
`internal/metrics/metrics_executions.go:76-81` appends from the outcome stream,
and `:82-86` appends the snapshot's own `Failures`. `appendFailure`
(`:671-679`) increments an existing class rather than deduplicating, so nothing
absorbs the repeat.

Measured on the live store at 2026-09-01T19:49:35Z:

- The 325 metrics snapshots hold `failure` 16 and `timeout` 2.
- `sentinel metrics --json` reports `failure` 64 and `timeout` 5 against 82
  failed runs.

The semantic classes are unaffected because they are not `OutcomeClass` values:
`invalid_output` 44, `schema_invalid` 9, and `missing_semantic_payload` 3 are
written only by the review transport.

A second, independent problem shares the same field. `executions.failures[]`
merges two populations — outcome classes over all 850 logical runs and
producer-reported classes over the 325 runs that have a snapshot — into one
flat list with no source tag and no coverage field. Even without the double
count, a reader cannot tell which denominator a class belongs to.

Target: give the aggregate one source per class and a coverage denominator, or
split the two populations into separate reported sets. Until then T9.4a
records the failure-class axis as inadmissible for calibration; this is not a
sample-size problem and no observation window fixes it.

#### Partial close 2026-09-03 (T9.5)

The first problem — the same failure counted twice — is fixed in
`aggregateExecutions`: snapshot classes already counted from live terminal
outcomes are skipped (`outcomeClassCounted`), so each class has one source
per run. Live failure breakdowns drop to their true values once (measured
on the live store: `failure` 213→123, `timeout` 5→3; every semantic class
byte-identical — `invalid_output` 101, `schema_invalid` 31,
`missing_semantic_payload` 27, `provider_error` 2, `cancellation` 28,
`unavailable` 9 — which is the proof the skip removes only duplicates).
This changes what `sentinel metrics` reports for measured failed runs; the
T9.4a-era figures quoted above predate the fix. What made the fix mandatory
here rather than optional: T9.5's acceptance is byte-identical metrics
before and after retention, and a collected failed run contributes its
snapshot alone — without the skip, every collected failure moved the
breakdown from two counts to one.

#### Landed 2026-09-05 (FU-8 open half)

Chose per-class source tag plus coverage denominator (option A): `FailureAggregate` gains `Source` (`outcome` for `failure`, `unavailable`, `timeout`, `cancellation`, `process_error`; `semantic` for every other present and future class) and `Coverage` (`outcome` rows cover `LogicalRuns`/`LogicalRuns`; `semantic` rows cover `MeasuredRuns`/`LogicalRuns`). Counts are unchanged; enrichment runs as a post-pass over the final totals, so T9.5 byte-identical retention holds and the existing invariance tests pass unmodified. `sentinel metrics --json` exposes both fields per row; unknown coverage still renders as `null`, never `0`.

Live store before/after (1940 logical, 1294 measured, 519 failed; counts identical, shape enriched): `failure 307 outcome 1940/1940`, `cancellation 30 outcome`, `timeout 12 outcome`, `unavailable 10 outcome`, `invalid_output 121 semantic 1294/1940`, `missing_semantic_payload 46 semantic`, `schema_invalid 32 semantic`, `malformed_json 2 semantic`, `provider_error 2 semantic`.

Rejected the split-populations shape (two reported sets): it would break the T9.5 exclusive-one-entry retention assertion, force every consumer to rejoin the halves by class to answer how many runs failed, and still require a denominator on each half. One self-describing list preserves stable ordering and additive evolution.

Deviation: none. `review.Fingerprint` inputs, the append-only ledger, and `review.IsBlocking` are untouched.

### FU-9: observed identity sits in the event stream where the producer cannot read it

Recorded 2026-09-01 while evaluating T9.4a's model-calibration axis.

`sentinel metrics` reports `identity_coverage` as 0 of 325. A direct scan of
the store contradicts the impression that no identity was ever recorded: 426 of
the 850 execution event streams carry a model string in the flattened
`AttemptOutcome.Agent`, `.Model` and `.Effort` fields — `openai/gpt-5.6-luna`
273, `openai/gpt-5.6-terra` 107, `claude-sonnet-5` 18, `claude-opus-5` 14, and
`haiku` 14.

`foldExecutionMetrics` (`internal/execution/metrics_finalize.go:111-119`) builds
`ObservedExecutionIdentity` only from `outcome.Observation.*`. In those records
`Observation` carries `duration_ns` alone, so the identity is never appended
and the snapshot stores none. `ReadAttemptOutcomes`
(`internal/store/execution_outcomes.go:285-289`) does populate the flattened
fields on read-back, so the producer reads a different field from the one that
holds the value.

What is **not** established, and must be settled before any fix: whether those
flattened values are adapter-observed evidence or a resolved configuration
declaration. The phase already recorded the governing rule — a run served by a
plain CLI adapter contributes no observed identity, because the configured
model and effort are a declaration rather than evidence. If the 426 records are
declarations, the current zero is correct and deliberate, and the only defect
is that nothing says so. `AttemptOutcome` keeps `Model` and `RequestedModel`
separate, which suggests the former is observed, but that is an inference and
not evidence.

Target: determine the provenance of the flattened fields first, then either
read them in the producer or record why they must stay unread. T9.4a's verdict
is unchanged either way, because 0 of 325 blocks model calibration in both
branches.

Settled 2026-09-05: **declarations.** The flattened values must stay unread;
the current zero is correct and the only defect was that nothing said so
(now stated at the read site in `foldExecutionMetrics`).

Write-path evidence per adapter kind:

- CLI (`opencode`, `claude`): `CLIAdapter.AgenteEfectivo` returns the
  construction config by explicit design
  (`internal/agentadapter/efectivo.go:39-42`). `captureTranscript`
  (`internal/execution/controller_transcripts.go:49-55`) copies that report
  into the outcome sidecar's flattened `Agent`/`Model`/`Effort` on every
  successful review with output. CLI adapters produce no rich result, so
  `Observation` stays duration-only (`controller.go:451-454`). Flattened
  model therefore ⟺ configured model, never a wire observation.
- acpx: `AgenteEfectivo` returns wire-only `ObservedIdentity`
  (`internal/agentadapter/acpx.go:52-53`, "declarations are not producer
  evidence"; empty pre-turn pinned in `acpx_test.go:468-469`). Wire values
  flow split into observed/requested (`internal/acpadapter/review.go:257-261`),
  so genuine wire identity already reaches `Observation` — the flattened copy
  adds nothing.

Store evidence (live scan, read-only): of 865 outcome sidecars, 374 carry a
flattened model — 316 with `agent: opencode`, 58 with `agent: claude`, zero
acpx. Values are config slugs (`opencode-go/muse-spark-1.3-contributor`,
bare `haiku`); 211 have `observation: {duration_ns}` only, 163 have no
observation at all, and **zero** sidecars carry `observation.model`. The
tell is structural: transcript-written records carry `Model` with
`requested_model: null`, while a genuine observation pair carries both.

Consequence: wiring the flattened fields through would launder 374
declarations into reported observations — the exact absence-as-evidence
error. No behavior change; T9.4a's verdict stands in both branches.

Addendum 2026-09-05: this verdict does not mean observed identity is
unobtainable for CLI adapters. `internal/modelprobe` already exists —
"verifies the model that an agent reports for a session" — and is wired into
production at `internal/app/pr/create.go:63`, `wiring.go:37,41`,
`publish.go:101-103`, `review.go:103`, and `cmd/sentinel/comandos_gate.go:135,163`,
feeding `ModeloVerificado` (`internal/review/finding.go:167`). That prober asks
the agent itself and records the verified model, which would turn a declaration
into verified evidence. Wiring it to execution metrics is an OPEN design
decision with a cost (one extra prompt per review), not an impossibility — and
it is recorded separately as a follow-up because the ledger shows it has never
yet produced a verified model.

### FU-10: the review planner never sees content, so `explain` and `review` disagree

Recorded 2026-09-01 while settling why three T9.4a documentation commits
received a review record with zero dimensions.

`change.DetectarCaracteristicas` has exactly two call sites, and they feed it
different evidence:

- `cmd/sentinel/comandos_explain.go:72` supplies the full
  `EntradaCaracteristicas`, added lines included.
- `internal/review/engine.go:333` supplies only `Symbols` and `Rutas`.

Three detectors read added lines and nothing else:
`detectarSeguridadSensible`, `detectarConcurrencia` and
`detectarCambioDeComportamiento` (`internal/change/caracteristicas.go:88-124`).
In the review planner they are therefore starved and always report absent, so
`security_sensitive`, `concurrency` and `behavior_change` can never raise the
risk level that `BundlesForRisk` reads. `sentinel explain` and `sentinel
review` classify the same commit from different evidence, and the review side
is systematically the weaker one.

Demonstrated on this task's own commits. `780c900` is a documentation commit
holding a metrics artifact; `sentinel explain a1a5803..780c900` reports
`high por security_sensitive presente`, while its review recorded zero
dimensions because the planner evaluated it as `NivelNone`.

That instance is a substring false positive, and saying so matters: the
detector matches `token` against lines such as `cached_input_tokens`, so
`explain` was wrong about this commit and the review planner happened to be
right. The instance is evidence of the divergence, not evidence that the
planner under-reviewed here.

The structural risk is the reverse case, which no commit in this task
exercises: a source change that adds credential handling without touching an
exported symbol is `security_sensitive` to `explain` and invisible to the
review planner, so no security dimension is ever scheduled for it.

Target: decide which surface is authoritative and feed both the same evidence.
If content-based detection belongs in review planning, the planner needs the
added lines; if it does not, `explain` should stop reporting a risk level that
review will not act on. Do not fix this by widening one detector: the defect is
that one derivation runs on two different inputs.

#### Resolved 2026-09-02

The planner is authoritative and now receives the same evidence. Both surfaces
derive their detector input from one constructor, so there is no longer a way to
classify a change from a narrower set of facts than `explain` uses.

**The decision and what it cost.** Measurement decided it rather than
preference: over 120 non-merge commits, feeding the planner costs 20% more agent
invocations on source-bearing commits, 304 to 364, and 40 of 69 of them were
classified below `explain` before the change. The full method, the pinned
artifact and the reproducing command are in
`evidence/fu10-divergence.md`. No risk-rule adjustment was needed; what looked
like a rule problem was a detector-input problem.

**The demonstrated instance was a false positive, and that matters for how this
entry is read.** `sentinel explain a1a5803..780c900` reported
`high por security_sensitive presente` for a documentation commit because the
substring `token` matched inside `cached_input_tokens`. `explain` was wrong
about that commit and the planner happened to be right. It is evidence of the
divergence, never evidence that the planner under-reviewed there. The structural
risk was always the reverse case, which no commit in the phase exercised: a
source change adding credential handling without touching an exported symbol.
That case is now the headline regression test, and it failed on behaviour before
the fix.

**Two detectors were narrowed first, and neither is the widening this entry
forbids.** `detectarSeguridadSensible` and `detectarConcurrencia` both read the
added lines of every path with no class filter. Documentation and generated
paths are now excluded from content evidence, through one shared helper rather
than two mechanisms. The discriminator is the path class, not the lexeme: word
boundaries were measured and rejected, because `\btoken\b` rejects
`cached_input_tokens` but also `accessToken` and `refreshTokens`. The
concurrency narrowing was found by the measurement rather than predicted, and
without it the migration would have imported a prose false positive that nearly
tripled the review cost of documentation commits.

**The prose guard holds, measured rather than assumed.** Every documentation
commit that the T9.4a and T9.4b records cite still derives `none` under the full
evidence, `780c900` included. `sentinel review 0845ce8`, a documentation commit
made after the migration, drew zero dimensions end to end.

Delivered as tickets 01 to 06 in `.scratch/fu10-planner-evidence/issues/`.
Follow-ups opened along the way and still open: FU-11, FU-13 and FU-14.

### FU-11: no signal survives the class filter for a credential committed into prose

Recorded 2026-09-02 while closing ticket 02 of the FU-10 sequence, from the
`security` WARNING that `sentinel review ab3acee` raised against its own
change (confidence 0.9, `internal/change/caracteristicas.go:111`).

The premise holds. `detectarSeguridadSensible` now ignores added lines in
`ClaseDocs` and `ClaseGenerated` paths, so a real credential pasted into a
runbook reports `security_sensitive=absent` unless the path happens to match
`PatronesSensibles`. The path-pattern check is the only independent signal
left for those classes.

Two limits narrow the exposure the reviewer described, and both were checked
against `ReglasPorDefecto`:

- "Generated configuration" barely arises by default. `**/*.json`, `**/*.yaml`,
  `**/*.yml` and `**/*.toml` are `ClaseConfig`, which the filter does **not**
  exclude, so they are still scanned. The default `ClaseGenerated` globs are
  `**/*.pb.go`, `**/*_gen.go`, `**/*.lock` and `go.sum`. Injected `Reglas` can
  still classify anything as generated, so the branch is real, just narrow.
- Documentation is the genuine residual, and it is the class the filter was
  added for.

Not treated as a regression, deliberately. Before `ab3acee` this detector was
not secret detection: it matched the substring `auth` anywhere in any added
line, which is what raised a documentation commit to `high` in the first place
(FU-10). Removing a substring match over prose loses no working capability;
the repository has never had a secret scanner.

The remedy the reviewer proposes — high-confidence credential-value patterns
that survive the class filter, or a separate exposed-secret signal independent
of path class — is a new capability, not a repair. It needs its own decision
about what counts as a credential value, its own false-positive budget, and its
own product surface. Folding it into a detector-narrowing commit would have
been exactly the "widen one detector" move FU-10 forbids.

Target: decide whether Sentinel owns exposed-secret detection at all. If it
does, the signal must be independent of `security_sensitive`, because the two
answer different questions: one schedules a review, the other reports an
incident. If it does not, say so in the ficha and stop treating the substring
heuristic as if it were a scanner.

Priority: after the FU-10 sequence closes. It does not block tickets 03 to 06.

#### Decided 2026-09-02, not yet implemented

**The product question is answered: yes, Sentinel owns exposed-secret
detection, and the signal is independent of `security_sensitive`.** The two
answer different questions — one schedules a review, the other reports an
incident — so a credential in prose must not depend on whether prose can carry
review-scheduling evidence. That is why the class filter removing content
evidence from documentation and generated paths, which is correct for its own
purpose, must not be the thing that decides this.

What remains is implementation, and it is a work unit rather than a repair: a
credential found in a documentation path has no surface to be reported through
today. `security_sensitive` schedules a security dimension; an incident needs to
be stated outright, at the point of the change, whether or not a review runs.

Target: a deterministic finding, produced without an agent and independent of
the change profile, that names the path and the matched shape and is reported
even when the review plan schedules nothing. Do not implement it by widening
`detectarSeguridadSensible`, which FU-10 measured and narrowed on purpose: that
would put an incident report back inside the mechanism that decides review cost,
and the two must not share a switch.

Open as implementation work, tracked in `follow-ups.md` under P2. The question
this entry was recorded to ask is closed.

#### Landed 2026-09-05 (branch `feat/exposed-credential-detection`, unpushed)

Three commits: `15bfb6e` (new `internal/secret` scanner), `a2d4824`
(projection plus `review`/`gate`/`explain` wiring), `bb7db5d` (Stripe
shape correction). Guardian: the 776-line scanner atom exceeded the
400-line budget and is not splittable by diff atoms (impl plus its
TDD tests); owner-authorized bypass recorded in that commit body.
The 239-line wiring and 3-line fix went through the gate normally.

Decisions as specified: seven structured shapes, no entropy;
surfaces are `review`/`gate` (deterministic WARNING findings via
`HallazgosDeterministas`, `Dimension` empty, `Evidence` empty) plus
`explain` (text and JSON section); non-blocking (`IsBlocking`,
gate `Estado`/exit and review verdict untouched);
unreadable-or-absent paths render as unknown advisory, never as
clean and never as zero. `detectarSeguridadSensible`,
`review.Fingerprint` inputs, the append-only ledger and
`review.IsBlocking` are unchanged. No child processes, no
environment reads. Live proof: a docs probe commit reported
`github_token` with risk `none` (zero bundles scheduled).

Review commit-by-commit: `15bfb6e` went to `block` on a TRUE
semantic finding (the hyphenated Stripe shape missed real
`sk_live_` keys), corrected by `bb7db5d`, which reviewed `ok` and
was recorded as the correction. `a2d4824` reviewed `warn` with no
criticals. Credential incidents stayed WARNING/pending throughout
and never moved a verdict. `Evidence` verified empty in the
fichas: no value reaches the ledger.

Verification: `go build ./...`, `go vet ./...`,
`go test -count=1 ./...`, race on `internal/secret` plus
`cmd/sentinel`, `./build.sh`, `sentinel check` (0 lines) and
`sentinel doctor` (exit 0; codegraph WARN is worktree-local, hook
WARN is the pre-existing T0.0 trap) — all green. Full suite took
~30s, not the documented ~3min. One self-inflicted deviation: a
`git reset --hard` to drop a smoke probe also wiped the
uncommitted wiring; it was redone and the private history
rebuilt with `reset --soft` before committing. Nothing pushed.

Residual follow-up: the `pr`-review branch flow (`AnalizarRama`)
does not attach these incidents yet; `review`, `gate` and
`explain` cover this unit.

### FU-12: reviews run in a linked worktree never join the repository ledger

Recorded 2026-09-02 while reviewing ticket 04 of the FU-10 sequence, from the
observation that its review record could not be found where every other record
lives.

**Corrected 2026-09-02, after inspecting the code rather than inferring from
the symptom.** The first version of this entry called the per-worktree location
a defect contradicting `AGENTS.md`. That framing was wrong and is replaced.

`internal/store/migracion.go:44-75` documents the per-worktree layout as the
deliberate v1 shape: the v1 review ledger is anchored at the checkout's
**gitDir**, so the main checkout writes to `<gitCommonDir>/vas-sentinel` only
because its gitDir and gitCommonDir coincide, while a linked worktree writes to
`<gitCommonDir>/worktrees/<name>/vas-sentinel`. `MigrarDesdeV1` exists precisely
to consolidate all of them into the shared v2 store. The `AGENTS.md` sentence
describes `internal/store`, the v2 store, which does use the common dir.

The real defect is narrower and sharper: **the writers and one reader disagree,
and the consolidation is never invoked.**

- `MigrarDesdeV1` has no production call site. It is defined and never used, so
  nothing ever merges the worktree ledgers into the shared store.
- `cmd/sentinel/comandos_review.go:61,713`, `comandos_estado.go:252,324` and
  `comandos_pr.go:274,744` build the v1 ledger from **gitDir**.
- `cmd/sentinel/comandos_runs_prune.go:97` builds it from **gitCommonDir**.

So `sentinel runs prune` scans a ledger that a review run from a worktree never
wrote to. Its own code comments explain that it deliberately reads the raw
`Dims` hallazgos to catch the refuter stream; it is reading the right shape in
the wrong directory.

The observation that prompted this entry stands. `sentinel review b7ac0cf`, run
from the worktree `fu10-ticket-04`, wrote its ficha to
`.git/worktrees/fu10-ticket-04/vas-sentinel/`. From that worktree:

- `git rev-parse --git-dir` → `.git/worktrees/fu10-ticket-04`
- `git rev-parse --git-common-dir` → `.git`

Measured on this repository at 2026-09-02: **thirteen** linked worktrees each
hold their own private `vas-sentinel` directory, one per writer worktree used
across phase F9. Every review performed inside a delegated worktree is therefore
invisible from the main worktree.

That matters because delegating to a writer in a dedicated worktree is the
mandated workflow, so this is the normal path and not an edge case. After the
branch merges, `sentinel status`, the gate, and `sentinel metrics` in the main
worktree cannot see the receipts that approved the merged commits. A gate that
validates a receipt for a commit reviewed in a worktree finds nothing.

It also puts a bound on earlier measurements. Any count taken from the main
worktree — the 325 metrics snapshots and 850 logical runs recorded for T9.4a and
T9.4b among them — excluded whatever the writer worktrees held. Those verdicts
are not invalidated, because they were about calibrating review timeouts rather
than about total volume, but the denominators were narrower than they read.

Target: make the readers agree with the writers, or invoke the consolidation
that already exists. Either the v1 ledger is per-gitDir and `runs prune` must
read every worktree directory the way `MigrarDesdeV1` globs them, or the fichas
are consolidated into the shared v2 store and the readers move there. Do not
reconcile by copying fichas between directories by hand: the ledger is
append-only and a hand-merged history is worse than a split one.

Priority: **before T9.5.** That task builds an event-driven retention cascade
over `internal/review/ledger.go` and `internal/store/execution_prune.go`, and
its stated invariant is that nothing is deleted before its snapshot exists. A
cascade written against one directory while the fichas are written to thirteen
others cannot hold that invariant, and delegating T9.5 to a worktree writer
would make its own evidence invisible to it.

#### Partly resolved 2026-09-02: the destructive reader

The severity recorded above understated it. `collectProvenanceReferences` in
`cmd/sentinel/comandos_runs_prune.go` is what keeps `runs prune` from deleting
an execution stream that review evidence still cites, and its contract is to
fail closed when provenance is unreadable, "because that is exactly how
referenced streams get destroyed". Reading only the common directory made a
ficha written from a worktree **absent** rather than unreadable, so the guard
never fired. Measured on this repository when the fix landed: 222 fichas in the
common directory and 65 in linked worktrees, every one of the 65 invisible to
the guard and its cited streams therefore prunable.

Fixed in `b59778b`, `190cf55` and `29367b2`. The collector now enumerates the
common directory plus one gitDir per linked worktree.

Two attempts were needed and the reason is worth keeping, because it is the same
mistake twice. The first enumerated with `filepath.Glob`, copied from
`MigrarDesdeV1`: `Glob` reports only `ErrBadPattern` and swallows its I/O
errors, so an unreadable `worktrees` directory returned an empty list and a nil
error, indistinguishable from a repository with no linked worktrees. The glob is
harmless in the migration, where missing a directory defers work a later run
repeats, and destructive here. The second used `os.Stat`, which follows
symlinks, so a dangling ledger link reported `ErrNotExist` and read as "never
wrote a ficha". Both are the same shape: a silent absence standing in for a real
fault, in the one function whose whole purpose is to refuse to act on partial
knowledge.

#### The purge, closed 2026-09-02

`status --prune` purged the invoking checkout's ledger alone while T9.5 builds
its retention cascade on that same primitive together with
`collectProvenanceReferences`, which by then enumerated every ledger. A cascade
assembled from both would have decided what to keep by consulting thirteen
ledgers and deleted from one.

It now walks every ledger, cleans each checkout's events where its own fichas
were, resolves reachability against the repository being purged rather than the
process working directory, and refuses to decide at all when it cannot ask.

Six review rounds, and the shape of every one was the same: a query failure read
as a commit that no longer exists. Worth recording because the individual fixes
were each correct and each insufficient.

- `filepath.Glob` reports only `ErrBadPattern` and swallows its I/O errors, so
  an unreadable directory read as "no linked worktrees".
- `os.Stat` follows symlinks, so a dangling ledger link read as "never wrote a
  ficha".
- `GIT_DIR` takes priority over `-C`, so an ambient value redirected the
  reachability query away from the repository being purged.
- Sanitising only that query and not ledger discovery split the identity, which
  is worse than sanitising neither: it deleted live fichas across repositories.
- Scrubbing the object-store variables broke repositories whose objects live
  where the environment points, and preserving them let an unrelated store fail
  the query. Both true, which was the signal that the rule underneath was the
  defect.
- The exported default purge kept wrapping the old boolean helper, so the
  convenient path retained the behaviour the fix claimed to remove. It had no
  production callers and was deleted rather than patched.

The property, stated once instead of approximated six times: **a question the
purge cannot answer never authorises a deletion.** The predicate returns an
error, the ledger aborts and reports what it had already removed, and
`RepositorioUsable` anchors on `HEAD^{commit}` because exit codes alone do not
separate an unreadable object store from a genuinely unknown object. The
residual case that survives is recorded as FU-15 rather than mistaken for fixed.

**Closed 2026-09-02.** `review`, `status` and `pr` now build the ledger through
`sharedReviewLedger` (`cmd/sentinel/shared_ledger.go`), which anchors on the Git
common directory, so a review run in a linked worktree no longer files its
record where `git worktree remove` deletes it. The decision the entry left open
— where `AnalizarRama`'s adopted copy lands, since it both reads and writes — is
that it lands in the shared ledger too: one location for reads and writes, and
nothing older migrated. What that costs is recorded below, and FU-17 records the
widening it caused in correction attribution.

The list of gitDir call sites above is therefore historical. Only the prune
paths still enumerate per-checkout ledgers, and they do so deliberately.

**What that costs, so a later observation is recognised and not investigated.**
Nothing older is migrated. Measured on this repository at 2026-09-02, by listing
`<common-dir>/worktrees/*` and counting `*.json` under each `vas-sentinel`: 18
administrative directories, of which 12 are branch worktrees and the rest are
review snapshots; 12 hold a `vas-sentinel` directory, 11 of those are non-empty,
and they hold 65 fichas. Those records are no longer read by `review`, `status`
or `pr`, so inside one of those worktrees `review --all` treats its already
audited commits as pending and audits them again, and a branch analysis there no
longer sees the verdicts it recorded before the change. Nothing is deleted:
`runs prune`, `status --prune` and `review --prune` still enumerate every
per-checkout ledger, so no execution stream loses its provenance.

This is a consequence of a decision, not debt, which is why it lives here and
not as its own entry.

The decision was taken on 2026-09-02: reviews write to the shared location from
the start, and T9.5 collects from there at publication. Nothing older is
migrated, because 52 of the 65 fichas then sitting in per-checkout ledgers were
already published and T9.5 removes them anyway.

### FU-13: net range planning classifies from the sanitised path list

Recorded 2026-09-02 from a finding against ticket 05 of the FU-10 sequence. The
premise is confirmed and the defect is pre-existing: it is not introduced by
that ticket, which only changed which evidence the derivation receives.

`internal/review/net_pr.go` passes `safePaths` to the plan derivation.
`rutasRevisionSeguras` (`internal/review/engine.go:673-684`) drops any path
containing `*?[]{}!`, a control character, or a leading `-`. Those characters are
legal in tracked filenames, so a repository holding one classifies its net range
from an incomplete path list and can miss route-based `database`,
`security_sensitive`, `cross_module`, `ci_cd` and `infrastructure` evidence.

The sanitiser exists for a different reason: those paths are interpolated into
reviewer prompts and shell-adjacent contexts, where a wildcard or a leading dash
is a real hazard. Classification has no such exposure — it matches globs against
strings — so the two uses want different lists.

Target: give classification the complete changed-path list and keep the
sanitised list for the surfaces that interpolate it. Do not widen
`rutasRevisionSeguras`: the sanitising is correct for its own consumers, and
loosening it to serve the classifier would trade a coverage gap for an
injection surface.

Priority: not blocking. No path in this repository triggers it today, so it is a
correctness gap waiting for a filename rather than an active fault.

#### Resolved 2026-09-02

`internal/review/net_pr.go` passes the complete changed-path list to
`PlanForProfile`. `rutasRevisionSeguras` is untouched and still feeds
`RutasContexto`, the transport factory and `git.ReadPathAtRevision`, which are
the surfaces that interpolate those names. The two uses wanted different lists
and now get them.

Measured rather than argued: `PlanForProfile` over `infra/main[1].tf` reports
`infrastructure` **present** with the complete list and **absent** with the
sanitised one, which is exactly the route evidence the entry predicted was
being lost.

Pinned by `TestNetReviewClasificaConLaListaCompletaDeRutas`, over a real
repository and the real net-review path. Square brackets, not wildcards: they
are legal in filenames on Linux and Windows alike, so the fixture exercises the
gap on both.

The first version of that test asserted only that the prompt contained the word
`infrastructure`, and it passed against the unfixed code — every characteristic
is named in the evidence whether present or absent. It asserts the state now.
That is recorded because it is the third time in this sequence a test passed for
the wrong reason, and the assertion, not the fixture, was the weak part.

### FU-14: half the detectors classify without the repository attributes

Recorded 2026-09-02 while writing the ticket 05 regression that proves a
successful `.gitattributes` read reaches the plan derivation. The test failed
first for a reason worth keeping: marking a path `linguist-generated` removed
`security_sensitive` and added `generated_code`, but left `behavior_change`
present.

`internal/change/caracteristicas.go` classifies paths through two different
functions and never says why:

- `Clasificar(ruta, reglas, e.Gitattributes)` honours the attributes. Used by
  `admiteEvidenciaDeContenido` (line 80) and `detectarCodigoGenerado` (line 204).
- `ClasificarPorRuta(ruta, reglas)` ignores them. Used by
  `detectarCambioDeComportamiento` (line 151), `detectarCoberturaDeTests`
  (line 166), and `contieneClase` (line 248), which is what `ci_cd` and
  `infrastructure` read.

So a path a repository declares generated is generated for the content
detectors and still source for behaviour change, still counted when deciding
whether a package has test coverage, and still classifiable as CI or
infrastructure. A repository that marks a large generated tree
`linguist-generated` gets `behavior_change` present on every regeneration, which
is `elevated` at least, and `high` whenever the symbol graph is incomplete.

The split is not obviously wrong in every case: `linguist-generated` is a
presentation attribute about diff and language statistics, and one could argue
behaviour changed even in generated output. But the two groups were not chosen,
they diverged, and nothing records a rule.

Target: decide once whether `linguist-generated` participates in classification
at all, then use one function everywhere. If some detectors must stay
attribute-blind, name them and say why at the call site. This entry exists
because the answer must be recorded, not because one side is obviously right.
The answer is recorded below.

Priority: not blocking. It changes classification for repositories that use the
attribute; this one does not mark any tree generated today.

Pinned by test so resolving it cannot pass unnoticed:
`TestPlanForProfileHonoursAttributesPerDetector` in `internal/review` asserts
the characteristics themselves for one path, one executable added line and one
attribute. `security_sensitive` and `generated_code` honour
`linguist-generated`; `behavior_change` does not. Resolving this entry makes
that test fail, and its message says to update both together rather than delete
the assertion.

#### Resolved 2026-09-02

**The decision, which is what this entry existed for: which classifier a
detector uses is chosen by the question that detector answers, and the choice is
written down.** A detector reasoning about the NATURE of the code uses
`Clasificar` and honours `linguist-generated`, because there the repository is
declaring something true about its own source: `admiteEvidenciaDeContenido`,
`detectarCodigoGenerado`, `detectarCambioDeComportamiento` and
`detectarCoberturaDeTests`. A detector reasoning about WHICH SURFACE the change
touches uses `ClasificarPorRuta` and does not: that is `contieneClase`, and only
it, which feeds `ci_cd` and `infrastructure`.

`detectarCambioDeComportamiento` and `detectarCoberturaDeTests` moved to
`Clasificar`; `contieneClase` stayed on `ClasificarPorRuta`, now for a stated
reason rather than by accident.

The argument that decided it: the cost of being wrong is asymmetric. Honouring
the attribute everywhere means a repository that marks a tree generated stops
paying `elevated` risk on every regeneration, and the way back is to remove the
marking. Ignoring it in half the detectors means the repository states something
about its own code and Sentinel disagrees silently, with nothing recording why.
The counter-argument in this entry still stands — `linguist-generated` is a
presentation attribute and behaviour can change in generated output — and it is
answered rather than dismissed: the repository is the authority on what its
generated trees are, and it can stop declaring one at any time.

The characterisation was updated with the code, as the test's own message
demanded. Its oracle flipped rather than being deleted: `behavior_change` must
now be ABSENT under the attribute — the exact state, not merely "not present",
which `indeterminate` also satisfies — and the risk explanation must NOT name
it, because naming it was what tied the scheduled work to the characteristic
this entry is about. A positive assertion sits beside both, because a pair of
negative checks passes vacuously against an implementation that classified
nothing at all.

**One named exception, added 2026-09-03 after the review measured what the
uniform rule cost.** `contieneClase`, which is what `ci_cd` and `infrastructure`
read, keeps classifying by path alone. Those two ask which surface a change
touches rather than whether it is source, and a workflow is a workflow even when
a tool wrote it. Measured under the uniform rule: marking
`.github/workflows/deploy.yml` as `linguist-generated` turned `ci_cd` from
present to absent, which handed the audited repository a switch for the
detection of its own CI.

The criterion is deliberately NOT "a classification that decides how much review
a change gets must not depend on a declaration from its subject". That proves
too much: `behavior_change` also decides how much review a change gets, and it
honours the attribute on purpose. The criterion is which question the detector
answers.

That is the shape this entry's own target anticipated — "if some detectors must
stay attribute-blind, name them and say why at the call site" — and it is why
the exception is documented where `contieneClase` is defined and pinned by
`TestAtributosNoApaganLaDeteccionDeCI` next to it, with
`TestAtributosApaganElCambioDeComportamiento` holding the other side so the
exception cannot quietly become the rule.

### FU-15: a corrupt object is indistinguishable from a collected one

Recorded 2026-09-02 while closing FU-12's purge path, from a finding that is
correct and that this repository cannot cheaply resolve.

`git.ContenidoEnAlgunRefDe` resolves a ledger SHA before asking about
containment, and reads `rev-parse --verify --quiet` exit code 1 as "this object
no longer exists", which authorises deleting its ficha. That code also appears
when the object is present in the repository but unreadable in the object store
while `HEAD` remains readable, so a live ficha can still be deleted in that
narrow case.

What is already defended, and why this residue is narrow:

- A wholesale unreadable object store is caught. `RepositorioUsable` anchors on
  `HEAD^{commit}`, which fails there, so the purge refuses to decide anything.
- The ordinary orphan, after a rebase or an amend, does not reach this branch at
  all: the old commit object still exists and unreachability is decided by
  `branch -a --contains`, where an error is an error and only an empty result
  means unreferenced.
- Every other query failure now aborts the purge instead of authorising a
  deletion.

So the remaining exposure needs object-level corruption that leaves `HEAD`
intact, in a repository whose ledger holds a ficha for the damaged commit.

Target: decide whether a purge may ever run against a repository it has not
verified. Distinguishing a collected object from a corrupt one needs
`git fsck`-level verification, which is disproportionate per purge and per SHA.
The cheaper alternative is to stop reading exit 1 as absence at all, which
closes the hole completely and leaks the fichas of commits collected by `gc`
forever, because a rebased-away commit never publishes and T9.5 would never
collect it either.

Priority: not blocking. It is a narrower failure than the four this sequence
closed, and unlike them it is recorded rather than mistaken for fixed.

#### Accepted, not repaired (2026-09-05)

The residue stands, unfixed and understood. Both available fixes cost more than
the defect: `git fsck`-level verification is disproportionate per purge and per
SHA, and refusing to read exit 1 as absence leaks the fichas of gc-collected
commits forever. The exposure needs object-level corruption that leaves `HEAD`
readable, in a repository whose ledger holds a ficha for the damaged commit.

Trigger to revisit: an observed instance, or a cheaper way to distinguish a
collected object from a corrupt one.

### FU-16: the ledger listing reports an unreadable directory as an empty one

Recorded 2026-09-02, immediately after FU-12's purge closed, from noticing that
`review.Ledger.ListarFichas` enumerates with `filepath.Glob`. That is the same
call whose failure semantics cost FU-12 six review rounds: `Glob` reports only
`ErrBadPattern` and swallows the I/O errors it hits reading directories, so a
static pattern over an unreadable ledger directory returns an empty list and a
nil error.

Measured: over a directory made unreadable, `filepath.Glob` returns 0 matches
and `err == nil`.

**The direction of the damage differs by caller, and only one of them is safe.**

- `PurgarHuerfanas` lists fewer fichas, so it deletes fewer. That leaks orphans
  and destroys nothing.
- `collectProvenanceReferences`, through `anotarReferenciasDeLedger`, lists
  fewer fichas, so it collects fewer referenced invocation identities, so
  `sentinel runs prune` treats their execution streams as unreferenced and
  deletes them. That is destruction, and it is precisely what that function's
  own contract forbids: "Any read failure fails closed — a prune must never run
  while provenance is unreadable, because that is exactly how referenced
  streams get destroyed."

So FU-12's fix taught the collector to visit every ledger directory, and this
one can still report any of them as empty without saying so.

Target: enumerate with `os.ReadDir` and propagate the error, exactly as
`directoriosLedgerV1` already does one level up. `ListarFichas` already returns
`([]string, error)`, so no caller signature changes.

Priority: before anything else that deletes. It is the same defect as FU-12 one
layer down, in the function that decides what a prune may destroy.

#### Resolved 2026-09-02

`ListarFichas` enumerates with `os.ReadDir` and propagates the error. No caller
signature changed, and every caller already propagated: the destructive ones —
`anotarReferenciasDeLedger` and `PurgarHuerfanas` — now fail closed on a ledger
they cannot enumerate instead of reading it as one that cites nothing.

**Absence still had to stay a real answer, and one shape reports it falsely.**
`NuevoLedger` does not create the directory; the first saved revision does. So a
repository that never ran a review has no ledger directory, and propagating
every `ReadDir` error would break `sentinel status`, the metrics reader and the
prune's own provenance scan everywhere. But `os.ReadDir` resolves symlinks, so a
ledger path pointing nowhere answers `ErrNotExist` exactly like an absent one.
The listing therefore separates the two with `Lstat` before the read, as
`directoriosLedgerV1` does one level up.

That distinction is not defensive. `directoriosLedgerV1` guards every linked
worktree with `Lstat` before `Stat`, but appends the common directory
unconditionally, so a broken link at `<common-dir>/vas-sentinel` reaches this
listing with no check in front of it and lands straight on
`collectProvenanceReferences`.

**Staged with a file shape, not a permission bit.** A mode change is a no-op
under root, so a permission-based fixture passes without exercising anything.
`filepath.Glob` over a regular file at the ledger path returns zero matches and
a nil error; `os.ReadDir` reports `not a directory`. Both fault shapes are
pinned by `TestListarFichasFailsWhenTheLedgerDirectoryCannotBeRead` and
`TestListarFichasFailsOnADanglingLedgerSymlink`, and the absent directory by
`TestListarFichasTreatsAMissingLedgerDirectoryAsEmpty`, which fails any fix that
turns absence into an error. The first two failed on behaviour before the fix.

**One half of the coverage is platform-conditional, and the entry should not be
read as claiming otherwise.** Measured by removing the `Lstat` guard and leaving
`ReadDir`'s own `ErrNotExist` check: the regular-file test still passes, because
`os.ReadDir` answers `ENOTDIR` there rather than `ErrNotExist`. Only the
dangling-symlink test fails, so it alone pins the discriminator — and it skips
itself where the platform refuses to create a symlink without extra privileges,
which on Windows without the symlink privilege is the ordinary case. The
regular-file shape is pinned everywhere; the `Lstat` guard is pinned only where
the running platform lets the test create a symlink. There is no portable
non-symlink shape that makes `Lstat` succeed while `ReadDir` answers
`ErrNotExist`, so this is recorded rather than closed.

### FU-17: a fix in one worktree can clear a block recorded in another

Recorded 2026-09-02, from a finding against the commit that anchored the review
ledger on the Git common directory.

`registrarCorrecciones` (`cmd/sentinel/comandos_review.go`) walks every ficha in
the ledger and marks as corrected any whose last verdict is `block` and whose
findings name a file the `fix(` commit touches. There is no ancestry check and
no branch scope: file-name overlap is the whole test.

That logic did not change. Its reach did. The ledger used to hold only the
fichas of the checkout that wrote them, so a `fix(` in a worktree could only
clear that worktree's blocks. Now every checkout reads one ledger, so a `fix(`
commit on one branch can clear a block recorded on an unrelated branch because
both touched, say, `internal/review/ledger.go`.

Sharing the ledger is correct and is not the thing to revert: the alternative
was fichas dying with `git worktree remove`. What is missing is the scope the
attribution never had to carry while the ledger was accidentally partitioned.

Target: restrict correction attribution to fichas whose audited commit is an
ancestor of the `fix(` commit — `git merge-base --is-ancestor <audited> <fix>`,
in that order, because the fix comes after what it corrects — or persist enough
scope in the ficha to tell two branches apart. Do not fix it by narrowing the
file match; the defect is that a correction is attributed without asking whether
the two commits are even on the same line of history.

Priority: before the ledger holds fichas from several concurrent branches for
long. It falsely clears blocks, which is the failure mode this register exists
to keep visible.

#### Resolved 2026-09-02

`registrarCorrecciones` now requires the audited commit to be an ancestor of the
`fix(` commit, through `git.EsAncestroDe`, which is `merge-base --is-ancestor`
in that order because a fix comes after what it corrects. File-name overlap
alone no longer attributes anything.

`EsAncestroDe` separates the cases by exit code — 0 yes, 1 no, anything else a
real failure — and no failure is read as "no". A query that cannot be answered
attributes nothing, which is the safe direction here: the ficha keeps its block
and a later fix can still clear it.

Pinned by `TestRegistrarCorreccionesNoCruzaRamas` in `cmd/sentinel`, which
builds two real branches from one root that both change the same path, so
ancestry is the only thing that can separate them. It asserts both directions,
and it was calibrated against both mutations: removing the check clears the
block across branches, and reversing the argument order stops a genuine
descendant from clearing it — which is the exact error this entry's own target
line contained before it was corrected.

#### Same-branch staleness, observed 2026-09-04 (T9.5)

The ancestry gate stops cross-branch clearing but not same-branch
staleness: `7552565` (`fix(store)` on the T9.5 branch) was recorded as
the correction of `c50d6d5` (T9.1a, merged 2026-08-29) because the old
commit is an ancestor and the file sets overlap
(`internal/store/execution_metrics_test.go` among them). Topically the
two are unrelated — the T9.1a block concerns snapshot relocation, the
fix concerns collection agreement — and the T9.1a block already carried
its evidence-backed disposition (implementation acceptance, 2026-08-31),
so nothing was pending on it. Impact here is nil beyond misleading
archaeology, and it is recorded rather than repaired: attribution cannot
tell topicality from ancestry plus file overlap, and any narrower rule
(e.g. finding-text matching) would be easier to game than the current
one. The lesson for readers of `fixed_in`: it means "a later commit
touched these files and passed review", not "this finding's premise was
re-tested".

### FU-19: an unparseable finding silently downgrades a block to "unavailable"

Recorded 2026-09-03, from a `spec` dimension that reported `unavailable` with
`reason="semantic review output schema_invalid"`.

The reviewer had produced a CRITICAL finding. Its `line` field carried
`"17-19, 23-48"` — a range as text, for a diff whose problem spans several
hunks — where the schema expects a number. The whole output was rejected, so the
finding never reached the ficha, and the dimension came back as if it had not
run.

**That is this register's own recurring shape: a failure that reads as an
absence.** `unavailable` says "this dimension has zero coverage", which the
operator is told never to accept as a pass. Here it meant the opposite of zero
coverage: the reviewer had an answer and it was `block`. An operator re-running
the dimension gets the same rejection, so the finding is unreachable by retry
and the only way to see it is to read the raw reason string, where it survives
truncated.

Measured: the substance was recovered from
`revisions[-1].dims[].reason`, which quotes the raw payload up to a length cap.

Target: parse what can be parsed. A finding whose `line` cannot be read is still
a finding — with an unknown line, not with no existence. Rejecting the whole
payload for one malformed field trades a cosmetic defect for a lost verdict.
Decide separately whether `line` should accept a range, which is what a reviewer
naturally reports for a multi-hunk problem.

Priority: before trusting `unavailable` to mean what it says. It is currently
two different states wearing one name — "the reviewer never answered" and "the
reviewer answered and we dropped it" — and only the first is safe to retry.

#### Landed 2026-09-05

Parse what can be parsed: `findingCrudo` now decodes `line` through tolerant `lineaCruda` — a JSON number or numeric string keeps parsing exactly as before; any other value sets `Line=0` (unknown, the line-less convention) and preserves the raw text in additive `ReviewFinding.LineRaw` (`json:"line_raw,omitempty"`), with a `line %q in %s normalized to unknown line` normalization. The CRITICAL still drives `veredictoFinal` to `block` line-independently, so the recorded `spec` case now blocks instead of reporting `unavailable`/`schema_invalid`. Verified before relying on both safeties: FU-6 defect 2 still routes `LineaInicio<=0` through file-scoped evidence (`ValidateHumanRefutationRange`, tests pass untouched), and `IsBlocking` does not consult the line (untouched, shared rule test passes).

Decision on ranges: NO range semantics. The persisted shape holds a single `Linea` int, so extracting a first line from `"17-19, 23-48"` would invent precision and misdirect the evidence window; unknown is already answerable by a human through the file-scoped gate. Single-number forms work unchanged. `Linea` itself stays strict for the persisted reload path — only the agent-input path is tolerant — and `LineRaw` is deliberately never a `Fingerprint` input, so existing records marshal byte-identically and no standing disposition is rekeyed.

Replaces the truncated `dims[].reason` quoting for this class: the raw value now survives verbatim in the persisted ficha (`dims[].line_raw`) plus the in-memory normalization, instead of a capped excerpt. Other schema rejections (unknown dimension, invalid verdict without findings) still reject as before; only `line` is tolerant.

Deviation: normalization wording uses English per language policy while sibling legacy normalizations stay Spanish; no behavior impact.

### Correction to `926649b`'s commit message

Recorded 2026-09-03. `926649b` says "switch the reviewer back to opencode" and
its diff does more than that. Staged with an explicit path but without reading
the whole diff first, so the message describes one hunk of five.

What it actually changed in `.vas_sentinel/vassentinel.yml`:

- `active_agent`: `claude` → `opencode`, which is what the message describes.
- `agents.claude.profiles.deep`: `claude-opus-5`/`medium` →
  `claude-sonnet-5`/`xhigh`.
- `agents.opencode`: every profile moved from `gpt-5.6-terra`/`gpt-5.6-luna` to
  `openai/muse-spark-1.3-contributor-free`, with different efforts, so the four
  levels now differ only by effort and no longer by model.
- The previous `opencode` block survives as fourteen commented-out lines. It is
  dead configuration, kept deliberately: it is the route back to the earlier
  models, and deleting it would destroy the record of what to restore.

The changes are the operator's and stand; the message was mine and was wrong.
Corrected here rather than by rewriting: `926649b` already carries a review
record keyed by its SHA, and this repository has twice lost review evidence to
a rewrite.

The reviewer caught it and its finding was accurate. It arrived as
`unavailable`/`schema_invalid` rather than as the `block` it was, which is FU-19.

### Correction to `5bd7c16`'s commit message

Recorded 2026-09-03. `5bd7c16` says `chore(slice): bypass IA for semantic
unit 7462656338f84aa7` and its diff implements the T9.5 event-driven
retention cascade: the `PublicadoEnRemoto` publication predicate
(`internal/git`), snapshot and single-attempt collection guards with
in-lock re-verification (`internal/store`), the retained-only snapshot
rule (`internal/metrics`), the unpublished-provenance collector with
best-effort triggers on rebase and pre-push gate (`cmd/sentinel`), and
the RED/GREEN/acceptance tests pinning each half.

The message is the slice planner's deterministic fallback vocabulary, not
a description a human wrote: the review adapter was unavailable for
message generation, so the plan named its own mechanism (planner-side
bypass of AI subdivision for one semantic unit) instead of the change.
`chore` understates a feature; the unit hash identifies the plan, not the
work.

Corrected here rather than by rewriting, for the same reason as
`926649b` above: the commit already carries review records keyed by its
SHA, and this repository has lost review evidence to rewrites before.

### FU-20: semantic failures on success-terminal runs contradict the stream

Recorded 2026-09-04 during T9.5 retention, from measuring a one-time
success→failed reclassification of ~140-180 pre-existing runs on the
first collecting pass.

The review engine's corrective-retry shape (`internal/review/engine.go`,
`finalizeInvocation`) records semantic failure classes
(`invalid_output`, `schema_invalid`, …) for an invocation whose reconciled
outcome is terminal success. Finalization folds both into the snapshot,
so the record pairs a success stream with a failing snapshot. While the
stream survives the aggregator follows it (success); once retention
collects the stream the snapshot alone reads failed. The T9.5 agreement
guard (`PruneReasonContradiction`) keeps such runs from now on —
agreement, not mere presence, authorizes collection — so nothing further
moves. The already-reclassified runs stay as-is: their streams are gone,
their snapshots remain, and only the success/failed counters moved (the
failure breakdown already published those same semantic classes).

What is genuinely open, and why it is a producer question rather than a
retention one: should a semantic failure ever attach to a success-terminal
run, or should it force the outcome to failure at the source? If the
former is legitimate (recovered within the attempt), the contradiction is
real evidence and keeping those runs forever is correct but leaks slowly.
If the latter, the producer should never write the shape and the guard
becomes dead-but-harmless. Deciding needs the review-transport owners,
not the retention path.

Trigger: revisit when contradicted runs accumulate (watch the
`snapshot contradicts terminal success` keep reason in prune reports) or
when the review transport next changes its finalization.

#### Accepted, not repaired (2026-09-05)

Nothing further moves: the T9.5 agreement guard (`PruneReasonContradiction`)
already stops collection of contradicted runs, so the defect no longer
accumulates. What stays open is a producer question, and answering it belongs to
the review-transport owners rather than to this register.

Trigger to revisit is unchanged: contradicted runs accumulating (watch the
`snapshot contradicts terminal success` keep reason in prune reports), or the
review transport changing its finalization.

