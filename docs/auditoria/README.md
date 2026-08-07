# Auditoría de subcomandos — plan y seguimiento

Diagnóstico estructurado de los 14 subcomandos del CLI (+2 subcomandos de `pr`).
Este documento es la **fuente de verdad del progreso**: cada comando lleva su
estado y su veredicto. No se corrige nada aquí; los arreglos salen después,
priorizados desde la tabla de veredictos.

## Ficha por comando

Cada comando se analiza con las mismas cinco preguntas, siempre en este orden:

1. **Trigger** — qué lo invoca (usuario, hook `pre-commit`, CI, otro comando).
2. **Cuándo y por qué** — precondiciones y punto del flujo en el que tiene sentido.
3. **Objetivo** — el contrato: qué promete y qué NO promete.
4. **¿El código es correcto?** — veredicto con evidencia.
5. **¿Cómo se mejora?** — propuesta concreta, con coste y riesgo.

### Regla de evidencia (punto 4)

Nada de «parece correcto». Todo veredicto va acompañado de una de estas tres
pruebas, con `file:line`:

- el test que lo cubre,
- la ejecución real que lo demuestra,
- la ruta de código exacta que lo hace evidente.

Si no se puede probar, el veredicto es **DUDA**, nunca VERIFICADO.

### Leyenda de estados y veredictos

| Estado | Significado |
|---|---|
| ⬜ | pendiente |
| 🔄 | en curso |
| ✅ | analizado |

| Veredicto | Significado |
|---|---|
| 🟢 VERIFICADO | contrato cumplido, con evidencia |
| 🟡 DUDA | no demostrable con la evidencia disponible |
| 🔴 DEFECTO | incumple su contrato |

## Seguimiento

Orden por **riesgo**, no alfabético. El grupo B primero porque es el propósito
del proyecto; A y F después porque escriben fuera del worktree.

| # | Grupo | Comando | Flags que acepta | Estado | Veredicto | Informe |
|---|---|---|---|---|---|---|
| 1 | B — Guardián | `check` | *(ninguno)* | ⬜ | — | `b-guardian.md` |
| 2 | B — Guardián | `slice` | *(ninguno)* | ⬜ | — | `b-guardian.md` |
| 3 | A — Ciclo de vida | `init` | *(ninguno)* | ⬜ | — | `a-ciclo-vida.md` |
| 4 | A — Ciclo de vida | `uninit` | *(ninguno)* | ⬜ | — | `a-ciclo-vida.md` |
| 5 | F — Distribución | `install` | *(ninguno)* | ⬜ | — | `f-distribucion.md` |
| 6 | F — Distribución | `upgrade` | *(ninguno)* | ⬜ | — | `f-distribucion.md` |
| 7 | F — Distribución | `uninstall` | *(ninguno)* | ⬜ | — | `f-distribucion.md` |
| 8 | C — Auditoría | `review` | `<sha\|HEAD~n>` `--dims` `--all` `--chain` `--gate` `--profile` `--answer` `--timeout` `--prune` `--json` | ⬜ | — | `c-auditoria.md` |
| 9 | C — Auditoría | `status` | `--json` `--prune` | ⬜ | — | `c-auditoria.md` |
| 10 | D — Publicación | `pr review` | `--base` `--only-unaudited` `--overview` `--json` | ⬜ | — | `d-publicacion.md` |
| 11 | D — Publicación | `pr create` | `--base` `--chain-pr` `--force` | ⬜ | — | `d-publicacion.md` |
| 12 | E — Utilidades | `lint` | *(ninguno)* | ⬜ | — | `e-utilidades.md` |
| 13 | E — Utilidades | `rebase` | *(ninguno)* | ⬜ | — | `e-utilidades.md` |
| 14 | G — Triviales | `version` | `--version` `-v` | ⬜ | — | `g-triviales.md` |
| 15 | G — Triviales | `help` | `--help` `-h` | ⬜ | — | `g-triviales.md` |

## Mapa de código por grupo

| Grupo | Archivos |
|---|---|
| A — Ciclo de vida | `cmd/sentinel/main.go`, `internal/setup/` |
| B — Guardián | `internal/git/` (`plan.go`, `slice.go`, `commit.go`, `diff.go`) |
| C — Auditoría | `cmd/sentinel/comandos_review.go`, `comandos_estado.go`, `internal/review/` |
| D — Publicación | `cmd/sentinel/comandos_pr.go`, `internal/review/rama.go`, `internal/ops/verificar.go` |
| E — Utilidades | `cmd/sentinel/comandos_estado.go` |
| F — Distribución | `internal/setup/` (`install.go`, `upgrade.go`, `uninstall.go`, `github.go`) |
| G — Triviales | `cmd/sentinel/main.go` |

