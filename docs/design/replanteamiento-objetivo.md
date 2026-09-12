# VAS Sentinel — Architectural Rethinking (legacy)

> **Legacy document.** This was the project's original architectural rethinking
> report, written in Spanish. It is kept for history: do not treat sections
> 1–2 and 4–26 as current descriptions of the codebase — the authoritative,
> current record of open work and decisions lives in [`docs/issues/`](../issues/)
> (`actionable.md`, `future.md`, `parked.md`, `decisions.md`).
>
> **Translated in place into English:** section 3 (architectural problems),
> section 27 (migration plan), section 28 (decisions taken), section 29 (what
> is deliberately not built), section 30 (risks), section 31 (four additional
> design decisions), and section 32 (conclusion). The remaining sections are
> preserved verbatim in Spanish as legacy history.

**Summary.** Analysis of the existing application and a target-architecture
proposal toward **Change Intelligence + Deterministic Validation +
Incremental AI Code Review + Quality Gate**. The document **implements
nothing**. It is diagnosis, architectural decision, and an evolution plan.
Every claim about the current state carries a `file:line` reference.

Sources inspected: the module's 68 `.go` files, `docs/guia-implantacion-revision.md`
(agreed design, 6 iterations), `docs/plan-fase-2.md`, `docs/auditoria/*`
(own audit in progress), `.vas_sentinel/vassentinel.yml`, `release.yml`.

---

## 1. Arquitectura actual

### 1.1 Lo que realmente existe

No es un guardián de volumen con un review pegado: **ya hay un sistema de
revisión de tres capas**, con motor, ledger, eventos, análisis de rama,
verificación y publicación de PR. El `CLAUDE.md` del proyecto describe una
versión anterior y más pequeña de la aplicación.

```
cmd/sentinel/                  entrypoint + TODA la orquestación
  main.go              949 l   dispatcher, init/uninit, check, slice, diálogos
  comandos_pr.go       570+ l  pr review / pr create / passthrough gh
  comandos_estado.go   ~370 l  flags compartidos, lint, rebase, status, purga
  comandos_review.go   ~310 l  review, resolución de SHAs, exit codes, fixed_in
internal/
  git/        diff, slice, plan, commit, mergebase, gitdir, ci, root
  config/     parser YAML propio, perfiles agente×modelo×esfuerzo
  agentadapter/ CLIAdapter (claude/opencode), cadena con fallback, factory
  review/     engine, finding, prompts, ledger, rama, renderer
  ops/        events.jsonl, verificación dual
  setup/      install / upgrade / uninstall / token GitHub
tools/release/
```

Sin dependencias externas (`go.mod` sin `require`). Go 1.26.

### 1.2 Los tres subsistemas

**A. Guardián de volumen** (`check` / `slice`)

- `CheckDiffLimits` (`internal/git/diff.go:10`) suma líneas de
  `ObtenerArchivosModificados` (rastreados vía numstat + no rastreados vía
  `status --porcelain -z -uall`). Clasifica `PEQUENO` / `PUNTO_OPTIMO` (200–400)
  / `CRITICO` (>400, exit 1).
- `slice` construye un plan por capas fijas `config → backend → frontend → test`
  (`internal/git/plan.go:44`), lotes ≤400 líneas, mensajes generados por el
  agente con fallback determinista, aprobación A/R/E/C y commits `--no-verify`
  (`plan.go:170`).
- La capa (`ClasificarCapa`, `internal/git/slice.go:214`) se deduce por
  subcadena de ruta y extensión.

**B. Motor de auditoría** (`review`, fase 1 de la guía)

- Seis dimensiones canónicas: `logic`, `style`, `design`, `tests`, `security`,
  `spec` (`internal/review/finding.go:212`).
- Matriz estática capa → dimensiones (`internal/review/engine.go:51`); `spec`
  siempre se añade.
- Una llamada al agente por dimensión, semáforo `parallel` (default 2),
  timeout 300 s (`internal/agentadapter/cli.go:966`).
- Prompt maestro por dimensión con el diff completo del commit y **prohibición
  explícita de herramientas** (`internal/review/prompts.go:555`).
- Contrato JSONL entre `BEGIN_REVIEW`/`END_REVIEW`, parseo tolerante con
  fallback a objeto multilínea y normalización de veredictos
  (`finding.go:328`, `finding.go:459`).
- Ledger: una ficha JSON por SHA en `<git-dir>/vas-sentinel/<sha>.json`,
  `revisions[]` append-only, escritura atómica, purga de huérfanas
  (`internal/review/ledger.go`).
- Perfiles agente×modelo×esfuerzo por dimensión (`internal/config/perfil.go:44`),
  inyectados por variables de entorno (`cli.go:1022`).

**C. Flujo PR** (fase 2)

- `AnalizarRama` (`internal/review/rama.go:625`): merge-base contra la base
  (default `main`), audita los SHAs sin ficha, mide volumen con numstat,
  `--overview` opcional (1 llamada de coherencia) y decide `single`/`chain` por
  volumen + coherencia.
- Verificación dual (`internal/ops/verificar.go:759`): ejecuta
  `lint_commands`/`test_commands`/`build_commands` del yml; si no hay ninguno,
  detecta CI y ofrece configurar / omitir / delegar en el agente con contrato
  `tested`.
- `pr create`: gate de `block`, plantilla markdown honesta, `gh pr create
  --draft`, fallback a portapapeles.
- `events.jsonl` append-only con `review`, `fix`, `pr-review`, `pr-verify`,
  `pr-create`.

### 1.3 Flujo real de datos

```
git diff/show ──► diff textual completo del commit
                          │
                          ▼
          matriz estática capa → dimensiones (por subcadena de ruta)
                          │
                          ▼
          N procesos CLI (claude -p / opencode run), 1 por dimensión
          contexto = SOLO el diff; herramientas PROHIBIDAS
                          │
                          ▼
          parseo JSONL tolerante ──► DimensionResult
                          │
                          ▼
          veredictoGlobal (block > question > unavailable > warn > ok)
                          │
                          ▼
          ficha por SHA ──► gate de PR ──► plantilla ──► gh
```

La verificación determinista **no participa en este flujo**: se ejecuta al final,
en `pr create`, y su único efecto es decorar la plantilla
(`cmd/sentinel/comandos_pr.go:546-548`).

---

## 2. Diagnóstico

### 2.1 Lo que está bien y hay que conservar

Esto no es un prototipo. Hay decisiones maduras que la arquitectura objetivo
debe heredar sin discusión:

1. **Estado en el common-dir de Git.** Nunca se commitea, aislamiento por
   worktree gratis, ciclo de vida ligado al repo, cero configuración
   (guía §4). Es la decisión correcta y sobrevive intacta.
2. **Ledger append-only con escritura atómica.** `revisions[]` nunca pisa,
   temp+rename con el caso Windows resuelto (`ledger.go:1038`). Es ya la base
   de un Review Store.
3. **Degradación tipada, nunca un PASS inventado.** `unavailable` con razón,
   `ErrVeredictoInvalido` sin hallazgos aborta en vez de aprobar
   (`finding.go:360`). La "regla de oro" de la plantilla es exactamente el
   principio correcto y está implementada.
4. **Separación plan/ejecución en `slice`.** El plan se construye sin tocar
   git; `ConstruirPlanFragmentacion` es puro salvo un callback
   (`plan.go:44`). Es el patrón que el Planner objetivo debe replicar.
5. **Costuras de inyección en todo el código nuevo**: `FabricaAuditor`,
   `OpcionesVerificar.Ejecutar/Preguntar`, `opcionesPublicarPR`,
   `lectorStdin`. Permiten test sin red ni stdin real.
6. **Trazabilidad `fixed_in`** hallazgo → corrección (`ledger.go:989`). Es el
   embrión del ciclo remediation → re-review.
7. **Auditoría propia con regla de evidencia** (`docs/auditoria/README.md`):
   un veredicto sin `file:line` es DUDA, no VERIFICADO. Este rigor es un activo
   del proyecto, no del código.
8. **Cero dependencias externas y multiplataforma real** (rutas con
   `filepath`, `-z` en git, hooks `#!/bin/sh`, quoting en Windows).

### 2.2 Lo que está mal — el diagnóstico de fondo

**El sistema confía en la fuente menos fiable y desconfía de la más fiable.**

Un `CRITICAL` semántico de un LLM **bloquea** la publicación
(`comandos_pr.go:528-536`). Un `go test ./...` en rojo **no bloquea nada**:
su exit code se imprime en la plantilla y el PR sale igual
(`verificar.go:783-791`, `comandos_pr.go:546`). La inversión es total.

Y no es teórica. La propia auditoría del proyecto la documenta como hallazgo
confirmado por ejecución:

> **H5** · El motor audita el diff del commit aislado […] Produce hallazgos
> CRITICAL falsos: denuncia defectos ya corregidos en commits posteriores y
> deduce la inexistencia de símbolos que sí existen. Un CRITICAL falso bloquea
> el gate de `pr review`.
> — `docs/auditoria/README.md`, con cuatro falsos positivos verificados

La causa raíz es arquitectónica, no de prompt: el prompt **prohíbe mirar el
repositorio** (`prompts.go:555`) para evitar que `opencode run` se cuelgue
lanzando builds. Se resolvió un problema de infraestructura amputando el
contexto del revisor. El revisor razona sobre un diff descontextualizado y
alucina lo que no puede ver — y su alucinación tiene poder de veto sobre un
`go test` verde.

Todo lo demás del diagnóstico se ordena alrededor de esto.

---

## 3. Architectural problems

### CRITICAL

**C1 · Semantic review is the gate; deterministic validation is not.**
`comandos_pr.go:528` blocks on audit verdict; `verificar.go` only reports.
Consequence: LLM false positives block correct PRs, and code that does not
compile can be published. It also spends the token budget reviewing code a
`go build` would have discarded in 2 seconds.

**C2 · The reviewer's context is an isolated diff and the repository is
forbidden.** `prompts.go:555`. Direct cause of H5. No amount of model, effort
or prompt tuning fixes the missing information: the reviewer cannot know
whether a symbol exists because it cannot look.

**C3 · Incrementality is anchored to the commit SHA.** The ledger is
`<sha>.json` (`ledger.go:926`) and `PurgarHuerfanas` deletes what stopped being
reachable (`ledger.go:1017`). Any `rebase`, `amend` or `squash` —the normal
flow of an agent branch, and the one `sentinel rebase` itself promotes—
invalidates 100 % of reviews even if the content did not change by one line.
Incrementality collapses precisely in the target scenario.

**C4 · There is no model of the change.** No AST, no symbol graph, no
dependency graph, no change profile, no risk model. Everything is decided by
two signals: line count and path substring. `LimiteDecisionChain = 400`
(`rama.go:587`) is literally the guardian's own threshold reused as the
architecture criterion for PRs.

