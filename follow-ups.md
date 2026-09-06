# Follow-ups

Living list of accepted follow-ups from the durable-runs units (A1/A2) and
live-probe findings, ordered by criticality and dependency. Tick items as they
land.

## P1 — Now (protect verification trust and core business)

- [x] **Exposed-credential detection, independent of `security_sensitive`.**
      Decided 2026-09-02 (FU-11 in `docs/reingenieria/f0-deuda.md`): Sentinel
      does own this, and the signal must not depend on the change profile.
      A credential in prose reports an incident; `security_sensitive` schedules
      a review. Two different questions, and today the second one answers for
      both — so a credential committed into documentation produces no signal at
      all, because the class filter correctly removes content evidence from
      documentation paths.

      Shape: a deterministic finding, produced without an agent, naming the path
      and the matched shape, reported even when the review plan schedules
      nothing. Not a review dimension, because a review that does not run must
      not swallow it.

      Do NOT implement it by widening `detectarSeguridadSensible`. FU-10
      measured and narrowed that detector on purpose; widening it would put an
      incident report back inside the mechanism that decides review cost, and
      the two must not share a switch.

      Landed 2026-09-05 on `feat/exposed-credential-detection` (unpushed):
      `internal/secret` scanner plus WARNING findings through
      `HallazgosDeterministas` in `review`/`gate` and a section in
      `explain`. Non-blocking; values never persisted. Full record in
      FU-11.

      Residual closed 2026-09-05 on `feat/credential-branch-flow`:
      per-commit factory plus net-range coverage in `AnalizarRama`,
      populated by `pr review` and `pr create`; live proof on a
      zero-dimension branch showed the incident with verdict `ok`.

## P2 — Next (clear value, small effort)

- [x] **`sentinel doctor` — preflight the environment a review depends on.**
      A review whose tools are missing does not fail fast: it falls back to
      the expensive path and dies on the timeout, so the operator reads a
      budget exhaustion instead of a missing binary. Observed 2026-08-29:
      `ripgrep` was absent, the OpenCode reviewer's `Grep`/`Glob` returned
      `ripgrep execution failed`, and two `pre-push` gates burned ten minutes
      each before anyone knew why.

      Checks, all local: every entry in `agents` plus `active_agent` resolves
      and answers; the resolved reviewer's search binary resolves; codegraph's
      binary, `.codegraph`, and the six conditions that silently disable
      review context (`HEAD == sha`, clean worktree, initialized index,
      matching `projectPath`, zero pending changes, no worktree mismatch); the
      `pre-commit` hook points at a stable binary rather than `bin/<version>/`
      (the T0.0 trap); and the yml loads strictly.

      **Resolve binaries with `exec.LookPath`, never a shell probe.** On the
      machine where this was found, `command -v rg` succeeded — a shell
      function re-execing another binary — while the review subprocess, which
      does a PATH lookup and inherits no shell functions, failed. A doctor
      written with `which` would have reported a healthy environment while
      every review kept dying. This single detail is most of the value.

      Advisory only: it reports and exits 0 like `check`, never gates
      anything, and never installs — it prints the exact command instead.
      Detecting an installed version is local; comparing it against the
      latest published one is a network call that changes what the command is,
      so it belongs behind an explicit `--check-updates` and never by default.

      Note this does NOT contradict the decision in `internal/review/`
      `diagnostico.go` to avoid a table of which agent needs which binary. On
      the failure path something already broke and the honest report is
      whatever the provider said failed; in a doctor the checklist is the
      deliverable, a human asked for it, and being wrong costs a warning
      rather than a broken review. Keep the two separate.

      Origin: T9.1b review sessions, where the missing binary surfaced only as
      an infrastructure timeout.
      Landed in `feat/doctor` (2026-09-05): `internal/graph.Preflight` (binary,
      `.codegraph`, six context gates), `internal/doctor` (strict yml, per-agent
      resolves plus a minimal real probe prompt per kind at 60s, rg relevance,
      hook T0.0 trap, opt-in `--check-updates`), `sentinel doctor` wiring.
      Deviations: "answers" is a real one-word prompt, not `--version` (a
      starting binary proves nothing about auth/model); only sentinel-owned
      fixes print an exact command (`sentinel init`, `sentinel upgrade`) —
      third-party tools report the failed `exec.LookPath` plus what must
      resolve, since their install commands are environment-specific.
      Follow-up fix (2026-09-05, `fix/graph-shebang`): the npm codegraph
      launcher is a `#!/usr/bin/env node` script, so the sanitized child PATH
      (tool directory only) failed shebang resolution with exit 127 on every
      invocation — review-context enrichment was silently skipping in
      production, and the doctor only made it visible. The child PATH now
      also carries the interpreter's directory (parsed from the shebang,
      resolved in the parent); no parent variables are inherited. Verified
      live: all eight codegraph rows report real values with a real index.
      Follow-up fix (2026-09-05, `fix/doctor-unknown`): an unrunnable
      `codegraph status` rendered as four failed conditions with four wrong
      remedies. Unprobed conditions are now UNKNOWN with no remedy
      (`Condition.Unknown`; unavailable vs unparsable stay distinct in the
      detail; skipped prerequisites are unknown too; `WarnCount` excludes
      them and the summary prints both counts). Dirty-count audit: the
      counter reports exactly what `git status --porcelain` emits under the
      sanitized env — repo-ignored paths never surface, and doctor and
      review share that env, so the preflight predicts what review sees. A
      shell can disagree only through global excludes (HOME is unset in the
      child); inheriting them would break containment, so nothing changed.
      Follow-up fix (2026-09-05, `fix/graph-excludes`): the predicted
      divergence above was the live defect — the child saw
      `.claude/settings.local.json` (ignored only by the XDG default
      `~/.config/git/ignore`) as untracked and the dirty-worktree gate
      skipped context on every review. The parent now resolves the effective
      global excludes file (configured `core.excludesFile` with `~` expanded,
      else the XDG default) and passes it as an explicit `-c` to both status
      calls (`Contexto` gate and preflight); no parent variable reaches the
      child. Missing/unresolvable/unconfigured all resolve to no argument,
      matching parent git; `.git/info/exclude` verified working unaided.
      Live proof: `doctor` reports `worktree_clean: worktree clean` with the
      file present, and a real `Contexto` run reaches past the dirty gate.
      Probe-cost decision (2026-09-05, `fix/doctor-probe`): the per-agent
      probe stays UNCONDITIONAL — no flag, no `--version` fallback. A green
      `--version` with a misconfigured model tells the operator nothing, and
      the check that prevents ten-minute timeouts must not silently stop
      running. Cost is one minimal one-word prompt per configured agent per
      run; do not "optimize" this into a version check. A probe that exceeds
      its 60s budget now reports `ProbeTimeout` (slow or wedged agent) with
      its own remedy, separate from provider-reported errors.

