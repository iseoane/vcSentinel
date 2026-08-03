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

## 8. Resiliencia sin tokens

| Situación | Comportamiento |
|---|---|
| Agente no disponible / rate-limit / timeout | `unavailable` + razón, no bloquea, no reintenta infinito |
| `slice` | Usa mensajes deterministas existentes (no necesita IA) |
| `check` | Sin IA, puro Git |
| `pr review` | `--only-unaudited` salta commits ya auditados |
| Fase 0 del roadmap | Solo infraestructura; los comandos llegan en fases 1–2 |
| Opcional v2 | `token_budget` para limitar consumo por sesión |

## 9. Exit codes

| Código | Significado |
|---|---|
| `0` | OK o warnings (veredicto global warn) |
| `1` | Block (CRITICAL sin superar) o `--gate` con hallazgos CRITICAL |
| `3` | `questions_pending` (el agente pidió aclaraciones) |
| `4` | `provider_unavailable` (timeout, rate-limit, auth) |

## 10. Semáforo y concurrencia

- Auditorías de dimensiones en paralelo, default 2 (semáforo).
- Timeout obligatorio por llamada (`context.WithTimeout` en `cli.go`), default
  120 s, configurable en `vassentinel.yml` (`review.timeout`).
- El agente automático (`CLIAdapter`) es one-shot y no interactivo; el timeout
  evita cuelgues de procesos que esperan entrada.

## 11. Comandos PR

### 11.1 `sentinel pr review`

- Analiza la rama entera contra la base (default `main`).
- `--overview`: eje Spec a nivel de rama (1 llamada, no N): ¿el conjunto de
  commits forma un cambio coherente con el título de la PR?
- `--only-unaudited`: audita solo SHAs sin ficha.
- Salida: matriz commit × dimensión + resumen de veredictos + riesgos.
- **No publica nada**.

### 11.2 `sentinel pr create`

- Genera `sentinel_pr.md` (plantilla adaptada de no-mistakes):
  - Línea de riesgo: 🚨 (block sin superar) / ⚠️ (warnings) / ✅ (todo ok).
  - Rationale en 3–5 líneas.
  - Matriz commit × dimensión (todos los SHAs de la rama).
  - Hallazgos con emojis por severidad (🔴 CRITICAL, 🟡 WARNING, 🔵 ADVISORY).
  - Firma: `Generated by VAS Sentinel` + versión.
  - Límite de tamaño de body con truncamiento marcado.
- Publica con `gh pr create --draft -F sentinel_pr.md`; si `gh` no está,
  fallback a archivo + portapapeles multiplataforma (`clip` / `wl-copy` /
  `xclip`).
- **PR descomunal**: umbral de 8 unidades de trabajo (commits) → propone PRs
  encadenadas (`--chain-pr`) en lugar de una PR gigante.

## 12. Eventos y estado

- `events.jsonl` (append-only, `O_APPEND`, una línea por operación):

```json
{"at":"2026-01-01T10:00:00Z","cmd":"review","exit":4,"shas":["a1b2c3d4"],"detail":"provider_unavailable","worktree":"C:\\repo"}
```

- `sentinel status`: read-only; pendiente vs HEAD, SHAs auditados, huérfanos,
  últimos eventos. `--json` para orquestadores.
- Sin log de prompts/respuestas IA: las fichas ya son registro.
- El ledger puede mentir si la IA aprueba mal: `status` muestra modelo y
  revisión para trazabilidad.

## 13. Riesgos declarados

1. La calidad del veredicto = calidad del modelo; un modelo débil puede
   aprobar defectos. Mitigación: modelo configurable, revisión visible.
2. El ledger es local: dos máquinas no comparten fichas. Mitigación:
   `pr review` re-audita lo que falte; el ledger es caché, no autoridad.
3. La revisión local no ve problemas globales (arquitectura entre commits).
   Mitigación: `--overview` (eje Spec a nivel de rama).
4. Re-auditar no es gratuito: `revisions[]` crece. Mitigación: solo crece con
   acciones explícitas; `pr review` usa `--only-unaudited`.

## 14. Roadmap de fases

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
- Subcomandos `rebase`, `lint`, `review`, `status`.

### Fase 2 — Renderizado y PR

- `internal/review/renderer.go`: matriz commit × dimensión, markdown.
- `pr review` / `pr create`, plantilla, `--overview`, `--chain-pr`.

Cada fase ≤400 líneas de cambio; `sentinel check` entre fases.