**C5 · Layer classification is a false taxonomy that feeds real decisions.**
`ClasificarCapa` (`slice.go:214`) returns `test` for any path containing the
substring `test` — `latest/`, `contest.go`, `internal/testdata/` — and `config`
for any `.json`/`.yml`/`.lock`. That value decides **which dimensions are
audited** (`engine.go:51`) and **how commits are grouped** (`plan.go:44`).
Wrong input ⇒ wrong review plan, silently.

**C6 · Business logic lives in `package main`.** `main.go` 949 lines,
`comandos_pr.go` 570+. Orchestration, policy, IO and presentation mixed. It is
the project's own finding **H3** and the cause of **B8** (zero tests in the
interactive layer until the current fixes). There is no use-case layer to hang
Planner, Scheduler, Aggregator or Remediation on.

### IMPORTANT

**I1 · Broken traceability in the record (H4, confirmed).** With
`active_agent: auto` the record stores the profile name, not the agent that
answered (`comandos_review.go:113-116`). If the chain falls back from `claude`
to `opencode`, the verdict has no author. Without this, section 38 (reviewer
quality) is unexecutable: noise cannot be attributed to a model.

**I2 · The model is injected through non-contractual environment variables.**
`CLAUDE_CODE_MODEL`, `OPENCODE_MODEL` (`cli.go:1022-1028`). The guide itself
documents that a nonexistent model **is silently ignored** and opencode uses
another (§10). The entire cost/quality strategy by profiles can be degraded
with nothing signaling it.

**I3 · The aggregator does not aggregate.** `veredictoGlobal`
(`engine.go:156`) is a precedence of five cases. It does not deduplicate, does
not correlate, does not merge evidence. With six dimensions with overlapping
mandates (`design` and `logic` share "complexity"; `security` and `logic`
share "unvalidated input"), the same defect is reported N times.

**I4 · The finding contract is insufficient for a gate.** `ReviewFinding`
(`finding.go:286`) has `dimension, file, line, severity, description,
suggestion`. Missing: `source` (validation|review), `confidence`, line range,
`evidence` (the verbatim quote that proves it), `impact`, and `fixable`.
Without `evidence` there is no way to refute a false positive except reading
the code by hand; without `confidence` there is no way to grade the block.

**I5 · The YAML parser is artisanal.** `aplicarDesdeRuta`
(`config/parser.go:145`) walks indentation levels, silently ignores every
unknown key and does not support lists inside nested maps. The repository
policy the target architecture needs (capabilities, profiles, thresholds,
exceptions, per-path rules) is not expressible with this parser.

**I6 · No cost or latency observability.** The `Evento` (`ops/events.go`)
stores `at, cmd, exit, shas, detail, worktree`. No duration, no tokens, no
effective model, no override rate. The objective's sections 37 and 38 have no
data to operate on.

**I7 · `style` as an LLM dimension is redundant expense.** Formatting,
conventional naming and consistency are the competence of `gofmt` and
`golangci-lint`, which are exact, free and instant. A model is being paid to
answer what a linter already knows.

### IMPROVEMENT

**M1 · Stacked PR is aspirational.** `--chain-pr` only allows publishing a
branch the system itself discourages (`comandos_pr.go:540`). There is no notion
of parent branch nor of *diff ownership*: `AnalizarRama` always uses
`merge-base(base, HEAD)` with base `main` (`rama.go:626-637`), so reviewing B
on top of A drags A's commits into the matrix and the gate.

**M2 · Volume metrics with mixed semantics.** Tracked: added lines of the
numstat. Not tracked: the file's physical lines. Branch range: added + deleted
(`mergebase.go:19`). Three definitions of "volume" for the same concept. It is
the residue of **B5** in the guardian's report.

**M3 · `--force` without an exception record.** `comandos_pr.go:528` allows
skipping the gate without leaving a traceable decision of who and why.

**M4 · The event's `detail` is a string with JSON inside.** It prevents
querying without re-parsing.

### OPTIONAL

**O1 · Legacy passthrough of `pr`** (`comandos_pr.go:50`) coexists with the
native verbs; compatibility debt to retire with a deprecation notice.

**O2 · Duplicated thresholds** (`400` in `diff.go`, `slice.go`, `rama.go`) —
**B4** of the guardian's report, still open.

---

## 4. Arquitectura objetivo

### 4.1 Principio rector

> Lo que una herramienta puede decidir con certeza, lo decide la herramienta.
> El LLM solo responde lo que ninguna herramienta puede responder, con el
> contexto mínimo suficiente, y su respuesta **nunca bloquea sin corroboración**.

### 4.2 Diagrama

```
                              Git
                               │
                    ┌──────────▼──────────┐
                    │   Change Analyzer   │  Go puro, sin IA
                    │  ranges + blobs     │
                    └──────────┬──────────┘
                               │
                    ┌──────────▼──────────┐
                    │   Code Model        │  opcional por lenguaje
                    │  pkg graph → symbol │  degrada a file-level
                    │  graph → test map   │
                    └──────────┬──────────┘
                               │
                    ┌──────────▼──────────┐
                    │  Change Profile     │  determinista
                    │  Risk Profile       │  + Cohesion
                    └──────────┬──────────┘
                               │
                    ┌──────────▼──────────┐
                    │      Planner        │  política del repo
                    └──────────┬──────────┘
                               │
              ╔════════════════▼════════════════╗
              ║  1) VALIDATION ENGINE (bloquea) ║
              ║  fmt · lint · types · build ·   ║
              ║  tests afectados · security     ║
              ╚════════════════┬════════════════╝
                          falla │ ─────────► STOP (no se gasta 1 token)
                               │ pasa
                    ┌──────────▼──────────┐
                    │   Agent Scheduler   │  0..N agentes según riesgo
                    └──────────┬──────────┘
                    ┌──────────▼──────────┐
                    │  Context Builder    │  read-only, paths acotados
                    └──────────┬──────────┘
                    ┌──────────▼──────────┐
                    │  Semantic Review    │  findings con evidencia
                    └──────────┬──────────┘
                    ┌──────────▼──────────┐
                    │  Verifier (refuter) │  solo para CRITICAL
                    └──────────┬──────────┘
                    ┌──────────▼──────────┐
                    │     Aggregator      │  dedup · correlate · supersede
                    └──────────┬──────────┘
                 ┌─────────────┼─────────────┐
                 ▼             ▼             ▼
               PASS       REMEDIATION   NEEDS_USER_REVIEW
                               │
                               ▼  re-validation → re-analysis → re-review

   ┌──────────────────────────────────────────────────────────────┐
   │  REVIEW STORE  —  <git-common-dir>/vas-sentinel/             │
   │  claves por CONTENIDO (blob/tree OID), no por commit SHA     │
   │  changes · validations · findings · fixes · decisions · cost │
   └──────────────────────────────────────────────────────────────┘
```

### 4.3 Diferencias frente al diagrama propuesto en el enunciado

Tres desviaciones deliberadas, con justificación:

1. **La validación va ANTES del scheduler, no en paralelo.** En el diagrama
   original Validation Engine y Agent Scheduler cuelgan del mismo nodo y corren
   a la vez. Es más rápido en el caso feliz y más caro en el caso real: si el
   build falla, todos los tokens gastados en revisar son basura. Con `go build`
   en ~2 s y una revisión en ~2-3 min por dimensión (guía §11), secuenciar
   cuesta segundos y ahorra minutos y dinero. Excepción: en `pre-commit`, donde
   no hay revisión semántica, es irrelevante.

2. **Aparece un Verifier entre Review y Aggregator.** No estaba en la propuesta.
   Lo justifica H5: con falsos positivos verificados, un `CRITICAL` semántico no
   puede bloquear sin corroboración. Coste: 1 llamada barata por finding
   bloqueante (no por finding). Alternativa si no se acepta el coste: que la
   revisión semántica nunca supere `WARN`.

3. **El Code Model es opcional y degradable, no un prerequisito.** El diagrama
   original lo pone en la ruta crítica. Aquí, si no hay proveedor para el
   lenguaje o el grafo está incompleto, el sistema cae a análisis a nivel de
   archivo y a validación completa. Nunca a un falso "afectados = 3".

---

## 5. Comparación de alternativas de granularidad de agentes

Supuestos de coste: una llamada con diff real tarda 2–3 min (guía §11), el
paralelismo por defecto es 2 (`vassentinel.yml`), y las dimensiones tienen
mandatos solapados.

| Alternativa | Coste/commit | Latencia | Solapamiento | Precisión | Veredicto |
|---|---|---|---|---|---|
| **Actual: 6 dims por matriz de capa** | 2–4 llamadas típicas, 6 en commit mixto | 3 tandas de 2 | Alto (design/logic/security) | Baja: H5 confirmado | Insostenible |
| **A · 6 agentes fijos** | 6 siempre | 3 tandas | Alto | Sin mejora sobre el actual: el problema es el contexto, no el número | Rechazada |
| **B · 3 agentes (Correctness / Quality / Security)** | 3 | 2 tandas | Medio | Mejor que A por menos duplicados | Buena base, insuficiente sola |
| **C · 1 agente multidimensión** | 1 | 1 | Nulo | Se degrada con el tamaño del diff; pierde profundidad en cambios grandes | Correcta solo para cambios triviales |
| **D · Dynamic Scheduling** | 0–5 según riesgo | 1–3 | Bajo | La más alta a igualdad de gasto | **Elegida** |
| **E · Dynamic + Verifier** | D + 1 por bloqueante | D + 1 | Bajo | Única que ataca los falsos positivos | **Elegida (D con E integrado)** |

**Decisión: D + E.**

La dimensión se conserva como **taxonomía del finding** (barata, útil para
reportar y para métricas por dimensión). El **agente físico** lo decide el
Planner:

| Riesgo | Agentes semánticos | Bundle |
|---|---|---|
| `none` (docs, formato puro, generated) | 0 | solo validación |
| `low` | 0–1 | Correctness (logic+spec+tests) con esfuerzo bajo |
| `standard` | 1–2 | Correctness · Quality (design+style residual) |
| `elevated` | 3 | Correctness · Quality · Security |
| `high` | 3–5 | los 3 anteriores + Contracts/Compatibility + Concurrency/Data, según características detectadas |

Y sobre cualquier `CRITICAL` semántico: 1 refutador independiente cuyo prompt es
*intentar demostrar que el hallazgo es falso*, con acceso de lectura al
repositorio. Si refuta, el finding baja a `NEEDS_USER_REVIEW` y no bloquea.

