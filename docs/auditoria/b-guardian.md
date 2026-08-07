# Grupo B — El guardián: `check` y `slice`

Es el núcleo del proyecto. Si este grupo falla, VAS Sentinel no cumple su
propósito. Analizado con la ficha de cinco preguntas de `README.md`.

**Veredicto del grupo: 🔴 DEFECTO.** `check` es ciego a los archivos nuevos sin
stagear, que es la vía por la que un agente de IA acumula código con más
frecuencia.

---

## `check`

### 1. Trigger

Tres vías, y las tres importan:

- El usuario, a mano.
- El hook `pre-commit` instalado en `<git-common-dir>/hooks/pre-commit`, con la
  ruta absoluta del binario. Se ejecuta en **cada commit** del repositorio.
- La regla de volumen inyectada en `AGENTS.md`/`CLAUDE.md`/`.claudecode.md`,
  que obliga al agente de IA a ejecutarlo antes de escribir código.

### 2. Cuándo y por qué

Antes de escribir código y antes de cada commit. La premisa del proyecto es que
un agente de IA acumula cambios sin darse cuenta hasta producir un diff
irrevisable; `check` es la medición que dispara el freno.

### 3. Objetivo

Contar las líneas pendientes del worktree y clasificarlas:

| Rango | Estado | Salida |
|---|---|---|
| < 200 | `PEQUENO` | 0 |
| 200–400 | `PUNTO_OPTIMO` | 0 |
| > 400 | `CRITICO` | 1 |

Exit 1 en `CRITICO` es lo que aborta el commit desde el hook.

### 4. ¿El código es correcto? — 🔴 DEFECTO

#### B1 · CRITICAL · `check` no ve los archivos nuevos sin stagear

`CheckDiffLimits` mide con `git diff HEAD` (`internal/git/diff.go:10`). Ese
comando **no incluye los archivos sin rastrear**. Un archivo nuevo es invisible
para el guardián hasta que alguien hace `git add`.

Reproducido en este repositorio con un archivo nuevo de 3001 líneas:

```
📊 Líneas modificadas en este Worktree: 0 [PEQUENO]
✅ Volumen bajo control. Puedes continuar.        (exit 0)
```

Tras `git add -N` sobre el mismo archivo, sin tocar el contenido:

```
📊 Líneas modificadas en este Worktree: 3001 [CRITICO]
⛔ ¡Peligro! El volumen supera las 400 líneas.    (exit 1)
```

El mismo estado del disco produce veredictos opuestos según el índice de git.

Por qué es la peor variante posible del fallo: crear archivos nuevos es
precisamente lo que hace un agente de IA cuando genera código. El guardián
protege el caso menos frecuente (editar lo existente) y deja pasar el más
frecuente. El hook `pre-commit` hereda la ceguera: el commit solo se examina
cuando ya se ha hecho `git add`, y en ese instante sí mide — pero el agente ha
podido escribir 3000 líneas nuevas con la bendición explícita de `check`.

#### B2 · CRITICAL · `check` y `slice` no miden lo mismo

`slice` sí cuenta los archivos nuevos: `ObtenerArchivosModificados`
(`internal/git/slice.go:38-48`) suma `archivosRastreados` **y**
`archivosNoRastreados`, este último vía `git status --short -uall`
(`slice.go:79`).

Las dos mitades del guardián discrepan sobre qué es "el volumen pendiente".
Consecuencias directas:

- `check` dice `PEQUENO` y `slice` encuentra miles de líneas que fragmentar.
- La nota de `CLAUDE.md` que justifica el `--no-verify` de los commits de slice
  («el hook mide el total pendiente») parte de una premisa que solo es cierta
  para los archivos rastreados.

#### B3 · WARNING · `strings.Fields` rompe las rutas con espacios

`archivosRastreados` parsea el numstat con `strings.Fields` y toma `campos[2]`
como ruta (`internal/git/slice.go:63-71`). Una ruta con espacios se parte en
varios campos y solo se conserva el primer fragmento.

Verificado: `git diff HEAD --numstat` devuelve `1\t0\tarchivo con espacios.go`,
que `campos[2]` reduce a `archivo`. `slice` intentaría commitear una ruta
inexistente. Afecta también a los renombrados, que el numstat emite como
`old => new`.

#### B4 · WARNING · Los umbrales están duplicados como literales

`200` y `400` están escritos a mano en `clasificarEstado`
(`internal/git/diff.go:31-38`), mientras `slice.go:17-21` define
`limiteLineasLote = 400`, `limiteConfigGigante = 400` y
`LimiteCodigoGigante = 500`. El mismo 400 vive en dos sitios sin relación entre
ellos: cambiar el umbral del guardián no cambia el tamaño de los lotes, y nada
lo señala.

#### B5 · ADVISORY · El mensaje dice "modificadas" pero solo cuenta añadidas

`contarLineasAnadidas` (`diff.go:21-29`) ignora las líneas borradas, pero la
salida las llama «Líneas modificadas». Para el propósito del guardián (frenar
la acumulación) contar solo altas es defendible; el texto debería decirlo.

#### B6 · ADVISORY · `check` acepta argumentos y los ignora en silencio

`sentinel check --loquesea` sale 0 sin protestar: el dispatcher
(`cmd/sentinel/main.go:54-56`) no pasa ni valida `os.Args[2:]`. Es la
confirmación de **H1** para este comando.

