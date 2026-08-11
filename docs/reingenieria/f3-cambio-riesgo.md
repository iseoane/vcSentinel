# F3 — Modelo de cambio y de riesgo

**Objetivo**: saber qué es un cambio y cuánto riesgo tiene **sin preguntárselo a
un LLM** y sin necesitar todavía un AST.

**Problema que resuelve** (informe §3, C4 y C5): hoy todo se decide con dos
señales — número de líneas y subcadena de la ruta. `ClasificarCapa`
(`internal/git/slice.go:214`) devuelve `test` para cualquier ruta que contenga
esa subcadena (`latest/`, `contest.go`, `testdata/`) y ese valor decide **qué
dimensiones se auditan** (`engine.go:51`) y **cómo se agrupan los commits**
(`plan.go:44`). Entrada incorrecta ⇒ plan incorrecto, en silencio.

**Criterio de salida de la fase**

1. `sentinel explain <rango>` imprime perfil, riesgo y **las reglas concretas que
   lo produjeron**.
2. Un cambio de 2000 líneas en un archivo generado sale con riesgo `none`; uno de
   40 líneas tocando autenticación sale `high`.
3. `ClasificarCapa` ya no decide nada: sustituida por reglas configurables.
4. `slice` agrupa por clústeres de cohesión, no por capa.

---

## T3.1 — Clases de archivo por reglas configurables

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 280 líneas |
| Depende de | F2 cerrada |
| Commit | `feat(change): clases de archivo por reglas de ruta configurables` |

**Contexto**: `internal/git/slice.go:214`, `internal/config/parser.go`, informe §8

**Hacer**: `internal/change/clases.go` con clases
`source | test | config | generated | docs | infra | ci`.

```yaml
change:
  classes:
    test:      ["**/*_test.go", "**/testdata/**", "**/*.spec.ts"]
    generated: ["**/*.pb.go", "**/*_gen.go", "**/*.lock", "go.sum"]
    infra:     ["Dockerfile", "**/*.tf", "infra/**"]
    ci:        [".github/workflows/**", ".gitlab-ci.yml"]
    docs:      ["**/*.md", "docs/**"]
```

Reglas: globs con precedencia por orden de declaración; defaults sensatos por
lenguaje; **primera regla que coincide gana** (determinista y explicable).

También: `.gitattributes linguist-generated` como señal de `generated`.

**Compatibilidad**: `ClasificarCapa` se conserva como envoltorio deprecado
mientras `slice` no migre (T3.7), para que la fase sea reversible por partes.

**Mapa de consumidores de `ClasificarCapa`** (verificado). Muere en tres sitios
distintos, y conviene no darla por retirada antes de tiempo:

| Consumidor | Para qué | Dónde se retira |
|---|---|---|
| `slice.go:83,133` → `ArchivoModificado.Capa` | Agrupar los lotes | **T3.7** (y parcialmente ya en F0-T0.12) |
| `engine.go:65` → `dimensionesPorCapa` | Qué dimensiones se auditan | **F5-T5.2**, bundles por riesgo |
| `comandos_review.go:299` → `calcularBucket` | `Ficha.Bucket` | **F2-T2.6**, al pasar la ficha a índice de trazabilidad |

El tercero es un hueco detectado al revisar el plan: no estaba nombrado en
ninguna ficha. El «bucket» de la ficha lo sustituye el `ChangeProfile` de T3.2.

**Aceptación**: test por tabla con los falsos positivos reales — `latest/x.go`,
`contest.go`, `internal/setup/install_test.go` — y con precedencia entre reglas
solapadas.

---

## T3.2 — ChangeProfile

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 300 líneas |
| Depende de | T3.1 |
| Commit | `feat(change): perfil determinista del cambio` |

**Contexto**: `internal/git/commit.go`, `internal/git/mergebase.go`, informe §8

**Hacer**: `PerfilDeCambio(base, head)` con `size`, `modules`, `file_classes` y
`kind`.

`kind` por precedencia determinista y documentada:

```
generated > dependency > infra > ci_cd > configuration > documentation
          > test_only > refactor > bugfix > feature
```

En esta fase, `symbols` va a cero: llega en F4 con el grafo. **Declararlo así en
el JSON, no omitirlo**: un consumidor debe poder distinguir «cero símbolos» de
«no calculado».

**Aceptación**: test por tabla de `kind` con diffs sintéticos, uno por rama de la
precedencia.

---

## T3.3 — Detectores de características

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 320 líneas |
| Depende de | T3.2 |
| Commit | `feat(change): detectores deterministas de caracteristicas del cambio` |

**Contexto**: `internal/change/*`, informe §8 (tabla de detectores)

**Hacer**: un detector por característica, cada uno **puro** y testeable
aisladamente: `public_api`, `database`, `security_sensitive`, `concurrency`,
`behavior_change`, `test_covered`, `cross_module`, `generated_code`, `ci_cd`,
`infrastructure`.

