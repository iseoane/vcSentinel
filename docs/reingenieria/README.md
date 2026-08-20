# Reingeniería de VAS Sentinel — Plan general

Plan de ejecución del replanteamiento arquitectónico descrito en
[`docs/arquitectura/replanteamiento-objetivo.md`](../arquitectura/replanteamiento-objetivo.md),
que es la **fuente de verdad del diseño**. Este directorio es la fuente de verdad
de la **ejecución**: qué se hace, en qué orden y con qué criterio de cierre.

Cada tarea está dimensionada para ser ejecutada por **un subagente Sonnet con
esfuerzo `high`**, con contexto acotado y presupuesto de líneas contenido.

---

## 1. Estado de partida (verificado)

| Hecho | Evidencia |
|---|---|
| `go test ./...` en verde | Ejecutado: 8 paquetes `ok` |
| `internal/git` tarda ~57 s | Ejecutado. Condiciona el bucle por tarea |
| `sentinel check` → **702 líneas, CRÍTICO** | Correcciones B1/B2/B3/B9 sin commitear |
| El hook apunta a `bin/0.2.0/sentinel.exe` | `.git/hooks/pre-commit` |
| No hay instalación global | `~/.vas_sentinel/bin/` no existe |

---

## 2. Riesgo operativo nº 1: el guardián se autodestruye

El hook `pre-commit` ejecuta `bin/0.2.0/sentinel.exe`, que es **la misma ruta que
`build.bat` regenera**. Consecuencia: cualquier tarea que rompa la compilación o
el comportamiento de `check` deja el repositorio **sin poder commitear**, incluso
para arreglar el fallo.

**Mitigación obligatoria antes de empezar (tarea T0.0):** separar el guardián
estable del binario en desarrollo.

```
~/.vas_sentinel/bin/sentinel.exe   guardián ESTABLE — al que apunta el hook
<repo>/bin/<version>/sentinel.exe  candidato en desarrollo — se rompe sin consecuencias
```

Promoción explícita al cerrar cada fase, nunca automática:

```
copiar bin/<version>/sentinel.exe  →  ~/.vas_sentinel/bin/sentinel.exe
```

Si el candidato no supera la puerta de fase, el guardián estable no se toca.

---

## 3. Contrato de ejecución para subagentes

Todo prompt de tarea hereda estas reglas. **No se repiten en cada ficha.**

### 3.1 Alcance

1. Lee **únicamente** los archivos listados en «Contexto». Si necesitas otro,
   detente y decláralo en el informe final en lugar de ampliarlo por tu cuenta.
2. No toques archivos fuera de «Archivos a modificar».
3. No refactorices de paso. Un hallazgo colateral se reporta, no se arregla.
4. Si el diseño de la ficha resulta inviable al implementarlo, **para y explica
   por qué**. No improvises una arquitectura alternativa.

### 3.2 TDD estricto (obligatorio en este repositorio)

1. Escribe primero el test que falla.
2. Ejecútalo y comprueba que **falla por la razón correcta**.
3. Implementa el mínimo para que pase.
4. Refactoriza solo dentro del alcance de la tarea.

Un test que nunca se vio fallar no es evidencia de nada.

### 3.3 Convenciones del repositorio

| Elemento | Convención | Ejemplo real del repo |
|---|---|---|
| Identificadores | **Castellano** | `ObtenerArchivosModificados`, `ConstruirPlanFragmentacion` |
| Comentarios | **Castellano**, explicando el *porqué* | ver `internal/git/plan.go:167-173` |
| Vocabulario de dominio del contrato | Inglés fijo | `logic`, `security`, `CRITICAL`, `block` |
| Textos de UI | Castellano estándar, **sin voseo** | «Puedes», nunca «Podés» |
| Mensajes de commit | Conventional Commits en castellano, **sin tildes** | `fix(config): no partir nombres de perfil con punto` |
| Atribución de IA en commits | **Prohibida.** Sin `Co-Authored-By` | — |

