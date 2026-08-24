# 14: Core Hardening — Flaky Tests, Prune Window, Envelope Parity

**What to build:** Three post-roadmap corrections with no new features: make
the two known-flaky tests deterministic by fixing their root causes, close the
residual prune admission window (a CreateRun between the in-lock child rescan
and directory removal can lose its stream), and route retry/recover through
principal-carrying envelopes like every other lifecycle action.

**Blocked by:** 13 (complete, merged at 778ef83).

**Status:** ready-for-agent

**Design contract (agreed analysis):**

- Flaky root causes first: reproduce both failures under load locally
  (`go test -count=N ./...` parallel pressure, or targeted stress), identify
  whether the defect is in production code or test setup, fix at the root.
  Known candidates: `TestCrearSnapshotConcurrenteNoDuplicaNiFalla` (internal/
  git, concurrent snapshot creation returning divergent paths under suite
  load) and `TestRunsRetryRelaunchesFailedRunInsideSameIdentity`
  (cmd/sentinel, intermittent). If a test asserts timing-sensitive behavior,
  make the production behavior deterministic rather than loosening the
  assertion, unless loosening is provably honest.
- Prune admission window: `CreateRun` writes its admission records without
  taking the execution event lock, so a run admitted between prune's in-lock
  child rescan and the final directory removal can be deleted. Close it at
  the smallest honest seam — options to judge during implementation: take the
  event lock inside CreateRun for the record-write section (making admission
  atomic with respect to prune), or have removal re-stat the target directory
  mtime/content after acquiring the lock and refuse when it changed since
  classification. The chosen seam must keep crash-interrupted remnants
  self-healing and must not deadlock with existing lock ordering.
- Envelope parity: RepositoryHost gains Recover/Retry operations carrying
  AuthContext like Inspect/Subscribe/Apply; cmd/sentinel recover --run and
  retry route through them; direct controller calls remain only inside the
  host implementation. Idempotent-head fallbacks keep working.
- Additive compatibility: no persisted-format changes; old streams and stores
  unaffected.

**Acceptance criteria:**

- [ ] Focused stress evidence: both previously flaky tests pass across
      repeated full-suite-equivalent runs (documented counts).
- [ ] Focused tests prove the prune window closed with the chosen seam
      (admission-during-prune scenario refuses or survives honestly).
- [ ] Focused tests prove recover/retry reject missing principal via the
      host path exactly like start/respond/abort.
- [ ] The implementation records build, vet, tests, guardian, independent
      `code-review`, rollback boundary, and follow-ups here.

## Evidence

*(appended per slice)*

## Follow-ups

*(recorded at closure)*