### Lo que sí está bien

- La clasificación por rangos es correcta en los bordes: 400 es `PUNTO_OPTIMO`
  y 401 es `CRITICO`, coherente con la regla documentada. Cubierto por
  `TestClasificarEstado` (`internal/git/diff_test.go:38`).
- `TestCheckDiffLimitsEnRepositorioReal` (`diff_test.go:62`) prueba contra un
  repositorio git de verdad, no contra un mock. Buena decisión — pero **solo
  cubre archivos rastreados**, y por eso B1 pasa verde.
- El exit 1 en `CRITICO` funciona y es lo que hace útil al hook.

### 5. ¿Cómo se mejora?

| Prioridad | Cambio | Coste | Riesgo |
|---|---|---|---|
| 1 | **B1+B2**: que `CheckDiffLimits` reutilice `ObtenerArchivosModificados` y sume `Lineas`. Una sola fuente de verdad para el volumen; los dos defectos se cierran juntos | Bajo — la función ya existe y está probada | Bajo. Sube el recuento en worktrees con archivos nuevos, que es exactamente lo que se busca |
| 2 | **B1**: test de regresión con un archivo nuevo sin stagear que exija `CRITICO`. Sin él, el defecto vuelve | Bajo | Ninguno |
| 3 | **B3**: parsear el numstat con `-z` y separar por `NUL`, o cortar por los dos primeros tabuladores en vez de por espacios | Bajo | Bajo |
| 4 | **B4**: mover los umbrales a constantes compartidas del paquete `git` | Bajo | Ninguno |
| 5 | **B5**: cambiar el texto a «Líneas añadidas» | Trivial | Ninguno |
| 6 | **B6**: rechazar argumentos desconocidos en los subcomandos sin flags | Bajo | Bajo. Rompe scripts que hoy pasan basura y no se enteran |

---

## `slice`

### 1. Trigger

El usuario, tras un `CRITICO`. La regla de volumen de `CLAUDE.md` lo declara
obligatorio: invocar `slice` **es** el desbloqueo del guardián.

### 2. Cuándo y por qué

Cuando el volumen pendiente supera las 400 líneas y hay que partirlo en commits
revisables antes de seguir escribiendo.

### 3. Objetivo

Construir un plan por capas en orden fijo `config → backend → frontend → test`
con lotes de ≤400 líneas, generar los mensajes con el adaptador configurado,
mostrar el plan para aprobación (A/R/E/C) y **no commitear nada** hasta que el
usuario apruebe. Los commits resultantes usan `--no-verify`.

### 4. ¿El código es correcto? — 🟡 PARCIAL

Comparte **B2** y **B3** con `check`; ambos nacen en `slice.go`.

#### B7 · Analizado sin ejecutar — límite de esta auditoría

El flujo de `slice` es **interactivo**: exige aprobación A/R/E/C y confirmación
`s/N` para los archivos gigantes. No pude ejercitarlo de extremo a extremo desde
una sesión no interactiva, así que la ejecución real del plan
(`EjecutarPlanFragmentacion`) queda **verificada solo por lectura y por sus
tests**, no por ejecución. Marcado como DUDA, no como VERIFICADO, conforme a la
regla de evidencia.

#### Lo que sí se puede afirmar

- Los límites están declarados como constantes con nombre
  (`slice.go:17-21`), a diferencia de los de `check`.
- El bypass de archivos gigantes de código exige confirmación explícita y
  aborta sin commitear si se rechaza (`plan.go:68`).
- `plan.go:171-173` documenta por qué los commits llevan `--no-verify`. El
  razonamiento es correcto **dado B2**: si `check` y `slice` midieran lo mismo,
  convendría revisar si el bypass sigue siendo necesario.
- Cobertura de tests amplia en `plan_test.go` (731+ líneas).

### 5. ¿Cómo se mejora?

| Prioridad | Cambio | Coste | Riesgo |
|---|---|---|---|
| 1 | **B3**: arreglar el parseo del numstat (compartido con `check`) | Bajo | Bajo |
| 2 | **B7**: flag `--dry-run` o `--yes` que permita ejercitar el plan sin interacción. Hoy no hay forma de probar `slice` en CI ni desde un agente | Medio | Bajo |
| 3 | Revisar el `--no-verify` una vez cerrado B2: con una medición unificada, el hook dejaría de rechazar lotes legítimos y el bypass podría sobrar | Bajo | Medio — tocar el desbloqueo del guardián merece su propio análisis |

---

## Resumen de hallazgos

| ID | Severidad | Comando | Estado |
|---|---|---|---|
| B1 | 🔴 CRITICAL | `check` | Confirmado por ejecución |
| B2 | 🔴 CRITICAL | `check` + `slice` | Confirmado por lectura de código |
| B3 | 🟠 WARNING | `check` + `slice` | Confirmado por ejecución de git |
| B4 | 🟠 WARNING | `check` | Confirmado por lectura de código |
| B5 | 🔵 ADVISORY | `check` | Confirmado por lectura de código |
| B6 | 🔵 ADVISORY | `check` | Confirmado por ejecución (confirma H1) |
| B7 | 🟡 DUDA | `slice` | No verificable sin sesión interactiva |
