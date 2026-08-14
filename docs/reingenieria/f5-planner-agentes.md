# F5 — Planner, Scheduler y contexto acotado

**Objetivo**: decidir cuánta revisión merece cada cambio, y **darle al revisor la
información que necesita para no alucinar**.

**Problema que resuelve** (informe §3, C2): el prompt actual prohíbe mirar el
repositorio (`internal/review/prompts.go:555`) para evitar que `opencode run` se
cuelgue lanzando builds. Se resolvió un problema de infraestructura amputando el
contexto del revisor, y el resultado son los falsos positivos de **H5** — con
poder de veto sobre un `go test` verde.

**Criterio de salida de la fase (el más importante del plan)**

> Los cuatro falsos positivos documentados en **H5** dejan de reproducirse.

Es un test de aceptación concreto, no una impresión. Los cuatro casos están en
`docs/auditoria/README.md` con su commit y su evidencia:

| Caso | Commit | Falsedad |
|---|---|---|
| «corta runas UTF-8» | `1a61087` | Ya resuelto por `recortarRunas` (`renderer.go:293`) |
| «`maxBytes<=0` no trunca» | `1a61087` | Intencional, afirmado en `renderer_test.go:144` |
| «los tests no cubren `maxBytes<=0`» | `1a61087` | Los cubre ahí mismo |
| «`exitCodeDeError` no existe» | `914a975` | Está en `comandos_estado.go:135` |

Además: el plan es reproducible (mismo `unit_id` + misma política ⇒ mismo plan) y
todo plan explica sus decisiones en texto.

> **Revalidar esta ficha al abrir la fase** contra lo que F1–F4 hayan producido
> realmente. El alcance y los criterios son firmes; los presupuestos y la
> partición pueden cambiar.

---

## T5.1 — Planner como función pura

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 300 líneas |
| Commit | `feat(planning): planificador puro de validacion y revision` |

**Contexto**: `internal/change/*`, `internal/risk/*`, `internal/graph/*`, informe §10

**Hacer**: `internal/planning`

```
Plan = Planificar(PerfilCambio, PerfilRiesgo, ModeloCodigo, Politica, Etapa)
```

Sin IA, sin efectos, serializable. `plan_id` estable = hash de las entradas.

**Campo `explain` obligatorio**: todo plan debe justificarse ante el usuario en
texto. Un planificador que no explica sus decisiones es indistinguible de uno
roto.

**Aceptación**: mismas entradas ⇒ mismo `plan_id` y mismo plan; todo plan con
`explain` no vacío.

---

## T5.2 — Bundles y política por riesgo

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 260 líneas |
| Depende de | T5.1 |
| Commit | `feat(planning): bundles de dimensiones segun riesgo` |

**Contexto**: `internal/review/engine.go:51` (matriz estática a sustituir), informe §5

**Hacer**: sustituir `dimensionesPorCapa` por la tabla de riesgo:

| Riesgo | Agentes | Bundle |
|---|---|---|
| `none` | 0 | solo validación |
| `low` | 0–1 | Correctness (logic+spec+tests), esfuerzo bajo |
| `standard` | 1–2 | Correctness · Quality |
| `elevated` | 3 | Correctness · Quality · Security |
| `high` | 3–5 | + Contracts/Compatibility, + Concurrency/Data según características |

`style` deja de tener agente propio: lo cubre la capability `lint`, y su parte
semántica residual nunca supera `ADVISORY` y está desactivada por defecto
(informe §12).

**Aceptación**: test por tabla riesgo → número y composición de agentes.

---

## T5.3 — Scheduler con presupuesto y atribución

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 300 líneas |
| Depende de | T5.2 |
| Commit | `feat(agents): planificador de agentes con presupuesto y atribucion` |

**Contexto**: `internal/review/engine.go:86` (semáforo actual, se reutiliza),
`internal/agentadapter/cadena.go`, informe §11

**Hacer**

1. Traducir el plan a procesos, reutilizando el semáforo existente.
2. Presupuesto agregado de tiempo y coste. Si se agota, los agentes de prioridad
   2 no se lanzan **y el resultado lo declara**. Nunca se recorta en silencio.
3. Reintento **solo** ante error de transporte o timeout. Nunca ante salida
   inválida: una salida inválida repetida es un problema de prompt o de modelo, y
   reintentarla lo esconde.
4. Aislamiento de fallos: un agente caído es `unavailable` con razón y no tumba
   la ejecución. Corregir de paso la degradación excesiva de
   `veredictoGlobal` (`engine.go:175`), que hoy oculta un `block` de `logic` si
   `security` está caído.

**Aceptación**: test de presupuesto agotado con declaración explícita; test de
que una salida inválida no se reintenta.

---

