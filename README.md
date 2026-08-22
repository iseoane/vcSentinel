# VAS Sentinel — Guardián de Worktrees para agentes de IA

Guardián local determinista en Go que evita la acumulación masiva de cambios en Git worktrees operados por asistentes de IA. Un solo binario que funciona igual en **Windows** y **Debian Linux**.

## Quick path

1. Compila: `build.bat` (Windows) o `./build.sh` (Debian) → `bin/<version>/sentinel(.exe)`
2. Configura: `sentinel init` → crea `.vas_sentinel/vassentinel.yml` e instala el hook `pre-commit` en este repositorio (no es global: no afecta a tus otros repos)
3. Trabaja: ejecuta `sentinel check` antes de cualquier cambio. Si dice **CRÍTICO** (>400 líneas), detente y ejecuta `sentinel slice`.

## Comandos

| Comando | Qué hace |
|---|---|
| `sentinel check` | Audita el volumen de líneas del worktree: `PEQUENO`, `PUNTO_OPTIMO` (200–400) o `CRÍTICO` (>400, exit 1). |
| `sentinel slice` | Fragmenta los cambios pendientes en micro-commits por capas con un plan que debes aprobar antes de commitear. |
| `sentinel slice plan` | Propone el plan **sin commitear nada**. Con `--json` emite el plan completo; exit 3 si hay decisiones que solo tú puedes responder. |
| `sentinel slice apply` | Ejecuta un plan ya aprobado: `--plan plan.json --answers respuestas.json`. |
| `sentinel init` | Inyecta la regla de volumen en los prompts de tus agentes, crea la configuración per-proyecto e instala el hook `pre-commit` **en este repositorio** (en su common-dir, no en una carpeta global: no afecta a tus otros repos). |
| `sentinel uninit` | Revierte `init` en este repositorio: retira la regla de volumen, borra la config per-proyecto y elimina el hook (solo si sigue siendo el que instaló VAS Sentinel). |
| `sentinel install` / `sentinel upgrade` | Instala o actualiza el binario desde la última release de GitHub, con fallback a `go install` si la release no está disponible. |
| `sentinel review` | Audita un commit (default HEAD) contra las dimensiones de su saco y guarda la ficha en el ledger. Flags: `<sha\|HEAD~n>` `--dims a,b` `--all` `--chain` `--gate` `--profile X` `--answer "..."` `--timeout N` `--prune` `--json`. `--timeout` sobrescribe `review.timeout` solo en esa invocación (segundos). |
| `sentinel lint` | Ejecuta los comandos definidos en `lint_commands` de la configuración. |
| `sentinel rebase` | Actualiza la rama con `fetch` + `rebase` contra su upstream (pide confirmación). |
| `sentinel status` | Resumen del guardián: volumen, fichas de auditoría y últimos eventos. Con `--json` emite JSON; con `--prune` borra fichas huérfanas. |
| `sentinel pr` | Crea un pull request con `gh`; antes limpia las fichas de auditoría huérfanas. Pasa los argumentos a `gh pr create`. |
| `sentinel pr review` | Analiza la rama sin publicar: matriz de fichas y decisión single/chain. Flags: `--base X` `--only-unaudited` `--overview` `--json`. |
| `sentinel uninstall` | Elimina el binario y la configuración global (`~/.vas_sentinel/`). |
| `sentinel version` / `sentinel --version` | Muestra la versión instalada. |
| `sentinel help` / `sentinel --help` | Muestra la ayuda completa. |

## Slice: fragmentación con plan

`sentinel slice` no commitea a ciegas. Flujo completo:

1. **Plan** — agrupa los cambios pendientes por capas (`config → backend → frontend → test`) en lotes de ≤400 líneas.
2. **Mensajes** — genera el mensaje de cada lote con tu agente configurado. Si el agente no responde, elige entre mensajes automáticos deterministas, otro agente disponible o cancelar.
3. **Aprobación** — muestra el plan completo y espera tu decisión: **(A)probar todo**, **(R)egenerar** un mensaje con otro agente, **(E)ditar** un mensaje manualmente o **(C)ancelar**. Enter aprueba.
4. **Resumen** — lista los commits creados y verifica que el worktree quedó limpio.

### Flujo conducido por un agente

