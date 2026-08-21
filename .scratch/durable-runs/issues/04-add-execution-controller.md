# 04: Add the Execution Controller

**What to build:** Run one provider-neutral agent attempt through a durable
per-run controller, preserving lifecycle, lineage, cancellation, and failure
evidence when the adapter or its process fails.

**Blocked by:** 03: Add the Durable Run Store.

**Status:** complete

- [x] `Start` admits a request and policy, persists immutable identities, and emits the pre-execution lifecycle before invoking an adapter.
- [x] The controller distinguishes one logical job from each physical attempt, retry, fallback, and linked response.
- [x] Success, failure, unavailable, timeout, cancellation, and adapter-process errors become durable normalized terminal events.
- [x] `Inspect` reconstructs status and operational events from persisted state after the controller process exits.
- [x] `Apply` supports the initial allowed control actions without moving semantic review authority into the adapter.
- [x] Focused fake-adapter and process-boundary tests prove no agent-call failure disappears.
- [x] The implementation records build, vet, tests, guardian, independent review, rollback boundary, and follow-ups here.

**Out of scope:** Repository daemon, TUI, remote execution, ACP/acpx production
adapters, and migration of every existing review/gate path.

**Rollback boundary:** Remove only the controller and its tests; leave R0, R1,
and R2 intact so the durable store remains independently usable.

## Evidence

- Implementation: `4f572bf`; event-authority correction: `b1dba23`.
- Review corrections: `eea31c1` and `ff6b648`.
- Build: `go build ./...` passed.
- Vet: `go vet ./...` passed.
- Focused tests: `go test -count=1 ./internal/agentrun ./internal/store ./internal/execution` passed.
- Full tests: `go test -count=1 ./...` passed.
- Race checks: `go test -race -count=1 ./internal/agentrun ./internal/store ./internal/execution` passed.
- Process boundary: `TestAdapterProcessBoundaryFailureIsDurable` passed through the focused execution package tests.
- Guardian: checked before every bounded write. The initial 592-line semantic unit required and received the user's explicit bypass; the review correction measured 80 authored lines and needed no decision.
- Independent review: the mandated `code-review` subagent transport was invoked but rejected with `opencode_review_transport_binding_invalid: Task prompt has no provider-issued review binding`. The coordinating agent independently reviewed `e17f9a9...HEAD`, found that a corrupt legacy outcome side file could hide an embedded terminal event, and corrected it in `eea31c1`/`ff6b648`.
- Rollback: revert the R3 controller and its focused tests; R0-R2 remain usable.
- Follow-ups: R4 records production retry and fallback invocations. R5 owns operator retry/recover commands and durable restart response admission; a recovered R3 controller explicitly rejects a response without its live adapter request context.
