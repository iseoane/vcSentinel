# VAS Sentinel — Guía de implantación de auditoría por commits

Documento de diseño acordado (6 iteraciones) para la evolución de VAS Sentinel:
de guardián de volumen a auditor de cambios por commits. Fuente de verdad para
las fases 0–2. El vocabulario canónico en inglés es contrato Go+prompt; la UI
se muestra en castellano.

---

## 1. Propósito

VAS Sentinel protege los worktrees operados por agentes de IA de dos formas:

1. **Guardián de volumen** (existente): limita el cambio acumulado a ≤400 líneas
   por fragmento (`check`/`slice`).
2. **Auditor de commits** (nuevo): audita cada commit contra 6 dimensiones de
   calidad, guarda el veredicto en un ledger local y prepara revisiones de PR
   con evidencia trazable.

La auditoría es **local y determinista por construcción**: sin servicios en
nube, sin estado global, sin commits de metadatos. Todo vive en el common-dir
de Git del repositorio.

## 2. Comandos finales

| Comando | Descripción | Estado |
|---|---|---|
| `sentinel check` | Volumen pendiente vs HEAD (0–200 OK, 200–400 PUNTO_OPTIMO, >400 CRITICO) | Existente, sin cambios |
| `sentinel slice` | Fragmenta el acumulado en lotes ≤400 líneas, plan aprobado (A/R/E/C), commits `--no-verify` | Existente, sin cambios de alcance |
| `sentinel rebase` | `fetch` + `rebase` con confirmación explícita. Nunca automático, nunca dentro del hook | Fase 1 |
| `sentinel lint` | Ejecuta los comandos de lint definidos en `lint_commands` de la config | Fase 1 |
| `sentinel review [<target>]` | Audita un commit (default HEAD) por dimensiones | Fase 1 |
| `sentinel pr review` | Analiza la rama entera; **no publica** | Fase 2 |
| `sentinel pr create` | Genera y publica el PR (`gh pr create`) con la plantilla | Fase 2 |
| `sentinel status` | Estado del repositorio: pendiente, auditados, eventos recientes | Fase 1 |

## 3. Regla del guardián (se mantiene)

- `check` antes de cualquier cambio o plan; >400 líneas = prohibido escribir
  más código hasta `slice`.
- `slice` en modo plan: propone lotes y mensajes, no commitea sin aprobación.
- Los commits de slice usan `--no-verify`: invocar slice ES el desbloqueo y
  cada lote ya está validado.
- **Dogfooding**: el desarrollo de VAS Sentinel se hace con su propio
  guardián — `check` antes de cada cambio, `slice` al superar 400. Cuando la
  auditoría exista (fase 1+), los commits de las propias fases se auditan con
  `sentinel review` y los resultados quedan en el ledger local.

## 4. Ubicación del estado: `<git-dir>/vas-sentinel/`

Todo estado nuevo (ledger de fichas, `events.jsonl`) vive bajo el common-dir
de Git del repositorio, resuelto con `git rev-parse --git-dir` + `filepath.Join`
(nunca la ruta literal `.git`; en worktrees enlazados y en Windows `.git` puede
ser un archivo gitfile).

Justificación (4 razones):

1. **Nunca se commitea**: el common-dir queda fuera del árbol de trabajo, no
   depende de `.gitignore` ni de revisores.
2. **Aislamiento per-worktree gratis**: cada worktree tiene su propio
   common-dir (`git rev-parse --git-dir` devuelve `.git/worktrees/<nombre>`),
   así cada rama audita contra su propio ledger.
3. **Ciclo de vida ligado al repositorio**: se borra con el clone/rama, sin
   basura global en `~/.vas_sentinel`.
4. **Sin configuración extra**: la ruta se deriva, no se configura.

Alternativas descartadas: `~/.vas_sentinel` (mezcla repos), `.vas_sentinel` en
la raíz (depende de `.gitignore`), refs de Git (contaminan push), SQLite
(over-engineering para ficha + append).

## 5. Contrato de salida del agente (JSONL)

El adaptador recibe un prompt con la dimensión a auditar y el diff del commit.
Debe responder JSONL delimitado por `BEGIN_REVIEW` / `END_REVIEW` (el parseo
tolera texto alrededor y fences de markdown).

Cada línea es un objeto:

```json
{"dimension":"logic","file":"internal/git/slice.go","line":42,"severity":"WARNING","description":"...","suggestion":"..."}
{"dimension":"spec","file":"cmd/sentinel/main.go","line":10,"severity":"CRITICAL","description":"...","suggestion":"..."}
```

### Dimensiones canónicas (validación estricta)

`logic`, `style`, `design`, `tests`, `security`, `spec`.

- `spec` es transversal: compara el diff contra el mensaje de commit (eje Spec
  de Pocock). Se audita SIEMPRE, además de las dimensiones del bucket.
- Dimensión desconocida → rechazo con mensaje explícito (nunca silencio).
- Severidad desconocida → normalización a `ADVISORY` con aviso.

### Severidades

| Valor | Significado |
|---|---|
| `CRITICAL` | Bloqueante: el commit no debería existir tal cual |
| `WARNING` | Riesgo o deuda razonable: mejorar en este commit o en un follow-up |
| `ADVISORY` | Sugerencia, sin impacto funcional |

### Veredictos de dimensión

`ok` | `warn` | `block` | `question` | `unavailable`

- `question`: el agente necesita aclaración; el campo `questions` lleva las
  preguntas (máx. 3). Se resuelven con `--answer "Q1: ...; Q2: ..."` inyectado
  por Go en el prompt, máximo 1 ronda extra.
- `unavailable`: sin tokens/rate-limit; se guarda con razón
  (`rate_limit`, `auth`, `timeout`, `empty`). Nunca es bloqueante.

### Normalización de veredictos (de facto → derivados)

Los agentes reales no siempre respetan el contrato (p. ej. devuelven
`"verdict":"issues"`). La normalización en `internal/review/finding.go`
resuelve el veredicto final así:

1. `question` / `unavailable` explícitos → se respetan tal cual.
2. Veredicto de facto con hallazgos (`issues`, etc.) → se deriva de las
   severidades: CRITICAL → `block`, WARNING → `warn`, solo ADVISORY → `warn`
   con severidad máxima visible.
3. `ok` declarado con hallazgos CRITICAL → se eleva a `block`; con WARNING /
   ADVISORY → `warn`. La elevación es ascendente: nunca se degrada un `block`
   declarado con severidades menores.
4. Veredicto inválido sin hallazgos → `ErrVeredictoInvalido` (no se inventa
   un PASS).

### Ejemplo con preguntas

```json
{"dim":"logic","verdict":"question","questions":[{"id":"Q1","text":"¿El rebase debe interrumpir si hay cambios sin commitear?"}]}
{"dim":"security","verdict":"ok","findings":[]}
```

## 6. Ledger de auditoría

- **Ficha por SHA**: `<git-dir>/vas-sentinel/<sha>.json` (un archivo por
  commit auditado, nunca un JSON único que se corrompe con escrituras
  concurrentes).
- **Escritura atómica**: temp + rename (en Windows, borrar previo + rename).
- **`revisions[]` append-only**: re-auditar el mismo SHA añade una revisión,
  no pisa la anterior. El veredicto mostrado es el de la última revisión.

```json
{
  "sha": "a1b2c3d4",
  "message": "feat(review): ...",
  "bucket": "backend",
  "model": "claude-sonnet",
  "revisions": [
    {
      "at": "2026-01-01T10:00:00Z",
      "result": "block",
      "dims": [{"dim":"spec","verdict":"ok"},{"dim":"security","verdict":"block","findings":[...]}]
    },
    {
      "at": "2026-01-01T10:05:00Z",
      "result": "ok",
      "dims": [{"dim":"spec","verdict":"ok"},{"dim":"security","verdict":"ok"}]
    }
  ]
}
```

- **Huérfanos**: SHA del ledger ausente del historial (`git log`) → se reporta
  en `status` como huérfano, no se borra automáticamente.
- **Trazabilidad de correcciones** (`fixed_in`): un commit posterior con
  mensaje `fix(` y veredicto no bloqueante marca como corregida la ficha de
  cualquier commit previo en `block` cuyos archivos toca el fix
  (`Ficha.FixedIn`, `Revision.Fixed`). La primera corrección gana
  (`MarcarCorregida` es idempotente). `status` muestra `🔧 corregida en <sha>`.
- **`pr review`** muestra revisión: `spec ✅ (2ª rev — CRITICAL superado)`.

## 7. Dimensiones: definiciones, matriz y prompt maestro