El diálogo anterior es un REPL sobre `stdin`: un agente no puede conducirlo. Para eso existe la vía de dos pasos, que **no cambia quién decide** — la decisión sigue siendo tuya, solo cambia el transporte de la pregunta:

```bash
sentinel slice plan --json > plan.json   # propone; no commitea nada
# exit 0 → no hay nada que preguntar
# exit 3 → el plan trae decisiones_pendientes que debes responder tú
sentinel slice apply --plan plan.json --answers respuestas.json
```

`respuestas.json` liga la aprobación a un plan concreto:

```json
{ "plan_id": "<el plan_id del plan emitido>", "respuestas": { "<id de la decisión>": "bypass" } }
```

Tres ligaduras que `apply` verifica antes de crear un solo commit:

1. **Al árbol** — si cambió el contenido de alguna ruta del plan, se niega y hay que replanificar.
2. **Al plan** — las respuestas llevan el `plan_id`; una aprobación de un plan anterior no sirve.
3. **Sin defaults** — toda decisión pendiente exige respuesta explícita (`bypass` o `abortar`). Ni siquiera «aprobar todo» es implícito.

Esto elimina el accidente de confundir «nadie al teclado» con «el humano aprobó». Lo que **no** promete es impedir que un agente deliberado llame a `git commit` por su cuenta: eso queda fuera del modelo de amenaza.

Casos especiales:

- **Archivos gigantes:** config >400 líneas se aísla automáticamente (`chore(deps): track lock and auto-generated files`); código >500 líneas pide confirmación y hace bypass explícito (`chore(slice): bypass IA for massive file …`) o aborta sin commitear nada.
- **Hook y slice:** los commits de slice omiten el hook (`--no-verify`). Slice es el mecanismo de desbloqueo del guardián y cada lote ya está validado; el hook sigue protegiendo los commits manuales.

## Compilación

## Contexto opcional con CodeGraph

