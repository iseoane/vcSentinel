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

## T7.2 — Agente de remediación con permisos acotados ✅ (hash de cierre `1999213`)

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 280 líneas |
| Depende de | T7.1 |
| Commits | `79b81aa` (implementación: `internal/remediation/scope.go` — interfaz `Editor` con solo `Read`+`Edit`, sin método de shell/red posible por construcción del tipo; `Scope`/`NewScope` a partir de `[]review.Hallazgo`; `ScopedEditor` que envuelve un `Editor` y aplica la política antes de delegar), `ceb4a99` (fix tras revisión: normaliza rutas y rechaza traversal en la excepción de archivo de test nuevo), `bc49d2f` (fix: la política completa —incluida la seguridad de ruta— vive en `Scope.Allows`; se delega la ruta ya normalizada al `Editor` subyacente), `339b989` (fix: la ruta insegura se rechaza **antes** de cualquier E/S, ni siquiera llega al `Read` de comprobación de existencia), `f12ce4e` (fix: rechaza también la ruta vacía/`"."` que produce `normalizePath("")`), `a8acf94` y `1999213` (tests: verifican que los dos mensajes de rechazo son distinguibles y que el `Read` subyacente nunca se invoca en un rechazo temprano) |

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

**Aceptación**: cumplida. `TestScopedEditorEdit` (`scope_test.go`) usa un doble
(`fakeEditor`) que registra cada llamada real a `Read`/`Edit`: un archivo con
hallazgo o un archivo de test nuevo se escribe; un archivo sin hallazgo, una
ruta absoluta, un `..`/traversal o una ruta vacía se rechazan **sin que el
doble reciba ninguna llamada** (ni `Edit` ni, desde `339b989`, tampoco `Read`) —
no solo se verifica el error devuelto.

Puntos 1 y 2 de "Hacer" quedan implementados en `internal/remediation/scope.go`
como propiedad estructural del tipo (`Editor` no tiene método de shell/red
posible; `ScopedEditor.Edit` no delega la escritura si `Scope.Allows` la
rechaza). El punto 3 (prompt del agente real) **no** se implementa aquí:
esta tarea no conecta ningún binario `claude`/`opencode` real — eso queda para
cuando exista un `Editor` concreto respaldado por CLI (T7.3 ya necesita un
diff real que solo existe una vez que algo escribe de verdad en el árbol de
trabajo, así que es el candidato natural, o una tarea de wiring dedicada).

Hallazgos de `sentinel review` aceptados sin fix adicional (código sin
consumidor de producción todavía, revisión ya en `warn` sin bloqueantes):

- `isPathSafe` es puramente textual: no resuelve symlinks ni reconoce
  separadores de Windows en un host POSIX. **Follow-up**: cuando exista un
  `Editor` concreto respaldado por sistema de archivos, debe resolver la ruta
  real (`filepath.EvalSymlinks` o equivalente) y verificar que sigue dentro
  de la raíz del repositorio antes de escribir.
- La excepción de "archivo de test nuevo" no es idempotente: una segunda
  llamada `Edit` sobre el mismo archivo recién creado en la misma ronda se
  rechaza (ya no es "nuevo"). **Follow-up para T7.4**: si el flujo de
  re-validación de una sola ronda necesita que el agente retoque un archivo
  de test que él mismo acaba de crear, `Scope` necesitará rastrear también
  los archivos creados por la propia ronda, no solo los que tienen hallazgo.
- `NewScope` depende del tipo completo `review.Hallazgo` cuando solo usa
  `Location.Archivo` (acoplamiento a `internal/review` por un único string).
  Aceptado: es el punto de integración natural con `remediation.Route` (T7.1),
  que ya trabaja sobre `review.Hallazgo`.
- El TOCTOU entre el `Read` de existencia y el `Edit` real, y la duplicación
  intencional de `isPathSafe` en dos sitios (`Edit` y `Allows`, defensa en
  profundidad) quedan documentados en el propio código como decisiones, no
  como descuidos.

`go build ./...`, `go vet ./...` y `go test ./...` en verde para todo el
módulo. `sentinel gate --stage pre-push` sobre `1999213` → `PASS`.

---

