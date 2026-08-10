# F2 — Contrato de finding y store por contenido

**Objetivo**: findings con evidencia verificable, y una memoria que sobreviva al
rebase.

**Problema que resuelve** (informe §3, C3 e I4): el ledger se indexa por SHA
(`internal/review/ledger.go:926`), así que cualquier `rebase`, `amend` o `squash`
—el flujo normal de una rama de agente— invalida el 100 % de las revisiones
aunque el contenido no haya cambiado una línea. Y el finding actual
(`finding.go:286`) no tiene `source`, `confidence` ni `evidence`, así que un
falso positivo no se puede refutar sin leer el código a mano.

**Criterio de salida de la fase**

1. Un `git rebase` que no altera contenido conserva el **100 %** de los findings.
   Test de regresión que lo demuestre contra un repositorio real.
2. Un finding sin `evidence` verificable contra su blob **se descarta en el
   parseo**, con el descarte registrado.
3. Las fichas v1 existentes se siguen leyendo y se reindexan sin pérdida.
4. Store, snapshots y grafo comparten el common-dir; los ledgers privados por
   worktree se migran.

---

## T2.1 — Finding v2

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 280 líneas |
| Depende de | F1 cerrada |
| Commit | `feat(review): contrato de finding v2 con evidencia y confianza` |

**Contexto**

- `internal/review/finding.go` (completo)
- `internal/review/finding_test.go`
- Informe §14

**Hacer**: estructura nueva conviviendo con la v1.

```go
type Hallazgo struct {
    ID           string
    Source       string   // "validation" | "review"
    Producer     Productor // agente, binario, modelo, esfuerzo, modelo_verificado
    Dimension    string
    Severity     string
    Confidence   float64
    Status       string   // pending|confirmed|refuted|accepted_by_user|fixed|reopened
    Title        string
    Description  string
    Location     Ubicacion // file, blob, line_start, line_end, symbol
    Evidence     string    // cita literal del código
    Impact       string
    Recommendation string
    Fixable      string    // safe | needs_review | manual
    IntroducedBy string
    Fingerprint  string
}
```

Un finding de validación rellena `Source: "validation"`, `Confidence: 1.0`,
`Producer.Comando`, y `Evidence` con la salida real del comando.

**No tocar**: `Linea.UnmarshalJSON` (`finding.go:64`) — la tolerancia a línea como
número o como string sigue siendo necesaria.

**Aceptación**: test de serialización ida y vuelta, y test de que una ficha v1 se
deserializa a v2 con los campos nuevos vacíos.

---

## T2.2 — Rechazo de findings sin evidencia verificable

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 220 líneas |
| Depende de | T2.1 |
| Commit | `feat(review): descartar hallazgos cuya evidencia no existe en el blob` |

**Contexto**: `internal/review/finding.go`, `internal/git/commit.go`

**Por qué**: es el filtro más barato y más eficaz contra las alucinaciones
documentadas en **H5**. Un hallazgo que cita código que no aparece literalmente
en el blob referenciado es falso por comprobación mecánica, sin necesidad de un
modelo.

**Hacer**

1. Descartar el finding si: falta `Evidence`, la ubicación no resuelve a un
   archivo del cambio, o `Evidence` no aparece en el contenido del blob.
2. La comparación normaliza espacios en blanco y finales de línea — un LLM que
   reindenta al citar no debe fallar el filtro.
3. Cada descarte se registra con motivo. **Descartar el finding nunca invalida la
   ejecución completa.**

**Aceptación**

- Test: evidencia inventada → descartado con motivo.
- Test: evidencia real con indentación distinta → aceptado.
- Test: tres findings, uno inválido → los otros dos sobreviven.

---

## T2.3 — Fingerprint estable

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 180 líneas |
| Depende de | T2.1 |
| Commit | `feat(review): huella estable de hallazgo para deduplicar y reabrir` |

**Contexto**: `internal/review/finding.go`

**Hacer**: `sha256(dimension | símbolo o ruta | evidencia normalizada | regla)`.

Propiedades exigidas, cada una con su test:

| Propiedad | Test |
|---|---|
| Estable ante reindentado y cambio de línea | mismo fingerprint |
| Estable ante renumeración por edición en otra parte del archivo | mismo fingerprint |
| Distinto para el mismo defecto en dos símbolos | fingerprints distintos |
| Distinto para dos defectos en el mismo símbolo | fingerprints distintos |

