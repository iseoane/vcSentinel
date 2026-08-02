# VAS Sentinel — Guía de trabajo

Guardián local determinista en Go que evita la acumulación masiva de cambios en Git worktrees operados por agentes de IA. Módulo: `github.com/ISeoane-Quental/vas.sentinel` (Go 1.26).

## REGLA CRÍTICA DE VOLUMEN (EL GUARDIÁN)

- Antes de realizar cualquier cambio o proponer un plan, DEBES ejecutar: `sentinel check`.
- Si el estado es "CRÍTICO" (>400 líneas), tienes ESTRICTAMENTE PROHIBIDO seguir escribiendo código.
- Debes detenerte de inmediato e invocar: `sentinel slice` para fragmentar el código acumulado antes de continuar.
- `sentinel slice` trabaja en modo plan: propone los lotes y los mensajes, y NO commitea nada hasta que el usuario apruebe el plan.
- Los commits de slice omiten la verificación del hook (`--no-verify`): invocar slice ES el desbloqueo del guardián y cada lote ya está validado (≤400 líneas, salvo gigantes con bypass explícito). El hook de volumen mide el total pendiente y rechazaría por error commits legítimos durante la fragmentación.

## Build y verificación

- **Windows:** `build.bat` → genera `bin\<version>\sentinel.exe`
- **Debian/Linux:** `./build.sh` → genera `bin\<version>\sentinel` (requiere `chmod +x build.sh`)
- Ambos scripts ejecutan `gofmt -w .` → `go vet ./...` → `go build -ldflags="-s -w -X main.version=<versión>"`. Úsalos en vez de un `go build` a secas.
- La versión se lee de `release.yml` (fuente de verdad del proyecto); se puede forzar con la variable `SENTINEL_VERSION`.
- Los binarios compilados van a `bin/<version>/` (nunca se commitean, están en `.gitignore`).
- Verificación rápida de un cambio: `go build ./... && go vet ./...`
- Publicar assets de release: `go run ./tools/release` (genera los binarios multiplataforma en `bin/<version>/`). Publicación completa con `infra/release.bat` o `infra/release.sh` (vet + assets + `gh release create`).
- Bootstrap de instalación para quien no tiene el binario: `go install github.com/ISeoane-Quental/vas.sentinel/cmd@latest`.

## Multiplataforma (obligatorio)

Código y scripts DEBEN funcionar igual en Windows y Debian:

- Usa `filepath.Join` para construir rutas; nunca concatenes `/` a mano.
- Usa `filepath.ToSlash` al pasar rutas a git (p. ej. `core.hooksPath`).
- El hook `pre-commit` es `#!/bin/sh` y funciona en ambos: Git para Windows lo ejecuta con `sh.exe`.
- El hook global (`~/.git_global_hooks/pre-commit`) ejecuta `sentinel check` y requiere que el binario esté en el PATH (o que el hook use la ruta absoluta del binario).

## Arquitectura

- `cmd/main.go` — entrypoint CLI: subcomandos `version`, `help`, `init`, `check`, `slice` (+ `install`, `upgrade`)
- `internal/config` — parsea `vassentinel.yml` (model + reasoning_effort por agente), con precedencia per-proyecto (`.vas_sentinel/vassentinel.yml`) sobre global (`~/.vas_sentinel/vassentinel.yml`)
- `internal/agentadapter` — interfaz `AgentAdapter` + `CLIAdapter` (claude/opencode) que delega la generación del mensaje de commit
- `internal/git` — `CheckDiffLimits` (umbral de volumen), `ObtenerArchivosModificados` (numstat + untracked), plan de fragmentación y ejecución de lotes

## Lógica de negocio clave

- `check`: 200–400 líneas = `PUNTO_OPTIMO`, >400 = `CRITICO` → exit 1. El string exacto es `"CRITICO"` (sin tilde) y `main.go` lo compara tal cual.
- `slice`: construye un plan por capas en orden fijo `config → backend → frontend → test`, lotes de ≤400 líneas. Genera los mensajes con el adaptador configurado; si el adaptador automático no responde, ofrece mensajes automáticos deterministas, otro agente disponible o cancelar. Muestra el plan para aprobación (A/R/E/C) antes de commitear, y resume los commits al final.
  - Archivos gigantes: config >400 líneas se aíslan con `chore(deps): track lock and auto-generated files`; código >500 líneas pide confirmación (`s/N`) y, si se confirma, hace bypass con `chore(slice): bypass IA for massive file <archivo>`; si se rechaza, aborta sin commitear nada.
  - Todos los commits de slice usan `--no-verify` (ver REGLA CRÍTICA DE VOLUMEN).
- `init`: inyecta la regla de volumen en `AGENTS.md`, `CLAUDE.md`, `.claudecode.md`, crea `.vas_sentinel/vassentinel.yml` per-proyecto e instala el hook global en `~/.git_global_hooks/pre-commit` con la ruta absoluta del binario.

## Configuración

- `vassentinel.yml`: `active_agent` (default `auto`) — agente activo; `agents:` con `model` y `reasoning_effort` por agente. `auto` usa el primer agente de `agents:` disponible en el PATH. Precedencia: per-proyecto `.vas_sentinel/vassentinel.yml` > global `~/.vas_sentinel/vassentinel.yml` > defaults.
- `MY_SUB_AGENT` (env): override opcional — ya no es el mecanismo principal.

## Convenciones

- Mensajes de commit: Conventional Commits (`chore(slice): ...`, etc.).
- Textos de UI y prompts: castellano estándar, NO voseo — "Puedes", "Analiza", "Devuelve", nunca "Podés", "Analizá", "Devolvé".
- Los prompts al agente devuelven SOLO la línea del mensaje de commit, sin markdown ni comillas.