## Hallazgos previos al análisis

Detectados al construir el plan, aún **sin verificar en profundidad**. Se
confirman o se descartan dentro de la ficha de su comando.

| # | Hallazgo | Evidencia | Comando afectado | Estado |
|---|---|---|---|---|
| H1 | Los subcomandos sin flags ignoran `os.Args[2:]` en silencio: `sentinel check --loquesea` se acepta sin error | `cmd/sentinel/main.go:54-73,85-89` | `check`, `slice`, `lint`, `rebase`, `init`, `uninit`, `install`, `upgrade`, `uninstall` | ⬜ sin verificar |
| H2 | Sin cobertura de tests en las rutas de entrada de varios comandos | CodeGraph: `ejecutarInit`, `ejecutarUninit`, `ejecutarPr`, `PrepararTokenGitHub`, `LotePlanificado` | `init`, `uninit`, `pr`, `install`/`upgrade`, `slice` | ⬜ sin verificar |
| H3 | `main.go` con 902 líneas concentra lógica de negocio en el paquete `cmd` (arquitectura plana) — coincide con el `design:warn` de la auditoría de `d45a208` | `cmd/sentinel/main.go` | transversal | ⬜ sin verificar |
| H4 | Con `active_agent: auto` la ficha registra el **nombre del perfil**, no el agente que respondió. Si la cadena cae de `claude` a `opencode`, el veredicto queda sin constancia de quién lo emitió: agujero de trazabilidad en el ledger | cadena en `internal/agentadapter/cadena.go:75-88`; el modelo guardado sale de `flags.profile` en `cmd/sentinel/comandos_review.go:112-118`. Comprobado: la ficha de `6c079a8` guarda `"model": "default"` tras responder claude | `review`, `pr review` | 🟢 confirmado (código + ejecución) — **sin corregir** |
| H5 | El motor audita **el diff del commit aislado**, sin el estado posterior del repositorio ni los símbolos fuera del diff. Produce hallazgos CRITICAL falsos: denuncia defectos ya corregidos en commits posteriores y deduce la inexistencia de símbolos que sí existen. Un CRITICAL falso bloquea el gate de `pr review` | Falsos positivos verificados en la auditoría de `1a61087`: «corta runas UTF-8» ya resuelto por `recortarRunas` (`internal/review/renderer.go:293,299`); «`maxBytes<=0` no trunca» es intencional y está afirmado en `internal/review/renderer_test.go:144-148`; «los tests no cubren `maxBytes<=0`» es falso, los cubre ahí mismo. En `914a975`: «`exitCodeDeError` no existe, el build falla», pero está en `cmd/sentinel/comandos_estado.go:135` | `review`, `pr review` (gate) | 🟢 confirmado (código + ejecución) — **sin corregir** |
| H6 | `preguntarTokenGitHub` lee el token de GitHub desde `os.Stdin` con el eco del terminal activo: el token queda visible en pantalla al escribirlo o pegarlo | Vigente en HEAD: `internal/setup/github.go:71` usa `bufio.NewReader(os.Stdin).ReadString('\n')` sin desactivar el eco; alcanzable desde `PrepararTokenGitHub` (`:45`). Detectado por la dimensión `spec` en la auditoría de `d5b04a0` | `install`, `upgrade` | 🟢 confirmado en HEAD — **sin corregir** |

## Corregido durante la auditoría

| Defecto | Corrección | Evidencia |
|---|---|---|
| `ResolverPerfil` partía por el primer punto cualquier nombre de perfil, sin comprobar que el prefijo fuese un agente configurado. Un perfil como `gpt-4.1` producía `Binario: "gpt-4"` (binario inexistente) sin heredar modelo ni esfuerzo | El prefijo solo cuenta como agente si existe en `cfg.Agents`; si no, cae a la vía v1 | `internal/config/perfil.go:54-62`; tests en `internal/config/perfil_test.go` (`TestResolverPerfilPuntoSinAgenteNoRompe`, `...HeredaDelActivo`, `TestResolverPerfilV2SigueFuncionandoConAgenteReal`) |

## Cómo se actualiza este documento

Al cerrar cada comando: marcar el estado a ✅, escribir el veredicto, y mover a
la tabla de hallazgos previos cualquier confirmación o descarte. Un comando no
se marca ✅ mientras alguna de las cinco preguntas siga sin respuesta con
evidencia.