Multiplataforma obligatorio: `filepath.Join`, `filepath.ToSlash` al pasar rutas a
git, nada de concatenar `/`.

### 3.4 Presupuesto

Cada ficha declara un presupuesto de líneas. **El techo absoluto es 400** (regla
del guardián). Si al implementar ves que te pasas:

1. Para.
2. Informa de qué parte se sale y por qué.
3. No partas la tarea por tu cuenta: la partición la decide el orquestador.

### 3.5 Informe final del subagente

Formato fijo, breve:

```
ESTADO: completado | bloqueado | parcial
ARCHIVOS: <lista de los modificados>
LINEAS: <salida de sentinel check>
TESTS: <comando ejecutado y resultado>
DESVIACIONES: <qué se separó de la ficha y por qué, o "ninguna">
HALLAZGOS COLATERALES: <lo que viste y NO arreglaste, o "ninguno">
```

---

## 4. Bucle de dogfooding

Sentinel se desarrolla con Sentinel. El bucle es distinto **por tarea** y **por
fase**, porque la suite completa tarda demasiado para ejecutarla en cada paso.

### 4.1 Por tarea (lo ejecuta el subagente)

```
1. go build ./... && go vet ./...
2. go test ./internal/<paquete-tocado>/...      ← solo el paquete afectado
3. ~/.vas_sentinel/bin/sentinel check            ← guardián ESTABLE
      > 400  → parar e informar; NO ejecutar slice por tu cuenta
4. informe final
```

El subagente **no commitea**. Commitear implica aprobar un plan de fragmentación
y esa aprobación es el único control humano del desbloqueo del guardián
(hallazgo B9). La ejecución de `slice` la conduce el orquestador con el usuario.

### 4.2 Por tarea (lo ejecuta el orquestador tras el informe)

**Hasta T0.10** — diálogo interactivo:

```
5. revisar el diff
6. sentinel slice        → aprobar plan (A/R/E/C)  ← control humano por stdin
7. sentinel review HEAD  → ficha en el ledger
```

**Desde T0.10** — conducido por el orquestador, con la decisión en el usuario:

```
5. revisar el diff
6. sentinel slice plan --json
      exit 0 → no hay nada que decidir
      exit 3 → trasladar las preguntas AL USUARIO y recoger sus respuestas
7. sentinel slice apply --plan plan.json --answers respuestas.json
8. sentinel gate --stage pre-push
```

La decisión humana **no desaparece**: cambia de transporte. `slice plan` es
read-only y seguro de ejecutar en bucle; `apply` exige respuesta explícita a cada
decisión pendiente, ligada al `plan_id` y al estado del árbol. Un `--yes` de
autoaprobación sigue descartado (`docs/auditoria/b-guardian.md`).

Esto es un multiplicador para el resto del plan: sin ello, cada una de las ~55
tareas se detiene en el commit esperando una sesión interactiva.

A partir de **F1**, el paso 7 pasa a ser `sentinel gate --stage pre-push`, que ya
ejecuta validación antes que revisión.

### 4.2.1 Trampa del dogfooding: recompilar antes de fragmentar

**Si la tarea modificó la lógica de `slice`, `check` o las clases de archivo,
recompila e instala el binario ANTES de fragmentar.** El binario que ejecuta la
fragmentación es el mismo que estamos cambiando: usar el anterior aplica el
criterio viejo y produce commits que la tarea acababa de arreglar.

Ocurrió de verdad al cerrar T0.12: se lanzó `slice` con el binario previo al
cambio y hubo que abortarlo antes de que commiteara con el agrupado antiguo.

```
gofmt -w . && go vet ./... \
  && go build -ldflags="-s -w -X main.version=<version>" -o bin/<version>/sentinel.exe ./cmd/sentinel \
  && cp bin/<version>/sentinel.exe ~/.vas_sentinel/bin/sentinel.exe
```

