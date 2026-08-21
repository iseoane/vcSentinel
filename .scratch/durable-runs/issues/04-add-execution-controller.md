# 04: Add the Execution Controller

**What to build:** Run one provider-neutral agent attempt through a durable
per-run controller, preserving lifecycle, lineage, cancellation, and failure
evidence when the adapter or its process fails.

**Blocked by:** 03: Add the Durable Run Store.

**Status:** ready-for-agent

- [ ] `Start` admits a request and policy, persists immutable identities, and emits the pre-execution lifecycle before invoking an adapter.
- [ ] The controller distinguishes one logical job from each physical attempt, retry, fallback, and linked response.
- [ ] Success, failure, unavailable, timeout, cancellation, and adapter-process errors become durable normalized terminal events.
- [ ] `Inspect` reconstructs status and operational events from persisted state after the controller process exits.
- [ ] `Apply` supports the initial allowed control actions without moving semantic review authority into the adapter.
- [ ] Focused fake-adapter and process-boundary tests prove no agent-call failure disappears.
- [ ] The implementation records build, vet, tests, guardian, independent review, rollback boundary, and follow-ups here.

**Out of scope:** Repository daemon, TUI, remote execution, ACP/acpx production
adapters, and migration of every existing review/gate path.

**Rollback boundary:** Remove only the controller and its tests; leave R0, R1,
and R2 intact so the durable store remains independently usable.