Justificación del salto respecto a la propuesta: 6 agentes que ven poco producen
más ruido que 3 agentes que ven bien. El gasto marginal rinde más en *contexto y
verificación* que en *más lentes*.

---

## 6. AST / Change Graph — cómo construirlo y cuánto construir

### 6.1 Decisión: grafo escalonado, no grafo total

Un grafo de símbolos multilenguaje es un producto en sí mismo. Comprometerse con
él convierte una herramienta de 68 archivos en una plataforma. La decisión es
construirlo en tres niveles y **cobrar valor en cada uno**:

**Nivel 0 — Grafo de archivos (sin AST).** Directorios, extensiones, reglas de
ruta configurables, y el grafo de co-cambio histórico (`git log --name-only`:
qué archivos cambian juntos). Sustituye ya a `ClasificarCapa` y resuelve **C5**.
Coste: bajo. Valor: cohesión, agrupación de commits, perfil de cambio.

**Nivel 1 — Grafo de paquetes y mapa de tests (Go nativo).**
`golang.org/x/tools/go/packages` da imports, paquetes y ficheros `_test.go` sin
escribir un parser. Con eso se calcula el cierre inverso de imports:
`paquete cambiado → paquetes que lo importan → tests de esos paquetes`.
Coste: una dependencia y ~1 fase. Valor: **validación incremental segura**, que
es el mayor ahorro de tiempo del sistema.

**Nivel 2 — Símbolos (solo donde paga).** `go/ast` sobre los ficheros cambiados
para extraer los símbolos exportados tocados y sus *callers* directos. No un
grafo global persistente: una consulta acotada al conjunto cambiado. Valor:
selección de contexto para el revisor y señal de riesgo (`public_api`).

Nivel 3 (grafo global persistente, multilenguaje, con invalidación incremental)
**no se construye** salvo que las métricas lo exijan. Coste alto, invalidación
difícil, y el nivel 2 ya cubre la selección de contexto.

### 6.2 Otros lenguajes

Interfaz `LanguageProvider` con `Packages()`, `Imports()`, `TestsFor()`,
`SymbolsIn()`. Implementación Go primero. TypeScript/Python después vía
Tree-sitter **como módulo opcional**. Sin proveedor: nivel 0 + validación
completa siempre. Nunca se infiere un conjunto de afectados sin proveedor.

### 6.3 Almacenamiento e invalidación

- **No se persiste un grafo global.** Se calcula por ejecución y se cachea por
  `tree OID` del árbol de fuentes en `<git-common-dir>/vas-sentinel/graph/`.
  Un árbol idéntico reutiliza el grafo; cualquier cambio recalcula. La
  invalidación deja de ser un problema: la clave *es* el contenido.
- Coste de recálculo en Go para un repo de este tamaño: sub-segundo. Para
  monorepos, cache por paquete con clave = OID del directorio del paquete.

### 6.4 Casos que rompen el análisis

| Caso | Comportamiento obligatorio |
|---|---|
| Código generado (`//go:generate`, `.pb.go`, `_gen.go`, lock files) | Se marca `generated`; riesgo bajo; excluido de revisión semántica; validación completa igual |
| Reflexión, `plugin`, `go:linkname`, DI por strings | El grafo se marca `incomplete` → **validación completa obligatoria** |
| Lenguajes dinámicos sin proveedor | Nivel 0 + validación completa |
| Cambio en `go.mod`, `Makefile`, CI, config de build | Fuerza validación completa siempre, sin excepción |
| Fichero que no parsea | El cambio ya está roto: la validación lo detectará; el grafo se marca incompleto |

**Regla dura:** si el grafo no es completo y verificable para *todos* los
archivos cambiados, el conjunto de validaciones es el completo. La optimización
de tiempo nunca degrada la confianza del gate.

---

## 7. Validation Engine

### 7.1 Modelo

Tres conceptos, en este orden:

- **Capability** — qué se comprueba (`format`, `lint`, `typecheck`,
  `unit_test`, `integration_test`, `e2e`, `build`, `static_analysis`,
  `dependency`, `security`, `custom:<nombre>`). Vocabulario cerrado + prefijo
  `custom:` abierto.
- **Provider** — cómo se ejecuta esa capability en *este* repo: comando,
  directorio, timeout, parser del resultado, si acepta selección de destino.
- **Profile** — qué capabilities se ejecutan en qué momento y con qué alcance.

```yaml
validation:
  capabilities:
    format:
      command: "gofmt -l ."
      fails_when: output_not_empty      # el comando sale 0 aunque haya trabajo
    lint:
      command: "go vet ./..."
      supports_scope: true              # acepta paquetes concretos
      scoped_command: "go vet {packages}"
    build:
      command: "go build ./..."
    unit_test:
      command: "go test ./..."
      supports_scope: true
      scoped_command: "go test {packages}"
      timeout: 300
  profiles:
    fast:     [format, lint]
    standard: [format, lint, build, unit_test]
    full:     [format, lint, build, unit_test, static_analysis, security, dependency]
```

`supports_scope` es la pieza que hace posible la validación incremental sin
adivinar: si un provider no declara cómo acotarse, **no se acota**.

### 7.2 Qué se ejecuta y cuándo

| Momento | Perfil base | Alcance |
|---|---|---|
| `pre-commit` | `fast` | acotado si el grafo es completo; si no, completo |
| `pre-push` | `standard` | acotado con fallback; `full` si el riesgo es `high` |
| `pr` | `full` | completo siempre |
| tras remediation | el mismo perfil que falló | acotado a lo tocado + su cierre inverso |

El Planner puede **elevar** el perfil por características del cambio
(`security_sensitive` → añade `security`; `database` → `integration_test`;
`public_api` → `dependency`), nunca reducirlo por debajo del mínimo del momento.

### 7.3 Contrato de resultado

Cada ejecución produce un `ValidationRun`:

```json
{
  "capability": "unit_test",
  "command": "go test ./internal/git/...",
  "scope": "partial",
  "scope_reason": "affected_packages=3 graph=complete",
  "exit": 1,
  "duration_ms": 4211,
  "findings": [ { "source": "validation", "severity": "CRITICAL", "...": "..." } ]
}
```

`scope: partial` sin `graph=complete` es un estado imposible por construcción:
el motor rechaza acotar sin grafo completo.

### 7.4 Ganancia inmediata sobre el código actual

`ops.Verificar` (`verificar.go:759`) ya ejecuta la lista de comandos y ya
distingue determinista/delegado/omitido con motivos tipados. El trabajo no es
reescribirlo: es (a) darle el modelo de capabilities, (b) darle alcance, y
(c) **conectarlo al gate antes que la revisión**.

---

## 8. Change Profile

Se calcula **sin IA**, a partir de Git + reglas de ruta + grafo (si existe).

```json
{
  "unit_id": "sha256:...",
  "base": "a1b2c3d",
  "head": "e4f5a6b",
  "kind": "feature",
  "size": { "files": 7, "added": 214, "deleted": 38, "hunks": 19 },
  "symbols": { "added": 4, "modified": 9, "deleted": 1, "exported_touched": 3 },
  "modules": ["internal/review", "internal/ops"],
  "characteristics": [
    "public_api", "behavior_change", "test_covered", "cross_module"
  ],
  "file_classes": {
    "source": 4, "test": 2, "config": 1,
    "generated": 0, "docs": 0, "infra": 0
  },
  "cohesion": { "clusters": 2, "score": 0.62, "suggested_split": true },
  "graph": { "provider": "go", "complete": true }
}
```

**`kind`** (uno solo, por precedencia determinista):
`generated` > `dependency` > `infra` > `ci_cd` > `configuration` > `documentation`
> `test_only` > `refactor` > `bugfix` > `feature`.
`refactor` se detecta por firma: símbolos movidos/renombrados sin cambio de
cuerpo (comparación de AST normalizado o, sin AST, por similitud de bloques).
`bugfix` por convención de mensaje **corroborada** por tocar código no nuevo.

**`characteristics`** (multi-etiqueta, cada una con su detector determinista):

| Característica | Detector |
|---|---|
| `public_api` | símbolo exportado tocado, o fichero en rutas declaradas API en la política |
| `database` | rutas de migración, ficheros `*.sql`, paquetes declarados data en la política |
| `security_sensitive` | rutas/paquetes marcados en política + identificadores (`auth`, `token`, `crypto`, `password`, `secret`) en el diff |
| `concurrency` | aparición de `go `, `sync.`, `chan `, `context.` en las líneas añadidas |
| `performance_sensitive` | rutas marcadas en política; bucles anidados nuevos |
| `behavior_change` | hay cambio en código no-test y no-comentario |
| `test_covered` | los paquetes afectados tienen tests en el mapa de tests |
| `cross_module` | ≥2 módulos de primer nivel tocados |
| `generated_code` | marcadores de generación o `.gitattributes linguist-generated` |
| `ci_cd`, `infrastructure` | rutas de CI / IaC |

La lista no es cerrada: la política del repo puede añadir características por
regla de ruta o por patrón. Lo que **no** se admite es una característica sin
detector: nada se etiqueta "a ojo".

---

## 9. Risk Model

### 9.1 Separación de ejes

`size`, `risk`, `complexity` y `cohesion` son ejes independientes y se reportan
por separado. Hoy el sistema colapsa los cuatro en "líneas" y por eso una
regeneración de `go.sum` de 2000 líneas y un cambio de 200 líneas en
autenticación reciben el mismo trato.

### 9.2 Cálculo

`risk` no es un número mágico: es un **máximo sobre reglas**, no una suma
ponderada. Las sumas ponderadas se calibran a ciegas y son indefendibles ante el
usuario; un máximo sobre reglas siempre puede explicar *qué regla* elevó el
riesgo.

```
risk = max(regla aplicable) sobre:

  none      kind ∈ {documentation, generated} y ninguna característica de riesgo
  low       test_only · refactor con grafo completo y sin API pública tocada
  standard  por defecto
  elevated  public_api · cross_module · concurrency · behavior_change sin test_covered
  high      security_sensitive · database · grafo incompleto con behavior_change
            · dependency con cambio de versión mayor
```

El **tamaño no eleva el riesgo por sí solo**; eleva la *profundidad* (más
contexto, más presupuesto) y dispara la sugerencia de split. Esto invierte la
regla actual de 400 líneas: 400 líneas de config generada siguen siendo riesgo
`none`, y 40 líneas en el middleware de auth son `high`.

**El guardián de volumen se conserva tal cual** — es una regla de *proceso*
(mantener los cambios revisables), no de riesgo. Se renombra conceptualmente:
"volumen" mide revisibilidad, "riesgo" mide consecuencia. Hoy se confunden.

---

## 10. Review / Validation Planner

Función pura, sin IA, sin efectos:

```
Plan = plan(ChangeProfile, RiskProfile, CodeModel, RepositoryPolicy, Stage)
```