## T7.3 — Diff Guard ✅ (hash de cierre `cebfdba`)

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 260 líneas |
| Depende de | T7.2 |
| Commits | `545b3c9` (`internal/git/hunks.go`: `LineRange` y `TouchedRanges(before, after string) ([]LineRange, error)`, diff de dos contenidos en memoria vía `ejecutarGitDiffNoIndex --unified=0` sobre archivos temporales, parseando cabeceras `@@ -a,b +c,d @@` a rangos en la numeración del contenido "después"), `d6a9d8d` (`internal/remediation/diffguard.go`: `DiffGuard`/`NewDiffGuard`, `DiffGuard.Check` compara antes/después contra las ventanas ±margin de los hallazgos localizados en ese archivo; `GuardedEditor` decora un `Editor` aplicando el guard), `2d08d13` (fix: `GuardedEditor.Edit` pasa de aplicar-y-revertir a comprobar-y-aplicar — el fix nunca toca el `Editor` subyacente si el guard lo rechaza, eliminando el caso en que un archivo nuevo fuera de alcance quedaba creado sin revertir; y fusión de ventanas contiguas/solapadas de distintos hallazgos del mismo archivo para no rechazar un hunk legítimo que las cruza), `a6cb500` (fix: un hallazgo sin ubicación ya no anula la ventana de otro hallazgo sí localizado en el mismo archivo — solo autoriza el archivo completo cuando *ningún* hallazgo del archivo tiene ubicación), `fee026f`+`18223b7`+`1699f3f`+`d4af2a9` (fix: saturación de `start`/`end` en `allowedWindows` antes de sumar/restar el margen, y guarda de `End == math.MaxInt` en `mergeWindows`, para que un `LineaFin`/`LineaInicio` extremo en el hallazgo —dato no validado por este paquete— no desborde a un rango inválido; `margin` se normaliza a no-negativo dentro de `allowedWindows`), `cebfdba` (test: corrige el comentario y una aserción no discriminante en el test de margen negativo) |

**Contexto**: `internal/git/*`, informe §16

**Hacer**: tras la edición, comprobar que el diff del fix toca **solo** las zonas
de los hallazgos ±N líneas (N configurable). Si no: **se descarta el fix entero**
y se reporta `remediation out of scope`.

Es la pieza que hace la remediación aceptable. Sin ella, el resto de la fase es
un riesgo neto.

**Aceptación**: cumplida. `TestDiffGuardOutOfScopeDiscardsWholeFix` (`diffguard_test.go`)
usa un fix que corrige la línea del hallazgo y además renombra algo en una línea
claramente fuera de su ventana: se rechaza con `"remediation out of scope"` y
`editor.editCalls` queda en 0 — el `Editor` subyacente nunca llega a aplicar nada,
ni parcial ni con revert posterior.

El descarte íntegro es **por archivo**, no todavía por fix multiarchivo:
`GuardedEditor.Edit` es una función pura por llamada, sin estado entre invocaciones
que registre qué rutas ya se aplicaron. Un fix que edita los archivos A y B, donde
A pasa el guard y B se rechaza, deja A aplicado — la ficha de fase habla de
"se descarta el fix entero" a nivel de la unidad de cambio completa, y esa
atomicidad cruzando archivos no existe todavía en `GuardedEditor` tal cual está.
Es aceptable ahora porque no hay ningún `Editor` real (CLI-backed) que produzca
fixes multiarchivo — todo lo escrito hasta T7.2/T7.3 pasa por `fakeEditor` en
tests, de un archivo por caso. **Follow-up para T7.4**: cuando exista wiring real
con un agente que pueda tocar varios archivos en una misma ronda, `GuardedEditor`
(o quien orqueste la ronda) necesita rastrear y revertir todas las rutas ya
aplicadas si cualquier archivo posterior de la misma unidad de cambio sale de
alcance.

`TestDiffGuardMergedAdjacentWindowsCoverGapBetweenFindings`
y `TestDiffGuardLocatedFindingIsNotSwallowedByUnlocatedFinding` cubren los dos
defectos reales que aparecieron al iterar sobre el diseño inicial (fusión de
ventanas contiguas y protección de un hallazgo localizado frente a uno sin
ubicación en el mismo archivo). `TestAllowedWindowsClampsExtremeLineaFinWithoutWrapping`,
`TestAllowedWindowsNegativeMarginDoesNotOverflowEndClamp` y
`TestAllowedWindowsMergesSaturatedMaxIntWindowWithAdjacentFiniteWindow` cubren la
saturación de enteros. `go build ./...`, `go vet ./...` y `go test ./internal/git/...
./internal/remediation/...` en verde. `sentinel gate --stage pre-push` sobre
`cebfdba` → `PASS`.

