# 13: Remove Core Migration Scaffolding

**What to build:** After one stable release window with evidence of no fallback
use, the temporary dual-path scaffolding is removed: the release-bounded
compatibility switches disappear, the dead legacy scheduler and gate execution
paths are deleted while readers for historical review and durable-run records
are preserved, the minimum supported schema and upgrade path are documented,
and install/upgrade/repository-hook behavior is verified. The unit also lands
the accumulated hardening pool from R8-R10 judgment days.

**Blocked by:** 12 (complete, merged at 17ab39c) and the agreed stability
window (user authorization for this unit).

**Status:** ready-for-agent

**Design contract (agreed analysis):**

- Hardening pool first (small, independent, keeps behavior):
  - PruneExecutions in-lock re-verification must ALSO re-check provenance
    references and parent linkage before deletion (JD-R10 W1), closing the
    classify→delete TOCTOU window.
  - `--older-than` and other single-command flags are rejected with usage exit
    1 when passed to a `runs` subcommand that does not declare them
    (JD-R10 W2), ending the shared-parser laxness.
  - Refutation attempts thread their admitted InvocationID into finding
    metadata for parity with dimensions (R10 L1); prune provenance guards then
    naturally cover them.
  - adaptersites inventory gains canary tokens for store-primitive mutation
    calls (CreateRun/AppendTerminalEvent/WriteAttemptOutcome) outside declared
    files (R10 M2).
- Switch removal is a cutover completion, not a format change: both
  `review.durable_runs` and `gate.durable_runs` yaml keys are removed; config
  parsing rejects unknown keys per existing strict-load rules; the legacy
  non-durable execution branches in the review engine and gate orchestration
  are deleted together with their now-unreachable helpers; refutation always
  routes through transport (nil branch removed).
- Readers stay forever: historical ledger revisions and completed durable run
  streams written by older binaries remain fully readable and inspectable;
  nothing rewrites or deletes records during this migration.
- Minimum supported schema documented: the oldest store/stream shape still
  readable after this unit, and the upgrade steps for repositories created
  before R1 (they simply gain new files on demand; no conversion step).
- Install/upgrade/hook verification: end-to-end checks that `sentinel init`
  writes the marked guardian rule and pre-commit hook, `install`/`upgrade`
  place the binary correctly, and the hook enforces staged volume through the
  installed binary — exercised via tests where feasible, otherwise scripted
  and recorded as evidence.
- Additive compatibility preserved everywhere else; no persisted-format
  changes in this unit.

**Acceptance criteria:**

- [ ] Focused tests prove the prune in-lock window closed: a reference
      persisted between classification and lock refuses deletion.
- [ ] Focused tests prove every `runs` subcommand rejects undeclared flags
      with usage exit 1, including `--older-than` outside prune.
- [ ] Focused tests prove refutation findings carry invocation identity and
      are protected by prune provenance scanning.
- [ ] Tests prove inventory canaries catch store-primitive mutations outside
      declared files.
- [ ] Focused tests prove gate/review operate identically with the switches
      gone (config without those keys loads strictly; old yamls containing
      them fail fast with an explicit unknown-key error).
- [ ] Tests/readers prove historical records from pre-R11 binaries remain
      fully inspectable after removal (ledger + runs streams).
- [ ] Docs state the minimum supported schema and the upgrade path for
      pre-R1 repositories.
- [ ] Evidence records install/upgrade/hook verification outcomes.
- [ ] The implementation records build, vet, tests, guardian, independent
      `code-review`, rollback boundary, and follow-ups here.

## Evidence

*(appended per slice)*

## Follow-ups

*(recorded at closure)*