`Stage ∈ {pre-commit, pre-push, pr, remediation}`.

Salida:

```json
{
  "stage": "pre-push",
  "validation": {
    "profile": "standard",
    "capabilities": ["format","lint","build","unit_test"],
    "scope": { "mode": "affected", "packages": ["internal/review","internal/ops"],
               "reason": "graph=complete provider=go" }
  },
  "review": {
    "agents": [
      { "id":"correctness", "dims":["logic","spec","tests"],
        "profile":"normal", "priority":1,
        "context":{"budget_files":12,"include":["callers","tests"]} },
      { "id":"security",    "dims":["security"],
        "profile":"deep",   "priority":1,
        "context":{"budget_files":8,"include":["callers","config","boundaries"]} }
    ],
    "verify_blocking": true,
    "max_parallel": 2
  },
  "explain": [
    "risk=elevated por public_api (internal/review/finding.go: ReviewFinding)",
    "security agent activado por characteristic=security_sensitive",
    "style omitido: cubierto por capability lint"
  ]
}
```

El campo `explain` es obligatorio: **todo plan debe poder justificarse ante el
usuario en texto**. Un planificador que no explica sus decisiones es
indistinguible de uno roto.

El plan es serializable y se guarda en el Review Store. Dos ejecuciones con el
mismo `unit_id` y la misma política deben producir el mismo plan — es la base de
la reproducibilidad exigida en la sección 44 del enunciado.

---

## 11. Agent Scheduler

Traduce el plan a procesos. Responsabilidades:

- **Bundling**: fusionar dimensiones en agentes según el plan (§5).
- **Concurrencia**: semáforo global (ya existe, `engine.go:92`) más un
  presupuesto agregado de tiempo y coste por invocación. Si el presupuesto se
  agota, los agentes de prioridad 2 no se lanzan y el resultado lo declara —
  nunca se recorta en silencio.
- **Aislamiento de fallos**: un agente que falla es `unavailable` con razón, no
  tumba la ejecución (ya implementado, `engine.go:107-114`).
- **Reintento**: 1 reintento solo ante error de transporte/timeout, nunca ante
  salida inválida (una salida inválida repetida es un problema de prompt o de
  modelo; reintentar la esconde).
- **Atribución**: registrar el agente, binario, modelo **efectivo** y esfuerzo
  realmente aplicados. Cierra **H4/I1**.

Verificación del modelo efectivo (I2): antes de la primera llamada de una
sesión, el adaptador ejecuta una sonda mínima que pide al agente identificar su
modelo. Si no coincide con el solicitado, se registra `model_mismatch` y se
degrada el perfil a `unverified` en el store. Sin esto, las métricas por modelo
mienten.

---

## 12. Skills / lentes

Se conservan **seis dimensiones como taxonomía**, con un cambio de fondo en dos
de ellas.

| Dimensión | Responsabilidad | Fuera de alcance | Fuente principal |
|---|---|---|---|
| `spec` | El cambio hace lo que dice; sin trabajo fuera de alcance; breaking changes declarados | Calidad interna | LLM (mensaje + diff + contratos tocados) |
| `logic` | Correctitud, condiciones, estados, errores, concurrencia | Arquitectura | LLM + resultados de tests |
| `tests` | Valor de la cobertura, casos límite, tests frágiles | "¿hay tests?" (eso lo dice el grafo) | Grafo + LLM |
| `design` | Responsabilidades, acoplamiento, cohesión, extensibilidad, mantenibilidad | Naming local | LLM |
| `security` | Autz/autn, entrada no confiable, secretos, fronteras de confianza | Especulación sin evidencia | Scanner + LLM |
| `style` | **Solo lo que el linter no puede ver**: claridad de intención, comentarios que mienten, complejidad accidental | Formato, naming convencional, orden de imports | Linter (determinista) + LLM residual, ADVISORY |

**Cambio material en `style`** (resuelve I7): deja de ser un agente y pasa a ser
mayoritariamente una capability determinista. Su parte semántica es residual,
nunca supera `ADVISORY` y está desactivada por defecto.

**Cambio material en `tests`**: deja de preguntar "¿hay tests?" —eso lo responde
el mapa de tests del grafo, exactamente— y pasa a evaluar solo el *valor* de los
tests presentes, con los resultados reales de la ejecución en el contexto.

### Capacidades transversales

No se crea un agente por capacidad. Asignación:

| Capacidad | Asignación |
|---|---|
| Understandability | `style` (local) + `design` (estructural) |
| Maintainability | `design` |
| Compatibility | `spec` (contrato) + capability `dependency` (determinista) |
| Performance | `logic` si es algorítmico; `design` si es estructural; nunca por intuición |
| Observability | `design` |
| Concurrency | `logic`, activada por la característica `concurrency` |
| Database / Data | `security` + `spec`; migraciones fuerzan `integration_test` |
| API / Contracts | `spec`, activada por `public_api` |
| Infrastructure / CI-CD | capability determinista + `security` |

---

## 13. Context Strategy

**Se levanta la prohibición de herramientas y se sustituye por acotación.** La
prohibición actual (`prompts.go:555`) resolvía que `opencode run` se colgara
haciendo builds; el arreglo correcto es un *toolset* de solo lectura, no la
ceguera.

Toolset del revisor: `Read`, `Grep`, `Glob` — restringidos a una lista de rutas
calculada por el Context Builder. Sin `Bash`, sin escritura, sin red. Un timeout
por llamada (ya existe) y un límite de llamadas a herramienta por agente.

Contexto entregado en capas, en este orden, hasta agotar presupuesto:

```
1. diff del work unit + mensajes de commit
2. resultados de validación (tests fallidos con su salida, lint, build)
3. contenido completo de los ficheros tocados en su estado FINAL
4. símbolos cambiados + sus callers directos
5. tests de los paquetes afectados
6. contratos/interfaces implicadas y configuración relacionada
7. lista de rutas explorables (no contenido) para lookup bajo demanda
```

El punto 3 es el que mata H5: hoy el revisor ve un diff donde un símbolo parece
no existir, y no puede comprobarlo. Con el estado final del fichero, la clase
entera de falsos positivos "esto no existe / esto está roto" desaparece.

El punto 2 es el que evita el trabajo duplicado: el revisor que ya sabe qué test
falló no reporta problemas que la validación cubre.

El presupuesto de contexto lo fija el plan (`budget_files`), escalado por
`size`, no por `risk`.

---

## 14. Finding Contract

Un único esquema para hallazgos deterministas y semánticos, discriminado por
`source`:

```json
{
  "id": "fp:9f2c1a…",
  "source": "review",
  "producer": { "agent":"security", "binary":"claude", "model":"claude-opus",
                "effort":"high", "model_verified": true },
  "dimension": "security",
  "severity": "CRITICAL",
  "confidence": 0.82,
  "status": "confirmed",
  "title": "Token de GitHub leído con eco de terminal activo",
  "description": "…",
  "location": {
    "file": "internal/setup/github.go",
    "blob": "b1c2d3…",
    "line_start": 71,
    "line_end": 74,
    "symbol": "preguntarTokenGitHub"
  },
  "evidence": "bufio.NewReader(os.Stdin).ReadString('\\n')",
  "impact": "El secreto queda visible en pantalla y en el scrollback del terminal",
  "recommendation": "Leer con term.ReadPassword y descartar el eco",
  "fixable": "safe",
  "introduced_by": "d5b04a0",
  "fingerprint": "sha256(dimension|symbol|normalized_evidence|rule)"
}
```

Campos que hoy no existen y son imprescindibles:

- **`source`** — separa las dos naturalezas de evidencia (§24 del enunciado).
- **`confidence`** — permite graduar el bloqueo. Sin él, la única opción es
  bloquear con todo o con nada.
- **`evidence`** — cita textual del código. **Un finding sin `evidence` se
  descarta en el parseo.** Es el filtro más barato y más eficaz contra
  "podría ser más limpio".
- **`location.blob`** — ancla el hallazgo al contenido, no al commit. Es lo que
  hace que la revisión sobreviva a un rebase (C3).
- **`fingerprint`** — identidad estable para dedup, para "ya lo vi", y para
  detectar hallazgos reabiertos.
- **`status`** — `pending` | `confirmed` | `refuted` | `accepted_by_user` |
  `fixed` | `reopened`.
- **`fixable`** — `safe` | `needs_review` | `manual`. Entrada del Remediation
  Planner.

Un finding determinista rellena `source: "validation"`, `confidence: 1.0`,
`producer.command`, y `evidence` con la salida real del comando.

**Regla de rechazo** (endurece lo que ya hace `finding.go:328`): se descartan
findings sin `evidence`, sin ubicación resoluble en un fichero del cambio, o
cuya `evidence` no aparece literalmente en el blob referenciado. Este último
chequeo es puramente mecánico y elimina una fracción grande de las
alucinaciones antes de que lleguen al agregador.

---

## 15. Aggregator

Todo en Go. Etapas, en orden:

1. **Validar** — esquema, campos obligatorios, `evidence` presente y
   verificable contra el blob. Lo que no pasa se descarta con motivo registrado.
2. **Normalizar** — rutas, rangos de línea, severidades (ya existe,
   `finding.go:371`).
3. **Deduplicar** — por `fingerprint`; y por proximidad (mismo símbolo, rangos
   solapados, similitud de descripción > umbral) entre dimensiones distintas.
   La fusión conserva la severidad máxima y **acumula las evidencias**: dos
   agentes independientes señalando lo mismo *sube* la confianza.
4. **Supersede** — un finding determinista invalida los findings semánticos que
   describen el mismo problema en la misma ubicación. Si `lint` ya lo dijo, el
   LLM no lo repite. Reducción de ruido directa.
5. **Correlacionar** — agrupar por síntoma común (mismo test fallando, misma
   frontera de confianza) para reportar una causa, no diez efectos.
6. **Calcular estado**.

Estados de salida (§34 del enunciado, con matiz):

| Estado | Condición | Exit |
|---|---|---|
| `PASS` | sin CRITICAL; WARN dentro de umbral de política | 0 |
| `VALIDATION_FAILED` | cualquier CRITICAL con `source: validation` | 1 |
| `CODE_REVIEW_FAILED` | CRITICAL semántico **confirmado** por el verificador | 1 |
| `NEEDS_USER_REVIEW` | CRITICAL semántico refutado o de baja confianza; ambigüedad de requisito; excepción de política | 2 |
| `REVIEW_INFRASTRUCTURE_ERROR` | agente/proveedor no disponible, timeout, salida no parseable | 4 |

`VALIDATION_FAILED` y `CODE_REVIEW_FAILED` se reportan por separado aunque
compartan exit code: la causa es distinta y la acción del usuario también.
Nunca se mezclan en un "falló la revisión".

