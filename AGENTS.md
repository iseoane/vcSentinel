# VAS Sentinel — Guía de trabajo

Guardián local determinista en Go que evita la acumulación masiva de cambios en Git worktrees operados por agentes de IA. Módulo: `github.com/ISeoane-Quental/vas.sentinel` (Go 1.26).

## REGLA CRÍTICA DE VOLUMEN (EL GUARDIÁN)

- Antes de realizar cualquier cambio o proponer un plan, DEBES ejecutar: `sentinel check`.
- Si el estado es "CRÍTICO" (>400 líneas), tienes ESTRICTAMENTE PROHIBIDO seguir escribiendo código.
- Debes detenerte de inmediato e invocar: `sentinel slice` para fragmentar el código acumulado antes de continuar.
- `sentinel slice` trabaja en modo plan: propone los lotes y los mensajes, y NO commitea nada hasta que el usuario apruebe el plan.
- Los commits de slice omiten la verificación del hook (`--no-verify`): invocar slice ES el desbloqueo del guardián y cada lote ya está validado (≤400 líneas, salvo gigantes con bypass explícito). El hook de volumen mide el total pendiente y rechazaría por error commits legítimos durante la fragmentación.

### Si eres un agente: usa el flujo de dos pasos

`sentinel slice` sin argumentos es un REPL sobre `stdin` y no puedes conducirlo. Usa en su lugar:

1. `sentinel slice plan --json > plan.json` — propone y **no commitea nada**. Es idempotente: sobre el mismo árbol devuelve el mismo `plan_id`.
   - Exit `0`: no hay nada que preguntar.
   - Exit `3`: el plan trae `decisiones_pendientes[]`. **Trasládaselas al usuario tal cual y espera su respuesta.** No respondas por él ni elijas un valor por defecto.
2. Escribe `respuestas.json` con la respuesta literal del usuario: `{"plan_id": "<plan_id>", "respuestas": {"<id>": "bypass"|"abortar"}}`.
3. `sentinel slice apply --plan plan.json --answers respuestas.json` — commitea solo si el árbol no cambió, las respuestas son de ese `plan_id` y toda decisión tiene respuesta explícita.

La decisión sigue siendo del humano: este flujo cambia el transporte de la pregunta, no quién la contesta.

## Build y verificación

- **Windows:** `build.bat` → genera `bin\<version>\sentinel.exe`
- **Debian/Linux:** `./build.sh` → genera `bin\<version>\sentinel` (requiere `chmod +x build.sh`)
- Ambos scripts ejecutan `gofmt -w .` → `go vet ./...` → `go build -ldflags="-s -w -X main.version=<versión>"`. Úsalos en vez de un `go build` a secas.
- La versión se lee de `release.yml` (fuente de verdad del proyecto); se puede forzar con la variable `SENTINEL_VERSION`.
- Los binarios compilados van a `bin/<version>/` (nunca se commitean, están en `.gitignore`).
- Verificación rápida de un cambio: `go build ./... && go vet ./...`
- Publicar assets de release: `go run ./tools/release` (genera los binarios multiplataforma en `bin/<version>/`). Publicación completa con `infra/release.bat` o `infra/release.sh` (vet + assets + `gh release create`).
- Bootstrap de instalación para quien no tiene el binario: `go install github.com/ISeoane-Quental/vas.sentinel/cmd/sentinel@latest` (el paquete se llama `sentinel`; un `go install .../cmd@latest` instalaría un binario llamado `cmd`, que colisiona con el de Windows).

## Multiplataforma (obligatorio)

Código y scripts DEBEN funcionar igual en Windows y Debian:

- Usa `filepath.Join` para construir rutas; nunca concatenes `/` a mano.
- Usa `filepath.ToSlash` al pasar rutas a git.
- El hook `pre-commit` es `#!/bin/sh` y funciona en ambos: Git para Windows lo ejecuta con `sh.exe`.
- El hook se escribe directamente en el common-dir del repositorio (`<git-common-dir>/hooks/pre-commit`, vía `git rev-parse --git-common-dir`), no en una carpeta global ni con `core.hooksPath`: solo afecta al repo donde se corrió `init`, y es el mismo para todos sus worktrees enlazados. Ejecuta `sentinel check` con la ruta absoluta del binario.

