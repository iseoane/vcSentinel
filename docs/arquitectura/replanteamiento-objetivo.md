# VAS Sentinel — Replanteamiento arquitectónico

Análisis de la aplicación existente y propuesta de arquitectura objetivo hacia
**Change Intelligence + Deterministic Validation + Incremental AI Code Review +
Quality Gate**.

Este documento **no implementa nada**. Es diagnóstico, decisión arquitectónica y
plan de evolución. Toda afirmación sobre el estado actual va con `file:line`.

Fuentes inspeccionadas: los 68 `.go` del módulo, `docs/guia-implantacion-revision.md`
(diseño acordado, 6 iteraciones), `docs/plan-fase-2.md`, `docs/auditoria/*`
(auditoría propia en curso), `.vas_sentinel/vassentinel.yml`, `release.yml`.

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

## 3. Problemas arquitectónicos

### CRITICAL

**C1 · La revisión semántica es el gate; la validación determinista no lo es.**
`comandos_pr.go:528` bloquea por veredicto de auditoría; `verificar.go` solo
informa. Consecuencia: falsos positivos de LLM bloquean PRs correctas y código
que no compila puede publicarse. Además se gasta presupuesto de tokens
revisando código que un `go build` habría descartado en 2 segundos.

**C2 · El contexto del revisor es un diff aislado y el repositorio está
prohibido.** `prompts.go:555`. Causa directa de H5. Ningún ajuste de modelo,
esfuerzo o prompt corrige la falta de información: el revisor no puede saber si
un símbolo existe porque no puede mirar.

**C3 · La incrementalidad está anclada al SHA del commit.** El ledger es
`<sha>.json` (`ledger.go:926`) y `PurgarHuerfanas` borra lo que dejó de ser
alcanzable (`ledger.go:1017`). Cualquier `rebase`, `amend` o `squash` —el flujo
normal de una rama de agente, y el que el propio `sentinel rebase` promueve—
invalida el 100 % de las revisiones aunque el contenido no haya cambiado ni una
línea. La incrementalidad se derrumba precisamente en el escenario objetivo.

**C4 · No existe modelo del cambio.** No hay AST, ni grafo de símbolos, ni de
dependencias, ni perfil de cambio, ni modelo de riesgo. Todo se decide por dos
señales: número de líneas y subcadena de la ruta. `LimiteDecisionChain = 400`
(`rama.go:587`) es literalmente el mismo umbral del guardián reutilizado como
criterio de arquitectura de PRs.

**C5 · La clasificación por capa es una taxonomía falsa que alimenta
decisiones reales.** `ClasificarCapa` (`slice.go:214`) devuelve `test` para
cualquier ruta que contenga la subcadena `test` — `latest/`, `contest.go`,
`internal/testdata/` — y `config` para cualquier `.json`/`.yml`/`.lock`. Ese
valor decide **qué dimensiones se auditan** (`engine.go:51`) y **cómo se
agrupan los commits** (`plan.go:44`). Entrada incorrecta ⇒ plan de revisión
incorrecto, en silencio.

**C6 · La lógica de negocio vive en `package main`.** `main.go` 949 líneas,
`comandos_pr.go` 570+. Orquestación, política, IO y presentación mezcladas. Es
el hallazgo **H3** del propio proyecto y la causa de **B8** (cero tests en la
capa interactiva hasta las correcciones actuales). No hay capa de casos de uso
donde colgar Planner, Scheduler, Aggregator o Remediation.

### IMPORTANT

**I1 · Trazabilidad rota en la ficha (H4, confirmado).** Con `active_agent:
auto` la ficha guarda el nombre del perfil, no el agente que respondió
(`comandos_review.go:113-116`). Si la cadena cae de `claude` a `opencode`, el
veredicto queda sin autor. Sin esto, la sección 38 (calidad del reviewer) es
inejecutable: no se puede atribuir ruido a un modelo.

**I2 · El modelo se inyecta por variables de entorno no contractuales.**
`CLAUDE_CODE_MODEL`, `OPENCODE_MODEL` (`cli.go:1022-1028`). La propia guía
documenta que un modelo inexistente **se ignora en silencio** y opencode usa
otro (§10). Toda la estrategia de coste/calidad por perfiles puede estar
degradada sin que nada lo señale.

**I3 · El agregador no agrega.** `veredictoGlobal` (`engine.go:156`) es una
precedencia de cinco casos. No deduplica, no correlaciona, no fusiona
evidencia. Con seis dimensiones de mandatos solapados (`design` y `logic`
comparten "complejidad"; `security` y `logic` comparten "entrada no validada")
el mismo defecto se reporta N veces.

