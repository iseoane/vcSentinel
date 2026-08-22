# 08: Own Cancellation And Process Trees

**What to build:** Abort, timeout, and shutdown have deterministic cross-platform
process ownership. Cancellation first cooperates through propagated contexts,
then escalates within a bounded budget against owned process groups or job
objects, and every step leaves durable evidence. A cancellation can never be
reported complete while descendants remain unaccounted for, and an owner dying
mid-cancellation is reconciled on restart instead of leaking a phantom live run.

**Blocked by:** 04 (complete) and 06 (complete, merged at d0f9a3f). Both done;
R6 also merged at 9202f28.

**Status:** ready-for-agent

**Design contract (agreed analysis):**

- Cooperative cancellation becomes real today's missing half: the review
  adapter's Execute accepts and forwards its context to the spawned provider
  process, so `Apply(ActionAbort)` and deadline contexts reach the child
  immediately. The controller, not a late adapter result, authors the terminal
  event for a run whose binding was rejected or whose context was canceled —
  closing the JD-A1 gap where an ignored context let a late success overwrite
  the intended canceled settlement.
- Process ownership starts at spawn: provider processes launch into an owned
  process group on Linux (setpgid) and a kill-on-close job object on Windows.
  Ownership is assigned in one platform-seamed constructor so callers cannot
  create unowned children.
- Bounded escalation follows cooperation: after a configurable grace period the
  owner attempts graceful termination of the whole tree, then hard kill, then
  declares the tree orphaned if reaping fails. Each transition appends exactly
  one durable evidence event: cancellation-requested, termination-attempted,
  reaped, or orphaned. Escalation is reversible at construction time via
  `review.cancellation_escalation` (default true); disabling it keeps
  cooperative cancellation and orphan detection while never issuing kill
  signals beyond the direct child.
- Orphan accounting: when a cancellation ends without confirmed reaping, the
  run settles with an orphaned terminal class and the recorded pids stay
  inspectable through existing `sentinel runs` output — no silent green.
- Restart reconciliation covers owner death during cancellation: a run found
  non-terminal whose recorded state shows cancellation requested but no reaped
  evidence is classified orphaned-canceled on next observation, never resumed
  silently and never fabricated as completed. Full recovery classification
  stays R8 scope; this unit only guarantees the honest terminal record.
- Evidence stays additive: new event kinds extend the append-only stream;
  projections, admission rules, fingerprints, and ledger formats from R1-R6
  remain byte-compatible. Existing successful paths write no new events.
- Cross-platform behavior is identical at the contract level: build-tagged
  platform files implement one small ownership interface; tests use real
  subprocess harnesses (child and grandchild trees, signal-ignoring children,
  cancel races, prompt exit) that run on both Debian and Windows.

**Acceptance criteria:**

- [ ] Focused tests prove cooperative cancellation: aborting a running review
      terminates the provider subprocess promptly via forwarded context, and
      the durable record settles canceled authored by the controller even when
      the adapter would have returned later.
- [ ] Focused tests prove bounded escalation: a child that ignores cooperative
      cancellation is terminated with its whole tree within the grace budget,
      leaving termination-attempted and reaped evidence.
- [ ] Focused tests prove descendant coverage: grandchild processes die with
      the tree on both platforms (process group on Linux, job object on
      Windows), verified with real subprocess harnesses.
- [ ] Focused tests prove orphan honesty: when reaping cannot be confirmed the
      run settles with the orphaned class and recorded pids remain visible in
      runs inspection; nothing reports success or clean cancellation.
- [ ] Focused tests prove restart reconciliation: a run persisted as
      cancellation-requested without reaped evidence is classified
      orphaned-canceled on next observation, with no fabricated completion and
      no silent resume.
- [ ] Tests prove `review.cancellation_escalation=false` keeps cooperative
      cancellation and orphan detection while no kill beyond the direct child
      is ever issued.
- [ ] Tests prove additive compatibility: existing run streams without
      cancellation events project byte-identically; admission, fingerprints,
      and ledger formats unchanged.
- [ ] Focused tests cover the race windows: cancel arriving before start
      completes, child exiting during escalation, and double abort being
      idempotent.
- [ ] The implementation records build, vet, tests, guardian, independent
      `code-review`, Judgment Day, rollback boundary, and follow-ups here.

## Evidence

*(appended per slice)*

## Follow-ups

*(recorded at closure)*