---

## 16. Remediation

```
Finding (fixable ∈ {safe, needs_review})
   │
   ▼
Remediation Planner (Go, determinista)
   ├─ safe        → Remediation Agent
   ├─ needs_review→ propuesta al usuario, sin aplicar
   └─ manual      → informe
   │
   ▼
Remediation Agent  (Edit + Read, sin Bash, sin red)
   permisos: SOLO ficheros con findings; ficheros nuevos solo si son tests
   │
   ▼
Diff Guard (Go): ¿el diff del fix toca solo las zonas de los findings ±N líneas?
   no → se descarta el fix entero y se reporta "remediation out of scope"
   │
   ▼
Re-validation  (mismo perfil que falló, alcance = afectados por el fix)
   │
   ▼
Re-analysis (nuevo ChangeProfile) → Re-plan → Re-review SOLO de lo tocado
   │
   ▼
Findings no resueltos + findings nuevos introducidos por el fix
```

**Decisión sobre permisos**: el agente de remediación **no** obtiene shell. Es
la diferencia entre "corrige esta línea" y "haz que los tests pasen", y la
segunda es exactamente cómo se introducen tests debilitados y `//nolint`. La
validación la ejecuta Go, no el agente.

El **Diff Guard** es la pieza que hace la remediación aceptable: sin él, un
"arregla esto" se convierte en un refactor oportunista imposible de revisar.

Límite duro: **una ronda de remediación por unidad de cambio**. Si tras la
re-validación siguen quedando bloqueantes, el resultado es
`NEEDS_USER_REVIEW`. Los bucles fix→review→fix son donde el coste se descontrola
y donde el sistema deja de ser reproducible.

---

## 17. Incremental Review

### 17.1 Decisión: Review Store por contenido, no caché por SHA

Se separan explícitamente, como pide el enunciado:

- **Change Graph** responde *qué está afectado*.
- **Review Store** responde *qué sabemos ya*.

Y se cambia la clave. Hoy: `<sha>.json`. Objetivo:

| Registro | Clave |
|---|---|
| Validación de una capability | `hash(capability, comando, tree OID del alcance)` |
| Revisión de una unidad | `unit_id = hash(base_tree, head_tree, plan_id)` |
| Finding | `fingerprint` + `location.blob` |

Consecuencia: un `rebase` que no altera el contenido **conserva todos los
resultados**. Un `amend` que cambia solo el mensaje invalida únicamente `spec`.
Un `squash` de commits ya revisados reutiliza los findings por blob.

Esto no elimina la ficha por SHA: se conserva como **índice de trazabilidad**
(qué commit introdujo qué finding, `introduced_by`), que es su valor real. Lo
que deja de ser es la clave de reutilización.

### 17.2 Qué se reutiliza y qué se invalida

| Cambio | Efecto |
|---|---|
| Fichero sin cambiar (mismo blob) | Findings de ese fichero se reutilizan íntegros |
| Fichero cambiado | Findings de ese blob se marcan `stale`; se re-revisa el fichero |
| Cambia un dependiente (según el grafo) | Se re-revisa `spec` y `logic` del dependiente; no el resto |
| Cambia el plan (política, modelo, versión de skill) | Invalida la revisión semántica; la validación se conserva si el comando y el árbol no cambiaron |
| Cambia la versión del skill/prompt | Invalida solo las dimensiones cuyo prompt cambió |

El registro guarda `plan_id`, versión de skill, modelo y esfuerzo; sin ellos la
invalidación por cambio de política es imposible (y hoy no se guardan de forma
fiable — I1).

---

## 18. Pre-commit

Presupuesto objetivo: **< 3 segundos, cero llamadas a LLM.**

```
1. Change Analyzer (git)               ~50 ms
2. Change Profile + Risk (sin IA)      ~50 ms
3. Cohesión y propuesta de split       ~100 ms
4. Guardián de volumen (actual)        ~50 ms
5. Validation profile `fast` acotado   1–2 s
```

Salida: `PASS` / `VALIDATION_FAILED` / aviso de volumen / aviso de cohesión.
Nunca revisión semántica. Nunca modifica el historial.

**Cohesión y split.** Sustituye a "más de 400 líneas = mal commit". Se calcula
por componentes conexas sobre el grafo de imports de los ficheros cambiados,
más las reglas de clase de fichero:

```
Commit
 ├── cluster 1: internal/review/{finding,ledger}.go  + sus tests   [cohesivo]
 ├── cluster 2: internal/setup/github.go                           [independiente]
 └── cluster 3: .github/workflows/ci.yml                           [infra]

→ Sugerencia: 3 unidades. Ninguna se aplica automáticamente.
```

`slice` sigue existiendo y sigue siendo el mecanismo de aplicación, pero su
criterio de agrupación pasa de "capa por subcadena de ruta" a "clúster de
cohesión". Es una mejora directa y aislada del resto de la arquitectura.

**El sistema nunca reescribe historia sin aprobación explícita.** La aprobación
A/R/E/C actual se conserva íntegra, incluida la corrección de EOF (B9).

---

## 19. Pre-push — el Quality Gate

```
1. Determinar el rango real (§21) y construir la unidad de cambio
2. Consultar el Review Store por blob → qué está ya validado y revisado
3. Validation `standard` acotada a lo afectado no validado
   ├─ falla → VALIDATION_FAILED, STOP (sin gastar tokens)
   └─ pasa  ↓
4. Plan de revisión sobre lo NO revisado
5. Scheduler → agentes → Verifier sobre bloqueantes
6. Aggregator → estado
7. Persistir: plan, runs, findings, coste, duración
```

Requisitos operativos que el enunciado pide y hoy no están cubiertos:

- **Fallo parcial**: un agente caído no invalida el resto; el estado incluye
  `partial: true` y qué faltó. Hoy `veredictoGlobal` degrada todo a
  `unavailable` si alguna dimensión lo está (`engine.go:175-179`) — demasiado
  agresivo: un `security` caído no debería ocultar un `logic` en block.
- **Cancelación**: `context.Context` propagado desde el CLI hasta el proceso del
  agente. Hoy el contexto nace y muere dentro de `ejecutarComandoConTimeout`
  (`cli.go:1015`); un Ctrl-C no cancela limpiamente.
- **Reintento**: solo transporte, 1 vez (§11).
- **Commits ya revisados**: se saltan por blob, no por SHA (§17).

---

## 20. PR Review

La PR **no** es la suma de sus commits. Se revisa como unidad propia:

```
unidad_pr = diff(merge_base(base, head) … head)
```

Reviewer input:
- the PR's net diff, not its commit sequence;
- per-commit findings from the Review Store as context, not a verdict;
- the recorded range intent from Piece 1 trailers, or an explicit no-intent value;
- the PR's aggregate change profile and risk.

`pr review` does not run deterministic validation. `gate` owns validation, and
[Piece 4](piece-4-pr-review-authors.md) owns the current PR-review authoring,
evidence, and persistence contract.

The net review evaluates concerns that a per-commit audit cannot see:

| Axis | Question |
|---|---|
| Intent | Does the PR deliver its recorded intent when one exists? |
| Integration | Do the pieces from different commits fit together? |
| Cross-commit interaction | Does one commit undo or contradict another? |
| Net regression | Does the final state break something the initial state did not? |
| Contracts | Are there undeclared breaking changes? |
| Coverage | Do tests cover the net behaviour, not each individual step? |

Regla explícita: **findings de commits intermedios sobre código que ya no existe
en el diff neto se archivan, no se reportan**. Es la segunda mitad del arreglo
de H5: hoy un defecto introducido en el commit 2 y corregido en el commit 5
bloquea la PR entera.

---

## 21. Stacked PR — diff ownership

El problema real: `AnalizarRama` (`rama.go:626-637`) usa siempre
`merge-base(base, HEAD)` con `base = "main"` por defecto. Sobre una pila
`main → A → B → C`, revisar C contra `main` arrastra A y B.

Regla objetivo:

```
parent(C) = la rama padre declarada, o inferida
diff_propio(C) = merge_base(parent(C), C) … C
contexto(C)    = diff acumulado de main…parent(C)   (solo lectura, no se revisa)
```

Inferencia del padre, por precedencia:
1. `--parent` explícito;
2. `gh pr view --json baseRefName` si la PR existe;
3. la rama configurada como upstream de tracking;
4. la rama local cuyo merge-base con la actual es el más reciente;
5. fallo explícito. **Nunca `main` por defecto en una pila**: adivinar mal la
   base es reportar cambios ajenos como propios, que es el peor error posible
   en una revisión.

Findings sobre líneas fuera del `diff_propio` se reportan como
`inherited: true` y no bloquean la PR actual. La corrección corresponde a la PR
que los introdujo.

Cuando una PR de la pila se actualiza (rebase de la base), el store por blob
conserva la revisión de todo lo que no cambió — que en un rebase limpio es casi
todo. Este es el caso de uso donde C3 más duele hoy.

---

## 22. Human-in-the-loop

Se pregunta **solo** cuando la respuesta no es derivable:

| Situación | Se pregunta |
|---|---|
| Requisito ambiguo (el revisor no sabe qué se pretendía) | Sí |
| Breaking change: ¿intencional? | Sí |
| Trade-off de arquitectura o de seguridad | Sí |
| ¿Bug o comportamiento deseado? | Sí |
| Excepción de política | Sí, con registro de quién y por qué |
| Split de commit sugerido | Sí (nunca automático) |
| CRITICAL semántico refutado por el verificador | Sí |
| Test en rojo, lint en rojo, build roto | **No.** Se reporta y se bloquea |
| Formato | **No.** Se aplica |
| Alcance de la validación | **No.** Lo decide el grafo |

El mecanismo existente `question` + `--answer` (§5 de la guía, `engine.go:141`)
es la base correcta. Le falta: persistir la decisión del usuario en el store,
para que la misma pregunta no se repita en la siguiente ejecución sobre el mismo
blob. Hoy la respuesta se pierde al terminar el proceso.

Las excepciones (`--force`, hoy sin traza — M3) pasan a ser
`decision` persistida: `{who, when, finding_id, reason, scope}`.

---

## 23. Persistence — modelo de datos

Ubicación: se conserva `<git-common-dir>/vas-sentinel/`. Decisión intacta.

Ruta concreta: `<raíz-del-repo>/.git/vas-sentinel/` — la misma carpeta donde ya
viven las fichas hoy (§31.3).

