# 11: Route Gate Through Durable Runs

**What to build:** `sentinel gate` is represented by one root durable run whose
logical jobs carry deterministic validation evidence and reviewer invocation
history. The current gate CLI, output text, and exit-code contract remain a
compatibility facade over the durable orchestration, and gate results can be
reconstructed from admitted durable state alone.

**Blocked by:** 07 (complete, merged at 9202f28) and 10 (complete, merged at
7b725bb).

**Status:** ready-for-agent

**Design contract (agreed analysis):**

- One root run per gate execution: validation commands and review dimensions
  become separate logical jobs under it, preserving today's
  validation-before-review ordering — review jobs never start before every
  validation job has settled successfully.
- `--stage` lifecycle context and `--profile` validation-profile semantics are
  embedded in the root run's request so any later observation can explain why
  the run exists; profile resolution stays deterministic and fails explicitly
  exactly as today.
- Terminal classes stay separate and honest: validation failure, review
  findings, and infrastructure failure map to distinct terminal outcomes on
  their own jobs, aggregated by the root run without erasing which layer
  failed. Existing exit codes and stdout text of `sentinel gate` are pinned
  byte-for-byte as the compatibility facade.
- Review jobs route through the existing durable transport (evidence
  admission, owned process trees, cancellation) — no parallel execution path;
  validation jobs record command, exit status, and duration as deterministic
  evidence frames without invoking any agent.
- Reconstruction: given only the store contents for the root run, the gate
  summary (validation verdicts, review verdicts per dimension, final class)
  can be rebuilt identically to what the live run printed — proven by tests
  that replay admitted state and compare against the recorded facade output.
- Rollback seam: a construction-time switch routes gate back to the legacy
  orchestration while durable history already written stays inspectable via
  `sentinel runs`; the default is the durable path (this unit IS the cutover),
  matching the R6 evidence-admission pattern of reversible strictness.
- Additive compatibility: no JSON tag changes to persisted structs; new event
  usage rides existing frame machinery; stores from previous units keep
  scanning and operating unchanged.

**Acceptance criteria:**

- [ ] Focused tests prove one root run with separate validation and review
      logical jobs, and that review never starts before all validation jobs
      settle successfully.
- [ ] Focused tests prove --stage and --profile land in the root request and
      profile-absent failure behavior is unchanged.
- [ ] Focused tests prove validation evidence (command, exit status,
      duration) is recorded deterministically without agent involvement.
- [ ] Focused tests prove terminal classes stay distinct per layer and the
      root aggregation names the failing layer.
- [ ] Focused tests pin the gate CLI contract: stdout text, exit codes, and
      error paths byte-identical to pre-R9 for success, validation failure,
      review failure, and infrastructure failure shapes.
- [ ] Focused tests prove reconstruction from admitted durable state alone
      reproduces the printed gate summary.
- [ ] Tests prove the rollback switch restores legacy orchestration behavior
      while prior durable history remains fully inspectable.
- [ ] Focused tests prove additive compatibility: old streams and stores
      unaffected; review jobs inherit admission/cancellation behavior from the
      shared transport.
- [ ] The implementation records build, vet, tests, guardian, independent
      `code-review`, rollback boundary, and follow-ups here.

## Evidence

*(appended per slice)*

## Follow-ups

*(recorded at closure)*