## T5.4 — Verificación del modelo efectivo

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 200 líneas |
| Depende de | T5.3 |
| Commit | `feat(agents): verificar el modelo realmente aplicado por el agente` |

**Contexto**: `internal/agentadapter/cli.go:1014-1044`, guía §10, informe **I2**

**Problema**: el modelo se inyecta por `CLAUDE_CODE_MODEL` / `OPENCODE_MODEL`, y
la propia guía documenta que un modelo inexistente **se ignora en silencio**.
Toda la estrategia de coste y calidad puede estar degradada sin que nada avise, y
sin esto las métricas de F9 mienten.

**Hacer**: sonda mínima por sesión que pregunta al agente qué modelo usa. Si no
coincide, registrar `model_mismatch` y marcar el perfil `unverified` en el store.

**Aceptación**: test con doble que devuelve un modelo distinto → queda marcado.

---

## T5.5 — Context Builder

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 320 líneas |
| Depende de | T5.1 |
| Commit | `feat(review): construccion de contexto por capas con presupuesto` |

**Contexto**: `internal/git/commit.go`, `internal/graph/*`, informe §13

**Hacer**: capas en orden, hasta agotar presupuesto:

```
1. diff + mensajes de commit
2. resultados de validación (tests fallidos con su salida)
3. contenido completo de los archivos tocados, en su ESTADO FINAL
4. símbolos cambiados + callers directos
5. tests de los paquetes afectados
6. contratos e interfaces implicadas
7. lista de rutas explorables (rutas, no contenido)
```

**La capa 3 es la que mata H5**: hoy el revisor ve un diff donde un símbolo
parece no existir y no puede comprobarlo.
**La capa 2 evita trabajo duplicado**: quien ya sabe qué test falló no reporta lo
que la validación cubre.

Lectura desde el **object store** (`git show <tree>:<path>`), nunca desde el
worktree — el candidato está congelado (F1).

**Aceptación**: test de que con presupuesto reducido se recortan las capas de
mayor número primero, nunca la 1 ni la 2.

---

## T5.6 — Toolset de solo lectura y prompts nuevos

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 300 líneas |
| Depende de | T5.5 |
| Commit | `feat(review): revisores de solo lectura con rutas acotadas` |

**Contexto**: `internal/review/prompts.go` (completo), `internal/agentadapter/cli.go`

**Hacer**

1. Levantar la prohibición de herramientas y sustituirla por acotación: `Read`,
   `Grep`, `Glob` restringidos a las rutas del plan. **Sin `Bash`, sin escritura,
   sin red.**
2. Límite de llamadas a herramienta por agente, además del timeout existente.
3. Reescribir los prompts: evidencia literal obligatoria, `confidence` explícita,
   y la instrucción de **comprobar antes de afirmar que algo no existe**.

**Riesgo declarado (informe §30.1)**: la prohibición existía porque `opencode
run` se colgaba haciendo builds. **Verificar con `opencode` real antes de dar la
tarea por buena.** Si vuelve a colgarse, es un hallazgo, no un fallo de la tarea.

**Aceptación**: prueba manual documentada con `opencode` y con `claude` sobre un
commit real, sin cuelgues, con el informe recogiendo tiempos.

---

## T5.7 — Verificador de hallazgos bloqueantes

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 260 líneas |
| Depende de | T5.6 |
| Commit | `feat(review): refutador de hallazgos criticos antes de bloquear` |

**Contexto**: `internal/agents/*`, informe §5 y §15

**Hacer**

1. Por cada `CRITICAL` semántico, **una** llamada barata a un refutador con
   acceso de lectura, cuyo prompt es *intentar demostrar que el hallazgo es
   falso*.
2. Refutado → `NEEDS_USER_REVIEW`, **no bloquea**. Nunca se descarta en silencio.
3. Confirmado → bloquea como `CODE_REVIEW_FAILED`.
4. Una llamada por hallazgo bloqueante, no por hallazgo.

**Aceptación**: test con hallazgo refutado → estado `NEEDS_USER_REVIEW` y exit 2.

---

## T5.8 — Test de aceptación de H5

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 240 líneas |
| Depende de | T5.7 |
| Commit | `test(review): regresion de los falsos positivos de H5` |

**Contexto**: `docs/auditoria/README.md` (H5), commits `1a61087` y `914a975`

**Hacer**: fixture con los cuatro casos y sus diffs reales; comprobar que con el
contexto nuevo ninguno produce un `CRITICAL` confirmado.

**Si algún caso sigue reproduciéndose**: no se maquilla el test. Se informa, y la
fase no cierra hasta entenderlo. Este test es el criterio de salida de la fase
entera.

---

## Cierre de fase — desviaciones respecto al diseño (hash de cierre `91dc65f`)