```
<repo>/.git/vas-sentinel/
  policy.lock.json          política efectiva resuelta + su hash
  graph/<tree-oid>.json     grafo cacheado por árbol
  snapshots/<tree-oid>/     worktree congelado para validar (§31.3)
  units/<unit-id>.json      unidad de cambio: perfil, riesgo, plan, estado
  runs/<run-id>.json        ejecución: validaciones, agentes, coste, duración
  findings/<fingerprint>.json  finding + historial de estados
  commits/<sha>.json        índice de trazabilidad (ficha actual, adelgazada)
  decisions.jsonl           decisiones humanas, append-only
  events.jsonl              eventos operativos (existente)
```

Entidades y relaciones:

```
Unit ──1:N──► ValidationRun ──1:N──► Finding(source=validation)
  │
  ├──1:N──► ReviewRun ──1:N──► Finding(source=review)
  │                              │
  │                              ├──0:N──► Verification (refuter)
  │                              ├──0:1──► Remediation ──► ValidationRun
  │                              └──0:N──► Decision
  │
  └──N:M──► Commit (índice de trazabilidad)
```

**Qué se persiste y qué se deriva:**

| Se persiste | Se deriva |
|---|---|
| Findings, con estado e historial | El veredicto global (función pura de los findings) |
| Ejecuciones de validación con exit y duración | El conjunto de afectados (recalculable del grafo) |
| Plan y política efectiva (con hash) | El Change Profile (recalculable del diff) |
| Decisiones humanas | La matriz de la plantilla PR |
| Coste, modelo efectivo, esfuerzo | Cualquier agregado de métricas |

**Formato**: se mantiene JSON por archivo con escritura atómica. SQLite se
descarta por ahora por la misma razón que en la guía §4 (over-engineering) y
porque el volumen es de cientos de registros, no de millones. Se reevalúa si
`findings/` supera ~10⁴ archivos.

**Migración desde el ledger actual**: las fichas `<sha>.json` existentes se leen
en modo compatibilidad y se reindexan por blob al primer uso. No se borra nada.

---

## 24. Observability

Por ejecución:

```
duración total · duración por capability · duración por agente
tokens in/out por agente · coste estimado · modelo efectivo · esfuerzo
findings por fuente / dimensión / severidad
alcance: completo vs afectado, y cuánto se ahorró
cache: reutilizados vs recalculados
fallos: timeout, salida inválida, proveedor caído
```

Agregado (lo que responde de verdad las preguntas de las §37-38 del enunciado):

| Métrica | Qué decide |
|---|---|
| Findings por dimensión / confirmados por dimensión | Si una dimensión merece existir |
| Tasa de refutación por agente y modelo | Si un modelo genera ruido |
| Tasa de override del usuario por dimensión | Si el umbral de severidad está mal calibrado |
| Findings reabiertos | Si las correcciones son reales |
| Éxito de remediation | Si la remediación automática compensa |
| Coste por finding confirmado | La métrica de eficiencia del sistema |
| Latencia p50/p95 por etapa | Si el pre-commit sigue siendo usable |

Todas se calculan del store; ninguna requiere telemetría externa. El
`events.jsonl` pasa a tener `detail` como objeto, no string (M4).

**Sin estas métricas, el scheduling dinámico no se puede afinar.** Por eso su
fase va antes de cualquier ajuste fino, no después.

---

## 25. Cost / Performance

Palancas, ordenadas por rendimiento:

1. **Validación antes que revisión** — evita gastar tokens en código roto.
   Ahorro estimado: todo el coste de revisión en las ejecuciones que fallan.
2. **Riesgo `none`/`low` → 0 agentes** — docs, generado, formato, config.
   En un repo real es una fracción grande de los commits.
3. **Reutilización por blob** — en un rebase limpio, cerca del 100 %.
4. **Bundling de dimensiones** — de 6 llamadas a 1–3, con menos duplicados.
5. **`style` a determinista** — elimina una llamada por commit de frontend.
6. **Contexto acotado por presupuesto** — el coste crece con el tamaño del
   prompt, no solo con el número de llamadas.
7. **Perfil por riesgo, no por dimensión** — `deep` solo donde el riesgo lo
   justifica, no siempre en `design` y `security` como hoy
   (`vassentinel.yml`).

Presupuestos objetivo:

| Etapa | Latencia | Llamadas LLM |
|---|---|---|
| pre-commit | < 3 s | 0 |
| pre-push, riesgo standard | < 60 s | 1–2 |
| pre-push, riesgo high | < 5 min | 3–5 + verificación |
| PR | sin límite duro | plan completo |

---

## 26. Failure handling

Principio: **fallo de infraestructura ≠ fallo de calidad**. Nunca se convierte
uno en otro, en ninguna dirección.

| Fallo | Comportamiento |
|---|---|
| Agente no disponible / rate-limit | `unavailable` con razón; la dimensión no cuenta; el estado global es `REVIEW_INFRASTRUCTURE_ERROR` solo si **ninguna** dimensión respondió |
| Timeout de agente | 1 reintento; luego `unavailable` |
| JSON inválido | Sin reintento; `unavailable` con razón `contract_violation`; se registra la salida cruda para diagnóstico |
| Finding sin evidencia verificable | Se descarta el finding, no la ejecución |
| Fallo de Git | Error duro. Sin Git no hay unidad de cambio |
| GitHub/`gh` no disponible | Fallback a archivo + portapapeles (ya implementado); nunca bloquea |
| Lint / test / build en rojo | `VALIDATION_FAILED`. Bloquea. Es el caso *correcto* de bloqueo |
| Fallo parcial de reviewers | `partial: true`, se reporta qué faltó; el usuario decide |
| Findings contradictorios entre agentes | El verificador arbitra; sin resolución → `NEEDS_USER_REVIEW` |
| Fallo de remediation | Se revierte el fix (Diff Guard), se reporta, no se reintenta |
| Rechazo del usuario | Se persiste como `Decision`; no se vuelve a preguntar sobre el mismo blob |
| Grafo incompleto | Validación completa. Nunca acotada |
| Política inválida | Error duro al arrancar, con la línea exacta. **Hoy se ignora en silencio** (I5) |

---

## 27. Migration Plan

Small, verifiable phases, respecting the guardian's rule (≤400 lines per work
unit, `check` between units). Each phase delivers value on its own and none
requires a rewrite.

### F0 — Settle the open debt *(prerequisite, no new architecture)*

- Commit the pending B1/B2/B3/B9 corrections in the worktree (702 lines).
- Close **H4** (record the effective agent and model in the record) and **H6**
  (token echo).
- Close **B4/O2**: thresholds into shared constants.
- **Exit criterion**: `go test ./...` green, `check` green, H4/H6 closed with
  evidence in `docs/auditoria/`.

### F1 — Invert the gate *(the plan's highest-return phase)*

- `internal/validation`: extract `ops.Verificar` into a capabilities-aware
  engine.
- Run validation **before** review in `pr create` and in a new
  `sentinel gate --stage pre-push`.
- `VALIDATION_FAILED` blocks; the semantic verdict becomes *advisory*
  transiently until F5.
- **Candidate freeze** (§31.3): validation stops running over the live worktree
  and moves to an ephemeral worktree of the frozen tree; staleness detection on
  completion. It belongs in this phase because a gate that measures the wrong
  tree is not a gate.
- **Exit criterion**: a PR with red `go test` is not published; a PR with a
  semantic CRITICAL and green tests is, with a warning; and with uncommitted
  changes on disk during the run, the result is still that of the published
  tree.
- Risk: it changes the gate's observable behavior. Announced in the README.

### F2 — Finding contract v2 and content-addressed store

- `source`, `confidence`, `evidence` (mandatory), `line_start/end`, `blob`,
  `fingerprint`, `status`, `fixable`.
- Rejection of findings without evidence verifiable against the blob.
- Blob-indexed store; the per-SHA record degraded to a traceability index, with
  compatibility reads of existing records.
- **Exit criterion**: a `git rebase` without content changes preserves 100 % of
  the findings. A regression test proves it.

### F3 — Deterministic Change Profile and Risk Model *(no AST)*

- `internal/change`: profile, characteristics, file classes by **configurable
  path rules** — replaces `ClasificarCapa` (C5).
- `internal/risk`: risk as maximum over rules, with `explain`.
- Cohesion by clusters (co-change + directory) → split suggestion in
  `pre-commit`.
- **Exit criterion**: `sentinel explain` prints profile, risk, and the rules
  that produced it for any range.

### F4 — Go Code Model and incremental validation

- `GraphProvider` + a `native` implementation with `go/packages` — **the only
  one authorized to bound validation** (§31.2).
- Optional `codegraph` provider, only for reviewer context selection; absent →
  silent degradation.
- Reverse import closure → affected packages → affected tests.
- `supports_scope` on the validation providers; hard fallback to full.
- Graph cache keyed by `tree OID`.
- **Exit criterion**: `pre-push` time on a single-package change drops
  measurably, and a test exists that forces `graph=incomplete` and checks that
  full validation runs.

### F5 — Planner, Scheduler, and bounded context

- `internal/planning` (pure function, with `explain`) and `internal/agents`
  (bundling, budget, attribution).
- Replace the static `dimensionesPorCapa` map with the plan.
- Lift the tool prohibition; read-only toolset with bounded paths; layered
  context carrying the **final state of the files**.
- Verifier over blocking findings; the semantic verdict regains blocking power,
  now corroborated.
- **Exit criterion**: the four false positives documented in H5 stop
  reproducing. That is a concrete acceptance test, not an impression.

### F6 — Aggregator v2

- Dedup by fingerprint and by proximity; deterministic→semantic supersede;
  correlation by cause.
- States `PASS` / `VALIDATION_FAILED` / `CODE_REVIEW_FAILED` /
  `NEEDS_USER_REVIEW` / `REVIEW_INFRASTRUCTURE_ERROR`.
- **Exit criterion**: in a change with one defect that triggers three
  dimensions, **one** finding is reported with three pieces of evidence.

### F7 — Bounded remediation

- Remediation planner, agent with restricted `Edit`, **Diff Guard**,
  bounded re-validation, a single round.
- **Exit criterion**: a fix touching files outside the scope is discarded in
  full and reported.

### F8 — Stacked PR and global PR review

- Parent-branch inference, `diff_propio`, `inherited` findings.
- PR review as a net unit, with archival of findings over code that no longer
  exists.
- **Exit criterion**: reviewing B on top of A neither reports nor blocks on
  A's findings.

### F9 — Observability and calibration

- Cost, latency, tokens, effective model on every run.
- `sentinel metrics` with the aggregations of §24.
- **Only then** are bundles, thresholds and profiles tuned — with data.

**Delivered 2026-09-04** (T9.0-T9.5, detail in
[`docs/issues/decisions.md`](../issues/decisions.md)).
What actually shipped, and what did not:

- `sentinel metrics` answers from local storage over the durable-run store.
  Duration and success/failure are observed; **cost, tokens and scope stay
  `null`** because no adapter reports them yet. Absence is rendered as unknown,
  never as zero — that distinction is the phase's central rule.
- One default was calibrated from evidence: `review.timeout` `300s`/`600s` to
  `900s`, from 325 measured runs, carrying its right-censoring caveat. It is
  the only axis the data supported.
- T9.5 retention collects the execution streams of published commits, keeping
  fichas, events and every metrics snapshot. It deviates from its original
  acceptance clause, which required deleting fichas too; that clause was
  unsatisfiable together with byte-identical measurement, and the deviation is
  ratified in the phase document.
- One measurement discontinuity exists: on 2026-09-04 the first retention pass
  moved the success and failure counters once, irreversibly. **Series spanning
  that date are not comparable.** The calibrated default above predates it and
  is unaffected.

### Transversal phases, in parallel and without blocking

- **Repository policy**: replace the artisanal parser with `yaml.v3` plus
  schema validation and an explicit error on unknown keys (I5). It is the
  project's first external dependency: accepted because the target policy's
  complexity is not hand-expressible and because the current parser fails
  silently, which is the worst possible failure mode.
- **Extraction of `cmd/sentinel`** (C6): move orchestration to `internal/app`
  incrementally, one subcommand per work unit, starting with the ones each
  phase touches. It is not done as a one-shot refactor.

### Order and dependencies

```
F0 ──► F1 ──► F2 ──► F3 ──► F4 ──► F5 ──► F6 ──► F7
                      │              │
                      └──────────────┴──► F8 ──► F9
       (yaml.v3 policy and cmd/ extraction run in parallel from F1)
```

---

## 28. Decisions taken — summary

What the statement's §43 asked to decide explicitly:

| Decision | Choice | Reason in one line |
|---|---|---|
| 6 agents vs dynamic scheduling | **Dynamic (0–5) + verifier of blocking findings** | The problem is not the number of lenses, it is the context and the lack of corroboration |
| AST graph vs simple dependency | **Staged: files → packages+tests → bounded symbols**; no persistent global graph | Level 1 gives 90 % of the value at 10 % of the cost |
| Cache vs Review Store + invalidation | **Content-addressed store (blob/tree OID)**; the per-SHA record becomes a traceability index | Rebase is the normal flow and today it destroys all incrementality |
| Full vs affected validation | **Affected only with a complete graph and a provider that declares scope; otherwise full** | The gate's confidence is not traded for time |
| Claude's tools | **Read-only reviewers with bounded paths** (not prohibition, not freedom) | The current prohibition is the root cause of H5 |
| Model selection | **By risk, not by dimension**, with verification of the effective model | A silently degraded model invalidates the whole cost strategy |
| Effort selection | **By risk and context depth**, not fixed per dimension | High effort on `style` is pure expense |
| Parallel vs sequential agents | **Sequential validation first; agents in parallel with a budget** | Do not pay for reviewing code that does not compile |
| Review granularity | **The change unit (net range), with attribution to commit**; per-commit only for cohesion | Reviewing isolated commits produces false positives by construction |
| Finding aggregation | **Go, fingerprint + proximity, with deterministic→semantic supersede** | Less noise without losing signal |
| Remediation permissions | **`Edit` bounded to files with findings, no shell, with Diff Guard, one round** | Without a limit, "fix this" becomes an unreviewable refactor |
| Dimensions | **6 kept as taxonomy**; `style` becomes deterministic; `tests` stops counting tests | Do not ask an LLM what a linter already knows |
| Severity | `CRITICAL` / `WARN` / `ADVISORY` + `NEEDS_USER_REVIEW`, with `source` | A red `go test` and a style advisory cannot share treatment |
| Policy | **`vassentinel.yml` extended with `yaml.v3`**, not a new `.review/` | One single place for configuration; the current parser fails silently |

---

## 29. What is deliberately NOT built

Every discarded component, with its reason — the statement's section 42
requires justifying cost and alternative for everything that is added; this is
its complement.

| Discarded | Reason | Alternative adopted |
|---|---|---|
| Persistent multi-language global symbol graph | Disproportionate cost and invalidation; it is a separate product | Per-tree graph, cached, level 1–2 |
| SQLite for the store | Hundreds of records, not millions; JSON+rename is already atomic and auditable | JSON files per entity |
| Service or daemon | Contradicts "local and deterministic by construction" (guide §1) | Pure CLI |
| One agent per transversal capability | Multiplies cost without adding signal | Assignment to dimensions (§12) |
| Unlimited fix→review→fix loop | Uncapped cost and non-reproducible outcome | One round + `NEEDS_USER_REVIEW` |
| Risk score by weighted sum | Impossible to calibrate and to explain | Maximum over rules, with `explain` |
| Shell for the remediation agent | Direct path to weakened tests | Validation executed by Go |
| Auto-split of history | Rewriting history without a human is unacceptable | Suggestion + `slice` with approval |

---

## 30. Risks of the proposal

1. **Lifting the tool prohibition may reintroduce hangs.** That is why the
   tools were prohibited in the first place (guide §7.3, point 10).
   Mitigation: explicit toolset without `Bash`, tool-call limit, the already
   existing timeout. It must be verified with a real `opencode` before F5 is
   declared good.
2. **A verifier may refute true findings.** Mitigation: a refutation only
   lowers to `NEEDS_USER_REVIEW`, never silently discards; and the refutation
   rate is a watched metric (§24).
3. **The content-addressed store complicates traceability to commit.**
   Mitigation: `introduced_by` and the `commits/<sha>.json` index are kept.
4. **First external dependencies (`yaml.v3`, `go/packages`).** It breaks a
   current property of the project. Accepted consciously in F4 and in the
   policy phase; both are official or de-facto Go ecosystem dependencies.
5. **The plan has nine phases.** The real risk is abandoning it halfway. That
   is why F1 and F2 are ordered first: if the plan stopped there, the system
   would already be substantially better than today (correct gate +
   incrementality that survives rebase) without having built any of the graph.

---

## 31. Design review — four additional decisions

Questions raised after the first version of the report. Three modify the
design; the fourth confirms it with a nuance.

### 31.1 Configuration location: it stays in the worktree

The rule is not "unify state", it is **origin of the data**:

| Data | Location | Reason |
|---|---|---|
| Human configuration (policy) | worktree, **versioned** | Reviewed in PR, cloned, has history |
| Derived state (store, graph, events) | common-dir | Recomputed, not shared, not reviewed |

Moving `vassentinel.yml` to the common-dir would break the three properties
that make a quality policy useful: it would not be cloned (every developer
would rebuild it by hand), it would not be versioned (lowering a `security`
threshold would leave no trace in any diff) and it would not be reviewable.
For a tool whose purpose is the traceability of quality decisions, hiding the
policy is an internal contradiction.

The decision is already made elsewhere in the system: `init` injects the volume
rules into `AGENTS.md` / `CLAUDE.md` / `.claudecode.md` (`main.go:211`), which
are committed.

**Two files are kept.** A third, gitignored level was considered and rejected:
the case it would solve (a local user override over one concrete repo) **is
already covered** by existing ephemeral mechanisms, which are the right means
for an ephemeral override.

| Need | Current mechanism |
|---|---|
| Per-invocation override | `--profile`, `--timeout` (`comandos_estado.go:367-390`) |
| Per-session or per-machine override | `MY_SUB_AGENT` (`agentadapter/factory.go:18,54`) |

One more persistent file would have duplicated that function and added state
nobody remembers exists.

```
defaults                          in the binary
~/.vas_sentinel/vassentinel.yml   user — created by install (install.go:387)
.vas_sentinel/vassentinel.yml     repository, versioned — init (install.go:412)
```

**The real problem is not the number of files: it is two classes of keys with
opposite owners coexisting in the same schema.**

| Class | Examples | Who must decide |
|---|---|---|
| **Policy** | `validation.*`, `review.dims`, thresholds, commands | The repository: it is a team contract |
| **Resources** | `active_agent`, `model`, `reasoning_effort`, `timeout` | The user: it is their quota, their credentials, their machine |

Today the repository wins everywhere (`parser.go:125`: the per-project file is
applied last). A verifiable practical consequence: this very repository's
`.vas_sentinel/vassentinel.yml` pins `claude-opus`; whoever clones it without a
Claude quota silently inherits a binary that cannot run.

**Decision**: precedence is not inverted — that would be surprising and would
break existing configurations. A **warning** is emitted instead: on load, if
the repository file pins resource keys, a one-line warning is printed (`this
repository pins agent 'claude'; your global preference is ignored`). Trivial
cost, eliminates the silent failure, and the separation is documented in the
policy schema.

### 31.2 CodeGraph: integration with a bounded role

CodeGraph (a SQLite index of symbols and edges, with a daemon and watcher) is
already installed in the user's projects. It is integrated, with a strict
separation:

> The graph that decides **what NOT to run** must be verifiable.
> The graph that decides **what to read** may be heuristic.

| Role | Provider | Consequence of an error |
|---|---|---|
| Context selection for the reviewer | **CodeGraph** (when present) | Less context → a worse finding |
| Scope of incremental validation | **`native` only** (`go/packages`) | A skipped test → **bug shipped with the gate green** |

Three reasons an external index cannot decide validation scope:

1. **It exposes no completeness contract.** It does not answer "I parsed these
   N files and none uses reflection". Without that signal, scoping would
   violate the hard rule of §6.4.
2. **Mutable state with lag.** The watcher syncs with delay; a gate whose
   result depends on whether the daemon had finished is not reproducible, and
   reproducibility is an explicit requirement of the goal.
3. **It changes the product's distribution.** Sentinel is today one binary
   (`go install`, release assets). Requiring a second installed component
   turns it into a stack.

**Design**: a `GraphProvider` interface with two implementations and disjoint
roles.

```
native    (go/packages)  mandatory for Go
                         the ONLY one authorized to bound validation scope
codegraph (optional)     enriches reviewer context, multi-language
                         absent → silent degradation, never failure
```

Effect on the plan: **F4 gets cheaper**. Reverse import closure with
`go/packages` is ~100 lines; the expensive part was the multi-language graph,
and that role is covered by CodeGraph in the one place where being wrong costs
nothing.

The documentation will state how to install it and will link to its repository,
as an optional capability — never as a requirement.

### 31.3 Candidate freeze — changes during review

**Current defect**: `pr create` audits and then calls `ops.Verificar`, which
executes the commands with `cmd.Dir = worktree` over the **live worktree**
(`internal/ops/verificar.go:894`, invoked from `comandos_pr.go:546`). With
uncommitted changes on disk, the PR publishes exit codes that **do not
correspond to the code being merged**. It is an honest PASS over the wrong
tree.