Con `review.codegraph_context: true`, consentimiento local para diff externo,
un índice `.codegraph/` limpio y el CLI upstream en `PATH`, Sentinel puede
añadir metadatos opcionales de rutas de tests afectadas a la revisión semántica.
Son datos advisory no confiables: pueden informar la revisión, pero nunca
autorizan alcance de validación y se omiten ante cualquier incertidumbre. No se
envía salida fuente de CodeGraph. Sentinel tampoco indexa ni administra CodeGraph:
[CodeGraph](https://github.com/colbymchenry/codegraph).

Los binarios se generan en `bin/<version>/` (nunca se commitean, están en `.gitignore`). La versión se lee de `release.yml` (fuente de verdad del proyecto) y puedes forzarla con la variable `SENTINEL_VERSION`:

```bash
# Windows: genera bin\0.1.0\sentinel.exe
build.bat

# Debian/Linux: genera bin/0.1.0/sentinel
chmod +x build.sh
./build.sh

# Override opcional de la versión (ignora la de release.yml)
export SENTINEL_VERSION=1.2.0
./build.sh
```

Ambos scripts ejecutan `gofmt -w .` → `go vet ./...` → `go build -ldflags="-s -w -X main.version=<versión>"`.

Verificación rápida de un cambio: `go build ./... && go vet ./...`

## Publicar una release

1. Actualiza `version` en `release.yml` con una versión **superior** a la última publicada.
2. Publica con los scripts de `infra/` (ejecutan vet, generan los assets multiplataforma y crean la release):

```bash
# Windows
infra\release.bat

# Debian/Linux
chmod +x infra/release.sh
./infra/release.sh
```

> `tools/release` consulta la última release publicada con `gh` y **aborta si la versión de `release.yml` es igual o inferior**: debes incrementarla antes de publicar.

También puedes hacerlo manualmente: `go run ./tools/release` para generar los assets en `bin/<version>/` y después `gh release create v<version> bin/<version>/*`.

## Instalación

### Opción A — Bootstrap con Go (no necesitas el binario previo)

```bash
go install github.com/ISeoane-Quental/vas.sentinel/cmd/sentinel@latest
```

Esto compila el binario en `$(go env GOPATH)/bin`. Asegúrate de que esa carpeta esté en tu `PATH` y comprueba con `sentinel --version`.

> **Repo privado:** configura `GOPRIVATE` y las credenciales de git antes:
> `go env -w GOPRIVATE=github.com/ISeoane-Quental/*`

### Opción B — Con el propio sentinel

`sentinel install` descarga la última release publicada y la instala de forma global:

- **Windows:** copia el binario a `~/.vas_sentinel/bin/` y lo añade al PATH de usuario.
- **Debian/Linux:** instala en `/usr/local/bin/sentinel` (reintenta con `sudo`) y deja la ruta en `~/.zshrc` / `~/.bashrc`.

> **Repos privados:** `sentinel install` / `upgrade` resuelven el token en este orden: variable `GITHUB_TOKEN`, token de la sesión de `gh` (`gh auth token`) o pregunta interactiva. No necesitas exportar nada si ya tienes `gh` autenticado.

> **Fallback a go install:** si la release no está disponible (red, token o asset ausente), `install`/`upgrade` reintentan automáticamente con `go install github.com/ISeoane-Quental/vas.sentinel/cmd/sentinel@latest` y dejan el binario en el mismo destino.

## Actualizaciones automáticas

`sentinel upgrade` descarga la última release y reemplaza el binario actual. En Windows el binario en ejecución está bloqueado, por lo que se renombra el actual a `.old` como respaldo durante la operación.

## Desinstalación

`sentinel uninstall` elimina el binario instalado (tanto `~/.vas_sentinel/bin/sentinel` en Windows como `/usr/local/bin/sentinel` en Linux, así como cualquier copia dentro de `GOPATH/bin`) y borra la configuración global `~/.vas_sentinel/`.

## Convención de nombres de assets

Cada release debe publicar assets con esta convención para que `install`/`upgrade` encuentren el binario correcto:

- `sentinel-windows-amd64.exe`
- `sentinel-linux-amd64`
- `sentinel-linux-arm64` (si se publica)

## Configuración (Control de Adaptadores)

La configuración se busca en este orden (el primero que define un campo predomina):

1. **Per-proyecto:** `.vas_sentinel/vassentinel.yml` — creado por `sentinel init`
2. **Global:** `~/.vas_sentinel/vassentinel.yml` — creado por `sentinel install`
3. **Defaults** integrados

`active_agent` (default `auto`) selecciona el agente. En `auto`, la preferencia
sigue el **orden en que los agentes están declarados en el yml** (no alfabético):
el primero cuyo binario esté en el PATH se usa primero y, si falla en una
petición, se prueba el siguiente en ese mismo orden (fallback en cadena por
petición, nunca cacheado). También puedes fijar `active_agent: "claude"` u
`"opencode"` para un agente concreto. `MY_SUB_AGENT` sigue existiendo como
override temporal en la terminal, pero ya no es el mecanismo principal.

Cada agente define su modelo y esfuerzo base, más **perfiles** anidados
(`cheap`, `normal`, `deep`…) que sobreescriben modelo y/o esfuerzo. Las
dimensiones de auditoría (`review.dims`) mapean cada dimensión a un perfil en
dos sintaxis: `agente.perfil` (agente explícito) o solo `perfil` (se aplica el
`active_agent`: el perfil de ese agente si es concreto, o la cadena auto en
orden del yml con fallback).

Ejemplo de `.vas_sentinel/vassentinel.yml`:

```yaml
version: "2.0"
active_agent: "auto"
agents:
  claude:
    model: "claude-5-sonnet"
    reasoning_effort: "high"
    profiles:
      cheap:  { model: "claude-5-sonnet", reasoning_effort: "low" }
      normal: { model: "claude-5-sonnet", reasoning_effort: "high" }
      deep:   { model: "claude-opus",     reasoning_effort: "high" }
  opencode:
    model: "deepseek-v4-flash-free"
    reasoning_effort: "max"
    profiles:
      cheap:  { model: "deepseek-v4-flash-free", reasoning_effort: "default" }
      normal: { model: "deepseek-v4-flash-free", reasoning_effort: "high" }
      deep:   { model: "deepseek-v4-flash-free", reasoning_effort: "max" }
review:
  timeout: 600
  parallel: 2
  # Evidence admission over durable runs (default true). Set false only to
  # roll back to unverified acceptance; runs stay inspectable via
  # `sentinel runs`.
  evidence_admission: true
  dims:
    spec: "opencode.cheap"
    style: "opencode.cheap"
    tests: "opencode.normal"
    logic: "opencode.normal"
    design: "deep"
    security: "deep"
```