- [ ] **Model prober wired but never verifying.** `internal/modelprobe`
      ("verifies the model that an agent reports for a session") is wired into
      production (`internal/app/pr/create.go:63`, `wiring.go:37,41`,
      `publish.go:101-103`, `review.go:103`,
      `cmd/sentinel/comandos_gate.go:135,163`) feeding `ModeloVerificado`
      (`internal/review/finding.go:167`) — yet the live ledger holds zero
      `model_verified:true` (325 review files scanned 2026-09-05, 328 on
      2026-09-06 with the same result; the only true hits repo-wide are
      fixture copies under `snapshots/`).

      Investigated 2026-09-06 (branch `investigate/modelprobe-verification`,
      read-and-trace, no code changed). Answer: NEITHER — the prober IS
      invoked and IS working, but `model_verified:true` is unreachable by
      construction. `Verificador.Verificar`
      (`internal/modelprobe/verificador.go:37`) returns void: on match,
      probe error, or unparseable answer it returns silently, and only on
      mismatch does it write `profiles/<name>.json` with status
      `unverified`. Three such live mismatch records exist
      (`cheap`/`normal`/`deep`), which proves invocation with a real
      store, a answered probe, and a parseable reply. Nothing ever sets
      `Productor.ModeloVerificado=true`: `stamparProductorEfectivo`
      (`internal/review/engine.go:1019-1024`) stamps agent, binary, model
      and effort only, and repo-wide grep finds no other setter — the
      ledger field asks a question no producer answers. Additionally,
      `store.LeerPerfil` has no production caller, so even the mismatch
      signal is currently write-only.

      Context for the decision, not a defect to fix here: the three
      mismatch records show configured-vs-serving drift (profiles declare
      `opencode-go/glm-5.3-flash` / `muse-spark-1.3-contributor`, the
      agent reports `openai/gpt-5.6-sol`). A fix must define verified
      semantics (probe error/unparseable/mismatch stays false;
      probed-and-matched becomes true), plumb the outcome out of the
      void `Verificar` (or consult `LeerPerfil` at stamp time), decide
      whether a match writes a positive profile record, and keep all
      328 historical files at false. Rough cost: 150-250 lines plus
      tests; the risk is writing false `true` claims into the durable
      ledger, which is worse than the current honest `false`.
      STOPPED for a decision: no implementation in this unit.
      Origin: FU-9 review.

- [ ] **Cache shared audit evidence across review dimensions.** A five-dimension
      audit sends the same commit message, diff, allowed paths, and CodeGraph
      context to isolated `opencode run --pure` invocations, while producing
      short outputs. Today the dimension-specific contract comes before the
      diff and each invocation creates its own snapshot and tool permissions,
      so the rendered prompt prefix cannot be shared.

      Preserve the review contract while making one immutable snapshot per
      audit and rendering a stable evidence envelope first: anti-injection
      rules, commit message, diff, permitted paths, and CodeGraph context.
      Append the dimension contract and output schema as a suffix. Reuse a
      provider cache only within a group with the same model, reasoning effort,
      and tool definitions; never assume cache sharing across `cheap`,
      `normal`, and `deep` profiles. Add a stable cache key derived from the
      audited SHA if OpenCode exposes it.

      Measure input tokens, cached-token reads, latency, and review-equivalence
      before selecting this design. Do not cache model outputs or reduce
      dimension coverage. Origin: FU-6 review-token investigation, 2026-09-04.