### 7.1 Definiciones

- **logic**: correctitud del comportamiento, condiciones, manejo de errores,
  casos límite.
- **style**: idioma, claridad, adherencia a las convenciones del repositorio.
- **design**: acoplamiento, abierto/cerrado, profundidad de módulos (glosario
  de Pocock: deep modules, dominio aislado, dependencias dirigidas al dominio).
- **tests**: cobertura significativa, determinismo, que un test falla solo si
  el comportamiento cambia.
- **security**: límites de privilegio, entrada no confiable, datos expuestos.
- **spec** (transversal): el diff cumple lo que dice el mensaje del commit;
  sin trabajo fuera de alcance ni afirmaciones sin respaldo.

### 7.2 Matriz saco × dimensión (configurable en `dimension_map`)

| Saco | Dimensiones |
|---|---|
| config | security, design |
| backend | logic, design, security |
| frontend | style, logic |
| test | tests |

`spec` siempre se añade al conjunto.

### 7.3 Prompt maestro (contenido esencial; la plantilla final vive en `internal/review/prompts.go`)

1. Rol: auditor técnico riguroso de un solo commit en el repositorio X.
2. Tarea: auditar SOLO el diff adjunto contra la dimensión D.
3. Reglas: marcar solo hallazgos accionables; `CRITICAL` solo si el diff
   introduce un defecto real; distinguir preexistente del introducido.
4. Smells de Fowler como guía de estilo/design: primitivos obsesivos, código
   duplicado, acoplamiento torbellino, switch/case con heurísticas, etc.
5. Formato: JSONL exacto entre `BEGIN_REVIEW` / `END_REVIEW`, claves
   canónicas, sin markdown fuera de los delimitadores, sin texto adicional.
6. Glosario (en el prompt): definiciones de las 6 dimensiones para que la
   salida use SIEMPRE los términos canónicos.
7. Si no hay hallazgos: `{"dim":"D","verdict":"ok"}` (findings opcional).
8. Si la información es insuficiente: veredicto `question` con `questions[]`
   (máx. 3, concisas, contestables sí/no o elección concreta).
9. Salida exclusivamente JSONL. Cero markdown fuera de los delimitadores.
10. **Prohibición explícita de herramientas**: el prompt ordena NO ejecutar
    comandos ni inspeccionar el repositorio. `opencode run` es un agente con
    herramientas: sin esta regla se pone a hacer builds/tests y se cuelga
    hasta el timeout en vez de responder.

## 8. Resiliencia sin tokens

| Situación | Comportamiento |
|---|---|
| Agente no disponible / rate-limit / timeout | `unavailable` + razón, no bloquea, no reintenta infinito |
| `slice` | Usa mensajes deterministas existentes (no necesita IA) |
| `check` | Sin IA, puro Git |
| `pr review` | `--only-unaudited` salta commits ya auditados |
| Sin `test_commands` configurados | Aviso + elección: configurar / omitir / delegar en el agente (opt-in) |
| Delegación de pruebas al agente | Contrato `tested` obligatorio; timeout reusado; fallo → aviso, no bloquea |
| Sin CI detectado | Aviso de que el PR no tendrá verificación automática; el template lo declara |
| Fase 0 del roadmap | Solo infraestructura; los comandos llegan en fases 1–2 |
| Opcional v2 | `token_budget` para limitar consumo por sesión |

## 9. Exit codes

| Código | Significado |
|---|---|
| `0` | OK o warnings (veredicto global warn) |
| `1` | Block (CRITICAL sin superar) o `--gate` con hallazgos CRITICAL |
| `3` | `questions_pending` (el agente pidió aclaraciones) |
| `4` | `provider_unavailable` (timeout, rate-limit, auth) |

## 10. Modelo, esfuerzo y perfiles

No todas las auditorías merecen el mismo modelo ni el mismo presupuesto de
razonamiento. La configuración distingue cuatro piezas:

| Pieza | Qué es | Ejemplo |
|---|---|---|
| Agente (binario) | El programa que ejecuta el trabajo | `claude`, `opencode` |
| Modelo | El cerebro dentro del agente | `deepseek-v4-flash-free`, `claude-3-5-sonnet` |
| Esfuerzo | Cuánto razona el modelo | deepseek: `default`/`high`/`max`; claude: `high` |
| Perfil | Receta con nombre: agente + modelo + esfuerzo | `cheap`, `normal`, `deep` |