**I4 · El contrato de finding es insuficiente para un gate.** `ReviewFinding`
(`finding.go:286`) tiene `dimension, file, line, severity, description,
suggestion`. Faltan `source` (validation|review), `confidence`, rango de líneas,
`evidence` (la cita textual que lo prueba), `impact` y `fixable`. Sin `evidence`
no hay forma de refutar un falso positivo salvo leyendo el código a mano; sin
`confidence` no hay forma de graduar el bloqueo.

**I5 · El parser YAML es artesanal.** `aplicarDesdeRuta` (`config/parser.go:145`)
recorre por niveles de indentación, ignora en silencio toda clave desconocida y
no soporta listas dentro de mapas anidados. La política de repositorio que la
arquitectura objetivo necesita (capacidades, perfiles, umbrales, excepciones,
reglas por ruta) no es expresable con este parser.

**I6 · Sin observabilidad de coste ni latencia.** El `Evento`
(`ops/events.go`) guarda `at, cmd, exit, shas, detail, worktree`. No hay
duración, ni tokens, ni modelo efectivo, ni tasa de override. Las secciones 37
y 38 del objetivo no tienen datos sobre los que operar.

**I7 · `style` como dimensión de LLM es gasto redundante.** Formato, naming
convencional y consistencia son competencia de `gofmt` y `golangci-lint`, que
son exactos, gratis e instantáneos. Se está pagando un modelo por responder lo
que un linter ya sabe.

### IMPROVEMENT

**M1 · Stacked PR es aspiracional.** `--chain-pr` solo permite publicar una
rama que el sistema desaconseja (`comandos_pr.go:540`). No hay noción de rama
padre ni de *diff ownership*: `AnalizarRama` siempre usa
`merge-base(base, HEAD)` con base `main` (`rama.go:626-637`), así que revisar B
sobre A arrastra los commits de A a la matriz y al gate.

**M2 · Métricas de volumen con semánticas mezcladas.** Rastreados: líneas
añadidas del numstat. No rastreados: líneas físicas del archivo. Rango de rama:
añadidas + borradas (`mergebase.go:19`). Tres definiciones de "volumen" para el
mismo concepto. Es el residuo de **B5** del informe del guardián.

**M3 · `--force` sin registro de excepción.** `comandos_pr.go:528` permite
saltar el gate sin dejar una decisión trazable de quién y por qué.

**M4 · `detail` del evento es un string con JSON dentro.** Impide consultar
sin re-parsear.

### OPTIONAL

**O1 · Passthrough legacy de `pr`** (`comandos_pr.go:50`) convive con los verbos
propios; deuda de compatibilidad a retirar con aviso.

**O2 · Umbrales duplicados** (`400` en `diff.go`, `slice.go`, `rama.go`) —
**B4** del informe del guardián, aún abierto.

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

Entrada del revisor:
- el diff neto de la PR (no la secuencia de commits);
- el historial de findings por commit del Review Store, como *contexto*, no como
  veredicto;
- resultados de validación `full`;
- perfil de cambio y riesgo agregados de la PR.

Se evalúan explícitamente cosas que la revisión por commit no puede ver:

| Eje | Pregunta |
|---|---|
| Intención | ¿La PR hace lo que su título/descripción promete? |
| Integración | ¿Las piezas de los distintos commits encajan? |
| Interacción entre commits | ¿Un commit deshace o contradice a otro? |
| Regresión neta | ¿El estado final rompe algo que el estado inicial hacía? |
| Contratos | ¿Hay breaking changes no declarados? |
| Cobertura | ¿Los tests cubren el comportamiento neto, no cada paso? |

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

Fases pequeñas y verificables, respetando la regla del guardián (≤400 líneas por
unidad de trabajo, `check` entre unidades). Cada fase entrega valor por sí sola y
ninguna requiere reescritura.

### F0 — Saldar la deuda abierta *(prerequisito, sin arquitectura nueva)*

- Commitear las correcciones B1/B2/B3/B9 pendientes en el worktree (702 líneas).
- Cerrar **H4** (registrar agente y modelo efectivos en la ficha) y **H6**
  (eco del token).
- Cerrar **B4/O2**: umbrales a constantes compartidas.
- **Criterio de salida**: `go test ./...` verde, `check` en verde, H4/H6 cerrados
  con evidencia en `docs/auditoria/`.

### F1 — Invertir el gate *(la fase de mayor retorno del plan)*

- `internal/validation`: extraer `ops.Verificar` a un motor con capabilities.
- Ejecutar validación **antes** de la revisión en `pr create` y en un nuevo
  `sentinel gate --stage pre-push`.
- `VALIDATION_FAILED` bloquea; el veredicto semántico pasa a *advisory* de forma
  transitoria hasta F5.
