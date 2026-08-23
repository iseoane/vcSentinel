# 12: Complete Core Durable Control

**What to build:** Durable runs become the single execution lifecycle authority
for review and gate. Remaining parallel lifecycle paths are closed or explicitly
compatibility-gated, every adapter attempt flows through admitted invocation
envelopes, operator documentation and JSON schemas are complete, retention and
purge behave, end-to-end harnesses prove crash/timeout/abort/retry/response/
fallback/recovery behavior, and linked worktrees observe the same run state.

**Blocked by:** 11 (complete, merged at bccb76e).

**Status:** ready-for-agent

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