## Arquitectura

- `cmd/sentinel` — entrypoint CLI y diálogos interactivos. `main.go` despacha los subcomandos; `comandos_review.go`, `comandos_estado.go` y `comandos_pr.go` implementan `review`, `status` y `pr`.
- `internal/config` — parsea `vassentinel.yml` (agentes, perfiles anidados, `commit_language`, comandos de lint/test/build), con precedencia defaults → global (`~/.vas_sentinel/vassentinel.yml`) → per-proyecto (`.vas_sentinel/vassentinel.yml`).
- `internal/agentadapter` — `AgentAdapter` + `CLIAdapter` (claude/opencode) y `CadenaAdaptador`, que prueba los agentes en orden y hace fallback por petición. Reporta el agente efectivo que atendió cada llamada.
- `internal/git` — umbrales de volumen (`umbrales.go`), medición (`MedirVolumen`), plan de fragmentación (`plan.go`), vía no interactiva `plan`/`apply` (`planagente.go`, `aplicarplan.go`) y clasificación de archivos (`clases.go`).
- `internal/review` — motor de auditoría por dimensiones, prompts, ledger de fichas por commit y análisis de rama (decisión single/chain).
- `internal/ops` — registro de eventos en el common-dir: alta, rotación, purga y lectura de los últimos.
- `internal/setup` — instalación, upgrade y desinstalación del binario, y plantillas de configuración.

Documentos de referencia: [`docs/arquitectura/replanteamiento-objetivo.md`](docs/arquitectura/replanteamiento-objetivo.md) (hacia dónde va el producto) y [`docs/reingenieria/`](docs/reingenieria/) (plan por fases y estado de las tareas).

## Subcomandos

| Subcomando | Qué hace |
|---|---|
| `version` | Versión instalada. |
| `help` | Ayuda de subcomandos. |
| `init` | Inyecta la regla de volumen, crea la config per-proyecto e instala el hook `pre-commit`. |
| `uninit` | Revierte `init` en este repositorio. |
| `check` | Audita el volumen de líneas añadidas de código del worktree. |
| `slice` | Fragmenta los cambios en commits de ≤400 líneas (REPL interactivo). |
| `slice plan` | Propone el plan sin commitear. `--json`; exit 3 si hay decisiones pendientes. |
| `slice apply` | Ejecuta un plan aprobado. `--plan X --answers Y`. |
| `review` | Audita un commit contra las dimensiones de su saco y guarda la ficha. |
| `lint` | Ejecuta los `lint_commands` de la configuración. |
| `rebase` | `fetch` + `rebase` contra el upstream, con confirmación. |
| `status` | Volumen, fichas de auditoría y últimos eventos. `--json`, `--prune`. |
| `pr` | Crea un pull request con `gh` (passthrough). |
| `pr review` | Analiza la rama sin publicar: matriz y decisión single/chain. |
| `install` / `upgrade` / `uninstall` | Gestión del binario instalado. |

Los subcomandos sin flags rechazan cualquier argumento extra con salida 1.

## Lógica de negocio clave

- `check`: 200–400 líneas = `PUNTO_OPTIMO`, >400 = `CRITICO` → exit 1. El string exacto es `"CRITICO"` (sin tilde) y `main.go` lo compara tal cual.
- `slice`: construye un plan por capas en orden fijo `config → backend → frontend → test`, lotes de ≤400 líneas. Genera los mensajes con el adaptador configurado; si el adaptador automático no responde, ofrece mensajes automáticos deterministas, otro agente disponible o cancelar. Muestra el plan para aprobación (A/R/E/C) antes de commitear, y resume los commits al final.
  - Archivos gigantes: config >400 líneas se aíslan con `chore(deps): track lock and auto-generated files`; código >500 líneas pide confirmación (`s/N`) y, si se confirma, hace bypass con `chore(slice): bypass IA for massive file <archivo>`; si se rechaza, aborta sin commitear nada.
  - Todos los commits de slice usan `--no-verify` (ver REGLA CRÍTICA DE VOLUMEN).