- **Congelación del candidato** (§31.3): la validación deja de ejecutarse sobre
  el worktree vivo y pasa a un worktree efímero del árbol congelado; detección
  de obsolescencia al terminar. Va en esta fase porque un gate que mide el árbol
  equivocado no es un gate.
- **Criterio de salida**: una PR con `go test` en rojo no se publica; una PR con
  un CRITICAL semántico y tests verdes sí, con aviso; y con cambios sin
  commitear en disco durante la ejecución, el resultado sigue siendo el del
  árbol publicado.
- Riesgo: cambia comportamiento observable del gate. Se anuncia en el README.

### F2 — Contrato de finding v2 y store por contenido

- `source`, `confidence`, `evidence` (obligatoria), `line_start/end`, `blob`,
  `fingerprint`, `status`, `fixable`.
- Rechazo de findings sin evidencia verificable contra el blob.
- Store indexado por blob; ficha por SHA degradada a índice de trazabilidad, con
  lectura en compatibilidad de las fichas existentes.
- **Criterio de salida**: un `git rebase` sin cambios de contenido conserva el
  100 % de los findings. Test de regresión que lo demuestre.

### F3 — Change Profile y Risk Model deterministas *(sin AST)*

- `internal/change`: perfil, características, clases de fichero por **reglas de
  ruta configurables** — sustituye `ClasificarCapa` (C5).
- `internal/risk`: riesgo por máximo sobre reglas, con `explain`.
- Cohesión por clústeres (co-cambio + directorio) → sugerencia de split en
  `pre-commit`.
- **Criterio de salida**: `sentinel explain` imprime perfil, riesgo y las reglas
  que lo produjeron para cualquier rango.

### F4 — Code Model Go y validación incremental

- `GraphProvider` + implementación `native` con `go/packages` — **única
  autorizada a acotar la validación** (§31.2).
- Proveedor `codegraph` opcional, solo para selección de contexto del revisor;
  ausente → degradación silenciosa.
- Cierre inverso de imports → paquetes afectados → tests afectados.
- `supports_scope` en los providers de validación; fallback duro a completo.
- Cache de grafo por `tree OID`.
- **Criterio de salida**: el tiempo de `pre-push` en un cambio de un paquete
  baja de forma medible, y existe un test que fuerza `graph=incomplete` y
  comprueba que se ejecuta la validación completa.

### F5 — Planner, Scheduler y contexto acotado

- `internal/planning` (función pura, con `explain`) y `internal/agents`
  (bundling, presupuesto, atribución).
- Sustituir el mapa estático `dimensionesPorCapa` por el plan.
- Levantar la prohibición de herramientas; toolset de solo lectura con rutas
  acotadas; contexto en capas con el **estado final de los ficheros**.
- Verificador sobre findings bloqueantes; el veredicto semántico recupera poder
  de bloqueo, ahora corroborado.
- **Criterio de salida**: los cuatro falsos positivos documentados en H5 dejan de
  reproducirse. Es un test de aceptación concreto, no una impresión.

### F6 — Aggregator v2

- Dedup por fingerprint y por proximidad; supersede determinista→semántico;
  correlación por causa.
- Estados `PASS` / `VALIDATION_FAILED` / `CODE_REVIEW_FAILED` /
  `NEEDS_USER_REVIEW` / `REVIEW_INFRASTRUCTURE_ERROR`.
- **Criterio de salida**: en un cambio con un defecto que dispara tres
  dimensiones, se reporta **un** finding con tres evidencias.

### F7 — Remediation acotada

- Planner de remediación, agente con `Edit` restringido, **Diff Guard**,
  re-validación acotada, una sola ronda.
- **Criterio de salida**: un fix que toca ficheros fuera del alcance se descarta
  íntegro y se reporta.

### F8 — Stacked PR y PR review global

- Inferencia de rama padre, `diff_propio`, findings `inherited`.
- Revisión de PR como unidad neta, con archivado de findings sobre código que ya
  no existe.
- **Criterio de salida**: revisar B sobre A no reporta ni bloquea por hallazgos
  de A.

### F9 — Observabilidad y calibración

- Coste, latencia, tokens, modelo efectivo en cada run.
- `sentinel metrics` con las agregaciones de §24.
- **Solo entonces** se ajustan bundles, umbrales y perfiles — con datos.

### Fases transversales, en paralelo y sin bloquear

- **Política de repositorio**: sustituir el parser artesanal por `yaml.v3` con
  validación de esquema y error explícito ante clave desconocida (I5).
  Es la primera dependencia externa del proyecto: se acepta porque la
  complejidad de la política objetivo no es expresable a mano y porque el parser
  actual falla en silencio, que es el peor modo de fallo posible.