- [x] **Make `runs attach` discoverable by help** — `attach` is missing from
      the `sentinel runs --help` subcommand list, and `runs attach --help`
      exits with a rejection although the command accepts real flags
      (`--run`, `--follow`). The ticket-15 central `-h/--help` interception
      does not reach the `runs` subcommand dispatcher. Origin: D3 / ticket 17
      follow-up found while auditing help surfaces post-A2.

## P3 — Conditional / gated

- [ ] **Windows validation of the acpx spawn chain** (`npx → node
      __queue-owner → npm exec → node <agent>-acp`, TTL queue owner) —
      UNCONFIRMED on native Windows. Blocking ONLY if production runs on
      Windows; no action needed while Debian is the deployment platform.
      Origin: A1 probes.
- [x] **Consolidate adapter-family dispatch** when a third adapter kind
      appears — kind switch currently lives in three factory sites. Trigger:
      third kind existing. Origin: 16 slice 3 review.
- [x] **Durable raw-transcript threading** — persist raw NDJSON transcripts /
      observed model / stop reason into durable run records. GATED on the
      capability-policy runtime work (requires store schema extension), which
      is roadmap-scale and not yet ticketed; revisit when that work starts.
      The enforcement declaration itself is ALREADY durably recorded via the
      `agent.enforcement` admission capability. Origin: 16 (JD-E remainder).

## P-Future — parked (maybe someday)

No current trigger or use case; revisited only if circumstances change.

- [ ] **D4: remote host contract** — mutual auth, repository/run
      authorization, disconnect ownership transfer, trust boundaries for
      code/prompts/credentials/evidence. GATED on a concrete operator use
      case for executing durable runs on another machine; no such scenario
      exists today and the local daemon + attach TUI fully cover current
      needs. Architecture is already prepared (transport-neutral six-op
      RepositoryHost, JSON envelopes, cursor replay, idempotency identities),
      so parking this costs nothing future. Origin: roadmap D4.
- [ ] **D5: complete remote control** — remote adapter/host implementation,
      partition reconnect, hostile-network tests. GATED on D4 landing AND an
      approved threat model; R10 dependency already satisfied. Origin:
      roadmap D5.

- [ ] **Admission upgrade via native sandboxes** — codex reviews admitted with
      `mode=read-only` stamped at session admission (verified write
      containment, new exit code 5 on denied ops); claude via project sandbox
      settings incl. `denyRead`; opencode stays grant-by-design declared.
      Converts today's biggest containment gap into real enforcement for the
      dominant use case (reviews). No blocking dependencies. Origin: 16 +
      live probes.
      Live-probed instance (2026-08-26): OpenCode `grep`/`glob` permission
      rules match search expressions, not searched paths, so an injected
      prompt inside committed content can point them at arbitrary host paths;
      `read` is exactly confined (ask-fallback contract), and Claude confines
      `Grep`/`Glob` by path, so the channel is adapter-specific.

      **Deferred 2026-09-05.** Reclassified from P1 to parked: the enforcement
      it asks for is adapter-native, so it cannot be built here — it can only
      be requested from each adapter and verified. GATED on an adapter
      exposing a path-scoped search permission. The gap stays documented and
      unmitigated: an injected prompt inside committed content can still point
      OpenCode's `grep`/`glob` at arbitrary host paths. Trigger to revisit: an
      adapter ships path-scoped search rules, or reviews start running on
      untrusted third-party content.

## Resolved

- [x] **P2 hygiene batch** — shared `process.ContainAfterCancellation`
      (R7 semantics, nil-ctx guard included) replaced both watchdog copies;
      acpx failure/timeout details carry a bounded stderr excerpt (last 500
      chars, all three failure routes); `spawnHelper` signature simplified.
      Reviewed for behavior preservation (watchdog verified line-for-line
      against the R7 original). Merged as `b4405f3`. Origin: 16 reviews.
- [x] **Stabilize daemon wire-parity tests under load** — fixed upstream by
      `f2aa7a4 fix(execution): retry and recover cross-check durable truth
      over stale bookkeeping`: control-op classification no longer trusts
      in-memory bookkeeping over the durable stream, so the succeeded-run /
      stale-revision races are gone. Independently verified with a flake loop
      that went 3/5 red before the fix and 0/5 after. Origin: 16 slice 3
      verification.
- [x] **codex-acp `-32000 Authentication required`** — resolved by refreshing
      the ChatGPT login (`codex login`); adapter works end-to-end including
      gating probes and native-sandbox discovery. Origin: 16 environment note.
