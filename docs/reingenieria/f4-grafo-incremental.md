# F4 — Grafo de código y validación incremental

**Objetivo**: ejecutar solo las validaciones afectadas, **con garantía**. Es el
mayor ahorro de tiempo del sistema y también donde es más fácil romper la
confianza del gate.

**Regla que gobierna toda la fase**

> Si el grafo no es completo y verificable para **todos** los archivos cambiados,
> el conjunto de validaciones es el completo. La optimización de tiempo nunca
> degrada la confianza del gate.

**Criterio de salida de la fase**

1. El tiempo de `gate --stage pre-push` en un cambio de un solo paquete baja de
   forma medible respecto a la validación completa.
2. Existe un test que fuerza `graph=incomplete` y **comprueba que se ejecuta la
   validación completa**. Sin ese test, la fase no cierra.
3. `public_api` deja de ser heurística: se detecta con símbolos reales.
4. Sin proveedor de grafo, el sistema funciona igual y valida completo.

---

## T4.1 — Interfaz `GraphProvider` y contrato de completitud

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 220 líneas |
| Depende de | F3 cerrada |
| Commit | `feat(graph): interfaz de proveedor de grafo con contrato de completitud` |

**Contexto**: informe §6 y §31.2

**Hacer**: `internal/graph`

```go
type GraphProvider interface {
    Nombre() string
    Paquetes(rutas []string) ([]Paquete, error)
    Importadores(paquete string) ([]string, error)
    TestsDe(paquete string) ([]string, error)
    Completitud(rutas []string) Completitud
}

type Completitud struct {
    Completo bool
    Motivo   string   // "reflexion_detectada", "lenguaje_sin_proveedor", ...
    NoCubiertos []string
}
```

**El campo decisivo es `Completitud`**, no el grafo. Es lo que autoriza o prohíbe
acotar. Un proveedor que no lo puede afirmar devuelve `Completo: false` — nunca
un optimista por defecto.

**Aceptación**: proveedor doble en tests que devuelve incompletitud y verificación
de que nadie puede acotar con él.

---

## T4.2 — Proveedor `native` con `go/packages`

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 320 líneas |
| Depende de | T4.1 |
| Commit | `feat(graph): proveedor nativo de Go con go/packages` |

**Contexto**: `internal/graph/*`, `go.mod`

**Hacer**

1. Añadir `golang.org/x/tools`.
2. Cargar paquetes, imports y archivos `_test.go` del repositorio.
3. Marcar incompletitud ante: error de carga, `//go:linkname`, uso de `reflect`
   en los archivos cambiados, `plugin`, o cualquier archivo cambiado que no
   pertenezca a ningún paquete cargado.
4. Cambios en `go.mod`, `Makefile`, configuración de build o CI → **siempre**
   `Completo: false`.

**Aceptación**

- Test contra el propio repositorio: `internal/review` importa `internal/git`.
- Test: un archivo cambiado que usa `reflect` → incompleto con motivo.
- Test: cambio en `go.mod` → incompleto.

---

## T4.3 — Conjunto afectado por cierre inverso

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 240 líneas |
| Depende de | T4.2 |
| Commit | `feat(graph): cierre inverso de imports y conjunto de tests afectados` |

**Contexto**: `internal/graph/*`

**Hacer**: `paquetes cambiados → cierre transitivo de importadores → sus tests`.
Devolver el conjunto **junto con su justificación**, para que el `explain` de la
validación pueda decir por qué se acotó.

**Aceptación**: test contra el repositorio real — cambiar `internal/git/diff.go`
debe arrastrar `internal/review` y `cmd/sentinel`, que lo importan
transitivamente.

---

## T4.4 — Caché del grafo por `tree-oid`

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 200 líneas |
| Depende de | T4.3 |
| Commit | `feat(graph): cachear el grafo por arbol en el common-dir` |

**Contexto**: `internal/graph/*`, `internal/git/snapshot.go` (F1)

**Hacer**: `<repo>/.git/vas-sentinel/graph/<tree-oid>.json`. La clave **es** el
contenido, así que no hay invalidación que diseñar: un árbol distinto es otra
entrada. Purga por antigüedad, como los snapshots.

**Aceptación**: test de que el segundo cálculo sobre el mismo árbol no recarga
paquetes.

---

## T4.5 — Alcance en las capacidades de validación

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 260 líneas |
| Depende de | T4.3 |
| Commit | `feat(validation): acotar capacidades al conjunto afectado con respaldo total` |

**Contexto**: `internal/validation/*` (F1), `internal/graph/*`

**Hacer**

1. Sustituir `{packages}` en `scoped_command` por el conjunto afectado.
2. Acotar **solo** si se cumplen las tres condiciones: la capability declara
   `supports_scope`, el grafo es completo, y ningún archivo cambiado fuerza
   completo.
3. `ValidationRun.Alcance` y `MotivoAlcance` reflejan la decisión y quedan
   persistidos.

**Estado imposible por construcción**: `Alcance: "parcial"` sin
`graph=complete`. El motor debe rechazarlo, no solo evitarlo.

**Aceptación (criterio de salida de la fase)**

> Test que fuerza `Completo: false` y verifica que el comando ejecutado es el
> completo, no el acotado.

---

## T4.6 — Proveedor `codegraph` opcional, solo para contexto

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 240 líneas |
| Depende de | T4.1 |
| Commit | `feat(graph): proveedor codegraph opcional para seleccion de contexto` |

**Contexto**: informe §31.2

**Hacer**

1. Detectar CodeGraph disponible (`.codegraph/` presente y CLI en el PATH).
2. Exponerlo **solo** para selección de contexto del revisor. Debe ser imposible,
   por tipos, que alimente el alcance de validación — no basta con no llamarlo:
   sepáralo en una interfaz distinta.
3. Ausente o con error → degradación silenciosa al proveedor nativo. Nunca falla.
4. Documentar en el README cómo instalarlo, como capacidad **opcional**, con
   enlace a su repositorio.

**Aceptación**: test de que sin CodeGraph el sistema funciona igual; y que el
tipo del proveedor de contexto no satisface la interfaz de alcance.

---

## T4.7 — Símbolos reales en el perfil de cambio

| | |
|---|---|
| Agente | sonnet / high |
| Presupuesto | ≤ 220 líneas |
| Depende de | T4.2 |
| Commit | `feat(change): simbolos reales y public_api exacto en el perfil` |

**Contexto**: `internal/change/*` (F3), `internal/graph/*`

**Hacer**

1. Rellenar `symbols` del `ChangeProfile` con `go/ast` sobre los archivos
   cambiados: añadidos, modificados, borrados, exportados tocados.
2. Sustituir la heurística de `public_api` de T3.3 por la detección exacta y
   **retirar la marca `heuristic`**.

**Aceptación**: test de que un cambio en el cuerpo de una función exportada, sin
tocar su firma, no marca `public_api`; y que cambiar la firma sí lo marca.
