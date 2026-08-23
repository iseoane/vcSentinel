# 12: Complete Core Durable Control

**What to build:** Durable runs become the single execution lifecycle authority
for review and gate. Remaining parallel lifecycle paths are closed or explicitly
compatibility-gated, every adapter attempt flows through admitted invocation
envelopes, operator documentation and JSON schemas are complete, retention and
purge behave, end-to-end harnesses prove crash/timeout/abort/retry/response/
fallback/recovery behavior, and linked worktrees observe the same run state.

**Blocked by:** 11 (complete, merged at bccb76e).

**Status:** complete

**Design contract (agreed analysis):**

- No silent legacy bypass: the review.durable_runs=false and
  gate.durable_runs=false switches remain as the release-bounded compatibility
  facade (R10 rollback contract), but no OTHER path may start provider work or
  mutate run lifecycle outside the controller; any discovered parallel path is
  closed or converted to an admitted envelope flow this unit.
- Envelope totality: audit every adapter execution site (review dimensions,
  gate validation jobs where applicable, prompt/commit-message helpers) — each
  must carry an admitted invocation envelope or be documented as
  lifecycle-out-of-scope with the reason recorded in code.
- Operator completeness: `docs/runs-cli.md` gains the final schema reference
  (all stable JSON shapes in one table), command help matches reality,
  retention/purge semantics for executions are defined (what purges, what is
  retained forever as append-only evidence) and implemented if missing.
- End-to-end harness suite: one test file per scenario (crash mid-run,
  timeout, abort, retry, respond, adapter fallback chain, owner-loss recovery)
  driving real store+controller+transport on temp repos, asserting honest
  terminal states and inspectability through `sentinel runs`.
- Linked-worktree consistency: a run created from one worktree is observable
  with identical state from a linked worktree of the same repository
  (common-dir store semantics proven by test).
- Compatibility/migration policy: a short section documenting which persisted
  shapes changed across R1-R10, how old stores are handled (read-compatible,
  never rewritten), and the deprecation policy for the two rollback switches.
- Additive compatibility: no breaking JSON tag changes; old stores keep
  scanning and operating unchanged.

**Acceptance criteria:**

- [ ] Focused tests prove no non-controller path can mutate run lifecycle
      (audit-backed enumeration of adapter execution sites).
- [ ] Focused tests prove every in-scope adapter attempt carries an admitted
      invocation envelope; out-of-scope sites are enumerated with reasons.
- [ ] Focused tests cover all seven end-to-end scenarios with honest terminal
      states and runs-inspectable outcomes.
- [ ] Focused tests prove linked-worktree observation consistency.
- [ ] Tests prove retention/purge behavior matches the documented policy.
- [ ] Documentation: complete schema table, help accuracy check, migration/
      compatibility policy section.
- [ ] The implementation records build, vet, tests, guardian, independent
      `code-review`, Judgment Day, rollback boundary, and follow-ups here.

## Evidence

*(appended per slice)*

## Follow-ups

*(recorded at closure)*

### Slice 1 — lifecycle audit, envelope totality, refutation closure
- Commits: inventory package (276), refutation conversion + behavioral tests
  (194), inventory canaries (241) — hook-enforced; worktree clean.
- AUDIT RESULT: one REAL verdict-influencing parallel path found and closed —
  critical-finding refutations called the refutador directly even under
  durable transport. Converted: non-nil transport routes through admission
  (distinct bundleName "refutation", per-attempt sequence identity); nil keeps
  legacy byte-identical (rollback switch); transport rejection preserves the
  original blocker. Behavioral tests prove all three branches ×5 deterministic.
- Inventory: ~45 sites across five classes in internal/adaptersites with
  canary drift alarm; 12 spot-checked correct by independent review; exactly
  two compatibility-gated lifecycle entries (the two durable_runs switches);
  promptRunAdapter verified already-durable; prompt helpers documented
  out-of-scope (text production, never verdicts).
- Dual-axis review: GO; carried M1 (behavioral refutation tests — done), L1
  (refutation InvocationID provenance parity → follow-ups), M2 canary gap for
  store-primitive mutations (→ follow-ups). Oversized-file note: engine.go
  ~668 and engine_test.go ~1179 pre-existing debt, this slice adds ~19 net to
  engine.go — bypass recorded per guardian rule.
- Verification snapshot: gofmt empty; build+vet linux+windows; full suite
  green 24 packages.

### Slice 2 — end-to-end harness suite and linked-worktree proof
- Commits: shared harness (314), crash/timeout/abort (295),
  retry/respond/fallback (229), recovery/worktree (193) — hook-enforced;
  test-only package internal/durableruns_e2e, zero production changes
  (verified, not trusted: every referenced symbol checked against HEAD).
- Seven scenarios on REAL store+controller(+transport): honest dual crash
  classification; subprocess timeout kill proven via /proc pid polling;
  controller-authored abort with late-result drop pinned; retry fresh
  identity + Verify across lineage; host-envelope response hash binding;
  REAL CadenaAdaptador over fake PATH binaries with effective-agent =
  secondary; orphaned-canceled two-step relaunch with ContentHash-untouched
  prefix. Linked-worktree: real git worktree add, identical common-dir
  resolution, DeepEqual inspections from both worktrees — portable.
- runs-inspection consistency assertion applied per scenario (8 direct +
  justified manual trio for reconciled non-terminal expectations + stronger
  cross-worktree DeepEqual).
- Orphaned streams seeded through REAL AppendEvent machinery (validator-
  coherent, cannot drift from format reality).
- Dual-axis review: GO. Applied opportunistically: bounded waitStarted helper
  replacing two unbounded channel receives (N1); MY_SUB_AGENT neutralized in
  fallback fixture (N2). Cosmetic worktree-prune note left as-is (temp dirs).
- Verification snapshot: gofmt empty; build+vet linux+windows; -race clean;
  ×5 deterministic.

### Slice 3 — operator completeness: retention, schemas, migration policy
- Commits: prune guards (341), pins+reasons (377), removal flow (320), CLI
  contract pins (202), docs (156 net) — hook-enforced; clean. Note:
  comandos_runs_prune.go rode along in the guards-pin commit (label
  imperfection, content coherent).
- PruneExecutions: explicit-only operator action with full guard chain —
  non-terminal, corrupt/incomplete-tail, provenance-referenced (finding blobs
  + ledger fichas, fail-closed on unreadable), parent-of-survivor protected,
  orphaned-canceled deliberately kept (documented conservative rule);
  Windows-safe two-phase removal under the cross-process lock; in-lock tail
  re-verification closes the live-controller race.
- M1 fixed: crash-interrupted removals (lock-only dirs) complete on next
  prune as prunable-remnant; dirs missing request.json WITH events bytes
  still refused. L1 done: exported prune reasons + doc-pin test.
- Docs: consolidated stable-JSON schema table with keep-in-sync pointer;
  retention/purge policy matching implementation 1:1 (verified by reviewer
  against projectionFor UpdatedAt semantics and ops independence);
  compatibility/migration policy covering R1→R10 additive shapes,
  never-rewritten guarantee, release-bounded switch deprecation.
- Dual-axis review: GO — all five adversarial hunts resolved (crash
  atomicity now self-healing via remnant completion; reference completeness
  verified against blob indexes/decisions.jsonl/ops which carry no run IDs;
  writer-side last-event At decides cutoff with fail-safe skew asymmetry).
- Verification snapshot: gofmt empty; build+vet linux+windows; full suite
  green across 25 packages; -race clean on touched packages; ×5 deterministic.

**Unit complete pending Judgment Day.**