Nota: `build.bat` no es invocable desde Git Bash con `cmd //c`; estos son sus
mismos pasos.

### 4.3 Puerta de fase

```
1. go build ./... && go vet ./... && go test ./...     ← suite completa
2. build.bat  (o ./build.sh)                          ← candidato
3. criterios de salida de la fase, uno por uno, con evidencia
4. sentinel pr review --base main                     ← matriz de la fase
5. promover el candidato al guardián estable
6. actualizar la tabla de progreso de este documento
```

Una fase no se cierra con «parece que funciona». Cada criterio de salida lleva su
evidencia: el test que lo cubre, la ejecución que lo demuestra o la ruta de
código exacta. Es la misma regla de evidencia de `docs/auditoria/README.md`.

---

## 5. Fases

| # | Fase | Objetivo en una línea | Ficha | Depende de |
|---|---|---|---|---|
| F0 | Deuda abierta | Dejar la base limpia y el guardián a salvo | [`f0-deuda.md`](f0-deuda.md) | — |
| F1 | Invertir el gate | Que lo determinista bloquee y lo semántico avise | [`f1-gate.md`](f1-gate.md) | F0 |
| F2 | Contrato y store | Findings con evidencia; memoria por contenido | [`f2-contrato-store.md`](f2-contrato-store.md) | F1 |
| F3 | Cambio y riesgo | Saber qué es el cambio sin preguntárselo a un LLM | [`f3-cambio-riesgo.md`](f3-cambio-riesgo.md) | F2 |
| F4 | Grafo e incremental | Validar solo lo afectado, con garantía | [`f4-grafo-incremental.md`](f4-grafo-incremental.md) | F3 |
| F5 | Planner y agentes | Decidir cuánta revisión merece cada cambio | [`f5-planner-agentes.md`](f5-planner-agentes.md) | F4 |
| F6 | Agregador | Un defecto, un hallazgo | [`f6-agregador.md`](f6-agregador.md) | F5 |
| F7 | Remediación | Corregir sin refactorizar de paso | [`f7-remediacion.md`](f7-remediacion.md) | F6 |
| F8 | PRs apiladas | Revisar lo tuyo, no lo de la PR de abajo | [`f8-pr-apilados.md`](f8-pr-apilados.md) | F2 |
| F9 | Observabilidad | Saber qué aporta valor antes de afinarlo | [`f9-observabilidad.md`](f9-observabilidad.md) | F6 |

```
F0 ─► F1 ─► F2 ─► F3 ─► F4 ─► F5 ─► F6 ─► F7
             │                        │
             └──────► F8              └──────► F9
```

**F1 y F2 son las de mayor retorno.** Si el plan se detuviera ahí, el sistema ya
tendría el gate correcto y una incrementalidad que sobrevive al rebase, sin haber
construido nada del grafo.

### Nota de honestidad sobre F5–F9

Las fichas de F0–F4 están dimensionadas contra el código actual y son ejecutables
tal cual. Las de F5–F9 describen alcance y criterio de salida correctos, pero sus
presupuestos y su partición **se revalidan al abrir la fase**, contra lo que las
fases anteriores hayan producido realmente. Planificar hoy el detalle de F9 sería
ficción.

---

## 6. Progreso

Leyenda: ⬜ pendiente · 🔄 en curso · ✅ cerrada con evidencia

