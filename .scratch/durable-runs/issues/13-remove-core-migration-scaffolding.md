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

**Status:** complete

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

- [x] Focused tests prove the prune in-lock window closed: a reference
      persisted between classification and lock refuses deletion. *(Race-window seam; late-child refusal too.)*
- [x] Focused tests prove every `runs` subcommand rejects undeclared flags
      with usage exit 1, including `--older-than` outside prune. *(Hardcoded 9×10 matrix + dispatcher e2e.)*
- [x] Focused tests prove refutation findings carry invocation identity and
      are protected by prune provenance scanning. *(F-1 fix: collector scans Dims[].Hallazgos; e2e over real GuardarRevision ficha covers producer AND refuter ids.)*
- [x] Tests prove inventory canaries catch store-primitive mutations outside
      declared files. *(CreateRun/AppendTerminalEvent/SaveAttemptOutcome tokens; live sweep = six declared durable files.)*
- [x] Focused tests prove gate/review operate identically with the switches
      gone (config without those keys loads strictly; old yamls containing
      them fail fast with an explicit unknown-key error). *(Project+global strict-rejection tests; cmd e2e exit 4; facade pins unchanged.)*
- [x] Tests/readers prove historical records from pre-R11 binaries remain
      fully inspectable after removal (ledger + runs streams). *(Pre-R9 stream fixture + legacy ledger revision tests with meaningful assertions.)*
- [x] Docs state the minimum supported schema and the upgrade path for
      pre-R1 repositories. *(docs/runs-cli.md minimum-schema subsection; files gained on demand, never converted.)*
- [x] Evidence records install/upgrade/hook verification outcomes. *(Slice 3: real git commit blocked/allowed proof; Windows coverage honestly classified.)*
- [x] The implementation records build, vet, tests, guardian, independent
      `code-review`, rollback boundary, and follow-ups here.

## Evidence

*(appended per slice)*

## Follow-ups

*(recorded at closure)*

### Slice 1 — hardening pool from R8-R10 judgment days
- Commits: prune window + refutation identity (265), strict flag contracts
  (151), matrix/canary/provenance pins (216) — hook-enforced.
- Prune in-lock window closed: locked section re-verifies provenance
  references AND late-appearing children (exact ParentRunID filter, undecodable
  siblings skipped justified); typed refusal mapped to stable report reasons;
  race-window test seam proves kept-not-deleted for both halves. Residual
  post-rescan admission window recorded as follow-up (F-2, consistent with
  remnant-repair design).
- Strict per-subcommand flags: single-source runsSubcommandFlags table;
  undeclared flags → usage exit 1 with named message; recover special-case
  intact; full hardcoded 9×10 accept/reject matrix + dispatcher e2e; existing
  call sites all conform (verified by independent review).
- Refutation InvocationID stamped on the refuted Hallazgo v2 BEFORE ledger
  persistence (legacy nil branch byte-identical). Review round caught F-1
  CRITICAL: the provenance collector never read Dims[].Hallazgos — the only
  home of refutation identity — so prune could delete cited streams. Fixed:
  collector scans Hallazgos, end-to-end collector test over a REAL GuardarRevision
  ficha proves producer AND refuter identities both protected; masking
  hand-seeded case reverted to plain reference seeding with pointer comment.
- Store-primitive canaries (CreateRun/AppendTerminalEvent/SaveAttemptOutcome)
  declared for the six legitimate durable files; undeclared-file detection
  tested; live sweep confirms zero unclassified carriers.
- Deviations accepted: SaveAttemptOutcome is the real primitive name (ticket
  text corrected here); ReviewFinding has no InvocationID field — Hallazgo v2
  is the persisted carrier, which is what prune reads; engine.go 682 lines is
  pre-existing debt (+13 net this slice).
- Verification snapshot: gofmt empty; build+vet linux+windows; full suite
  green; -race clean on store/review; ×5 deterministic.

### Slice 2 — switch removal and dead legacy path deletion
- Commits: config schema (27/−113), review unconditional routing (86/−92),
  gate single-path merge (180/−210), cmd wiring cleanup (98/−208),
  historical readability pins + docs (270 net) — hook-enforced; clean.
