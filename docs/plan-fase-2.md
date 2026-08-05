# VAS Sentinel — Plan de implantación Fase 2 (Renderizado y PR)

Plan de ejecución de la fase 2, según el diseño acordado en
`docs/guia-implantacion-revision.md` (secciones 12–13, 15). Cada unidad de
trabajo (commit) ≤400 líneas de cambio; `sentinel check` entre unidades;
dogfooding: los commits de la fase se auditan con `sentinel review` (fase 1).

## Dependencias entre unidades

```
S1 config ──────────────► S4 verificación dual (lee comandos del yml)
S2 renderer ──► S3 pr review (matriz) ──► S6 pr create (plantilla)
S3 analizarRama() ─────────────────────────► S6
S5 eventos PR: infra ya existe (events.jsonl); se registran desde S3/S4/S6
```

## Unidades de trabajo

### S1 — Config de comandos de verificación
- `internal/config/parser.go`: secciones `test` y `build` (hoy solo `lint`),
  defaults vacíos, validación de forma (no ejecutar nada vacío).
- Tests: `secciones_test.go` / `parser_test.go` (parseo, precedencia
  per-proyecto > global, defaults).
- Commit: `feat(config): comandos de verificacion lint/test/build en el yml`

### S2 — Renderer: matriz commit × dimensión
- `internal/review/renderer.go`: `RenderMatriz` (tabla markdown), `RenderResumen`
  (veredictos + riesgos), truncamiento marcado (límite de body).
- `internal/review/renderer_test.go` (fixtures de fichas reales).
- Commit: `feat(review): renderer de matriz commit x dimension en markdown`

### S3 — `analizarRama()` + `pr review`
- `internal/review/rama.go`: base (default `main`), `merge-base` + `SHAsRango`,
  auditar pendientes vía engine, `--only-unaudited`, `--overview` (Spec de rama,
  1 llamada), decisión single/chain por volumen (numstat) + coherencia.
- `cmd/sentinel/comandos_pr.go`: subcomando `pr review` (dry-run, no publica),
  salida terminal/`--json`.
- Tests: `rama_test.go`, `comandos_pr_test.go`.
- Commits: `feat(pr): review de rama con analizarRama y decision single/chain`
  y, si el volumen lo exige, `feat(pr): overview spec de rama (1 llamada)`

### S4 — Verificación dual + aviso de CI
- `internal/ops/verificar.go` (o `internal/review/verificar.go`): ejecución
  determinista de lint/test/build con exit codes; detección heurística de CI
  (`.github/workflows/`, `.gitlab-ci.yml`); flujo de aviso con elección
  (configurar | omitir | delegar); delegación al agente con contrato `tested`
  (prompt con shell libre, DISTINTO del de auditoría que prohíbe herramientas;
  timeout reusado de `review.timeout`).
- Tests: `verificar_test.go` (sin config, con config, agente devuelve `tested`,
  `unavailable`, CI detectado/ausente).
- Commit: `feat(pr): verificacion dual determinista/delegada con aviso de CI`

### S5 — Eventos PR + purga de actas
- `internal/ops/events.go`: eventos `pr-review` / `pr-verify` / `pr-create`
  (mismo esquema `Evento`, `detail` JSON); `PurgeEventosDePRsResueltas`:
  `gh pr view <nº> --json state`, purga `MERGED`/`CLOSED`, conserva
  `OPEN`/`DRAFT`, best-effort (sin gh/red → aviso, no bloquea).
- Tests: `events_test.go` (purga por estado, fallo de gh conserva, actas por
  fallback se conservan).
- Commit: `feat(ops): eventos del flujo pr y purga de actas mergeadas/cerradas`

### S6 — `pr create`: plantilla + publicación
- `internal/review/renderer.go`: plantilla `sentinel_pr.md` — línea de riesgo
  🚨/⚠️/✅ (veredicto de auditoría, no CI), rationale del `--overview` (3–5
  líneas), matriz, sección de verificación honesta (exit codes / `tested` /
  "tests no ejecutados"), emojis por severidad, firma + versión, truncamiento.
- `cmd/sentinel/comandos_pr.go`: `pr create` — gate (block sin superar → no
  publica, exit 1), `gh pr create --draft -F sentinel_pr.md`, fallback
  portapapeles (`clip`/`wl-copy`/`xclip`), `--chain-pr`.
- Tests: `renderer_test.go` (plantilla), `comandos_pr_test.go` (gate, fallback).
- Commit: `feat(pr): create con plantilla honesta, gate de block y fallback`

## Verificación final de la fase

- `go build ./... && go vet ./... && go test ./...` por unidad (y
  `build.bat`/`build.sh` al cierre).
- `sentinel check` ≤400 líneas por unidad; si se supera, `sentinel slice`.
- Dogfooding: `sentinel review` sobre los commits de la fase; los hallazgos
  corregidos quedan en el ledger (`fixed_in`).
- Si la implementación revela desviaciones del diseño, actualizar la guía.

## Riesgos

- Delegación al agente puede colgarse (como en auditoría): timeout obligatorio
  y contrato `tested` obligatorio; fallo → aviso, nunca bloquea.
- `gh pr view` requiere red: la purga de actas es best-effort.
- El template debe mantenerse honesto: sin PASS inventado (regla de oro).