Hallazgos de `sentinel review` aceptados sin fix adicional:

- **Falsos positivos por alcance de contexto de revisión aislada** (`545b3c9`,
  dimensión `logic`, 2× `CRITICAL`): el revisor solo ve los archivos tocados por
  ese commit concreto (`hunks.go`, `hunks_test.go`) y concluyó que
  `ejecutarGitDiffNoIndex` no existe y que el paquete no compila, porque esa
  función vive en `internal/git/slice.go` (mismo paquete, no tocado por este
  commit). Verificado directamente contra el árbol real: `func
  ejecutarGitDiffNoIndex` existe en `slice.go:370` y tolera el exit code 1 de
  `git diff --no-index` (`slice.go:377-379`). `go build ./...` y `go test
  ./internal/git/...` en verde lo confirman de forma independiente. No es un
  defecto del código, es una limitación del alcance de archivos que ve la
  revisión de un commit aislado cuando depende de un símbolo del mismo paquete
  no tocado por ese commit — no accionable dentro de T7.3.
- **Diagnosticabilidad del rechazo por hallazgo sin ubicación ignorado**
  (`a6cb500`, dimensión `logic`, `ADVISORY`): cuando un hallazgo sin ubicación
  coexiste con uno localizado en el mismo archivo, un fix legítimo para el
  hallazgo sin ubicación (que puede necesitar tocar cualquier línea) se
  rechaza como `"remediation out of scope"` sin ninguna señal que lo distinga
  de un fix realmente fuera de alcance. **Follow-up para T7.4**: al reportar
  hallazgos no resueltos tras la re-validación, distinguir este caso
  (hallazgo sin ubicación ignorado por la presencia de otro localizado) del
  rechazo real por alcance.
- **Diseño — acoplamientos aceptados sin consumidor de producción todavía**
  (varias rondas, `ADVISORY`): `git.LineRange` como vocabulario de dominio en
  `internal/remediation` en vez de un tipo propio del paquete; `GuardedEditor`
  depende del struct concreto `DiffGuard` en vez de una interfaz mínima;
  `GuardedEditor.Edit` asume que la ausencia de archivo se señaliza vía
  `errors.Is(err, os.ErrNotExist)` (mismo contrato implícito que `ScopedEditor`
  ya asume desde T7.2); el invariante `margin >= 0` se normaliza dentro de
  `allowedWindows` en vez de en el borde público (`NewDiffGuard`/
  `DiffGuard.Margin`). Aceptados: son decisiones de diseño válidas mientras no
  exista una segunda implementación de `Editor` o un llamador real que
  construya un `margin` negativo o necesite otra política de alcance — YAGNI,
  no descuido.
- **`parseHunkField`/`parseHunkRanges` no validan los enteros que parsean**
  (`545b3c9`, dimensión `security`, `WARNING`): aceptan cualquier entero que
  `strconv.Atoi` logre parsear. El único consumidor real hoy,
  `DiffGuard.allowedWindows`/`mergeWindows`, ya satura `start`/`end` antes de
  operar con ellos (`fee026f`…`d4af2a9`), así que el riesgo de índice fuera de
  rango que motivó el hallazgo no se materializa en el único camino que existe.
  Sigue siendo una validación ausente en el borde de `internal/git` si aparece
  un segundo consumidor; no accionable dentro del alcance de T7.3.
- **`Location.Blob` no se usa para detectar contenido desplazado**: `DiffGuard.Check`
  acota por `LineaInicio`/`LineaFin` contra lo que `Editor.Read` devuelva en ese
  momento, sin comparar con `Location.Blob` (el hash de contenido del hallazgo,
  pensado exactamente para detectar cuándo las líneas ya no significan lo mismo
  que cuando se generó el hallazgo — ver `internal/review/finding.go` y el índice
  de blobs de `internal/store`). Si el archivo cambió en el worktree desde que se
  registró el hallazgo, el guard autoriza una ventana que ya no corresponde al
  contenido real. Aceptado: no hay todavía ningún `Editor` real ni flujo que
  invoque `DiffGuard` con hallazgos potencialmente desactualizados; queda como
  candidato a revisar cuando exista ese wiring.