**Target design**: the review unit stops being "the worktree" and becomes a
pair of immutable trees.

```
on start     → freeze (base_tree, head_tree, HEAD)
reviewers    → read from the object store (git show <tree>:<path>); never from the worktree
validation   → git worktree add --detach .git/vas-sentinel/snapshots/<tree-oid> <head>
               tests run against the real tree, not the user's worktree
on finish    → did HEAD or the tree change? → `stale` result, receipt not valid
```

Consequences:

- **The user can keep working during review without corrupting it.**
- Tests measure what will be merged, not an intermediate disk state.
- It fits the content-addressed store (§17) at no cost: the key is already the
  tree OID.
- Staleness detection is cheap: `rev-parse HEAD` and a tree hash at the start
  and at the end.

Scope: applies to `pre-push` and `pr`. **Not** to `pre-commit`, which is fast
and operates on the index.

#### Location of the ephemeral worktree

Inside the `.git` folder **of the repository itself**. Neither the user's home
nor Git's installation directory:

```
<repo-root>/.git/vas-sentinel/snapshots/<tree-oid>/
```

Real example in this repository (`git rev-parse --git-common-dir` →
`C:/0-BackupVF/WorkSpace/vas.sentinel/.git`):

```
C:\0-BackupVF\WorkSpace\vas.sentinel\.git\vas-sentinel\snapshots\281428a2add4…\
```

It is not a new folder: **the ledger already lives there**
(`.git/vas-sentinel/<sha>.json`, `ledger.go:920`). The `git-common-dir`
expression is the guide's §4 jargon to distinguish the shared `.git` from a
linked worktree's private directory (`.git/worktrees/<name>`); the distinction
only matters with `git worktree`.

Verified on Windows: `git worktree add --detach` accepts a path inside the
common-dir, registers it in `git worktree list`, and `git worktree remove`
deletes it cleanly.

**The repository name is not part of the path, and must not become one**: the
path is already inside the repository, so adding it would be redundant
(`vas.sentinel/.git/vas-sentinel/vas.sentinel/…`). A `tree-oid` is unambiguous
by construction: it is a hash of that repository's object store and does not
leave it. Disambiguation would only be needed if the state lived in
`~/.vas_sentinel/`, which is precisely one of the reasons guide §4 rejected
the home directory.

Resulting state-directory layout:

```
<repo>/.git/vas-sentinel/
    <sha>.json               traceability records        (already exists)
    events.jsonl             events                      (already exists)
    snapshots/<tree-oid>/    frozen tree to validate     (new)
    graph/<tree-oid>.json    cached graph                (new)
```

#### From a linked worktree: always the main repository's `.git`

Checked with a real linked worktree:

```
From the linked worktree:
  git rev-parse --git-common-dir    → <main>/.git                            ← shared
  git rev-parse --absolute-git-dir  → <main>/.git/worktrees/<name>           ← private
```

A linked worktree **has no `.git` folder**: it has a *gitfile*, a text file
with one line `gitdir: <path>`. There is no local directory to host state;
everything ends, one way or another, inside the main repository's `.git`. The
only real decision is whether it goes at the shared root or in the private
`worktrees/<name>/` subdirectory.

**Nota histórica.** La disposición por worktree descrita en esta comparación
es heredada. La persistencia actual de los juicios de `pr review` usa
`<git-common-dir>/vas-sentinel/pr-reviews/<key>.json`; las evidencias largas
siguen siendo ficheros del worktree bajo `.vas_sentinel/evidence/`. El contrato
operativo de ambas ubicaciones pertenece a las secciones 2.7 y 3 de
[`piece-4-pr-review-authors.md`](piece-4-pr-review-authors.md).

**Corrección histórica que motivó §4.** En ese momento el ledger usaba
`ObtenerGitDir` (`gitdir.go:17`, `--absolute-git-dir`), es decir, el directorio
**privado** de cada worktree; la guía lo justificaba como «aislamiento gratuito
por worktree».

| | Disposición heredada | Estado actual |
|---|---|---|
| Function | `ObtenerGitDir` (`gitdir.go:17`) | `ObtenerGitCommonDir` (`gitdir.go:31`) |
| Path from a linked worktree | `.git/worktrees/<name>/vas-sentinel/` | `.git/vas-sentinel/` |
| Effect | Each worktree has its own ledger: auditing in A and switching to B forces **re-auditing everything** | Shared: work done in A is valid in B |

El aislamiento tenía sentido con una clave SHA (cada rama, su historial). Con
una clave de contenido deja de tenerlo: un hallazgo sobre un blob es válido en
cualquier worktree del mismo repositorio. **Store, snapshots and graph move to
the shared common-dir.**

Los dos detalles operativos siguientes pertenecen a la propuesta histórica y no
describen el comportamiento actual:


1. **Migration**: existing private ledgers would become invisible. On first
   startup, if `.git/worktrees/<n>/vas-sentinel/` contains records, they are
   moved to the shared one. Nothing is deleted.
2. **Concurrency**: two worktrees validating at the same time may request the
   same `snapshots/<tree-oid>/`. There is no corruption risk —the content is
   identical by definition— but `git worktree add` fails if the path exists.
   Create with a temporary name and rename; "already exists" is treated as
   success.

Five reasons against `os.TempDir()`:

1. **Same volume as the object store** → Git hardlinks and the checkout is
   cheap. On Windows `%TEMP%` can be on another disk and would force a copy.
2. **Coherence with the decision already made** (guide §4): all derived state
   lives in the common-dir; no new location is invented.
3. **Never committed, never dirties `git status`**: it is outside the working
   tree, without depending on `.gitignore`.
4. **Lifecycle tied to the clone**: it disappears with the repository, leaving
   no garbage in the system temp directory.
5. **It is not invisible state**: `git worktree list` shows it; an orphaned
   candidate after an interruption is diagnosable and `git worktree prune`
   cleans it.

The `tree-oid` key gives reuse at no cost: if a snapshot of that tree already
exists, it is reused without a checkout. Purge by age, same as the ledger's
orphaned records.

#### Declared limitation: unversioned files

A clean checkout **does not contain unversioned artifacts**: `node_modules`,
`.env`, downloaded fixtures, generated code outside Git. A `go test` works; an
`npm test` without `node_modules` does not.

That is why the mode is configurable, with explicit and never silent
degradation:

| Mode | Behavior |
|---|---|
| `worktree` (default) | Frozen candidate; isolated and correct |
| `inplace` | Runs in the real worktree **requiring it to be clean**; if it is dirty, it **aborts** instead of reporting a result for the wrong tree |

What cannot be repeated is the current behavior: running over a dirty worktree
and publishing the result as if it were the PR tree's.

Cost: one ephemeral checkout per new tree. It is the price of the result
meaning something.

### 31.4 File size: no limit, and a clarification of the current threshold

**Prior clarification**: `LimiteCodigoGigante = 500` (`slice.go:19`) **does not
measure file size**. It measures `ArchivoModificado.Lineas`, which is:

- tracked file → **added lines** from the numstat;
- new file → **physical lines** of the file (`slice.go:167`).

A 3000-line file with 10 modified triggers nothing; a new one with 501 does.
Two different metrics under the same name — this is **M2** applied to this
threshold, and it must be unified.

**Decision: file size is not limited.**

- Size is a *symptom*, not a defect. `main.go` at 949 lines is a problem for
  concentrating orchestration (**C6/H3**), not for its length; a generated
  5000-line table is correct.
- A hard limit produces the worst possible outcome: mechanical partitioning
  into `foo1.go` / `foo2.go` driven by a counter and without cohesion — worse
  than the big file, and harder to review.

**Signals that are adopted, always `ADVISORY`, never blocking:**

| Signal | What it indicates |
|---|---|
| Exported symbols in the file with no call relation among them | Real single-responsibility violation |
| Churn: changes in most commits, for a different reason each time | Split candidate |
| Number of lines | Informational only. Never a decision criterion |

The current interactive dialogue (refactor / bypass / abort, `main.go:457`) is
kept in full; only its trigger changes: cohesion instead of a counter.

**The guardian's 400 threshold stays hard.** It measures a different
magnitude: reviewability of a *change*, which is a property of the human
reviewer and does have a real ceiling. File size and change volume do not
share a threshold because they are not the same magnitude.

#### What counts for the threshold: the boundary is generated vs hand-written

The guardian measures **code reviewability**, not bytes. Verified against the
current code: nothing is exempt — writing 4055 lines of documentation took
`check` from 702 to 4701 lines and stalled development. And worse: an `.md` is
classified as `backend` (`ClasificarCapa`, `slice.go:214`), so a 1792-line
document triggered `esCodigoGigante` and `slice` offered to **split it with AI
applying SRP**.

The correct boundary is not «configuration vs code» —a hand-written
`docker-compose.yml` is code that runs— but **generated vs hand-written**:

| Class | Counts for the threshold? | Large-file threshold | Treatment in `slice` |
|---|---|---|---|
| `source` | Yes, **blocks** | 500 → refactor dialogue | normal batches |
| `test` | Yes, **blocks** | 500 → refactor dialogue | normal batches |
| hand-written `config` | Yes, **blocks** | 400 → isolate | its own batch |
| `generated` / lock | **Does not count** | — | its own `chore(deps)` batch |
| `docs` | **Does not block; it is reported** | 400 → isolate | its own `docs(...)` batch |

It is not an ad-hoc exception: it is the direct application of the risk model
of §9 — size alone does not raise risk, and 2000 lines of generated
configuration are still `none` risk.

For documentation the choice is **warn, not block**: 1792 lines of document
are also not reviewable in one sitting and deserve a warning; but stalling
the guardian over them is friction without safety in return.

Implemented ahead of the plan (`internal/git/clases.go`) because it blocked
the writing of this very report. See `internal/git/clases.go` (formerly
`docs/reingenieria/f0-deuda.md` → **T0.8**, consolidated into `docs/issues/`).
The per-repository configurable version and the removal of `ClasificarCapa`
belong to F3-T3.1.

---

## 32. Conclusion

VAS Sentinel does not need a rewrite. It needs to **invert its relationship of
trust** and **anchor its memory to content instead of to the commit**.

The system already has an atomic ledger, typed degradation, adapters with
fallback, branch analysis, configurable verification, an honest prompt
template, and an audit culture with an evidence rule. What it lacks is not
infrastructure: it is that the deterministic side commands, that the reviewer
can see, and that what is already known is not recomputed.

The first two phases of the plan (F1 and F2) change exactly that, depend on no
graph, and fit within the 400-lines-per-work-unit budget the guardian itself
imposes.

The final goal is not to have more agents. It is:

> analyze each change with the minimum computation and the minimum reasoning
> necessary to obtain a sufficiently reliable, reproducible, traceable and
> incremental review.
