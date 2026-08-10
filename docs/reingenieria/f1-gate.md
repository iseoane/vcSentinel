# F1 — Invertir el gate

**Objetivo**: que lo determinista bloquee y lo semántico avise. Es la fase de
mayor retorno del plan y no depende de ningún grafo.

**Problema que resuelve** (informe §2.2 y §3, C1): hoy un `CRITICAL` de un LLM
bloquea la publicación (`cmd/sentinel/comandos_pr.go:528`) y un `go test ./...`
en rojo solo decora la plantilla (`internal/ops/verificar.go:783`). Además la
verificación se ejecuta sobre el **worktree vivo** (`verificar.go:894`), así que
el PR puede publicar exit codes de un árbol distinto al que se va a mergear.

**Criterio de salida de la fase**

1. Una PR con `go test` en rojo **no se publica**.
2. Una PR con un `CRITICAL` semántico y validación en verde **sí se publica**, con
   aviso visible.
3. Con cambios sin commitear en disco durante la ejecución, el resultado sigue
   siendo el del árbol congelado — demostrado con un test.
4. Una clave desconocida en `vassentinel.yml` produce **error explícito con la
   línea**, no silencio.
5. `go test ./...` en verde y `build.bat` genera el candidato sin errores.

---

## T1.1 — Sustituir el parser artesanal por `yaml.v3`

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 350 líneas |
| Depende de | F0 cerrada |
| Commit | `refactor(config): parsear el yml con yaml.v3 y esquema explicito` |

**Contexto**

- `internal/config/parser.go` (completo)
- `internal/config/parser_test.go`, `secciones_test.go`, `comandos_verificacion_test.go`
- `.vas_sentinel/vassentinel.yml`
- Informe §3 → **I5**

**Problema**: `aplicarDesdeRuta` (`parser.go:145`) recorre por niveles de
indentación e **ignora en silencio toda clave desconocida**. Un `comand:` en
lugar de `command:` desactivaría una validación sin avisar. Para un gate, un
fallo silencioso es el peor modo de fallo posible.

**Hacer**

1. Añadir `gopkg.in/yaml.v3` — primera dependencia externa del proyecto,
   decisión ya tomada en el informe §27.
2. Definir structs con etiquetas `yaml:` para el esquema **actual**, sin añadir
   secciones nuevas todavía.
3. Usar decodificación estricta: una clave desconocida es error, con archivo y
   línea.
4. Conservar la precedencia `defaults → global → per-proyecto` y la semántica
   de sobreescritura campo a campo (`CargarConfiguracionLocal`, `parser.go:118`).
5. Conservar `AgentOrder`: el orden de declaración de los agentes alimenta la
   resolución `auto` y no puede perderse al pasar a un `map`.

**Criterio de aceptación (innegociable)**

> Los tests existentes de `internal/config` pasan **sin modificar ni una línea de
> test**. Si alguno hay que tocarlo, es que se ha cambiado comportamiento: para
> e infórmalo.

Además: test nuevo que comprueba que una clave desconocida devuelve error con
número de línea.

**Riesgo**: alto. Es el archivo del que depende todo lo demás. Por eso va primero
y solo hace la conversión, sin añadir funcionalidad.

---

## T1.2 — Sección `validation:` en la configuración

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 220 líneas |
| Depende de | T1.1 |
| Commit | `feat(config): capacidades y perfiles de validacion en el yml` |

**Contexto**

- `internal/config/parser.go` (ya convertido en T1.1)
- Informe §7.1

**Hacer**: añadir al esquema

```yaml
validation:
  capabilities:
    format:
      command: "gofmt -l ."
      fails_when: output_not_empty     # exit_code (default) | output_not_empty
    lint:
      command: "go vet ./..."
      supports_scope: true
      scoped_command: "go vet {packages}"
    unit_test:
      command: "go test ./..."
      supports_scope: true
      scoped_command: "go test {packages}"
      timeout: 300
  profiles:
    fast:     [format, lint]
    standard: [format, lint, build, unit_test]
    full:     [format, lint, build, unit_test, static_analysis, security]
  mode: worktree                       # worktree (default) | inplace
```