**Relación**: el agente es el contenedor, el modelo piensa dentro de él y el
esfuerzo decide cuánto piensa. Un mismo agente puede servir varios modelos
según su configuración local (p. ej. opencode lanza deepseek o claude según
el proveedor activo); el modelo del perfil se inyecta por variables de entorno
(`CLAUDE_CODE_MODEL` / `OPENCODE_MODEL` + su variante de razonamiento).

**Valores de esfuerzo por proveedor (importante)**: los valores NO son
universales. opencode valida el esfuerzo según el proveedor del modelo: para
deepseek los valores son `default`/`high`/`max` (un `low` se ignora o falla
silenciosamente); para claude, `high` (el `max` no es válido). Un modelo
inexistente en `OPENCODE_MODEL` (p. ej. `deepseek-v4-flash` en lugar del real
`deepseek-v4-flash-free`) se ignora silenciosamente y opencode usa el modelo
del agente por defecto.

**Shims npm en Windows**: si el agente se instala como shim `.cmd`
(global npm), `exec.Command` lo ejecuta vía `cmd.exe`, que rompe el quoting
de prompts largos (auditorías con diff). El adaptador resuelve el `.exe` real
buscando la línea `%dp0%\node_modules\...\bin\X.exe` del shim
(`resolverBinarioReal` en `internal/agentadapter/factory.go`).

**Decisión en runtime para cada dimensión**:

1. ¿Qué binario? El del perfil asignado a la dimensión; si el perfil no lo
   define, el de `active_agent` (`auto` = primer agente disponible en el PATH).
2. ¿Qué modelo/esfuerzo? Los del perfil; si el perfil no los define, los de
   `agents[binario]`.

```yaml
active_agent: opencode
agents:
  claude:   { model: claude-3-5-sonnet,  reasoning_effort: high }
  opencode: { model: deepseek-v4-flash-free,  reasoning_effort: max }
profiles:
  cheap:  { model: deepseek-v4-flash-free,  reasoning_effort: default }
  normal: {}                                  # receta vacía = configuración base
  deep:   { model: claude-3-5-sonnet,  reasoning_effort: high }
review:
  dims:
    spec: cheap
    style: cheap
    tests: normal
    logic: normal
    design: deep
    security: deep
```

**Criterio de asignación**: `style` y `spec` (comparar el diff contra el
mensaje) son baratos; `tests` y `logic` medios; `design` y `security` son los
más caros y los que más se benefician de un modelo fuerte. El beneficio
principal es de coste, no de calidad: un perfil no transforma un modelo débil.

**Override por invocación**: `sentinel review --profile deep` fuerza un perfil
sobre el mapa de dimensiones (p. ej. para un commit muy grande en una
dimensión nominalmente barata). El perfil usado queda registrado en la ficha
(campo `model`) para trazabilidad.

Este documento fija el modelo conceptual (las 4 piezas, la jerarquía y la
decisión en runtime); la sintaxis exacta del YAML y los valores por defecto
los decide el implementador en la fase 1.

## 11. Semáforo y concurrencia

- Auditorías de dimensiones en paralelo, default 2 (semáforo).
- Timeout obligatorio por llamada (`context.WithTimeout` en `cli.go`), default
  300 s (subido de 120 s tras verificar que opencode con diff real tarda
  ~2–3 min), configurable en `vassentinel.yml` (`review.timeout`).
- El agente automático (`CLIAdapter`) es one-shot y no interactivo; el timeout
  evita cuelgues de procesos que esperan entrada.

## 12. Comandos PR

### 12.1 Pipeline compartido: `analizarRama()`

`pr review` y `pr create` comparten UNA función `analizarRama()`. `pr create` NO
lanza `pr review` como subproceso: usa la misma función y añade render +
publicación.

1. Base de comparación (default `main`): `merge-base` + `SHAsRango`.
2. Audita los SHAs pendientes con el motor de fase 1. El ledger es caché, no
   autoridad: no se re-audita lo ya fichado salvo `--only-unaudited`.
3. `--overview` (opcional): eje Spec a nivel de rama, 1 llamada (no N) — ¿el
   conjunto de commits forma un cambio coherente con el título de la PR?