| Fase | Estado | Cierre | Notas |
|---|---|---|---|
| F0 | ✅ | T0.0–T0.13 | Todas cerradas: T0.0, T0.1 (13 commits), T0.2, T0.3, T0.4, T0.5, T0.6, T0.7, T0.8, T0.9, T0.10, T0.11, T0.12, T0.13 |
| F1 | ✅ | `ac455be` | T1.1–T1.8 cerradas (`5c728a5`..`f2dbc21`) + 4 correcciones de una revisión semántica de rama post-cierre (`8ff1b3c`, `b5bad54`, `e48f0a5`, `ac455be`), incluidos 2 CRITICAL de seguridad. Ver desviaciones abajo. |
| F2 | ✅ | `a1cd03b` | T2.1–T2.7 cerradas (`f35e1be`..`0ff43bf`) + 3 correcciones post-cierre (`b446df1`/`a200046`, `a1cd03b`): la más importante, los findings reales se perdían al reutilizar por blob tras un rebase — corregido con `Ledger.AdoptarFicha`. Ver desviaciones en `f2-contrato-store.md`. |
| F3 | ✅ | `3a9632a` | Registro retroactivo: la tabla no se había actualizado. T3.1–T3.7 cerradas con commits reales (`66c680a`..`3a9632a`), con mensajes distintos a la plantilla original de la ficha pero equivalentes en alcance. No hay evidencia registrada de que se ejecutara la puerta de fase formal (§4.3) en su momento; este cierre se respalda solo en el código y los commits existentes. |
| F4 | ✅ | `fc9cb82` | T4.1–T4.5 y T4.7 cerradas con evidencia verificada contra el código real. T4.6 (CodeGraph opcional) tuvo 2 rondas de corrección tras revisión semántica: `627430d` bloqueada por 3 críticos (contexto no ligado al commit, prompt injection vía texto crudo del repo, dependencia invertida `review`→`graph`), corregida en `fc80f96`; una segunda ronda dejó abierta una pregunta sobre presupuesto de timeout compartido entre subprocesos, resuelta en `5187fb0` (timeout independiente de 3s por subproceso, decisión del usuario). El criterio de salida "reducción medible de tiempo" no tenía evidencia (`grep "func Benchmark"` daba 0 resultados) y el de "sin GraphProvider valida completo" solo estaba cubierto de forma indirecta; ambos se cerraron en `fc9cb82` con `BenchmarkResolverComando_AlcanceParcialVsCompleto` (contra el propio repo, con el proveedor nativo real) y `TestEjecutarPerfilSobreCandidato_SinGraphProviderUsaCommandCompleto`. `sentinel pr review --base main` no aplica: todo el trabajo se comiteó directo a `main`, sin rama de feature. Guardián estable promovido en `~/.vas_sentinel/bin/sentinel`. |
| F5 | ✅ | `91dc65f` | T5.1–T5.8 ya estaban implementadas; lo que quedaba abierto era el `sentinel gate --stage pre-push` final, bloqueado por infraestructura del revisor OpenCode. La causa real (aislar `HOME` cortaba el acceso a `auth.json`) no era la diagnosticada antes ("fallo del proveedor"); corregida en `1c24f47` junto con un bug de parseo de `confidence` categórico. La revisión semántica ya en verde encontró y bloqueó 3 `CRITICAL` de seguridad reales en las 3 rondas siguientes de corrección (`bf14f4b`, `9fbaea0`, `0953311`): exposición del `auth.json` multi-proveedor completo, filtrado inconsistente entre fuente heredada y de archivo, y `XDG_DATA_HOME` sin aislar como fallback. Follow-up de cobertura de test cerrado en `91dc65f`. Ver desviaciones en `f5-planner-agentes.md`. |
| F6 | ✅ | `a883cac` | T6.1–T6.5 implemented; aggregation, deterministic supersede, cause correlation, five-state gate vocabulary, and aggregate rendering covered; historical review on `a883cac` was ok; current `go build ./...`, `go vet ./...`, `go test ./...`, guardian, and `sentinel gate --stage pre-push` passed on `aaf21f4`. |
| F7 | ✅ | `802e7e1` | T7.1–T7.6 closed; bounded remediation, one-round revalidation, and human-decision persistence are evidenced, and the final pre-push gate passed. |
| F8 | ⬜ | — | |
| F9 | ⬜ | — | |

Al cerrar una fase: marcar ✅, anotar el hash de cierre, y registrar en su ficha
las desviaciones respecto al diseño. Las desviaciones se documentan en la ficha
de la fase, no se ocultan.