Validaciones de forma, en el momento de la carga:

- Un perfil que nombra una capability inexistente → error.
- `supports_scope: true` sin `scoped_command` → error.
- `scoped_command` sin el marcador `{packages}` → error.

**Compatibilidad**: `lint_commands` / `test_commands` / `build_commands` siguen
funcionando y se traducen internamente a capabilities implícitas. No se rompe
ninguna configuración existente.

**Aceptación**: test por tabla de las tres validaciones de forma y de la
traducción de la configuración antigua.

---

## T1.3 — Motor de validación: `internal/validation`

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 320 líneas |
| Depende de | T1.2 |
| Commit | `feat(validation): motor de capacidades con resultado tipado` |

**Contexto**

- `internal/ops/verificar.go` (completo — es el punto de partida)
- `internal/ops/verificar_test.go`
- Informe §7.3

**Hacer**

1. Paquete nuevo `internal/validation` con `EjecutarPerfil(perfil, alcance, opts)`.
2. Tipo `ValidationRun` por capability:

```go
type ValidationRun struct {
    Capability  string
    Comando     string
    Alcance     string   // "completo" | "parcial"
    MotivoAlcance string
    Exit        int
    DuracionMs  int64
    Salida      string   // recortada, para la evidencia del finding
}
```

3. Traducir cada `ValidationRun` con `Exit != 0` a un finding con
   `source: "validation"`, `severity: CRITICAL`, y la salida real como evidencia.
   El tipo de finding v2 llega en F2: aquí basta una estructura mínima con
   `source`, y F2 la absorbe.
4. `fails_when: output_not_empty` para comandos como `gofmt -l`, que salen 0
   aunque haya trabajo pendiente.
5. Costura de inyección para el ejecutor, como ya hace `OpcionesVerificar.Ejecutar`.

**Conservar de `ops.Verificar`**: la vía de delegación al agente con contrato
`tested`, la detección de CI y los motivos tipados. Se mueven, no se pierden.

**No tocar**: `internal/ops/events.go`.

**Aceptación**

- Test: capability que falla → un finding CRITICAL con la salida del comando.
- Test: `output_not_empty` con exit 0 y salida no vacía → falla.
- Test: la delegación al agente sigue produciendo el contrato `tested`.

---

## T1.4 — Congelación del candidato: snapshots por `tree-oid`

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 260 líneas |
| Depende de | F0 cerrada *(independiente de T1.1-T1.3)* |
| Commit | `feat(git): snapshots inmutables del arbol para validar` |

**Contexto**

- `internal/git/gitdir.go` (ambas funciones)
- `internal/git/commit.go`
- Informe §31.3

**Hacer**: `internal/git/snapshot.go`

1. `CrearSnapshot(treeOID)` → `git worktree add --detach` en
   `<git-common-dir>/vas-sentinel/snapshots/<tree-oid>/`.
   **Usar `ObtenerGitCommonDir`, no `ObtenerGitDir`**: desde un worktree
   enlazado, el segundo devuelve el directorio privado.
2. Si la ruta ya existe con ese árbol → éxito, sin checkout. Es la reutilización.
3. Concurrencia: crear con nombre temporal y renombrar; «ya existe» es éxito, no
   error.
4. `PurgarSnapshots(antiguedad)` con `git worktree remove` + `git worktree prune`.
5. `ArbolDe(revisión)` → tree OID (`git rev-parse <rev>^{tree}`).

**Verificado ya**: Git acepta un worktree dentro del common-dir, lo lista y lo
elimina limpiamente, también en Windows.

**Aceptación**

- Test contra repositorio real: crear, reutilizar y purgar.
- Test: dos llamadas concurrentes al mismo árbol no producen error.
- Test: desde un worktree enlazado, el snapshot cae en el `.git` del principal.

---

## T1.5 — Detección de obsolescencia del candidato

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 150 líneas |
| Depende de | T1.4 |
| Commit | `feat(git): marcar obsoleto el resultado si el arbol cambio durante la ejecucion` |

**Contexto**: `internal/git/snapshot.go` (de T1.4), `internal/git/plan.go:196`
(`WorktreeLimpio`)

**Hacer**