**Regla dura**: no se admite una característica sin detector. Nada se etiqueta a
ojo.

**Honestidad sobre `public_api` en esta fase**: sin AST, se detecta por
identificadores exportados en las líneas añadidas. Es una **heurística** y debe
marcarse como tal en la salida (`heuristic: true`), para que el modelo de riesgo
sepa que esa señal no es exacta. En F4 se sustituye por la detección real y la
marca desaparece.

**Aceptación**: test por detector con casos positivos y negativos, incluidos los
límites (`test_covered` sin mapa de tests → indeterminado, no falso).

---

## T3.4 — Modelo de riesgo

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 260 líneas |
| Depende de | T3.3 |
| Commit | `feat(risk): riesgo por maximo sobre reglas con explicacion` |

**Contexto**: `internal/change/*`, informe §9

**Hacer**: `internal/risk` con `none | low | standard | elevated | high` como
**máximo sobre reglas**, nunca suma ponderada.

Cada nivel devuelve la regla exacta que lo produjo:

```
risk=high por characteristic=security_sensitive
  (internal/setup/github.go coincide con la regla security.paths)
```

**Regla estructural que invierte el criterio actual**: el tamaño **no** eleva el
riesgo. Eleva la *profundidad* (más contexto, más presupuesto) y dispara la
sugerencia de split. 2000 líneas de código generado siguen siendo `none`.

**No tocar**: el umbral de 400 del guardián. Mide revisibilidad de un cambio, que
es otra magnitud (informe §31.4).

**Aceptación**: test por tabla con los dos casos canónicos del informe — config
generada masiva → `none`; cambio pequeño en autenticación → `high` —, y que todo
nivel devuelve `explain` no vacío.

---

## T3.5 — Cohesión y sugerencia de split

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 300 líneas |
| Depende de | T3.2 |
| Commit | `feat(change): cohesion por clusteres y sugerencia de division` |

**Contexto**: `internal/change/*`, `internal/git/commit.go`, informe §18

**Hacer**: componentes conexas sobre dos señales, sin AST:

1. **Co-cambio histórico**: `git log --name-only` — qué archivos cambian juntos.
2. **Proximidad estructural**: mismo directorio o mismo paquete.

Salida: número de clústeres, puntuación y `suggested_split`.

**Regla inviolable**: se **sugiere**, nunca se aplica. Reescribir historia sin
humano no es aceptable, y la aprobación A/R/E/C sigue siendo el único control del
desbloqueo del guardián (hallazgo B9).

**Aceptación**: test con un commit de tres grupos independientes → 3 clústeres; y
con un cambio cohesivo grande → 1 clúster, sin sugerencia de split.

---

## T3.6 — `sentinel explain`

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 200 líneas |
| Depende de | T3.4, T3.5 |
| Commit | `feat(cli): subcomando explain con perfil, riesgo y sus reglas` |
| Estado | ✅ Completada |

**Contexto**: `cmd/sentinel/main.go` (dispatcher), `cmd/sentinel/comandos_estado.go`

**Hacer**: `sentinel explain [<rango>] [--json]` que imprime perfil,
características con su detector, riesgo con su regla, y cohesión.

Es la herramienta de depuración de toda la fase: sin ella, el modelo de riesgo es
una caja negra y nadie puede rebatirlo.

**Aceptación**: test de la salida `--json` con un cambio sintético conocido.

---

## T3.7 — `slice` agrupa por cohesión

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 280 líneas |
| Depende de | T3.5, T3.6 |
| Commit | `feat(slice): agrupar lotes por cohesion en vez de por capa` |
| Estado | ✅ Completada |

**Contexto**: `internal/git/plan.go` (`ConstruirPlanFragmentacion`, :46),
`internal/git/plan_test.go`

**Hacer**

1. El criterio de agrupación pasa de capa fija (`config → backend → frontend →
   test`) a clúster de cohesión.
2. Dentro de cada clúster se mantiene el orden por clase de archivo, para que el
   commit siga leyéndose de config hacia test.
3. El límite de 400 líneas por lote **no cambia**.

**Conservar íntegro**: el diálogo A/R/E/C, el trato de archivos gigantes, y el
`--no-verify` con su justificación (`plan.go:167-173`).

**Riesgo**: `plan_test.go` tiene 731+ líneas de cobertura sobre el criterio
actual. Espera tener que adaptarlos: **adaptar el test al criterio nuevo es
legítimo; borrarlo no lo es.** Si un test deja de tener sentido, explica por qué
en el informe.

**Aceptación**: los lotes resultantes siguen siendo ≤400 líneas y ningún clúster
queda partido entre dos lotes salvo por el límite de tamaño.