- **Extracción de `cmd/sentinel`** (C6): mover orquestación a `internal/app` de
  forma incremental, un subcomando por unidad de trabajo, empezando por los que
  toque cada fase. No se hace como refactor de una sola vez.

### Orden y dependencias

```
F0 ──► F1 ──► F2 ──► F3 ──► F4 ──► F5 ──► F6 ──► F7
                      │              │
                      └──────────────┴──► F8 ──► F9
       (política yaml.v3 y extracción de cmd/ en paralelo desde F1)
```

---

## 28. Decisiones tomadas — resumen

Lo que el enunciado §43 pedía decidir explícitamente:

| Decisión | Elección | Razón en una línea |
|---|---|---|
| 6 agentes vs dynamic scheduling | **Dynamic (0–5) + verificador de bloqueantes** | El problema no es el número de lentes, es el contexto y la falta de corroboración |
| AST graph vs dependencia simple | **Escalonado: archivos → paquetes+tests → símbolos acotados**; sin grafo global persistente | El nivel 1 da el 90 % del valor al 10 % del coste |
| Caché vs Review Store + invalidación | **Store por contenido (blob/tree OID)**; ficha por SHA como índice de trazabilidad | El rebase es el flujo normal y hoy destruye toda la incrementalidad |
| Validación completa vs afectada | **Afectada solo con grafo completo y provider que declara alcance; si no, completa** | La confianza del gate no se negocia por tiempo |
| Herramientas de Claude | **Revisores read-only con rutas acotadas** (no prohibición, no libertad) | La prohibición actual es la causa raíz de H5 |
| Selección de modelo | **Por riesgo, no por dimensión**, con verificación del modelo efectivo | Un modelo degradado en silencio invalida toda la estrategia de coste |
| Selección de esfuerzo | **Por riesgo y profundidad de contexto**, no fijo por dimensión | El esfuerzo alto en `style` es gasto puro |
| Agentes paralelos vs secuenciales | **Validación secuencial primero; agentes en paralelo con presupuesto** | No pagar por revisar código que no compila |
| Granularidad de revisión | **La unidad de cambio (rango neto), con atribución a commit**; per-commit solo para cohesión | Revisar commits aislados produce falsos positivos por construcción |
| Agregación de findings | **Go, fingerprint + proximidad, con supersede determinista→semántico** | Menos ruido sin perder señal |
| Permisos de remediación | **`Edit` acotado a ficheros con findings, sin shell, con Diff Guard, una ronda** | Sin límite, "arregla esto" se convierte en un refactor no revisable |
| Dimensiones | **Se conservan 6 como taxonomía**; `style` pasa a determinista; `tests` deja de contar tests | No preguntar a un LLM lo que un linter ya sabe |
| Severidad | `CRITICAL` / `WARN` / `ADVISORY` + `NEEDS_USER_REVIEW`, con `source` | Un `go test` rojo y un advisory de estilo no pueden compartir tratamiento |
| Política | **`vassentinel.yml` extendido con `yaml.v3`**, no un `.review/` nuevo | Un solo lugar de configuración; el parser actual falla en silencio |

---

## 29. Lo que deliberadamente NO se construye

Cada componente descartado, con su motivo — la sección 42 del enunciado exige
justificar coste y alternativa de todo lo que se añade; esto es su complemento.

| Descartado | Motivo | Alternativa adoptada |
|---|---|---|
| Grafo de símbolos global persistente multilenguaje | Coste e invalidación desproporcionados; es un producto aparte | Grafo por árbol, cacheado, nivel 1–2 |
| SQLite para el store | Cientos de registros, no millones; JSON+rename ya es atómico y auditable | Archivos JSON por entidad |
| Servicio o daemon | Contradice "local y determinista por construcción" (guía §1) | CLI puro |
| Un agente por capacidad transversal | Multiplica coste sin añadir señal | Asignación a dimensiones (§12) |
| Bucle fix→review→fix ilimitado | Coste no acotado y resultado no reproducible | Una ronda + `NEEDS_USER_REVIEW` |
| Score de riesgo por suma ponderada | Imposible de calibrar y de explicar | Máximo sobre reglas, con `explain` |
| Shell para el agente de remediación | Camino directo a tests debilitados | Validación ejecutada por Go |
| Auto-split del historial | Reescribir historia sin humano es inaceptable | Sugerencia + `slice` con aprobación |

---

## 30. Riesgos de la propuesta

1. **Levantar la prohibición de herramientas puede reintroducir cuelgues.**
   Es la razón por la que se prohibieron (guía §7.3, punto 10). Mitigación:
   toolset explícito sin `Bash`, límite de llamadas a herramienta, timeout ya
   existente. Debe verificarse con `opencode` real antes de dar F5 por buena.