- Both durable_runs keys removed from the config schema; old yamls fail fast
  via existing KnownFields(true) strict-load with file+line, tested at
  project and global levels plus cmd-level e2e (exit 4 naming the section).
- Review: refutation routes through transport unconditionally; invokeReview's
  nil fallback retained as documented engine-level injection seam — all five
  production sites verified non-nil post-wiring (review/gate/pr).
- Gate: EjecutarGate IS the durable orchestration now; legacy body deleted;
  facade helpers shared so the four pinned terminal shapes render unchanged;
  missing git common dir fails honestly per call instead of silently
  degrading (behavior change tested per half).
- Readers preserved forever: pre-R9 stream fixture and legacy ledger revision
  both proven readable after removal (meaningful assertions, not smoke).
- Inventory pins ZERO gated entries and zero durable_runs yaml tags.
- Dual-axis loop: GO conditional on stale gate_run_plan.go header (fixed) and
  Spanish comment block in parser.go (translated). Hunt conclusions: no
  hidden survivors; reader compatibility safe (config never serialized);
  facade drift none for pinned shapes (one honest delta: store-write failure
  during validation settlement now infrastructure instead of VALIDATION_
  FAILED); lenient-loader commands ignore removed keys silently — docs note
  candidate. F3 coverage gaps → follow-ups (global gate key case; cmd-level
  nil-sink composition).
- Verification snapshot: gofmt empty; build+vet linux+windows; full suite
  green across 25 packages; -race clean on gate/review/cmd-sentinel; ×5.

### Slice 3 — install, upgrade and hook verification
- Commits: disclaimer const + pin refresh (8), setup contracts (331), hook
  enforcement through real git commits (304) — hook-enforced; clean.
- Linux-executed: install placement (exec bit, artifact consumption, version
  report via real binary subprocess); upgrade preserves global+repo config
  byte-identically on both platform variants; uninstall removes binary with
  full repo-tree content-diff proof of non-interference; hook written to
  <git-common-dir>/hooks/pre-commit matching the production generator exactly,
  exec bit + idempotency; uninit reverts own artifacts preserving foreign
  hooks byte-for-byte; REAL `git commit` blocked over budget (HEAD unmoved,
  fake sentinel's reason surfaced) and allowed within.
- Compile-only/path-units on Windows declared explicitly (PowerShell PATH out
  of scope with rationale); .cmd fixtures genuinely execute for placement/
  version.
- Fixture bug caught during implementation: core.hooksPath pointed at the
  hook FILE not directory — test-side only; production writes the hook file
  into the default hooks dir (Git picks it up without hooksPath).
- Dual-axis loop: conditional GO on translating 50 Spanish literals across
  the two new test files (done; production-pinned substrings like the
  uninstall disclaimer stay Spanish until production translates — documented).
  Hunts: fake-sentinel realism proven composite (shared generator + argv pin
  sibling + exit-code propagation both directions); isolation clean;
  uninstall const extraction byte-identical; pre-existing flakes untouched.
- Verification snapshot: gofmt empty; build+vet linux+windows; full suite
  green 25 packages; -race clean on setup/cmd-sentinel; ×5 deterministic.

**Unit complete — R11 closes the core R-roadmap.**

### Unit closure — R11

- Final verification: gofmt empty; build+vet linux AND windows; full suite
  green across 25 packages; -race clean on store/review/setup/cmd-sentinel.
- Rollback boundary as contracted: cleanup reverts within the same schema
  generation by reverting the removal commits; durable records were never
  rewritten or downgraded — readers kept forever.
- Follow-ups pool carried forward: residual post-rescan admission window in
  prune (F-2, remnant-repair consistent); lenient-loader commands silently
  ignore removed yaml keys (docs sentence candidate); invokeReview nil-seam
  compile-time enforcement; engine line-ordering determinism; two pre-existing
  flaky tests (TestRunsRetryRelaunches..., TestCrearSnapshotConcurrente);
  production Spanish user-facing strings translation sweep (uninstall
  disclaimer etc.).

**Closed:** R11 complete — the core R-roadmap (R0-R11) is done: durable runs
are the single execution authority with no silent bypass, audited envelope
totality, operator-complete tooling, and preserved historical readability.