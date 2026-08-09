# F8 — PRs apiladas y revisión global de PR

**Objetivo**: que revisar la PR B no reporte ni bloquee por hallazgos de la PR A.

**Problema que resuelve** (informe §3, M1): `AnalizarRama`
(`internal/review/rama.go:626-637`) usa siempre `merge-base(base, HEAD)` con base
`main` por defecto. Sobre una pila `main → A → B → C`, revisar C contra `main`
arrastra A y B a la matriz y al gate.

**Depende de F2**, no de F5: la reutilización por blob es lo que hace barato
revisar una pila tras un rebase de la base. Puede ejecutarse en paralelo a F3–F7.

**Criterio de salida de la fase**

1. Revisar B sobre A no reporta ni bloquea por hallazgos de A.
2. Rebasar la base de una PR de la pila conserva la revisión de todo lo que no
   cambió.
3. Una PR se revisa como unidad neta, no como suma de sus commits.

> Revalidar la ficha al abrir la fase.

---

## T8.1 — Inferencia de la rama padre

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 260 líneas |
| Depende de | F2 cerrada |
| Commit | `feat(pr): inferir la rama padre para calcular el diff propio` |

**Contexto**: `internal/git/mergebase.go`, `internal/git/commit.go`
(`UpstreamOMain`, :133), informe §21

**Hacer**: precedencia estricta.

```
1. --parent explícito
2. gh pr view --json baseRefName   (si la PR existe)
3. upstream de seguimiento de la rama
4. rama local cuyo merge-base con la actual es el más reciente
5. FALLO EXPLÍCITO
```

**Regla dura**: **nunca `main` por defecto en una pila**. Adivinar mal la base es
reportar cambios ajenos como propios, que es el peor error posible en una
revisión. Es preferible fallar y pedir `--parent`.

**Aceptación**: test contra repositorio real con pila de tres ramas; y test de
que sin señal fiable falla en lugar de asumir `main`.

---

## T8.2 — Diff propio y hallazgos heredados

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 280 líneas |
| Depende de | T8.1 |
| Commit | `feat(pr): diff propio de la rama y hallazgos heredados` |

**Contexto**: `internal/review/rama.go`, informe §21

**Hacer**

```
diff_propio(C) = merge_base(parent(C), C) … C
contexto(C)    = diff acumulado de main…parent(C)   ← solo lectura, no se revisa
```

Los hallazgos sobre líneas fuera del `diff_propio` se marcan `inherited: true`,
**no bloquean** la PR actual, y su corrección corresponde a la PR que los
introdujo.

**Aceptación**: test con pila de tres ramas donde A tiene un `CRITICAL` → la PR
de B lo muestra como heredado y publica.

---

## T8.3 — La PR como unidad neta

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 300 líneas |
| Depende de | T8.2 |
| Commit | `feat(pr): revisar la PR como unidad neta con archivado de hallazgos obsoletos` |

**Contexto**: `internal/review/rama.go`, informe §20

**Hacer**

1. La unidad de revisión de la PR es el **diff neto**, no la secuencia de commits.
2. El historial de findings por commit entra como **contexto**, no como veredicto.
3. Ejes propios de la PR: intención, integración, interacción entre commits,
   regresión neta, contratos, cobertura del comportamiento neto.
4. **Regla explícita**: los hallazgos de commits intermedios sobre código que ya
   no existe en el diff neto **se archivan, no se reportan**.

El punto 4 es la segunda mitad del arreglo de **H5**: hoy un defecto introducido
en el commit 2 y corregido en el 5 bloquea la PR entera.

**Aceptación**: test con rama donde el commit 2 introduce un defecto y el 5 lo
corrige → la PR no lo reporta.

---

## T8.4 — Integración en `pr review` y `pr create`

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 260 líneas |
| Depende de | T8.3 |
| Commit | `feat(pr): integrar pila y unidad neta en los comandos de PR` |

**Contexto**: `cmd/sentinel/comandos_pr.go`, `cmd/sentinel/comandos_pr_test.go`

**Hacer**

1. Flag `--parent` en `pr review` y `pr create`.
2. `--chain-pr` deja de ser solo un permiso para publicar una rama descomunal y
   pasa a declarar la posición en la pila.
3. La matriz distingue visualmente lo propio de lo heredado.
4. Retirar el passthrough legacy de `pr` (informe **O1**) con aviso de
   deprecación, o documentar por qué se conserva.

**No tocar**: el fallback a portapapeles y la limpieza del temporal de plantilla
(`comandos_pr.go:447-472`), que están probados.

**Aceptación**: los tests existentes de `comandos_pr_test.go` que sigan aplicando
pasan; los que cambien se adaptan explicando por qué.