2. **Un verificador puede refutar hallazgos verdaderos.** Mitigación: refutar
   solo baja a `NEEDS_USER_REVIEW`, nunca descarta en silencio; y la tasa de
   refutación es una métrica vigilada (§24).
3. **El store por contenido complica la trazabilidad a commit.** Mitigación:
   `introduced_by` y el índice `commits/<sha>.json` se conservan.
4. **Primera dependencia externa (`yaml.v3`, `go/packages`).** Rompe una
   propiedad actual del proyecto. Se acepta conscientemente en F4 y en la fase
   de política; ambas son dependencias del ecosistema Go oficial o de facto.
5. **El plan tiene nueve fases.** El riesgo real es abandonarlo a mitad. Por eso
   F1 y F2 están ordenadas primero: si el plan se detuviera ahí, el sistema ya
   sería sustancialmente mejor que hoy (gate correcto + incrementalidad que
   sobrevive al rebase) sin haber construido nada del grafo.

---

## 31. Revisión del diseño — cuatro decisiones adicionales

Cuestiones planteadas tras la primera versión del informe. Tres modifican el
diseño; la cuarta lo confirma con un matiz.

### 31.1 Ubicación de la configuración: se mantiene en el worktree

La regla no es "unificar el estado", es **origen del dato**:

| Dato | Ubicación | Motivo |
|---|---|---|
| Configuración humana (política) | worktree, **versionada** | Se revisa en PR, se clona, tiene historia |
| Estado derivado (store, grafo, eventos) | common-dir | Se recalcula, no se comparte, no se revisa |

Mover `vassentinel.yml` al common-dir rompería las tres propiedades que hacen
útil a una política de calidad: no se clonaría (cada desarrollador la
reconstruye a mano), no se versionaría (bajar un umbral de `security` no dejaría
rastro en ningún diff) y no sería revisable. Para una herramienta cuyo propósito
es la trazabilidad de las decisiones de calidad, esconder la política es una
contradicción interna.

La decisión ya está tomada en otro punto del sistema: `init` inyecta las reglas
de volumen en `AGENTS.md` / `CLAUDE.md` / `.claudecode.md` (`main.go:211`), que
sí se commitean.

**Se mantienen dos archivos.** Un tercer nivel gitignored se consideró y se
descarta: el caso que resolvería (override local del usuario sobre un repo
concreto) **ya está cubierto** por mecanismos efímeros existentes, que son el
medio correcto para un override efímero.

| Necesidad | Mecanismo vigente |
|---|---|
| Override por invocación | `--profile`, `--timeout` (`comandos_estado.go:367-390`) |
| Override por sesión o máquina | `MY_SUB_AGENT` (`agentadapter/factory.go:18,54`) |

Un archivo persistente adicional habría duplicado esa función y añadido estado
que nadie recuerda que existe.

```
defaults                          en el binario
~/.vas_sentinel/vassentinel.yml   usuario — creado por install (install.go:387)
.vas_sentinel/vassentinel.yml     repositorio, versionado — init (install.go:412)
```

**El problema real no es el número de archivos: son dos clases de claves con
dueño opuesto conviviendo en un mismo esquema.**

| Clase | Ejemplos | Quién debe mandar |
|---|---|---|
| **Política** | `validation.*`, `review.dims`, umbrales, comandos | El repositorio: es contrato de equipo |
| **Recursos** | `active_agent`, `model`, `reasoning_effort`, `timeout` | El usuario: son su cuota, sus credenciales, su máquina |

Hoy el repositorio gana en todo (`parser.go:125`: el per-proyecto se aplica el
último). Consecuencia práctica verificable: el `.vas_sentinel/vassentinel.yml`
de este propio repositorio fija `claude-opus`; quien lo clone sin cuota de
Claude hereda un binario que no puede ejecutar, en silencio.

**Decisión**: no se invierte la precedencia — sería sorprendente y rompería
configuraciones existentes. Se **avisa**: al cargar, si el archivo del
repositorio fija claves de recurso, se emite un aviso de una línea (`este
repositorio fija el agente 'claude'; tu preferencia global se ignora`). Coste
trivial, elimina el fallo silencioso, y la separación queda documentada en el
esquema de la política.

### 31.2 CodeGraph: integración con rol acotado

CodeGraph (índice SQLite de símbolos y aristas, con daemon y watcher) ya está
instalado en los proyectos del usuario. Se integra, con una separación estricta:

> El grafo que decide **qué NO ejecutar** debe ser verificable.
> El grafo que decide **qué leer** puede ser heurístico.