4. Decide PR única vs `--chain-pr` por DOS ejes (no por contar commits):
   - **Volumen real** de la rama: líneas totales del diff (numstat).
   - **Coherencia** (`--overview`): una historia → PR única; unidades
     independientes con costuras → stacked.
5. Devuelve: SHAs + fichas + veredictos + riesgos + decisión single/chain.

`pr review` = cara de análisis (dry-run, no publica). `pr create` = la misma
función + plantilla + publicación.

### 12.2 `sentinel pr review`

- Analiza la rama entera contra la base (default `main`).
- `--overview`: eje Spec a nivel de rama (1 llamada, no N).
- `--only-unaudited`: audita solo SHAs sin ficha.
- Salida: matriz commit × dimensión + resumen de veredictos + riesgos.
- **No publica nada**.

### 12.3 Verificación: config determinista + aviso de CI + delegación

Tres comandos configurables en `vassentinel.yml` (misma precedencia que el
resto: per-proyecto `.vas_sentinel/vassentinel.yml` > global
`~/.vas_sentinel/vassentinel.yml`):

```yaml
lint_commands: ["go vet ./..."]
test_commands: ["go test ./..."]
build_commands: ["go build ./..."]
```

- **Vía 1 (determinista)**: si hay comandos configurados, Sentinel los ejecuta
  y el template reporta exit codes reales por comando.
- **Aviso de CI ausente**: si no se detecta CI (`.github/workflows/`,
  `.gitlab-ci.yml`, etc.), se avisa que el PR no tendrá verificación automática.
- **Vía 2 (delegación, opt-in)**: si NO hay `test_commands` configurados, al
  arrancar la PR se ofrece: (a) parar y configurar, (b) continuar sin ejecutar
  tests, (c) delegar en el agente: con shell libre, descubre las pruebas
  (Makefile, go.mod, convenciones), las lanza y devuelve el contrato
  `tested: [...]` (campo obligatorio: qué ejecutó exactamente).
- **Regla de oro del template**: nunca un PASS inventado. O exit codes de
  ejecución real, o `tested` del agente, o la línea explícita "tests no
  ejecutados".

La delegación usa un prompt DISTINTO al de auditoría: la auditoría prohíbe
herramientas (sin esa regla el agente se cuelga haciendo builds/tests); la
delegación de pruebas necesita shell libre y es un paso dedicado DESPUÉS de la
revisión — la revisión nunca corre tests.

### 12.4 `sentinel pr create`

- Genera `sentinel_pr.md` (plantilla adaptada de no-mistakes):
  - Línea de riesgo: 🚨 (block sin superar) / ⚠️ (warnings) / ✅ (todo ok).
    Veredicto de auditoría, NO estado de CI.
  - Rationale en 3–5 líneas (del `--overview`).
  - Matriz commit × dimensión (todos los SHAs de la rama).
  - Sección de verificación honesta: comandos ejecutados con exit codes, o
    `tested` del agente, o "CI no configurado — tests no ejecutados".
  - Hallazgos con emojis por severidad (🔴 CRITICAL, 🟡 WARNING, 🔵 ADVISORY).
  - Firma: `Generated by VAS Sentinel` + versión.
  - Límite de tamaño de body con truncamiento marcado.
- **Gate**: audita lo pendiente automáticamente; `block` sin superar → no
  publica (exit 1, lista los bloqueantes). El usuario resuelve o fuerza.
  Las fichas corregidas (`fixed_in` en el ledger) NO bloquean: su block fue
  resuelto en un commit posterior, y el gate solo mira las pendientes.
- Publica con `gh pr create --draft -F sentinel_pr.md`; si `gh` no está,
  fallback a archivo + portapapeles multiplataforma (`clip` / `wl-copy` /
  `xclip`). Si el usuario da `--base`, ese MISMO valor se propaga a
  `gh pr create --base`: la revisión y la PR no pueden targetear ramas
  distintas en silencio.
- **PR descomunal**: decisión por volumen + coherencia (12.1) → propone
  `--chain-pr` en lugar de una PR gigante.

## 13. Eventos y estado

- `events.jsonl` (append-only, `O_APPEND`, una línea por operación):

```json
{"at":"2026-01-01T10:00:00Z","cmd":"review","exit":4,"shas":["a1b2c3d4"],"detail":"provider_unavailable","worktree":"C:\\repo"}
```