1. `Congelar()` captura `HEAD` y el tree OID del worktree al empezar.
2. `SigueVigente()` los recompara al terminar.
3. Modo `inplace`: exige worktree limpio antes de ejecutar; si está sucio,
   **aborta** en lugar de reportar el resultado de un árbol distinto.

**Aceptación**: test que modifica el worktree entre congelar y comprobar, y
verifica que el resultado se marca obsoleto.

---

## T1.6 — Ejecutar la validación sobre el candidato congelado

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 200 líneas |
| Depende de | T1.3, T1.5 |
| Commit | `feat(validation): ejecutar las capacidades sobre el arbol congelado` |

**Contexto**: `internal/validation/*` (T1.3), `internal/git/snapshot.go`

**Hacer**

1. El motor recibe el directorio de ejecución del snapshot, no el worktree.
2. Modo `inplace` como alternativa configurable (§ T1.2).
3. Si el candidato quedó obsoleto, el resultado se marca y **no vale para el
   gate**.

**Limitación a documentar en el código**: un checkout limpio no tiene
`node_modules`, `.env` ni fixtures no versionados. Es la razón de existir del
modo `inplace`.

**Aceptación**: test que ejecuta una capability sobre un snapshot mientras el
worktree tiene cambios sin commitear, y comprueba que el resultado corresponde al
árbol congelado.

---

## T1.7 — `sentinel gate --stage pre-push`

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 280 líneas |
| Depende de | T1.6 |
| Commit | `feat(cli): subcomando gate con validacion antes de revision` |

**Contexto**

- `cmd/sentinel/main.go` (dispatcher, líneas 49-93)
- `cmd/sentinel/comandos_review.go`
- Informe §19

**Hacer**

1. Subcomando `gate` con `--stage pre-commit|pre-push|pr`.
2. Orden fijo: **validación primero**; si falla → `VALIDATION_FAILED`, exit 1, y
   **no se lanza ni un agente**.
3. Si la validación pasa → revisión semántica con el motor actual, en modo
   *advisory* hasta F5.
4. Exit codes: 0 PASS · 1 `VALIDATION_FAILED` · 2 `NEEDS_USER_REVIEW` ·
   4 `REVIEW_INFRASTRUCTURE_ERROR`.
5. Registrar el evento con el estado.

**Nota de arquitectura**: la lógica va en `internal/`, no en `cmd/`. Es el primer
paso de la extracción progresiva de `package main` (informe C6). El comando en
`cmd/` solo parsea flags e imprime.

**Aceptación**

- Test: validación en rojo → exit 1 y **cero llamadas al agente** (verificable
  con una fábrica de auditores que cuenta invocaciones).
- Test: validación en verde y `CRITICAL` semántico → exit 0 con aviso.

---

## T1.8 — `pr create`: la validación manda

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 240 líneas |
| Depende de | T1.7 |
| Commit | `feat(pr): la validacion determinista bloquea la publicacion` |

**Contexto**

- `cmd/sentinel/comandos_pr.go` (líneas 485-560)
- `internal/review/renderer.go` (sección de verificación de la plantilla)

**Hacer**

1. Ejecutar la validación **antes** de auditar. Si falla: no se publica, no se
   gasta ni un token, y se listan los comandos en rojo.
2. El gate de veredicto semántico pasa a advisory: publica con aviso destacado.
3. La plantilla distingue las dos naturalezas de evidencia: exit codes reales en
   una sección, hallazgos semánticos en otra.
4. `--force` sigue existiendo, pero ahora **registra la excepción** con motivo
   (informe M3).

**Regla de oro intacta**: nunca un PASS inventado. O exit codes de ejecución
real, o `tested` del agente, o la línea explícita «tests no ejecutados».

**Aceptación**

- Test: validación en rojo → `gh` **no se invoca** (comprobable con el seam
  `opcionesPublicarPR`).
- Test: `--force` sin motivo → se pide motivo; con motivo → queda registrado.

---

## Cierre de fase — desviaciones respecto al diseño (hash de cierre `ac455be`)

