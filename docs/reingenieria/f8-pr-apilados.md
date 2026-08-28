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

---

## Progress and deviations

Verified on 2026-08-27 against the code merged at `8f9a90a`
("Merge branch 'f8-t8-4-pr-cli' into main — T8.4 stacked PR support").
`go build ./...` and `go vet ./...` clean; full `go test ./...` green after the
criterion-2 wiring below (one recorded line reference refreshed in
`internal/adaptersites/inventory.go:113`, the same bookkeeping as `c462cf1`).

| Task | State | Closing commits |
|---|---|---|
| T8.1 | ✅ closed with deviation | `cc53e3b`, `834d5e9`, `7eb3846` |
| T8.2 | ✅ closed with deviation | `43c4109`, `edd67b8`, `467be1b` |
| T8.3 | ✅ closed | `43c4109` |
| T8.4 | ✅ closed with deviations | `467be1b`, `fd8cc09` |

Phase state: **✅ closed at `a244833`**. Tasks T8.1–T8.4 are implemented and
the three exit criteria have evidence. Exit criterion 2 was initially
unreachable from the PR commands and was closed in this same pass (see
«Criterion 2 — closed»).

§4.3 phase gate: `go build ./...`, `go vet ./...` and `go test ./...` green;
`./build.sh` produced `bin/0.2.0/sentinel`; `sentinel gate --stage pre-push`
returned **PASS** with review `ok` on `a244833`; the candidate was promoted to
`~/.vas_sentinel/bin/sentinel`. Step 4 (`sentinel pr review --base main`) does
not apply: every commit landed straight on `main`, so `merge-base(main, HEAD)`
equals `HEAD` and the range is empty — the same exception recorded for F4.

### Exit criteria

| # | Criterion | Status | Evidence |
|---|---|---|---|
| 1 | Reviewing B over A neither reports nor blocks on A's findings | Covered | `internal/review/rama_propio_test.go:112` `TestStackedBranchInheritedFindingDoesNotBlock`; read-only context at `:174` |
| 2 | Rebasing a stacked PR's base preserves the review of everything unchanged | Covered | `internal/review/rebase_test.go:221` `TestStackedBranchSurvivesBaseRebaseViaBlob`; CLI wiring at `cmd/sentinel/comandos_pr_test.go:1368` `TestResolveBlobStoreResolvesTheCommonDir` and `:1389` `TestExecutePrCreateWiresTheBlobStore` |
| 3 | A PR is reviewed as a net unit, not as the sum of its commits | Covered | `internal/review/net_pr_test.go:65` `TestNetReviewIndependentOfHistoricalFindings`, `:201` `TestStackedNetReviewUsesOwnRange` |

### Criterion 2 — closed

**Original state.** Blob-based reuse in `AnalizarRama` is gated on
`opts.Store != nil` (`internal/review/rama.go:172`), and `OpcionesRama.Store`
was never assigned in production code: both CLI call sites omitted it. Through
`sentinel pr review` and `sentinel pr create`, reuse never fired, so rebasing a
stack's base re-audited every commit. The only rebase-survival test,
`internal/review/rebase_test.go:95` `TestAnalizarRamaSobreviveRebaseViaBlob`, is
F2's criterion, injects its own store and has no stacked dimension.

Measured, not assumed: with `Store` removed from the new stacked test, the
second pass reports `Pendientes = [<2 SHAs>]` and the auditor is called **8
extra times** after the base rebase.

**Fix.** `resolveBlobStore(worktree)` (`cmd/sentinel/comandos_pr.go:52`) builds
the store from `git.ObtenerGitCommonDir` — the common dir, never the
per-worktree git dir, because linked worktrees share one store — and is wired
at both call sites: `pr review` directly (`comandos_pr.go:229`) and `pr create`
through the new `depsPrCreate.blobStore` seam (`:717`, `:844`), which exists
because `resolveBlobStore` shells out to git and `depsPrCreate` exists to keep
that out of tests. It returns `(store, error)`; each command formats the warning
through the writer it already uses for its own diagnostics, and a nil store
degrades to the previous SHA-only behaviour instead of aborting a real review.

Reuse is not a new trust boundary: `AnalizarRama` adopts an EXISTING ficha under
the new SHA (`ledger.AdoptarFicha`), which is the same authority the per-SHA
ledger cache in the same common dir already had. It never fabricates a verdict.

**Evidence.** `TestStackedBranchSurvivesBaseRebaseViaBlob`
(`internal/review/rebase_test.go:221`) builds a real `main → layer-a → layer-b`
stack, audits layer-b's own range, advances `main`, rebases `layer-a` onto it and
replants `layer-b` with `git rebase --onto`, then asserts 0 pending commits, 0
additional auditor calls, and that the real finding is still recoverable under
b1's new SHA. Unlike F2's test, the rewritten commit here is the **parent
layer**, so the child's own range is recomputed against a parent whose SHA also
changed.

`TestResolveBlobStoreResolvesTheCommonDir`
(`cmd/sentinel/comandos_pr_test.go:1368`) asserts the mandatory contract with a
real linked worktree: `resolveBlobStore` must resolve the SAME store from the
main worktree and from a `git worktree add` child. Verified by mutation —
pointing `git.ObtenerGitCommonDir` at `--git-dir` makes the linked worktree
resolve `.git/worktrees/linked/vas-sentinel` and the test fails.