---

## T2.4 — Parseo del contrato con compatibilidad v1

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 240 líneas |
| Depende de | T2.1, T2.2 |
| Commit | `feat(review): parseo del contrato v2 con compatibilidad v1` |

**Contexto**: `internal/review/finding.go` (`ParsearDimensionResult`, :328),
`internal/review/multilinea_test.go`, `linea_flexible_test.go`

**Hacer**: aceptar los campos nuevos manteniendo intacta toda la tolerancia
existente — delimitadores `BEGIN_REVIEW`/`END_REVIEW`, JSON multilínea
pretty-printed, normalización de severidades y de veredictos de facto.

**No tocar**: la normalización de veredictos (`veredictoFinal`, :459). Su
comportamiento está descrito en la guía §5 y cubierto por tests.

**Los prompts NO se tocan en esta fase**: se actualizan en F5, junto con el
contexto acotado. Aquí solo se amplía el parseo.

**Aceptación**: todos los tests de parseo existentes pasan sin modificarse.

---

## T2.5 — Layout del store por contenido

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 320 líneas |
| Depende de | T2.3 |
| Commit | `feat(store): almacen indexado por contenido en el common-dir` |

**Contexto**: `internal/review/ledger.go` (completo), informe §23

**Hacer**: `internal/store`

```
<repo>/.git/vas-sentinel/
    units/<unit-id>.json          unit-id = hash(base_tree, head_tree, plan_id)
    runs/<run-id>.json
    findings/<fingerprint>.json
    commits/<sha>.json            índice de trazabilidad
    decisions.jsonl               append-only
```

**Reutilizar tal cual**: la escritura atómica temp+rename con el caso Windows
resuelto (`ledger.go:1038`). Está probada y es correcta.

**Aceptación**: tests de atomicidad equivalentes a los del ledger actual, sobre
el layout nuevo.

---

## T2.6 — Migración al common-dir y desde v1

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 260 líneas |
| Depende de | T2.5 |
| Commit | `feat(store): migrar fichas per-worktree y v1 al almacen compartido` |

**Contexto**: `internal/git/gitdir.go`, `internal/store/*`, informe §31.3

**Dos migraciones distintas**

1. **De ubicación**: hoy el ledger usa `ObtenerGitDir`, que en worktrees
   enlazados es el directorio privado. Si existe
   `.git/worktrees/<n>/vas-sentinel/` con fichas, se trasladan al compartido.
2. **De formato**: `<sha>.json` v1 → `commits/<sha>.json` + `findings/<fp>.json`.

**Además**: retirar `calcularBucket` (`cmd/sentinel/comandos_review.go:297`), que
hoy deriva `Ficha.Bucket` de `ClasificarCapa` y es su tercer consumidor. En el
índice de trazabilidad el «bucket» no aporta: lo sustituye el `ChangeProfile`
(F3-T3.2). Mientras F3 no exista, basta con conservar el campo sin recalcularlo.

**Reglas**

- Idempotente: ejecutarla dos veces no duplica nada.
- **No borra nada.** Una ficha v1 migrada se conserva hasta que el usuario purgue
  explícitamente.
- Un archivo corrupto es error explícito, nunca borrado silencioso — como ya hace
  `LeerFicha` (`ledger.go:931`).

**Aceptación**: test con ledger v1 poblado + worktree enlazado con fichas
privadas; tras migrar, todo accesible desde el compartido y nada perdido.

---

## T2.7 — Reutilización por blob y regresión de rebase

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 240 líneas |
| Depende de | T2.6 |
| Commit | `feat(store): reutilizar hallazgos por blob y sobrevivir al rebase` |

**Contexto**: `internal/store/*`, `internal/review/rama.go`

**Hacer**

1. `YaRevisado(blob) → []Hallazgo` como consulta primaria de incrementalidad.
2. Reglas de invalidación del informe §17.2:

| Cambio | Efecto |
|---|---|
| Blob idéntico | Reutilizar íntegro |
| Blob distinto | Marcar `stale`, re-revisar |
| Cambia `plan_id`, modelo o versión de skill | Invalida solo lo semántico |

3. `AnalizarRama` consulta por blob antes que por SHA.

**Aceptación (criterio de salida de la fase)**

> Test contra repositorio real: auditar tres commits, hacer `git rebase` que
> reescribe los SHAs sin alterar el contenido, y comprobar que **cero** commits
> necesitan re-revisión.