- `sentinel status`: read-only; pendiente vs HEAD, SHAs auditados, huérfanos,
  últimos eventos. `--json` para orquestadores.
- Sin log de prompts/respuestas IA: las fichas ya son registro.
- El ledger puede mentir si la IA aprueba mal: `status` muestra modelo y
  revisión para trazabilidad.

### Eventos del flujo PR

El flujo PR registra en el mismo `events.jsonl` (esquema idéntico; `detail` es
string con JSON serializado):

- `pr-review`: análisis de rama — `base`, `rama`, `auditadas`, `nuevas`,
  `volumen`, `ci: bool`, `chain_pr: bool`, decisión de CI
  (`configurar | delegar | omitir`).
- `pr-verify`: verificación — por comando configurado `{cmd, exit}`; o
  delegación `tested: [...]`; o `motivo: no_configurado`.
- `pr-create`: acta de publicación — `pr_url`, `fallback`, `chain_pr`.

**Ciclo de vida de las actas**: el evento `pr-create` SOBREVIVE a la purga por
SHA (rebases/amend no lo borran). Se purga por ciclo de vida de la PR: en la
creación de una PR siguiente, el pre-flight consulta el estado de las PRs de
eventos `pr-create` previos (`gh pr view <nº> --json state`) y purga las
`MERGED` / `CLOSED`; `OPEN`/`DRAFT` se conservan. Best-effort: sin `gh` o sin
red, se conserva con aviso y no se bloquea la PR. Las PRs por fallback (sin
URL) no son verificables: su acta se conserva.

## 14. Riesgos declarados

1. La calidad del veredicto = calidad del modelo; un modelo débil puede
   aprobar defectos. Mitigación: perfil configurable por dimensión (sección 10),
   revisión visible en `status`.
2. El ledger es local: dos máquinas no comparten fichas. Mitigación:
   `pr review` re-audita lo que falte; el ledger es caché, no autoridad.
3. La revisión local no ve problemas globales (arquitectura entre commits).
   Mitigación: `--overview` (eje Spec a nivel de rama).
4. Re-auditar no es gratuito: `revisions[]` crece. Mitigación: solo crece con
   acciones explícitas; `pr review` usa `--only-unaudited`.

## 15. Roadmap de fases

### Fase 0 — Infraestructura (este documento + código base)

- `internal/git/gitdir.go`: `ObtenerGitDir()` (`git rev-parse --git-dir`).
- `internal/review/finding.go`: `ReviewFinding`, `DimensionResult`, parseo
  JSONL tolerante, validación estricta de dimensiones.
- `internal/review/ledger.go`: ficha por SHA, `revisions[]`, escritura atómica,
  huérfanos.
- `internal/ops/events.go`: `events.jsonl` append-only.
- Timeout con `context` en `internal/agentadapter/cli.go`.
- Tests: fixtures de salidas reales, atomicidad, appends, timeout.

### Fase 1 — Motor de auditoría

- `internal/review/engine.go`: semáforo, `--chain`, gates, degradación,
  `--answer`, exit codes.
- `internal/review/prompts.go`: plantillas dimensión × saco.
- Perfiles de agente/esfuerzo (sección 10): `profiles` + `review.dims` en
  `vassentinel.yml`, resolución binario → modelo → esfuerzo, override
  `--profile`.
- Subcomandos `rebase`, `lint`, `review`, `status`.

### Fase 2 — Renderizado y PR

- `internal/review/renderer.go`: matriz commit × dimensión en markdown y
  plantilla `sentinel_pr.md` (línea de riesgo, rationale, verificación honesta,
  emojis por severidad, firma + versión, truncamiento marcado).
- `analizarRama()` compartido (12.1): `pr review` (dry-run, no publica) y
  `pr create` (gate de block, publica con `gh` o fallback portapapeles).
- Decisión single vs `--chain-pr` por volumen real (numstat) + coherencia
  (`--overview`, Spec de rama en 1 llamada).
- Verificación dual (12.3): `lint_commands` / `test_commands` /
  `build_commands` en el yml, aviso de CI ausente, delegación opt-in al agente
  (contrato `tested`).
- Eventos PR (`pr-review`, `pr-verify`, `pr-create`) + purga de actas por PR
  mergeada/cerrada (13).
- Plan de ejecución: `docs/plan-fase-2.md`.

Cada fase ≤400 líneas de cambio; `sentinel check` entre fases.