**Hallazgo no relacionado, fuera de alcance**: `TestCrearSnapshotConcurrenteNoDuplicaNiFalla`
(`internal/git/snapshot_test.go`) es intermitente — falla aproximadamente 1 de
cada 3 ejecuciones con `"no se pudo reparar los metadatos del worktree del
snapshot ...: exit status 128"`, una condición de carrera preexistente en la
creación concurrente de snapshots que no toca ningún archivo de esta tarea.
No se investiga ni se corrige aquí.

---

## T7.4 — Re-validación y ronda única ✅ (hash de cierre `33e3227`)

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 240 líneas |
| Depende de | T7.3 |
| Commits | `36b9e6f` (implementación: `internal/remediation/round.go` — `RunSingleRound(profile, before, touchedFiles, revalidate Revalidate, reReview ReReview) (RoundResult, error)`, con `Revalidate`/`ReReview` inyectados en vez de llamar a `internal/validation`/`internal/graph` directamente; `RoundResult{Verdict, Unresolved []UnresolvedFinding, New []review.Hallazgo, Blocked bool}`; `UnresolvedFinding{Finding, SwallowedByLocated bool}`), `ceb2158` (fix tras revisión: un hallazgo CRITICAL introducido por el propio fix —presente en `New`, no en `before`— no forzaba `ResultNeedsUserReview`, solo los CRITICAL ya presentes en `Unresolved` lo hacían; además las rutas de error dejaban `Verdict` en su valor cero `""` en vez de en el lado bloqueante, y `swallowedByLocated` producía un falso positivo cuando dos hallazgos distintos compartían `Archivo == ""`), `33e3227` (fix: la ruta de error de `reReview` descartaba el valor real de `Blocked` que `revalidate` ya había devuelto con éxito antes de que `reReview` fallara) |

**Contexto**: `internal/validation/*` (F1), `internal/graph/*` (F4)

**Hacer**

1. Re-validar con el mismo perfil que falló, acotado a lo tocado por el fix más
   su cierre inverso.
2. Re-analizar, re-planificar y re-revisar **solo lo tocado**.
3. **Límite duro: una ronda.** Si quedan bloqueantes → `NEEDS_USER_REVIEW`.
4. Reportar hallazgos no resueltos **y** hallazgos nuevos introducidos por el fix.

**Por qué el límite**: los bucles fix→review→fix son donde el coste se descontrola
y donde el sistema deja de ser reproducible.

**Aceptación**: cumplida. `TestRunSingleRoundNeverLaunchesASecondRound` (`round_test.go`)
es el criterio literal de la ficha: con un bloqueante real tras la ronda
(`revalidate` devuelve `blocked=true` y el hallazgo CRITICAL sigue en `after`),
`RunSingleRound` devuelve `ResultNeedsUserReview` habiendo llamado a `revalidate`
y a `reReview` exactamente una vez cada uno — no hay ningún bucle en la función
que pueda relanzarlos; un segundo intento solo puede venir de que el propio
llamador invoque `RunSingleRound` otra vez. `TestRunSingleRoundBlockedAloneForcesReview`
y `TestRunSingleRoundCriticalUnresolvedAloneForcesReview` aíslan cada rama del
`OR` del veredicto por separado (antes solo existía un test con ambas
condiciones a la vez, que no discriminaba si alguna se rompía). El punto 1
("acotado a lo tocado por el fix") se resuelve pasando `touchedFiles` como
parámetro explícito de `RunSingleRound`, verificado con
`TestRunSingleRoundPassesThroughProfileAndTouchedFiles` (reenvío exacto a
`revalidate`/`reReview`, con un valor centinela distinto del resto de tests
para que un intercambio de argumentos no pase por coincidencia); el punto 4
se resuelve con `RoundResult.Unresolved`/`.New` (aritmética de conjuntos por
`Fingerprint` entre `before` y `after`, con `TestRunSingleRoundFingerprintArithmetic`
fijando también que un `New` no-CRITICAL no fuerza el veredicto).