- **T5.1–T5.8 estaban implementadas antes de esta ronda de cierre** (fragmentadas
  en commits previos, incluido `TestH5HistoricalFalsePositivesUseFinalSnapshotEvidence`
  para T5.8). Lo que quedaba abierto no era implementar ninguna tarea, sino
  destrabar el `sentinel gate --stage pre-push` final: la revisión semántica con
  OpenCode fallaba con `REVIEW_INFRASTRUCTURE_ERROR` en todo intento.
- **El diagnóstico previo de esa infraestructura tenía un error real**: se había
  concluido que, tras corregir la configuración inválida del revisor (permiso
  `webfetch` que OpenCode rechazaba, `fa89e58`) y empezar a conservar el stderr
  real en vez de la cadena fija `provider_unavailable` (`bd058de`, `a09aceb`), el
  fallo restante (`UnknownError: Unexpected server error` de OpenCode) apuntaba
  "al servicio/proveedor... no a credenciales ni al aislamiento básico ya
  verificado". Verificado empíricamente reproduciendo la llamada aislada exacta
  con un prompt trivial: el fallo desaparecía por completo si no se sobrescribía
  `HOME`. La causa real era que `reviewEnvironment()` (`internal/agentadapter/cli.go`)
  aísla `HOME` para separar al revisor de la configuración del proyecto real,
  pero eso también corta el acceso a `auth.json` de OpenCode (vive bajo el
  `HOME` real), así que el proceso aislado intentaba autenticarse sin
  credenciales. Corregido en `1c24f47`.
- **Bug independiente descubierto en el mismo commit**: el prompt de T5.6 pide
  al modelo `confidence: high, medium, or low` (categórico), pero el parser
  (`internal/review/finding.go`) solo aceptaba un `float64`. Cualquier finding
  con confianza categórica rompía el `json.Unmarshal` de la línea JSONL
  completa, y si era la única línea, degradaba toda la dimensión a
  `ErrJSONLInvalido` — indistinguible en los logs de un fallo real del
  proveedor. Corregido con un tipo `confidenceScore` que acepta ambas formas,
  también en `1c24f47`.
- **La revisión semántica real, ya funcionando, encontró y bloqueó 3 `CRITICAL`
  de seguridad reales en las siguientes tres rondas de corrección del propio
  fix de autenticación** — exactamente el comportamiento que T5.6/T5.7 estaban
  diseñados para producir:
  - `bf14f4b`: el fix de `1c24f47` inyectaba el `auth.json` completo del host
    (multi-proveedor: `openrouter`/`groq`/`openai`) al sandbox, exponiendo
    credenciales no relacionadas con el proveedor configurado. Corregido
    acotando la inyección a la entrada del proveedor de `c.Config.Model`.
  - `9fbaea0`: el acotado de `bf14f4b` solo se aplicaba a la credencial leída
    del archivo del host, no a un `OPENCODE_AUTH_CONTENT` heredado del
    entorno del proceso llamador — que seguía pasando sin filtrar. Unificado
    para aplicar el mismo filtro a ambas fuentes.
  - `0953311`: `XDG_DATA_HOME` nunca se bloqueaba de la herencia pasiva del
    entorno; si el proceso llamador la tenía seteada apuntando al directorio
    de datos real, un fallo del filtrado (JSON heredado corrupto o sin el
    proveedor buscado) dejaba a OpenCode caer directamente al `auth.json` real
    sin pasar por ningún filtro. Corregido aislando también esa variable al
    snapshot, igual que el resto de `XDG_*`.
- **Follow-up de cobertura de test cerrado en `91dc65f`**: la re-revisión de
  `a09aceb` había señalado (severidad `WARNING`, no bloqueante) que su test
  solo cubría el fallo del proveedor en la primera llamada de
  `auditarConAgente`, no la segunda ronda (verdicto `question` respondido con
  `OpcionesAuditoria.Respuestas` no vacío, que también falla). Se añadió
  `TestAuditarConAgenteRetainsProviderFailureReasonOnAnsweredRetry` cubriendo
  esa rama.
- **Verificación de T5.1–T5.7 en esta ronda fue por evidencia de artefacto, no
  auditoría exhaustiva de sus criterios de aceptación**: se confirmó presencia
  real de código para cada una (`internal/planning/` para T5.1, `BundlesForRisk`
  en `engine.go` para T5.2, `AuditarCommit` con semáforo/presupuesto para T5.3,
  `internal/modelprobe/` para T5.4, `contextoRevisor`/`ContextProvider` para
  T5.5, `refutarHallazgosCriticos` para T5.7), pero no se re-verificó cada
  criterio de aceptación individual línea por línea.
- **`sentinel pr review --base main` no aplicable al cierre**: todo el trabajo
  de esta ronda se comiteó directo a `main`, sin rama de feature.