- **T0.0 no estaba en pie al abrir la fase**: el hook `pre-commit` apuntaba al
  binario de desarrollo del repo (`bin/0.2.0/sentinel`), no a
  `~/.vas_sentinel/bin/sentinel`. Se perdió al mover el proyecto a WSL. Se
  rehizo como paso 0, antes de T1.1.
- **Cadencia de revisión semántica por tarea, descartada**: el README (§4.2)
  proponía `sentinel review HEAD` tras cada tarea hasta que existiera `gate`.
  Se comprobó que el costo total en tokens es el mismo que un único pase al
  cierre (el ledger no repite trabajo ya hecho), así que se reservó un único
  `sentinel pr review --base origin/main` al cerrar la fase. Hallazgo
  colateral de ese pase: el comando revisa cada commit pendiente
  individualmente (mismo motor que `review HEAD`, sin agregación), y su log
  de progreso no identifica a qué commit pertenece cada línea — deuda de UX
  del comando, no de esta fase, sin arreglar.
- **T1.8 partía de un `--force` ya existente** en `comandos_pr.go` (no
  documentado en la ficha original): se adaptó para exigir `--reason` y
  registrar la excepción, en vez de crearse desde cero.
- **T1.7 tomó una decisión de diseño no especificada por la ficha**: qué
  perfil de `validation.profiles` usar. Se añadió `--profile` (default
  `"standard"`); `--stage` solo identifica el punto del ciclo de vida para
  mensajes/registro, es un eje independiente del perfil ejecutado.
- **Requisito añadido por el orquestador, fuera del texto original**: el
  criterio de salida #4 de la fase (clave desconocida → error explícito)
  exigió `config.CargarConfiguracionLocalEstricta`, no prevista en ninguna
  ficha de T1.1-T1.8. Se añadió en T1.7 para `gate`, y se extendió después a
  `status`, `review`, `pr review` y `pr create` en una corrección posterior.
- **`gateBlock` (comandos_pr.go) se renombró a `avisoSemantico`** en T1.8:
  cambió de bloquear la publicación a solo avisar, y un nombre que ya no
  describe lo que hace es peor que renombrarlo.
- **Correcciones tras una revisión de rama post-cierre** (`sentinel pr
  review --base origin/main` sobre los 21 commits de la fase, ver hallazgos
  en el ledger): se encontraron y corrigieron 2 CRITICAL y varios warnings
  antes de considerar la fase realmente cerrada —
  - `8ff1b3c`: la vía de delegación al agente (T1.3) nunca podía producir un
    hallazgo aunque el agente reportara un fallo real (PASS fabricado por
    construcción, `Capability: "delegado"` sin entrada en `capacidades`);
    además, `alcance` se interpolaba sin validar en `scoped_command`
    (inyección de comandos vía shell). Ambos corregidos.
  - `b5bad54`: `CargarConfiguracionLocalEstricta` no se usaba en `status`,
    `review` ni `pr review`; `mode`/`fails_when` no se validaban contra su
    dominio cerrado (typos silenciosos). Ambos corregidos.
  - `e48f0a5`: el snapshot de validación se anclaba siempre a `ArbolDe("HEAD")`
    en vez de al árbol real del candidato congelado (que usa un stash-anchor
    si el worktree ya estaba sucio al llamar), así que un worktree sucio
    desde el principio (no durante la ejecución) validaba el HEAD limpio, no
    lo que el usuario creía estar validando. Corregido.
  - `ac455be`: `gate.CodigoSalida` fallaba abierto (estado desconocido → exit
    0/PASS); `pr create` no usaba la carga estricta de config; `--force`
    registraba una excepción en el evento aunque la validación estuviera en
    verde y no hubiera nada que forzar. Los tres corregidos.
  - Advertencias documentadas y NO corregidas en esta fase (deuda conocida
    para F2 o una tarea de hardening dedicada): duplicación entre
    `internal/validation` e `internal/ops` del parseo del contrato `tested` y
    de la ejecución shell; `PurgarSnapshots` traga errores de limpieza;
    `ArbolDe` no valida la revisión recibida (riesgo de option-injection si
    algún día recibe entrada no confiable); tests de integración de
    `comandos_pr_test.go` no herméticos (escriben en rutas relativas del cwd
    real sin `t.TempDir()`).