| Rol | Proveedor | Consecuencia de un error |
|---|---|---|
| Selección de contexto para el revisor | **CodeGraph** (si está) | Menos contexto → un finding peor |
| Alcance de la validación incremental | **Solo `native`** (`go/packages`) | Un test omitido → **bug con el gate en verde** |

Tres razones por las que un índice externo no puede decidir el alcance de la
validación:

1. **No expone un contrato de completitud.** No responde "he parseado estos N
   ficheros y ninguno usa reflexión". Sin esa señal no se puede acotar sin
   violar la regla dura de §6.4.
2. **Estado mutable con retraso.** El watcher sincroniza con lag; un gate cuyo
   resultado depende de si el daemon había terminado no es reproducible, y la
   reproducibilidad es requisito explícito del objetivo.
3. **Cambia la distribución del producto.** Sentinel es hoy un binario
   (`go install`, release assets). Exigir un segundo componente instalado lo
   convierte en un stack.

**Diseño**: interfaz `GraphProvider` con dos implementaciones y roles disjuntos.

```
native    (go/packages)  obligatorio para Go
                         ÚNICO autorizado a acotar la validación
codegraph (opcional)     enriquece el contexto del revisor, multilenguaje
                         ausente → degradación silenciosa, nunca fallo
```

Efecto sobre el plan: **F4 se abarata**. El cierre inverso de imports con
`go/packages` son ~100 líneas; lo caro era el grafo multilenguaje, y ese rol lo
cubre CodeGraph en el único lugar donde equivocarse no tiene coste.

La documentación indicará cómo instalarlo y enlazará a su repositorio, como
capacidad opcional — nunca como requisito.

### 31.3 Congelación del candidato — cambios durante la revisión

**Defecto vigente**: `pr create` audita y después llama a `ops.Verificar`, que
ejecuta los comandos con `cmd.Dir = worktree` sobre el **worktree vivo**
(`internal/ops/verificar.go:894`, invocado desde `comandos_pr.go:546`). Con
cambios sin commitear en disco, el PR publica exit codes que **no corresponden
al código que se va a mergear**. Es un PASS honesto sobre el árbol equivocado.

**Diseño objetivo**: la unidad de revisión deja de ser "el worktree" y pasa a ser
un par de árboles inmutables.

```
al iniciar   → congelar (base_tree, head_tree, HEAD)
reviewers    → leen del object store (git show <tree>:<path>); nunca del worktree
validación   → git worktree add --detach .git/vas-sentinel/snapshots/<tree-oid> <head>
               tests sobre el árbol real, no sobre el worktree del usuario
al terminar  → ¿cambió HEAD o el árbol? → resultado `stale`, receipt no válido
```

Consecuencias:

- **El usuario puede seguir trabajando durante la revisión sin corromperla.**
- Los tests miden lo que se va a mergear, no el estado intermedio del disco.
- Encaja sin coste con el store por contenido (§17): la clave ya es el tree OID.
- La detección de obsolescencia es barata: `rev-parse HEAD` y hash del árbol al
  principio y al final.

Alcance: aplica a `pre-push` y `pr`. **No** a `pre-commit`, que es rápido y opera
sobre el índice.

#### Ubicación del worktree efímero

Dentro de la carpeta `.git` **del propio repositorio**. Ni el home del usuario ni
el directorio de instalación de Git:

```
<raíz-del-repo>/.git/vas-sentinel/snapshots/<tree-oid>/
```

Ejemplo real en este repositorio (`git rev-parse --git-common-dir` →
`C:/0-BackupVF/WorkSpace/vas.sentinel/.git`):

```
C:\0-BackupVF\WorkSpace\vas.sentinel\.git\vas-sentinel\snapshots\281428a2add4…\
```

No es una carpeta nueva: **el ledger ya vive ahí** (`.git/vas-sentinel/<sha>.json`,
`ledger.go:920`). La expresión `git-common-dir` es jerga de la guía §4 para
distinguir el `.git` compartido del directorio privado de un worktree enlazado
(`.git/worktrees/<nombre>`); la distinción solo importa con `git worktree`.

Verificado en Windows: `git worktree add --detach` acepta una ruta dentro del
common-dir, la registra en `git worktree list` y `git worktree remove` la
elimina limpiamente.

**El nombre del repositorio no forma parte de la ruta, y no debe formarla**: la
ruta ya está dentro del repositorio, así que añadirlo sería redundante
(`vas.sentinel/.git/vas-sentinel/vas.sentinel/…`). Un `tree-oid` no es ambiguo
por construcción: es un hash del object store de ese repositorio y no sale de
él. Solo haría falta desambiguar si el estado viviera en `~/.vas_sentinel/`, que
es precisamente una de las razones por las que la guía §4 descartó el home.

Estructura resultante del directorio de estado:

```
<repo>/.git/vas-sentinel/
    <sha>.json               fichas de trazabilidad      (ya existe)
    events.jsonl             eventos                     (ya existe)
    snapshots/<tree-oid>/    árbol congelado a validar   (nuevo)
    graph/<tree-oid>.json    grafo cacheado              (nuevo)
```

#### Desde un worktree enlazado: siempre el `.git` del repositorio principal

Comprobado con un worktree enlazado real:

```
Desde el worktree enlazado:
  git rev-parse --git-common-dir    → <principal>/.git                       ← compartido
  git rev-parse --absolute-git-dir  → <principal>/.git/worktrees/<nombre>    ← privado
```

Un worktree enlazado **no tiene carpeta `.git`**: tiene un *gitfile*, un archivo
de texto con una línea `gitdir: <ruta>`. No existe un directorio local donde
alojar estado; todo termina, por una vía o por otra, dentro del `.git` del
repositorio principal. La única decisión real es si va en la raíz compartida o
en el subdirectorio privado `worktrees/<nombre>/`.

**Corrección derivada a la guía §4.** Hoy el ledger usa `ObtenerGitDir`
(`gitdir.go:17`, `--absolute-git-dir`), es decir el directorio **privado** de
cada worktree; la guía lo justificaba como "aislamiento per-worktree gratis".

| | Hoy | Objetivo |
|---|---|---|
| Función | `ObtenerGitDir` (`gitdir.go:17`) | `ObtenerGitCommonDir` (`gitdir.go:31`) |
| Ruta desde un worktree enlazado | `.git/worktrees/<nombre>/vas-sentinel/` | `.git/vas-sentinel/` |
| Efecto | Cada worktree tiene su propio ledger: auditar en A y cambiar a B obliga a **re-auditarlo todo** | Compartido: el trabajo hecho en A vale en B |

El aislamiento tenía sentido con clave por SHA (cada rama, su historia). Con
clave por contenido deja de tenerlo: un finding sobre un blob es válido en
cualquier worktree del mismo repositorio. **Store, snapshots y grafo pasan al
common-dir compartido.**

Dos detalles operativos del traslado:

1. **Migración**: los ledgers privados existentes quedarían invisibles. En el
   primer arranque, si `.git/worktrees/<n>/vas-sentinel/` contiene fichas, se
   trasladan al compartido. No se borra nada.
2. **Concurrencia**: dos worktrees validando a la vez pueden solicitar el mismo
   `snapshots/<tree-oid>/`. No hay riesgo de corrupción —el contenido es
   idéntico por definición— pero `git worktree add` falla si la ruta existe. Se
   crea con nombre temporal y se renombra; "ya existe" se trata como éxito.

Cinco razones frente a `os.TempDir()`:

1. **Mismo volumen que el object store** → Git enlaza en duro y el checkout es
   barato. En Windows `%TEMP%` puede estar en otro disco y obligaría a copiar.
2. **Coherencia con la decisión ya tomada** (guía §4): todo el estado derivado
   vive en el common-dir; no se inventa una ubicación nueva.
3. **Nunca se commitea ni ensucia `git status`**: está fuera del árbol de
   trabajo, sin depender del `.gitignore`.
4. **Ciclo de vida ligado al clon**: desaparece con el repositorio, sin dejar
   basura en el temporal del sistema.
5. **No es estado invisible**: `git worktree list` lo muestra; un candidato
   huérfano tras una interrupción es diagnosticable y `git worktree prune` lo
   limpia.

La clave por `tree-oid` da reutilización sin coste: si ya existe un snapshot de
ese árbol, se reaprovecha sin checkout. Purga por antigüedad, igual que las
fichas huérfanas del ledger.

#### Limitación declarada: ficheros no versionados

Un checkout limpio **no contiene los artefactos no versionados**:
`node_modules`, `.env`, fixtures descargados, código generado fuera de Git. Un
`go test` funciona; un `npm test` sin `node_modules`, no.

Por eso el modo es configurable, con degradación explícita y nunca silenciosa:

| Modo | Comportamiento |
|---|---|
| `worktree` (por defecto) | Candidato congelado; aislado y correcto |
| `inplace` | Ejecuta en el worktree real **exigiendo que esté limpio**; si está sucio, **aborta** en lugar de reportar un resultado del árbol equivocado |

Lo que no puede repetirse es el comportamiento actual: ejecutar sobre un
worktree sucio y publicar el resultado como si fuera el del árbol de la PR.

Coste: un checkout efímero por árbol nuevo. Es el precio de que el resultado
signifique algo.

### 31.4 Tamaño de fichero: sin límite, y aclaración del umbral actual

**Aclaración previa**: `LimiteCodigoGigante = 500` (`slice.go:19`) **no mide el
tamaño del fichero**. Mide `ArchivoModificado.Lineas`, que es:

- fichero rastreado → **líneas añadidas** del numstat;
- fichero nuevo → **líneas físicas** del fichero (`slice.go:167`).

Un fichero de 3000 líneas con 10 modificadas no dispara nada; uno nuevo de 501,
sí. Dos métricas distintas bajo el mismo nombre — es **M2** aplicado a este
umbral, y debe unificarse.

**Decisión: no se limita el tamaño de fichero.**

- El tamaño es un *síntoma*, no un defecto. `main.go` con 949 líneas es un
  problema por concentrar orquestación (**C6/H3**), no por su longitud; una
  tabla generada de 5000 líneas es correcta.
- Un límite duro produce el peor resultado posible: partición mecánica en
  `foo1.go` / `foo2.go` guiada por un contador y sin cohesión — peor que el
  fichero grande, y más difícil de revisar.

**Señales que sí se adoptan, siempre `ADVISORY`, nunca bloqueantes:**

| Señal | Qué indica |
|---|---|
| Símbolos exportados en el fichero sin relación de llamada entre sí | Violación real de responsabilidad única |
| Churn: cambia en la mayoría de commits, por motivos distintos cada vez | Candidato a división |
| Número de líneas | Solo informativo. Nunca criterio de decisión |

El diálogo interactivo actual (refactorizar / bypass / abortar, `main.go:457`) se
conserva íntegro; cambia únicamente su disparador: cohesión en lugar de contador.

**El umbral de 400 del guardián se mantiene duro.** Mide una magnitud distinta:
revisibilidad de un *cambio*, que es una propiedad del revisor humano y sí tiene
techo real. Tamaño de fichero y volumen de cambio no comparten umbral porque no
son la misma magnitud.

#### Qué cuenta para el umbral: la frontera es generado vs escrito a mano

El guardián mide **revisabilidad de código**, no bytes. Verificado en el código
actual: nada está exento — escribir 4055 líneas de documentación llevó `check` de
702 a 4701 líneas y frenó el desarrollo. Y peor: un `.md` se clasifica como
`backend` (`ClasificarCapa`, `slice.go:214`), así que un documento de 1792 líneas
disparaba `esCodigoGigante` y `slice` ofrecía **dividirlo con IA aplicando SRP**.

La frontera correcta no es «configuración vs código» —un `docker-compose.yml`
escrito a mano es código que se ejecuta— sino **generado vs escrito a mano**:

| Clase | ¿Cuenta para el umbral? | Umbral de archivo grande | Trato en `slice` |
|---|---|---|---|
| `source` | Sí, **bloquea** | 500 → diálogo de refactorización | lotes normales |
| `test` | Sí, **bloquea** | 500 → diálogo de refactorización | lotes normales |
| `config` escrita a mano | Sí, **bloquea** | 400 → aislar | lote propio |
| `generated` / lock | **No cuenta** | — | lote propio `chore(deps)` |
| `docs` | **No bloquea; se informa** | 400 → aislar | lote propio `docs(...)` |

No es una excepción ad hoc: es la aplicación directa del modelo de riesgo de §9 —
el tamaño no eleva el riesgo por sí solo, y 2000 líneas de configuración generada
siguen siendo riesgo `none`.

Sobre la documentación se elige **avisar, no bloquear**: 1792 líneas de documento
tampoco son revisables de una sentada, y merecen un aviso; pero frenar el
guardián por ellas es fricción sin seguridad a cambio.

Implementado de forma anticipada (`internal/git/clases.go`) porque bloqueaba la
propia redacción de este informe. Ver `docs/reingenieria/f0-deuda.md` → **T0.8**.
La versión configurable por repositorio y la retirada de `ClasificarCapa`
corresponden a F3-T3.1.

---

## 32. Conclusión

VAS Sentinel no necesita una reescritura. Necesita **invertir su relación de
confianza** y **anclar su memoria al contenido en lugar de al commit**.

El sistema ya tiene ledger atómico, degradación tipada, adaptadores con
fallback, análisis de rama, verificación configurable, plantilla honesta y una
cultura de auditoría con regla de evidencia. Lo que le falta no es infraestructura:
es que lo determinista mande, que el revisor pueda ver, y que lo ya sabido no se
recalcule.

Las dos primeras fases del plan (F1 y F2) cambian eso, no dependen de ningún
grafo, y caben en el presupuesto de 400 líneas por unidad de trabajo que el
propio guardián impone.

El objetivo final no es tener más agentes. Es:

> analizar cada cambio con la mínima computación y el mínimo razonamiento
> necesarios para obtener una revisión suficientemente fiable, reproducible,
> trazable e incremental.