**Production wiring, covered.** Both commands call `os.Exit`, so the assignments
were extracted into functions a test can drive: `opcionesRamaPrReview`
(`comandos_pr.go:189`) assembles the options `pr review` hands to
`AnalizarRama`, and `depsPrCreateReales` (`:717`) resolves the production seams
of `pr create`. `TestOpcionesRamaPrReviewWiresTheBlobStore` and
`TestDepsPrCreateRealesWiresTheBlobStore` compare the resolved store against
`resolveBlobStore` instead of asserting non-nil, and the second sweeps all
thirteen seams. Verified by mutation: deleting `Store` from the assembler,
repointing the production seam, and removing `publicar` each fail their test.

**Recorded, not acted on.** The review also raised: `depsPrCreate` now holds
three git/store-shaped members; the `--force` path resolves the common dir
twice in one run; `pr review` still prints through `fmt.Printf` while `pr
create` threads an `io.Writer`; and the store comparison uses
`reflect.DeepEqual` on the value rather than the common-dir path. Parameterizing
the store resolver in `opcionesRamaPrReview`, also suggested, was rejected: it
would move the assignment back inside the `os.Exit`-bound function and reopen
the gap this work closes.

**Inventory churn, removed.** `internal/adaptersites` recorded absolute line
numbers, so any insertion in `cmd/sentinel/comandos_pr.go` invalidated an entry
— this work alone forced two refreshes (551, 573, 579) with no audit signal in
them. `Site.Anchor` now pins the verbatim source text of the site and
`AnchorProblem` decides rot, with `TestAnchorProblemDetectsRot` covering its
four failure branches. Demonstrated on the very next commit: the spawn sites
moved from 425/534/579 to 440/549/594 and the inventory needed no edit.

**Review round.** `sentinel review cd5c6e9` returned `warn` on all five
dimensions, no `block`. Three warnings were accepted and fixed: the diagnostic
written to `os.Stderr` instead of the command writer, the helper test unable to
distinguish the common dir from the per-worktree git dir, and the loose
"optimization, not authority" wording. Two were recorded and not acted on: the
`depsPrCreate` cohesion objection to a third git/store-shaped member, and the
extra `git rev-parse` on the `--force` path, which resolves the common dir twice
in one run. One was dismissed with primary evidence: the reviewer computed the
inventory line as 572, but `grep -n exec.Command` puts the marker at 579 and the
inventory test asserts it.

### T8.1 deviations

- Step 4 of the precedence was substituted. The ficha asks for «the local branch
  whose merge-base with the current one is the most recent».
  `localMergeBaseParent` (`internal/git/parent.go:176-224`) instead requires the
  candidate tip to *be* the merge-base (strict ancestor), removes candidates that
  are ancestors of other candidates, and **fails with `ambiguous local parent
  candidates`** (`:221`) when more than one survives. Safer and consistent with
  the hard rule «never `main` by default», but not the specified rule.
- The work landed in a new `internal/git/parent.go` (272 lines);
  `internal/git/mergebase.go` was not modified. The legacy `UpstreamOMain`
  (`internal/git/commit.go:133`) still exists but is not on this path.
- Budget: 272 lines against a declared ≤ 260.

### T8.2 deviations

- There is no `inherited: true` field on a finding. The mechanism is structural:
  `HallazgoHeredado` (`internal/review/own_diff.go:57`) plus a separate
  `res.Heredados` slice that never enters `res.Fichas`, which is what makes it
  non-blocking. The JSON *key* `"inherited"` exists at
  `cmd/sentinel/comandos_pr.go:286`, but `HallazgoHeredado` carries no JSON tags.
  Behaviourally equivalent to the ficha and proven non-blocking; the named field
  does not exist.

### T8.4 deviations

- Item 2 is `pr create`-only. `pr review` hardcodes
  `stackOwnDiff(flags.parent, false)` (`cmd/sentinel/comandos_pr.go:210`), so
  `--chain-pr` has no stack semantics on `pr review`.
- Item 3 distinguishes own from inherited **by section**
  (`OWN (per-commit audit)` at `:246` and `INHERITED (non-blocking)` at `:248`),
  not by a per-row marker. A commit whose finding was archived by the net review
  still shows a `block` cell in the OWN matrix while the net verdict is `ok`;
  nothing marks that cell as archived.
- Item 4: the legacy passthrough was removed outright
  (`retiredPassthroughDisposition`, `cmd/sentinel/comandos_pr.go:37-39`, exit 1
  with guidance) rather than deprecated over a period.
  `docs/arquitectura/replanteamiento-objetivo.md:28` is now stale: it still
  describes `comandos_pr.go` as `pr review / pr create / passthrough gh`.
- `TestParsearFlagsPrCreate` (`cmd/sentinel/comandos_pr_test.go:549`) does not
  exercise `--parent`; the create-side parser boundary is only covered
  end-to-end.
- All four tasks used commit messages different from the ficha's mandated ones,
  equivalent in scope. Same precedent already accepted for F3.