El follow-up de T7.3 sobre distinguir un hallazgo sin ubicación "tragado" por
uno localizado del mismo archivo, de un rechazo genuino por alcance, **queda
resuelto aquí** (no es condicional): `UnresolvedFinding.SwallowedByLocated`
marca exactamente ese caso, reimplementando la regla de `DiffGuard.allowedWindows`
(T7.3) sobre `before` — `TestRunSingleRoundSwallowedByLocatedDistinction` cubre
el hallazgo sin ubicación que comparte archivo con uno localizado (marcado),
el que está solo en su archivo (no marcado) y el propio hallazgo localizado
(nunca marcado); `TestSwallowedByLocatedNoFalsePositiveOnEmptyArchivo` cierra
el caso borde de dos hallazgos con `Archivo == ""` coincidiendo por el valor
cero, no por compartir archivo real.

`go build ./...`, `go vet ./...` y `go test ./...` en verde para todo el
módulo, verificados de forma independiente en cada ronda (no solo el
autoinforme del subagente implementador). `sentinel gate --stage pre-push`
sobre `33e3227` → `PASS` (`logic` y `design` en `ok`; `spec`/`tests` quedan en
`warn` superficial sin hallazgos nuevos).

Hallazgos de `sentinel review` aceptados sin fix adicional (deuda de diseño
sin segundo consumidor todavía, mismo criterio que T7.2/T7.3):

- **`SwallowedByLocated` duplica la regla de `DiffGuard.allowedWindows` por
  copia en vez de que `DiffGuard` la exponga como predicado** (`WARNING`):
  cualquier cambio futuro en la ventana located/unlocated de T7.3 deja esta
  heurística en deriva silenciosa. Aceptado: no existe todavía un segundo
  consumidor de esa regla que justifique extraer un predicado compartido
  entre `internal/remediation` y `internal/git`/`internal/remediation`
  (`diffguard.go`) — YAGNI, no descuido.
- **`RoundResult.Verdict` es un `string` desnudo** (`ADVISORY`, primitive
  obsession) y **`RunSingleRound` mezcla orquestación, diferencia de
  conjuntos y la heurística de swallowing en un solo cuerpo** (`ADVISORY`,
  cohesión): aceptados mientras no haya un segundo llamador real que fuerce
  tipar el veredicto o extraer `diffFindings` como función pura.
- **La comprobación de "ubicación válida" (archivo + línea) vive en
  `remediation` en vez de como predicado de `review.Ubicacion`** (`ADVISORY`,
  feature envy): aceptado por el mismo motivo — es el único consumidor hoy.
- **Comentarios que citan "la ficha" o el comportamiento de `internal/gate`
  no verificables desde el diff de un commit aislado** (`ADVISORY`): mismo
  punto ciego de alcance de archivos por commit ya documentado en el cierre
  de T7.3 — verificado directamente contra `docs/reingenieria/f7-remediacion.md`
  y el código real de `internal/gate`; las afirmaciones son correctas, la
  revisión simplemente no tenía visibilidad de esos archivos.
- **Los tests usan los mismos valores en `before` y `after` (mismo
  `Fingerprint`), sin ejercitar un hallazgo cuya `Location` se desplace tras
  el fix** (`ADVISORY`): `swallowedByLocated` lee `Location` del lado
  `after`, así que un desplazamiento de línea con el mismo fingerprint es un
  caso real no cubierto. **Follow-up**: añadir un caso con `Location`
  divergente entre `before`/`after` cuando exista un consumidor real que
  pueda producir esa divergencia (hoy todo el árbol pasa por dobles de test).

**Follow-up para cuando exista un `Editor` real (CLI-backed)**, no antes:

- El punto 1 de "Hacer" ("acotado ... más su cierre inverso") solo se resuelve
  aquí como "pasar `touchedFiles` al callback inyectado"; convertir eso en una
  autorización real de `internal/graph` (`AutorizarAlcanceParcial` →
  `validation.OpcionesEjecucion`) es responsabilidad de quien construya el
  `Revalidate` real, no de este paquete — `internal/validation`'s campo
  `autorizacion` es privado del paquete a propósito (T1.x), y replicar aquí el
  pipeline completo de `internal/gate` habría duplicado esa fase entera muy
  por encima del presupuesto de esta tarea.
- Los follow-ups de T7.2 (`Scope` rastreando archivos creados por la propia
  ronda) y T7.3 (atomicidad multiarchivo al revertir un fix que sale de
  alcance) siguen sin aplicar aquí: `RunSingleRound` no aplica ningún cambio
  por sí mismo, solo revalida/re-revisa lo que el llamador ya aplicó — ambos
  siguen condicionados a que exista wiring real con un `Editor` CLI.

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