- `review`: audita un commit por dimensiones (`logic`, `style`, `design`, `tests`, `security`, `spec`), cada una con su perfil de agente. Guarda una ficha por commit en `<git-common-dir>/vas-sentinel/<sha>.json`, con `revisions[]` append-only. Cada revisión registra el agente EFECTIVO que respondió (`agent`/`model`/`effort`), no el perfil pedido.
- `pr review`: analiza la rama contra su base y decide single vs. chain con `review.LimiteDecisionChain` (el mismo 400 del guardián, por decisión). Si hay `lint_commands`/`test_commands`/`build_commands`, la verificación es determinista y NO consulta al agente.
- `status`: combina volumen, fichas del ledger y últimos eventos (`internal/ops`), que se registran en el common-dir con rotación y purga.
- `init`: inyecta la regla de volumen en `AGENTS.md`, `CLAUDE.md`, `.claudecode.md`, crea `.vas_sentinel/vassentinel.yml` per-proyecto e instala el hook `pre-commit` directamente en `<git-common-dir>/hooks/` del repositorio (sin carpeta global ni `core.hooksPath`), con la ruta absoluta del binario. Así solo se activa en el repo donde se corrió `init`, sin afectar otros repos del usuario. Solo se ejecuta en la raíz del worktree Git: si se invoca desde un subdirectorio, redirige automáticamente a la raíz (via `git rev-parse --show-toplevel`); si no hay repositorio Git, aborta con error.

## Configuración

- `vassentinel.yml`: `active_agent` (default `auto`) — agente activo; `agents:` con `model`, `reasoning_effort` y perfiles anidados por agente. `auto` usa el primer agente de `agents:` disponible en el PATH. Precedencia: per-proyecto `.vas_sentinel/vassentinel.yml` > global `~/.vas_sentinel/vassentinel.yml` > defaults.
- `commit_language` (default `es`): idioma de los mensajes de commit que genera `slice`.
- `review:` con `timeout`, `parallel` y `dims:` (perfil por dimensión canónica). `lint_commands`, `test_commands` y `build_commands` habilitan la verificación determinista de `pr review` SIN consultar al agente.
- Umbrales: `internal/git/umbrales.go` es la única fuente. `LimiteLineasRevisables` (400) manda sobre el guardián, el tamaño de lote y `review.LimiteDecisionChain`; `LimiteCodigoGigante` (500) dispara la decisión de archivo masivo.
- `MY_SUB_AGENT` (env): override opcional — ya no es el mecanismo principal.

## Convenciones

- Mensajes de commit: Conventional Commits (`chore(slice): ...`, etc.).
- Textos de UI y prompts: castellano estándar, NO voseo — "Puedes", "Analiza", "Devuelve", nunca "Podés", "Analizá", "Devolvé".
- Los prompts al agente devuelven SOLO la línea del mensaje de commit, sin markdown ni comillas.
- El idioma de los mensajes de commit lo fija `commit_language` en el yml (por defecto `es`, el del historial). El prompt lo dice explícitamente y con un ejemplo: sin fijarlo, el modelo mezclaba idiomas dentro de la misma ejecución.

<!-- vas-sentinel:begin -->
## REGLA CRÍTICA DE VOLUMEN (EL GUARDIÁN)
- Antes de realizar cualquier cambio o proponer un plan, DEBES ejecutar: "sentinel check".
- Si el estado es "CRÍTICO" (>400 líneas), tienes ESTRICTAMENTE PROHIBIDO seguir escribiendo código.
- Debes detenerte de inmediato e invocar: "sentinel slice" para fragmentar el código acumulado antes de continuar.
<!-- vas-sentinel:end -->
