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

## Evidence

### Single slice — three root-cause fixes
- Commits: snapshot serialization (27), sidecars-first read order (122),
  locked admission (174), principal envelopes (144) — hook-enforced; clean.
- FIX 1a DIAGNOSIS (production bug): concurrent CrearSnapshot calls each ran
  git worktree add and the winner's `git worktree repair` raced siblings'
  mutations of <git-common-dir>/worktrees admin state — git does not lock
  that across commands. Fix: package-level snapshotMu serializes in-process
  create-or-reuse AND purge; cross-process safety remains the atomic
  temp-checkout+rename. Stress ×30 standalone AND ×30 under parallel
  full-suite load: green both.
- FIX 1b DIAGNOSIS (production bug, different from suspected): AppendTerminal-
  Event writes terminal event then legacy sidecar under one lock, but lock-free
  ReadAttemptOutcomes scanned log first / sidecars second — a reader could pair
  a stale log with a fresh sidecar and misreport mid-persistence as "outcome
  has no terminal event". Fix: sidecars read FIRST (observing one proves its
  append completed), embedded-evidence branch before legacy errors,
  empty-stream+sidecar contract preserved, genuine corruption errors
  identically. Split-read regression test ×15 + suite ×5 -race.
- FIX 2: CreateRun admission now takes the same cross-process event lock prune
  holds (deadlock audited: sole production caller Controller.Start, invoked
  before any append — no nested acquisition); bounded retry on ErrNotExist
  covers the mkdir↔lock-open vanish window; remnant cleanup refuses when real
  content appeared under the lock (new stable reason) while pure lock-only
  remnants keep self-healing. Admission-during-prune test via race-window hook
  precedent + 20× true-concurrency invariant.
- FIX 3: RepositoryHost gains RecoverRequest/RetryRequest with auth_context
  mirroring InspectRequest/ApplyRequest; ErrMissingPrincipal consistent;
  cmd retry/recover route through host envelopes; direct controller calls
  remain only inside the host implementation; idempotent-head fallbacks
  verified by rewritten stale-revision test.
- REVIEW NOTE (honest): the explore-subagent provider was down through four
  launch attempts, so this slice's independent review was performed by the
  orchestrator directly: deadlock audit re-derived from call graph, read-order
  interleave analysis (both directions), envelope shape parity against sibling
  requests, stress reproduction before/after. All hunts resolved; schema-table
  drift for host envelopes noted as docs follow-up.
- Verification snapshot: gofmt empty; build+vet linux+windows; full suite
  green 25 packages ×3 runs zero FAIL; -race clean on git/store/execution/
  cmd-sentinel.

**Closed:** ticket 14 — the three post-roadmap high-priority corrections are in.
